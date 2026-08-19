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
	Execute(context.Context, Run, EventSink) error
}

// EventSink is the transport-neutral event channel used by a Worker executor.
// The executor can publish Agent lifecycle events without knowing whether the
// consumer is an SSE handler, a WebSocket gateway, or a durable event store.
type EventSink func(agent.Event) error

type ExecutorFunc func(context.Context, Run, EventSink) error

func (f ExecutorFunc) Execute(ctx context.Context, run Run, sink EventSink) error {
	return f(ctx, run, sink)
}

type Runner struct {
	store     Store
	executor  Executor
	eventSink func(Run) func(agent.Event) error
}

const leaseHeartbeatInterval = defaultLeaseDuration / 3

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
	if err := r.store.RequeueExpired(ctx); err != nil {
		return false, fmt.Errorf("requeue expired agent runs: %w", err)
	}
	run, err := r.store.ClaimNext(ctx)
	if errors.Is(err, ErrNoRun) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim agent run: %w", err)
	}
	if checkpointStore, ok := r.store.(ToolCheckpointStore); ok {
		checkpoints, checkpointErr := checkpointStore.ListToolCheckpoints(ctx, run.ID)
		if checkpointErr != nil {
			_ = r.store.MarkFailed(context.WithoutCancel(ctx), run.ID, checkpointErr.Error())
			return true, fmt.Errorf("load agent tool checkpoints: %w", checkpointErr)
		}
		run.Checkpoints = checkpoints
	}

	started := time.Now()
	sink := func(agent.Event) error { return nil }
	if r.eventSink != nil {
		if candidate := r.eventSink(run); candidate != nil {
			sink = candidate
		}
	}
	executionContext, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go r.renewLease(executionContext, run)
	err = r.executor.Execute(executionContext, run, sink)
	if err == nil {
		if markErr := r.store.MarkSucceeded(context.WithoutCancel(ctx), run.ID); markErr != nil {
			return true, markErr
		}
		r.cleanupTerminalCheckpoints(ctx, run.ID)
		return true, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if markErr := r.store.MarkCanceled(context.WithoutCancel(ctx), run.ID); markErr != nil {
			return true, markErr
		}
		r.cleanupTerminalCheckpoints(ctx, run.ID)
		return true, nil
	}
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	if markErr := r.store.MarkFailed(context.WithoutCancel(ctx), run.ID, message); markErr != nil {
		return true, markErr
	}
	r.cleanupTerminalCheckpoints(ctx, run.ID)
	slog.ErrorContext(ctx, "agent_run_failed", "run_id", run.RunID, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	return true, nil
}

func (r *Runner) cleanupTerminalCheckpoints(ctx context.Context, runID int64) {
	cleaner, ok := r.store.(ToolCheckpointCleaner)
	if !ok {
		return
	}
	if err := cleaner.DeleteToolCheckpoints(context.WithoutCancel(ctx), runID); err != nil {
		slog.WarnContext(ctx, "agent_checkpoint_cleanup_failed", "run_id", runID, "error", err)
	}
}

func (r *Runner) renewLease(ctx context.Context, run Run) {
	ticker := time.NewTicker(leaseHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.store.RenewLease(context.WithoutCancel(ctx), run.ID); err != nil {
				slog.WarnContext(ctx, "agent_run_lease_renew_failed", "run_id", run.RunID, "error", err)
			}
		}
	}
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
