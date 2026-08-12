package a2a_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/a2a"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

type runnerAnswerer struct {
	err error
}

type cleanupStoreStub struct {
	a2a.TaskStore
	err error
}

func (s cleanupStoreStub) DeleteTerminalBefore(context.Context, time.Time) (int, error) {
	return 0, s.err
}

func (a runnerAnswerer) Answer(context.Context, int64, string, int) (multiagent.Response, error) {
	return multiagent.Response{Answer: "完成"}, a.err
}

func TestRunnerClaimsAndCompletesMemoryTask(t *testing.T) {
	store := a2a.NewMemoryStore()
	if _, err := store.Create(context.Background(), a2a.CreateInput{ID: "task-1", KnowledgeBaseID: 7, Message: "问题", TopK: 5}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runner := a2a.NewRunner(store, runnerAnswerer{}, time.Minute, nil)
	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce() = (%v, %v), want processed task", processed, err)
	}
	task, err := store.Get(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if task.Status != a2a.StatusCompleted || task.Response.Answer != "完成" || task.AttemptCount != 1 {
		t.Fatalf("task = %#v, want completed task", task)
	}
}

func TestRunnerMarksPublicFailure(t *testing.T) {
	store := a2a.NewMemoryStore()
	if _, err := store.Create(context.Background(), a2a.CreateInput{ID: "task-2", KnowledgeBaseID: 7, Message: "问题", TopK: 5}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runner := a2a.NewRunner(store, runnerAnswerer{err: errors.New("provider secret detail")}, time.Minute, nil)
	if _, err := runner.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	task, err := store.Get(context.Background(), "task-2")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if task.Status != a2a.StatusFailed || task.Error != "task execution failed" {
		t.Fatalf("task = %#v, want sanitized failure", task)
	}
}

func TestMemoryStoreDeletesOnlyTerminalTasks(t *testing.T) {
	store := a2a.NewMemoryStore()
	if _, err := store.Create(context.Background(), a2a.CreateInput{ID: "completed-task", KnowledgeBaseID: 7, Message: "问题", TopK: 5}); err != nil {
		t.Fatalf("Create(completed-task) error = %v", err)
	}
	if _, err := store.ClaimNext(context.Background(), time.Minute); err != nil {
		t.Fatalf("ClaimNext() for completed task: %v", err)
	}
	if err := store.MarkCompleted(context.Background(), "completed-task", multiagent.Response{Answer: "完成"}); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	if _, err := store.Create(context.Background(), a2a.CreateInput{ID: "failed-task", KnowledgeBaseID: 7, Message: "问题", TopK: 5}); err != nil {
		t.Fatalf("Create(failed-task) error = %v", err)
	}
	if _, err := store.ClaimNext(context.Background(), time.Minute); err != nil {
		t.Fatalf("ClaimNext() for failed task: %v", err)
	}
	if err := store.MarkFailed(context.Background(), "failed-task", "task execution failed"); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	if _, err := store.Create(context.Background(), a2a.CreateInput{ID: "submitted-task", KnowledgeBaseID: 7, Message: "问题", TopK: 5}); err != nil {
		t.Fatalf("Create(submitted-task) error = %v", err)
	}

	deleted, err := store.DeleteTerminalBefore(context.Background(), time.Now().Add(time.Second))
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteTerminalBefore() = (%d, %v), want 2 deletions", deleted, err)
	}
	if _, err := store.Get(context.Background(), "submitted-task"); err != nil {
		t.Fatalf("submitted task should remain: %v", err)
	}
	if _, err := store.Get(context.Background(), "completed-task"); !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("completed task error = %v, want ErrTaskNotFound", err)
	}
}

func TestRunnerCleanupReportsStoreFailure(t *testing.T) {
	wantErr := errors.New("cleanup database unavailable")
	runner := a2a.NewRunnerWithCleanup(cleanupStoreStub{err: wantErr}, runnerAnswerer{}, time.Minute, time.Hour, time.Hour, nil)
	if _, err := runner.CleanupOnce(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("CleanupOnce() error = %v, want %v", err, wantErr)
	}
}

func TestRunnerCleanupRequiresCleanerWhenEnabled(t *testing.T) {
	store := &taskStoreWithoutCleanup{TaskStore: a2a.NewMemoryStore()}
	runner := a2a.NewRunnerWithCleanup(store, runnerAnswerer{}, time.Minute, time.Hour, time.Hour, nil)
	if _, err := runner.CleanupOnce(context.Background()); err == nil {
		t.Fatal("CleanupOnce() error = nil, want unavailable cleanup error")
	}
}

type taskStoreWithoutCleanup struct {
	a2a.TaskStore
}
