package worker_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/worker"
)

type retryTaskStoreStub struct {
	task       worker.Task
	requeued   bool
	retryAt    time.Time
	requeueErr error
	dead       bool
	deadError  string
	deadErr    error
}

func (s *retryTaskStoreStub) ClaimNext(context.Context) (worker.Task, error) {
	return s.task, nil
}

func (s *retryTaskStoreStub) MarkSucceeded(context.Context, int64) error { return nil }

func (s *retryTaskStoreStub) MarkFailed(context.Context, int64, string) error {
	return errors.New("legacy failure path should not be used")
}

func (s *retryTaskStoreStub) Requeue(_ context.Context, _ int64, _ string, retryAt time.Time) error {
	if s.requeueErr != nil {
		return s.requeueErr
	}
	s.requeued = true
	s.retryAt = retryAt
	return nil
}

func (s *retryTaskStoreStub) MarkDeadLetter(_ context.Context, _ int64, message string) error {
	if s.deadErr != nil {
		return s.deadErr
	}
	s.dead = true
	s.deadError = message
	return nil
}

func TestRetryPolicyUsesExponentialBackoffUntilMaximumAttempts(t *testing.T) {
	policy := worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: 5 * time.Second}
	now := time.Date(2026, time.August, 11, 16, 0, 0, 0, time.UTC)

	first, ok := policy.NextRetryAt(now, 1)
	if !ok || !first.Equal(now.Add(time.Second)) {
		t.Fatalf("first retry = %v, ok=%v, want %v", first, ok, now.Add(time.Second))
	}
	second, ok := policy.NextRetryAt(now, 2)
	if !ok || !second.Equal(now.Add(2*time.Second)) {
		t.Fatalf("second retry = %v, ok=%v, want %v", second, ok, now.Add(2*time.Second))
	}
	if _, ok := policy.NextRetryAt(now, 3); ok {
		t.Fatal("third attempt should not be retried")
	}
}

func TestRetryPolicyCapsBackoff(t *testing.T) {
	policy := worker.RetryPolicy{MaxAttempts: 6, InitialDelay: 4 * time.Second, MaxDelay: 5 * time.Second}
	now := time.Date(2026, time.August, 11, 16, 0, 0, 0, time.UTC)

	retryAt, ok := policy.NextRetryAt(now, 3)
	if !ok || !retryAt.Equal(now.Add(5*time.Second)) {
		t.Fatalf("retry = %v, ok=%v, want capped delay", retryAt, ok)
	}
}

func TestPermanentErrorIsNotRetryable(t *testing.T) {
	underlying := errors.New("invalid PDF")
	err := worker.Permanent(underlying)
	if !errors.Is(err, worker.ErrPermanent) {
		t.Fatalf("Permanent() error = %v, want ErrPermanent", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("Permanent() error = %v, want underlying error preserved", err)
	}
}

func TestRunnerRequeuesRetryableFailure(t *testing.T) {
	policy := worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Minute, MaxDelay: time.Minute}
	startedAt := time.Now()
	store := &retryTaskStoreStub{task: worker.Task{ID: 21, DocumentID: 10, AttemptCount: 1}}
	runner := worker.NewRunnerWithPolicy(store, func(context.Context, worker.Task) error {
		return errors.New("temporary embedding failure")
	}, policy)

	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v, want requeued task", processed, err)
	}
	if !store.requeued || store.dead {
		t.Fatalf("requeued=%v dead=%v, want requeue only", store.requeued, store.dead)
	}
	if store.retryAt.Before(startedAt.Add(policy.InitialDelay)) {
		t.Fatalf("retry_at=%v, want at least one initial delay after %v", store.retryAt, startedAt)
	}
}

func TestRunnerDeadLettersAfterMaximumAttempts(t *testing.T) {
	store := &retryTaskStoreStub{task: worker.Task{ID: 22, DocumentID: 11, AttemptCount: 3}}
	runner := worker.NewRunnerWithPolicy(store, func(context.Context, worker.Task) error {
		return errors.New("temporary embedding failure")
	}, worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: time.Minute})

	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v, want dead letter", processed, err)
	}
	if store.requeued || !store.dead || store.deadError != "temporary embedding failure" {
		t.Fatalf("requeued=%v dead=%v error=%q, want dead letter", store.requeued, store.dead, store.deadError)
	}
}

func TestRunnerDeadLettersPermanentFailureImmediately(t *testing.T) {
	store := &retryTaskStoreStub{task: worker.Task{ID: 23, DocumentID: 12, AttemptCount: 1}}
	runner := worker.NewRunnerWithPolicy(store, func(context.Context, worker.Task) error {
		return worker.Permanent(errors.New("invalid PDF"))
	}, worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: time.Minute})

	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v, want dead letter handled", err)
	}
	if store.requeued || !store.dead {
		t.Fatalf("requeued=%v dead=%v, want permanent failure dead letter", store.requeued, store.dead)
	}
}

func TestRunnerReportsRetryStateWriteFailure(t *testing.T) {
	store := &retryTaskStoreStub{
		task:       worker.Task{ID: 24, AttemptCount: 1},
		requeueErr: errors.New("queue write failed"),
	}
	runner := worker.NewRunnerWithPolicy(store, func(context.Context, worker.Task) error {
		return errors.New("temporary embedding failure")
	}, worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: time.Minute})

	processed, err := runner.RunOnce(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "requeue document processing task") {
		t.Fatalf("processed=%v err=%v, want requeue error", processed, err)
	}
}

func TestRunnerReportsDeadLetterWriteFailure(t *testing.T) {
	store := &retryTaskStoreStub{
		task:    worker.Task{ID: 25, AttemptCount: 3},
		deadErr: errors.New("dead letter write failed"),
	}
	runner := worker.NewRunnerWithPolicy(store, func(context.Context, worker.Task) error {
		return errors.New("temporary embedding failure")
	}, worker.RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: time.Minute})

	processed, err := runner.RunOnce(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "mark document processing task dead letter") {
		t.Fatalf("processed=%v err=%v, want dead letter error", processed, err)
	}
}
