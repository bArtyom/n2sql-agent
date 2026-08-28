package agentrun

import (
	"context"
	"errors"

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
	return b.live.SubscribeFrom(ctx, runID, knowledgeBaseID, afterEventID)
}

var _ EventStore = (*EventBridge)(nil)
var _ LiveEventStore = (*EventBridge)(nil)
