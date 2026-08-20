package agentrun

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

func TestEventStreamRunIDUsesParentStreamForChildEvent(t *testing.T) {
	run := Run{RunID: "child-1"}
	event := agentstream.Event{RunID: "parent-1", ID: "parent-1-child-event-1"}
	if got := eventStreamRunID(run, event); got != "parent-1" {
		t.Fatalf("event stream run id = %q, want parent-1", got)
	}
}

func TestEventStreamRunIDFallsBackToWorkerRun(t *testing.T) {
	run := Run{RunID: "root-1"}
	if got := eventStreamRunID(run, agentstream.Event{}); got != "root-1" {
		t.Fatalf("event stream run id = %q, want root-1", got)
	}
}
