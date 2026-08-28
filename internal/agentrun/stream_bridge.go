package agentrun

import (
	"context"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// StreamBridge is the selected short-lived transport for Agent events.
//
// A deployment chooses one implementation at startup: the in-process Hub for
// a single process, or Redis for a multi-process deployment. Durable replay is
// intentionally not part of this interface; it belongs to EventStore.
type StreamBridge interface {
	Publish(context.Context, Run, agentstream.Event) error
	Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error)
	SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error)
}

// HubStreamBridge adapts the in-process Hub to the transport boundary used by
// the persistent Agent runtime. The Hub remains useful for cancellation and
// approvals; this adapter only exposes its selected in-process event stream.
type HubStreamBridge struct {
	hub *agentstream.Hub
}

func NewHubStreamBridge(hub *agentstream.Hub) *HubStreamBridge {
	return &HubStreamBridge{hub: hub}
}

func (b *HubStreamBridge) Publish(_ context.Context, _ Run, event agentstream.Event) error {
	if b == nil || b.hub == nil {
		return ErrInvalidRun
	}
	return b.hub.Publish(event)
}

func (b *HubStreamBridge) Subscribe(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return b.SubscribeFrom(ctx, runID, knowledgeBaseID, "")
}

func (b *HubStreamBridge) SubscribeFrom(ctx context.Context, runID string, knowledgeBaseID int64, afterEventID string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	if b == nil || b.hub == nil {
		return nil, nil, nil, false, agentstream.ErrRunNotFound
	}
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}
	return b.hub.SubscribeFrom(runID, knowledgeBaseID, afterEventID, ctxDone)
}

var _ StreamBridge = (*HubStreamBridge)(nil)
