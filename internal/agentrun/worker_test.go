package agentrun

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

type runStoreStub struct {
	run    Run
	status Status
	failed string
}

func (s *runStoreStub) Create(context.Context, CreateInput) (Run, error) { return s.run, nil }
func (s *runStoreStub) ClaimNext(context.Context) (Run, error) {
	if s.status != StatusPending {
		return Run{}, ErrNoRun
	}
	s.status = StatusRunning
	s.run.Status = StatusRunning
	return s.run, nil
}
func (s *runStoreStub) MarkSucceeded(context.Context, int64) error {
	s.status = StatusSucceeded
	return nil
}
func (s *runStoreStub) MarkFailed(_ context.Context, _ int64, message string) error {
	s.status, s.failed = StatusFailed, message
	return nil
}
func (s *runStoreStub) MarkCanceled(context.Context, int64) error {
	s.status = StatusCanceled
	return nil
}

func TestRunnerMarksSucceededAfterExecution(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 1, RunID: "run-1"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(_ context.Context, run Run, sink EventSink) error {
		if run.RunID != "run-1" {
			t.Fatalf("run id = %q", run.RunID)
		}
		return sink(agent.Event{Type: agent.EventRunStarted})
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked || store.status != StatusSucceeded {
		t.Fatalf("RunOnce() = (%v, %v), status=%s, want succeeded", worked, err, store.status)
	}
}

func TestRunnerMarksFailedExecution(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 2, RunID: "run-2"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(context.Context, Run, EventSink) error {
		return errors.New("model unavailable")
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if store.status != StatusFailed || store.failed != "model unavailable" {
		t.Fatalf("status=%s error=%q, want failed model error", store.status, store.failed)
	}
}
