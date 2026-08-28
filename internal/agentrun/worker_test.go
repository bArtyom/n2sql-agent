package agentrun

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

type concurrentRunStore struct {
	mu        sync.Mutex
	pending   []Run
	completed int
}

func (s *concurrentRunStore) Create(context.Context, CreateInput) (Run, error) {
	return Run{}, nil
}

func (s *concurrentRunStore) ClaimNext(context.Context) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return Run{}, ErrNoRun
	}
	run := s.pending[0]
	s.pending = s.pending[1:]
	run.Status = StatusRunning
	if run.LeaseToken == "" {
		run.LeaseToken = fmt.Sprintf("lease-%d", run.ID)
	}
	return run, nil
}

func (*concurrentRunStore) RequeueExpired(context.Context) error { return nil }
func (*concurrentRunStore) RenewLease(context.Context, int64, string) error {
	return nil
}
func (s *concurrentRunStore) MarkSucceeded(context.Context, int64, string) error {
	s.mu.Lock()
	s.completed++
	s.mu.Unlock()
	return nil
}
func (*concurrentRunStore) MarkFailed(context.Context, int64, string, string) error {
	return nil
}
func (*concurrentRunStore) MarkCanceled(context.Context, int64, string) error {
	return nil
}

func (s *concurrentRunStore) Completed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completed
}

func TestRunWorkersExecutesRunsConcurrently(t *testing.T) {
	store := &concurrentRunStore{pending: []Run{
		{ID: 1, RunID: "run-1"},
		{ID: 2, RunID: "run-2"},
		{ID: 3, RunID: "run-3"},
	}}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	runner, err := NewRunner(store, ExecutorFunc(func(ctx context.Context, run Run, _ EventSink) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	runner.heartbeatInterval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runner.RunWorkers(ctx, 3, time.Millisecond, nil)
		close(done)
	}()

	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for three concurrent workers")
		}
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for store.Completed() != 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := store.Completed(); got != 3 {
		t.Fatalf("completed runs = %d, want 3", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunWorkers did not stop after context cancellation")
	}
}

type runStoreStub struct {
	run             Run
	status          Status
	failed          string
	stopReason      string
	requeued        bool
	deleted         int
	markedToken     string
	renewErr        error
	checkpointErr   error
	waiting         bool
	waitingApproval bool
	resumedParent   int64
	parentRun       Run
}

func (s *runStoreStub) Get(context.Context, string, int64) (Run, error) {
	if s.parentRun.RunID == "" {
		return Run{}, ErrRunNotFound
	}
	return s.parentRun, nil
}

func (s *runStoreStub) GetByID(context.Context, int64) (Run, error) {
	if s.parentRun.RunID == "" {
		return Run{}, ErrRunNotFound
	}
	return s.parentRun, nil
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
func (s *runStoreStub) GetLatestCheckpoint(context.Context, int64) (*Checkpoint, error) {
	return nil, s.checkpointErr
}
func (*runStoreStub) GetLatestThreadCheckpoint(context.Context, int64) (*Checkpoint, error) {
	return nil, nil
}
func (*runStoreStub) SaveCheckpoint(context.Context, Checkpoint) error  { return nil }
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
func (s *runStoreStub) MarkFailedWithReason(_ context.Context, _ int64, message, reason, token string) error {
	s.markedToken = token
	s.status, s.failed, s.stopReason = StatusFailed, message, reason
	return nil
}
func (s *runStoreStub) MarkTimedOut(_ context.Context, _ int64, message, token string) error {
	s.markedToken = token
	s.status, s.failed, s.stopReason = StatusTimeout, message, StopReasonTimeout
	return nil
}
func (s *runStoreStub) MarkCanceledWithReason(_ context.Context, _ int64, reason, token string) error {
	s.markedToken = token
	s.status, s.stopReason = StatusCanceled, reason
	return nil
}
func (s *runStoreStub) MarkCanceled(_ context.Context, _ int64, token string) error {
	s.markedToken = token
	s.status = StatusCanceled
	return nil
}
func (s *runStoreStub) MarkWaitingChildren(_ context.Context, _ int64, _ string) error {
	s.waiting = true
	s.status = StatusWaitingChildren
	return nil
}
func (s *runStoreStub) MarkWaitingApproval(_ context.Context, _ int64, _ string) error {
	s.waitingApproval = true
	s.status = StatusWaitingApproval
	return nil
}
func (*runStoreStub) ResolveApproval(context.Context, string, int64, bool) error { return nil }
func (s *runStoreStub) ResumeParentIfChildrenTerminal(_ context.Context, parentID int64) (bool, error) {
	s.resumedParent = parentID
	return true, nil
}
func (s *runStoreStub) DeleteThreadCheckpoints(_ context.Context, id int64) error {
	if id > 0 {
		s.deleted++
	}
	return nil
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
	if store.deleted != 0 {
		t.Fatalf("deleted checkpoints = %d, want 0 while run is terminal", store.deleted)
	}
	if store.markedToken != "lease-a" {
		t.Fatalf("marked token = %q, want lease-a", store.markedToken)
	}
}

func TestRunnerParksRunWhenApprovalIsPending(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 1, RunID: "approval-run", LeaseToken: "lease-a"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(context.Context, Run, EventSink) error {
		return agentruntime.ErrAgentApprovalPending
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked || !store.waitingApproval || store.status != StatusWaitingApproval {
		t.Fatalf("RunOnce() = (%v, %v), waiting=%v status=%s", worked, err, store.waitingApproval, store.status)
	}
}

func TestRunnerParksParentWhenChildRunsArePending(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 8, RunID: "parent-8", LeaseToken: "lease-a"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(context.Context, Run, EventSink) error {
		return agentruntime.ErrAgentWaitingChildren
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked || !store.waiting || store.status != StatusWaitingChildren {
		t.Fatalf("RunOnce() = (%v, %v), waiting=%v status=%s", worked, err, store.waiting, store.status)
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
	if store.deleted != 0 {
		t.Fatalf("deleted checkpoints = %d, want 0 while run is terminal", store.deleted)
	}
}

func TestRunnerPersistsStopReasonWhenStoreSupportsIt(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 3, RunID: "run-3"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(context.Context, Run, EventSink) error {
		return &StoppedError{Err: errors.New("provider rejected request"), Reason: StopReasonModelError}
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if store.stopReason != StopReasonModelError {
		t.Fatalf("stop reason = %q, want %q", store.stopReason, StopReasonModelError)
	}
}

func TestRunnerMarksChildTimeoutAsFailedAndResumesParent(t *testing.T) {
	store := &runStoreStub{run: Run{ID: 9, RunID: "child-9", ParentRunID: 4, RunKind: KindChild, LeaseToken: "lease-child"}, status: StatusPending}
	runner, err := NewRunner(store, ExecutorFunc(func(ctx context.Context, _ Run, _ EventSink) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	runner.SetChildTimeout(5 * time.Millisecond)
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = (%v, %v), want handled timeout", worked, err)
	}
	if store.status != StatusTimeout {
		t.Fatalf("status = %s, want timeout", store.status)
	}
	if store.failed != "child agent timed out after 5ms" {
		t.Fatalf("failure = %q, want timeout message", store.failed)
	}
	if store.resumedParent != 4 {
		t.Fatalf("resumed parent = %d, want 4", store.resumedParent)
	}
}

func TestRunnerPublishesParentResumeEventAfterLastChild(t *testing.T) {
	store := &runStoreStub{
		run:       Run{ID: 9, RunID: "child-9", ParentRunID: 4, RunKind: KindChild, LeaseToken: "lease-child"},
		status:    StatusPending,
		parentRun: Run{ID: 4, RunID: "parent-4", KnowledgeBaseID: 7},
	}
	var events []agent.Event
	runner, err := NewRunnerWithEventSink(store, ExecutorFunc(func(context.Context, Run, EventSink) error { return nil }), func(run Run) func(agent.Event) error {
		if run.RunID != "parent-4" {
			return func(agent.Event) error { return nil }
		}
		return func(event agent.Event) error {
			events = append(events, event)
			return nil
		}
	})
	if err != nil {
		t.Fatalf("NewRunnerWithEventSink() error = %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(events) != 1 || events[0].Type != agent.EventChildResult || events[0].RunID != "parent-4" {
		t.Fatalf("resume events = %#v, want one parent child_result", events)
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok || data["child_run_id"] != "child-9" || data["child_status"] != string(StatusSucceeded) || data["phase"] != "result" || data["result_available"] != true || data["parent_resumed"] != true {
		t.Fatalf("resume event data = %#v", events[0].Data)
	}
}

func TestRunnerCancelsExecutionWhenLeaseRenewalFails(t *testing.T) {
	store := &runStoreStub{renewErr: errors.New("lease lost")}
	runner := &Runner{store: store}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	runner.renewLeaseWithInterval(ctx, Run{ID: 1, RunID: "run-1", LeaseToken: "lease-a"}, time.Millisecond, cancel)
	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("lease renewal failure did not cancel execution")
	}
}

func TestExpiredRunStopsRetryingAfterMaximumAttempts(t *testing.T) {
	if !shouldRetryExpiredRun(1) {
		t.Fatal("attempt 1 should be retryable")
	}
	if shouldRetryExpiredRun(maxAgentRunAttempts) {
		t.Fatalf("attempt %d should stop retrying", maxAgentRunAttempts)
	}
}

func TestRunnerContinuesWhenCheckpointLoadFails(t *testing.T) {
	store := &runStoreStub{
		run:           Run{ID: 4, RunID: "run-4", LeaseToken: "lease-a"},
		status:        StatusPending,
		checkpointErr: errors.New("checkpoint store unavailable"),
	}
	runner, err := NewRunner(store, ExecutorFunc(func(_ context.Context, run Run, _ EventSink) error {
		if run.Checkpoint != nil {
			t.Fatalf("checkpoint = %#v, want empty fallback", run.Checkpoint)
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked || store.status != StatusSucceeded {
		t.Fatalf("RunOnce() = (%v, %v), status=%s, want succeeded fallback", worked, err, store.status)
	}
}

func TestRunnerLeavesRunReclaimableWhenLeaseIsLost(t *testing.T) {
	store := &runStoreStub{
		run:      Run{ID: 3, RunID: "run-3", LeaseToken: "lease-a"},
		status:   StatusPending,
		renewErr: errors.New("lease lost"),
	}
	runner, err := NewRunner(store, ExecutorFunc(func(ctx context.Context, _ Run, _ EventSink) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	runner.heartbeatInterval = time.Millisecond

	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("RunOnce() = (%v, %v), want handled lease loss", worked, err)
	}
	if store.status != StatusRunning {
		t.Fatalf("status = %s, want running for later lease recovery", store.status)
	}
}
