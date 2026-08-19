package agentrun

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

type runStoreStub struct {
	run         Run
	status      Status
	failed      string
	requeued    bool
	deleted     int
	markedToken string
	renewErr    error
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
func (s *runStoreStub) RequeueExpired(context.Context) error {
	s.requeued = true
	return nil
}
func (s *runStoreStub) RenewLease(context.Context, int64, string) error { return s.renewErr }
func (s *runStoreStub) MarkSucceeded(_ context.Context, _ int64, token string) error {
	s.markedToken = token
	s.status = StatusSucceeded
	return nil
}
func (s *runStoreStub) MarkFailed(_ context.Context, _ int64, message, token string) error {
	s.markedToken = token
	s.status, s.failed = StatusFailed, message
	return nil
}
func (s *runStoreStub) MarkCanceled(_ context.Context, _ int64, token string) error {
	s.markedToken = token
	s.status = StatusCanceled
	return nil
}
func (s *runStoreStub) DeleteToolCheckpoints(_ context.Context, id int64) error {
	if id > 0 {
		s.deleted++
	}
	return nil
}
func (*runStoreStub) CleanupTerminalToolCheckpoints(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

func TestRunnerMarksSucceededAfterExecution(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 1, RunID: "run-1", LeaseToken: "lease-a"}, status: StatusPending}
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
	if !store.requeued {
		t.Fatal("RunOnce() did not requeue expired runs before claiming")
	}
	if store.deleted != 1 {
		t.Fatalf("deleted checkpoints = %d, want 1", store.deleted)
	}
	if store.markedToken != "lease-a" {
		t.Fatalf("marked token = %q, want lease-a", store.markedToken)
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
	if store.deleted != 1 {
		t.Fatalf("deleted checkpoints = %d, want 1", store.deleted)
	}
}

func TestRunnerCancelsExecutionWhenLeaseRenewalFails(t *testing.T) {
	store := &runStoreStub{renewErr: errors.New("lease lost")}
	runner := &Runner{store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner.renewLeaseWithInterval(ctx, Run{ID: 1, RunID: "run-1", LeaseToken: "lease-a"}, time.Millisecond, cancel)
	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("lease renewal failure did not cancel execution")
	}
}
