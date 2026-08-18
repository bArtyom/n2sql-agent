package agentrun

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

type Executor interface {
	Execute(context.Context, Run, agentruntimeEventSink) error
}

// agentruntimeEventSink is kept local so the persistence worker does not
// depend on the HTTP transport. The handler can adapt it to the Agent event
// type and publish into the existing Hub.
type agentruntimeEventSink func(agent.Event) error

type ExecutorFunc func(context.Context, Run, func(agent.Event) error) error

func (f ExecutorFunc) Execute(ctx context.Context, run Run, sink agentruntimeEventSink) error {
	return f(ctx, run, sink)
}

type Runner struct {
	store     Store
	executor  Executor
	eventSink func(Run) func(agent.Event) error
}

func NewRunner(store Store, executor Executor) (*Runner, error) {
	if store == nil || executor == nil {
		return nil, ErrInvalidRun
	}
	return &Runner{store: store, executor: executor}, nil
}

func NewRunnerWithEventSink(store Store, executor Executor, eventSink func(Run) func(agent.Event) error) (*Runner, error) {
	runner, err := NewRunner(store, executor)
	if err != nil {
		return nil, err
	}
	runner.eventSink = eventSink
	return runner, nil
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	run, err := r.store.ClaimNext(ctx)
	if errors.Is(err, ErrNoRun) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim agent run: %w", err)
	}

	started := time.Now()
	sink := func(agent.Event) error { return nil }
	if r.eventSink != nil {
		if candidate := r.eventSink(run); candidate != nil {
			sink = candidate
		}
	}
	err = r.executor.Execute(ctx, run, sink)
	if err == nil {
		if markErr := r.store.MarkSucceeded(context.WithoutCancel(ctx), run.ID); markErr != nil {
			return true, markErr
		}
		return true, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if markErr := r.store.MarkCanceled(context.WithoutCancel(ctx), run.ID); markErr != nil {
			return true, markErr
		}
		return true, nil
	}
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	if markErr := r.store.MarkFailed(context.WithoutCancel(ctx), run.ID, message); markErr != nil {
		return true, markErr
	}
	slog.ErrorContext(ctx, "agent_run_failed", "run_id", run.RunID, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	return true, nil
}

func (r *Runner) Run(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
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
