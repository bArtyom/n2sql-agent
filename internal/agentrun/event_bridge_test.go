package agentrun

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

type eventStoreStub struct {
	events []agentstream.Event
	err    error
}

func (s *eventStoreStub) Append(_ context.Context, _ Run, event agentstream.Event) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, event)
	return nil
}

func (s *eventStoreStub) List(context.Context, string, int64) ([]agentstream.Event, error) {
	return append([]agentstream.Event(nil), s.events...), s.err
}

func (s *eventStoreStub) Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, nil, false, errors.New("not implemented")
}

func (s *eventStoreStub) SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, nil, false, errors.New("not implemented")
}

func TestEventBridgeWritesDurableAndLiveCopies(t *testing.T) {
	durable := &eventStoreStub{}
	live := &eventStoreStub{}
	bridge, err := NewEventBridge(durable, live)
	if err != nil {
		t.Fatalf("NewEventBridge() error = %v", err)
	}
	event := agentstream.Event{ID: "event-1", RunID: "run-1", Type: "message_delta"}
	if err := bridge.Append(context.Background(), Run{ID: 1, RunID: "run-1", KnowledgeBaseID: 7}, event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(durable.events) != 1 || len(live.events) != 1 {
		t.Fatalf("durable/live events = %d/%d, want 1/1", len(durable.events), len(live.events))
	}
	got, err := bridge.List(context.Background(), "run-1", 7)
	if err != nil || len(got) != 1 || got[0].ID != event.ID {
		t.Fatalf("List() = %#v, %v, want durable event", got, err)
	}
}

func TestEventBridgeToleratesLiveFailureAfterDurableWrite(t *testing.T) {
	durable := &eventStoreStub{}
	live := &eventStoreStub{err: errors.New("redis unavailable")}
	bridge, err := NewEventBridge(durable, live)
	if err != nil {
		t.Fatalf("NewEventBridge() error = %v", err)
	}
	if err := bridge.Append(context.Background(), Run{ID: 1, RunID: "run-1", KnowledgeBaseID: 7}, agentstream.Event{ID: "event-1", RunID: "run-1", Type: "message_delta"}); err != nil {
		t.Fatalf("Append() error = %v, want live failure to be best effort", err)
	}
	if len(durable.events) != 1 {
		t.Fatalf("durable events = %d, want 1", len(durable.events))
	}
}
