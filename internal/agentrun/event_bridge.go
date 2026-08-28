package agentrun

import (
	"context"
	"errors"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// EventBridge keeps the same event on two intentionally different layers:
// PostgreSQL is the durable run journal, while Redis is only the short-lived
// transport window consumed by SSE. A Redis outage must not make an already
// durable Agent execution fail.
type EventBridge struct {
	durable EventStore
	live    LiveEventStore
}

const liveEventCleanupDelay = time.Minute

func NewEventBridge(durable EventStore, live LiveEventStore) (*EventBridge, error) {
	if durable == nil {
		return nil, errors.New("durable agent event store is required")
	}
	return &EventBridge{durable: durable, live: live}, nil
}

func (b *EventBridge) Append(ctx context.Context, run Run, event agentstream.Event) error {
	if b == nil || b.durable == nil {
		return ErrInvalidRun
	}
	if err := b.durable.Append(ctx, run, event); err != nil {
		return err
	}
	// Redis is a delivery optimization. PostgreSQL has already recorded the
	// event, so a transient Redis failure is safe to tolerate; Hub still serves
	// subscribers in the current process and a later reconnect can use DB.
	if b.live != nil {
		_ = b.live.Append(ctx, run, event)
		if isTerminalAgentEvent(event.Type) {
			if cleaner, ok := b.live.(LiveEventCleaner); ok {
				streamRunID := eventStreamRunID(run, event)
				knowledgeBaseID := run.KnowledgeBaseID
				time.AfterFunc(liveEventCleanupDelay, func() {
					_ = cleaner.Delete(context.Background(), streamRunID, knowledgeBaseID)
				})
			}
		}
	}
	return nil
}

func (b *EventBridge) List(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, error) {
	if b == nil || b.durable == nil {
		return nil, ErrInvalidRun
	}
	return b.durable.List(ctx, runID, knowledgeBaseID)
}

func (b *EventBridge) Subscribe(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return b.SubscribeFrom(ctx, runID, knowledgeBaseID, "")
}

func (b *EventBridge) SubscribeFrom(ctx context.Context, runID string, knowledgeBaseID int64, afterEventID string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	if b == nil || b.live == nil {
		return nil, nil, nil, false, agentstream.ErrEventGap
	}
	snapshot, live, cancel, done, err := b.live.SubscribeFrom(ctx, runID, knowledgeBaseID, afterEventID)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if len(snapshot) == 0 && !done && afterEventID == "" {
		// A terminal Redis stream may already have been cleaned. Resolve that
		// state from the durable journal instead of leaving a new SSE client
		// waiting forever on a stream that can never receive another event.
		if stored, listErr := b.durable.List(ctx, runID, knowledgeBaseID); listErr == nil && hasTerminalEvent(stored) {
			if cancel != nil {
				cancel()
			}
			return stored, closedEventChannel(), func() {}, true, nil
		}
	}
	return snapshot, live, cancel, done, nil
}

func hasTerminalEvent(events []agentstream.Event) bool {
	for _, event := range events {
		if isTerminalAgentEvent(event.Type) {
			return true
		}
	}
	return false
}

var _ EventStore = (*EventBridge)(nil)
var _ LiveEventStore = (*EventBridge)(nil)
