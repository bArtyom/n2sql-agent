package a2a

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

type Runner struct {
	store    TaskStore
	answerer multiagent.Answerer
	metrics  *metrics.Registry
	lease    time.Duration
	timeout  time.Duration
}

func NewRunner(store TaskStore, answerer multiagent.Answerer, timeout time.Duration, registry *metrics.Registry) *Runner {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Runner{store: store, answerer: answerer, lease: timeout * 2, timeout: timeout, metrics: registry}
}

func (r *Runner) Run(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := r.RunOnce(ctx); err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	if r == nil || r.store == nil || r.answerer == nil {
		return false, errors.New("A2A runner is unavailable")
	}
	task, err := r.store.ClaimNext(ctx, r.lease)
	if errors.Is(err, ErrNoTask) {
		return false, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "a2a_task_claim_failed", "error", err)
		return false, err
	}
	started := time.Now()
	if r.metrics != nil {
		r.metrics.ObserveA2ATask(metrics.A2AStatusStarted, 0)
	}
	slog.InfoContext(ctx, "a2a_task_started", "task_id", task.ID, "knowledge_base_id", task.KnowledgeBaseID)

	taskContext, cancel := context.WithTimeout(ctx, r.timeout)
	response, err := r.answerer.Answer(taskContext, task.KnowledgeBaseID, task.Message, task.TopK)
	cancel()
	duration := time.Since(started)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return true, nil
		}
		publicError := publicTaskError(err)
		if markErr := r.store.MarkFailed(context.WithoutCancel(ctx), task.ID, publicError); markErr != nil {
			return true, markErr
		}
		if r.metrics != nil {
			r.metrics.ObserveA2ATask(metrics.A2AStatusFailed, duration)
		}
		slog.ErrorContext(ctx, "a2a_task_failed", "task_id", task.ID, "knowledge_base_id", task.KnowledgeBaseID, "duration_ms", duration.Milliseconds(), "error_kind", publicError)
		return true, nil
	}
	if err := r.store.MarkCompleted(context.WithoutCancel(ctx), task.ID, response); err != nil {
		return true, err
	}
	if r.metrics != nil {
		r.metrics.ObserveA2ATask(metrics.A2AStatusCompleted, duration)
	}
	slog.InfoContext(ctx, "a2a_task_completed", "task_id", task.ID, "knowledge_base_id", task.KnowledgeBaseID, "duration_ms", duration.Milliseconds())
	return true, nil
}
