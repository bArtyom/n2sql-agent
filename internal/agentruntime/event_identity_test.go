package agentruntime

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

func TestEventEmitterUsesExecutionScopedIDs(t *testing.T) {
	var first, second agent.Event
	firstEmitter := newEventEmitter("run-1", func(event agent.Event) error {
		first = event
		return nil
	}, ExecutionIdentity{ExecutionID: "worker-a-attempt-1"})
	secondEmitter := newEventEmitter("run-1", func(event agent.Event) error {
		second = event
		return nil
	}, ExecutionIdentity{ExecutionID: "worker-b-attempt-2"})

	if err := firstEmitter.emit(agent.EventRunStarted, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := secondEmitter.emit(agent.EventRunStarted, 0, nil); err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("event IDs collided across execution attempts: %q", first.ID)
	}
}
