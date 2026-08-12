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
