package agentstream_test

import (
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

func TestHubReplaysAndClosesFinishedRun(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.August, 13, 0, 0, 0, 0, time.UTC)
	if err := hub.PublishAgent(agent.Event{ID: "event-1", RunID: "run-1", Type: agent.EventRunStarted, CreatedAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "event-2", RunID: "run-1", Type: agent.EventRunFinished, CreatedAt: createdAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Finish("run-1"); err != nil {
		t.Fatal(err)
	}

	snapshot, live, cancel, done, err := hub.Subscribe("run-1", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if !done || len(snapshot) != 2 {
		t.Fatalf("done=%v snapshot=%d, want completed run with two events", done, len(snapshot))
	}
	if _, ok := <-live; ok {
		t.Fatal("finished run live channel must be closed")
	}
}

func TestHubDoesNotCrossKnowledgeBases(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := hub.Subscribe("run-1", 8, nil); err != agentstream.ErrRunNotFound {
		t.Fatalf("wrong knowledge base error = %v, want ErrRunNotFound", err)
	}
}

func TestHubBoundsEventsPerRun(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 520; index++ {
		if err := hub.Publish(agentstream.Event{RunID: "run-1", Type: "message_delta"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _, cancel, _, err := hub.Subscribe("run-1", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(snapshot) != 512 {
		t.Fatalf("snapshot events = %d, want bounded 512", len(snapshot))
	}
}

func TestHubCancelInvokesRegisteredCancelOnce(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := hub.RegisterCancel("run-1", func() { calls++ }); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := hub.Cancel("run-1", 7); err != nil {
			t.Fatalf("cancel attempt %d error = %v, want nil", attempt, err)
		}
	}
	if calls != 1 {
		t.Fatalf("cancel calls = %d, want exactly one", calls)
	}
}

func TestHubCancelRejectsUnknownRunOrWrongKnowledgeBase(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.RegisterCancel("run-1", func() {}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Cancel("run-unknown", 7); err != agentstream.ErrRunNotFound {
		t.Fatalf("unknown run error = %v, want ErrRunNotFound", err)
	}
	if err := hub.Cancel("run-1", 8); err != agentstream.ErrRunNotFound {
		t.Fatalf("wrong knowledge base error = %v, want ErrRunNotFound", err)
	}
	// The cancel function must not have been invoked by the rejected calls.
	if err := hub.Cancel("run-1", 7); err != nil {
		t.Fatal(err)
	}
}

func TestHubCancelWithoutRegisteredCancelIsNoOp(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.Cancel("run-1", 7); err != nil {
		t.Fatalf("cancel without registration error = %v, want nil", err)
	}
}
