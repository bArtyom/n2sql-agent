package agentrun

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

func TestHubStreamBridgeUsesHubAsTheSelectedTransport(t *testing.T) {
	hub := agentstream.NewHub()
	bridge := NewHubStreamBridge(hub)
	if bridge == nil {
		t.Fatal("NewHubStreamBridge() returned nil")
	}
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	event := agentstream.Event{
		Version: agentstream.EventSchemaVersion,
		ID:      "event-1",
		RunID:   "run-1",
		Type:    "run_started",
	}
	if err := bridge.Publish(context.Background(), Run{RunID: "run-1", KnowledgeBaseID: 7}, event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	snapshot, _, cancel, done, err := bridge.Subscribe(context.Background(), "run-1", 7)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer cancel()
	if done || len(snapshot) != 1 || snapshot[0].ID != "event-1" {
		t.Fatalf("Subscribe() = done=%v snapshot=%v, want one event", done, snapshot)
	}
}

func TestStreamBridgeImplementationsAreCompileTimeCompatible(t *testing.T) {
	var _ StreamBridge = (*HubStreamBridge)(nil)
	var _ StreamBridge = (*RedisEventStore)(nil)
}
