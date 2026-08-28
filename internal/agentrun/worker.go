package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
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
	store             Store
	executor          Executor
	eventSink         func(Run) func(agent.Event) error
	heartbeatInterval time.Duration
	childTimeout      time.Duration
}

const leaseHeartbeatInterval = defaultLeaseDuration / 3
const DefaultChildExecutionTimeout = 30 * time.Minute

func NewRunner(store Store, executor Executor) (*Runner, error) {
	if store == nil || executor == nil {
		return nil, ErrInvalidRun
	}
	return &Runner{store: store, executor: executor, heartbeatInterval: leaseHeartbeatInterval, childTimeout: DefaultChildExecutionTimeout}, nil
}

// SetChildTimeout bounds one asynchronous child Agent execution. Root runs do
// not use this timeout; they remain resumable through the durable run queue.
func (r *Runner) SetChildTimeout(timeout time.Duration) {
	if r == nil || timeout <= 0 {
		return
	}
	r.childTimeout = timeout
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
	if run.ExecutionID == "" {
		run.ExecutionID = fmt.Sprintf("%s-attempt-%d-%d", run.RunID, run.AttemptCount, time.Now().UnixNano())
	}
	if checkpointStore, ok := r.store.(CheckpointStore); ok {
		checkpoint, checkpointErr := checkpointStore.GetLatestCheckpoint(ctx, run.ID)
		if checkpointErr != nil {
			slog.WarnContext(ctx, "agent_checkpoint_load_failed", "run_id", run.RunID, "error", checkpointErr)
		} else {
			run.Checkpoint = checkpoint
		}
	}

	started := time.Now()
	sink := func(agent.Event) error { return nil }
	if r.eventSink != nil {
		if candidate := r.eventSink(run); candidate != nil {
			sink = candidate
		}
	}
	executionContext, stopHeartbeat := context.WithCancelCause(ctx)
	defer stopHeartbeat(nil)
	if run.RunKind == KindChild && r.childTimeout > 0 {
		var stopChildTimeout context.CancelFunc
		executionContext, stopChildTimeout = context.WithTimeout(executionContext, r.childTimeout)
		defer stopChildTimeout()
	}
	go r.renewLease(executionContext, run, stopHeartbeat)
	err = r.executor.Execute(executionContext, run, sink)
	if err == nil {
		if markErr := r.store.MarkSucceeded(context.WithoutCancel(ctx), run.ID, run.LeaseToken); markErr != nil {
			return true, markErr
		}
		run.Status = StatusSucceeded
		r.resumeParentAfterChild(ctx, run)
		return true, nil
	}
	if errors.Is(err, agentruntime.ErrAgentApprovalPending) {
		interruptStore, ok := r.store.(ApprovalInterruptStore)
		if !ok {
			return true, fmt.Errorf("agent requested durable approval but store cannot park interrupt: %w", err)
		}
		if markErr := interruptStore.MarkWaitingApproval(context.WithoutCancel(ctx), run.ID, run.LeaseToken); markErr != nil {
			return true, fmt.Errorf("mark agent run waiting for approval: %w", markErr)
		}
		return true, nil
	}
	if errors.Is(err, agentruntime.ErrAgentWaitingChildren) {
		coordinator, ok := r.store.(ParentRunCoordinator)
		if !ok {
			return true, fmt.Errorf("agent requested child wait but store cannot park parent: %w", err)
		}
		if markErr := coordinator.MarkWaitingChildren(context.WithoutCancel(ctx), run.ID, run.LeaseToken); markErr != nil {
			return true, fmt.Errorf("mark agent run waiting for children: %w", markErr)
		}
		return true, nil
	}
	if errors.Is(context.Cause(executionContext), ErrLeaseLost) {
		slog.WarnContext(ctx, "agent_run_lease_lost", "run_id", run.RunID, "duration_ms", time.Since(started).Milliseconds())
		return true, nil
	}
	if run.RunKind == KindChild && (errors.Is(err, context.DeadlineExceeded) || errors.Is(context.Cause(executionContext), context.DeadlineExceeded)) {
		message := fmt.Sprintf("child agent timed out after %s", r.childTimeout)
		if markErr := r.markTimeout(context.WithoutCancel(ctx), run, message); markErr != nil {
			return true, markErr
		}
		run.Status = StatusTimeout
		r.resumeParentAfterChild(ctx, run)
		slog.WarnContext(ctx, "child_agent_timed_out", "run_id", run.RunID, "duration_ms", time.Since(started).Milliseconds(), "timeout", r.childTimeout)
		return true, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if markErr := r.markCanceled(context.WithoutCancel(ctx), run, StopReasonCanceled); markErr != nil {
			return true, markErr
		}
		run.Status = StatusCanceled
		r.resumeParentAfterChild(ctx, run)
		return true, nil
	}
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	reason := StopReasonInternalError
	var stopped *StoppedError
	if errors.As(err, &stopped) && stopped.Reason != "" {
		reason = stopped.Reason
	}
	if markErr := r.markFailed(context.WithoutCancel(ctx), run, message, reason); markErr != nil {
		return true, markErr
	}
	run.Status = StatusFailed
	r.resumeParentAfterChild(ctx, run)
	slog.ErrorContext(ctx, "agent_run_failed", "run_id", run.RunID, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	return true, nil
}

func (r *Runner) markFailed(ctx context.Context, run Run, message, reason string) error {
	if store, ok := r.store.(StopReasonStore); ok {
		return store.MarkFailedWithReason(ctx, run.ID, message, reason, run.LeaseToken)
	}
	return r.store.MarkFailed(ctx, run.ID, message, run.LeaseToken)
}

func (r *Runner) markTimeout(ctx context.Context, run Run, message string) error {
	if store, ok := r.store.(StopReasonStore); ok {
		return store.MarkTimedOut(ctx, run.ID, message, run.LeaseToken)
	}
	return r.store.MarkFailed(ctx, run.ID, message, run.LeaseToken)
}

func (r *Runner) markCanceled(ctx context.Context, run Run, reason string) error {
	if store, ok := r.store.(StopReasonStore); ok {
		return store.MarkCanceledWithReason(ctx, run.ID, reason, run.LeaseToken)
	}
	return r.store.MarkCanceled(ctx, run.ID, run.LeaseToken)
}

func (r *Runner) resumeParentAfterChild(ctx context.Context, run Run) {
	if run.RunKind != KindChild || run.ParentRunID <= 0 {
		return
	}
	coordinator, ok := r.store.(ParentRunCoordinator)
	if !ok {
		return
	}
	resumed, err := coordinator.ResumeParentIfChildrenTerminal(context.WithoutCancel(ctx), run.ParentRunID)
	if err != nil {
		slog.WarnContext(ctx, "agent_parent_resume_failed", "child_run_id", run.RunID, "parent_run_id", run.ParentRunID, "error", err)
		return
	}
	if resumed {
		slog.InfoContext(ctx, "agent_parent_requeued", "child_run_id", run.RunID, "parent_run_id", run.ParentRunID)
		r.publishParentResumeEvent(ctx, run)
	}
}

func (r *Runner) publishParentResumeEvent(ctx context.Context, child Run) {
	if r.eventSink == nil {
		return
	}
	reader, ok := r.store.(DatabaseReader)
	if !ok {
		return
	}
	parent, err := reader.GetByID(context.WithoutCancel(ctx), child.ParentRunID)
	if err != nil {
		slog.WarnContext(ctx, "agent_parent_resume_event_lookup_failed", "child_run_id", child.RunID, "parent_run_id", child.ParentRunID, "error", err)
		return
	}
	sink := r.eventSink(parent)
	if sink == nil {
		return
	}
	var childIdentity struct {
		Request struct {
			ToolCallID string `json:"tool_call_id"`
			TraceID    string `json:"trace_id"`
		} `json:"request"`
		ToolCallID string `json:"tool_call_id"`
		TraceID    string `json:"trace_id"`
	}
	_ = json.Unmarshal(child.Request, &childIdentity)
	toolCallID := childIdentity.ToolCallID
	if toolCallID == "" {
		toolCallID = childIdentity.Request.ToolCallID
	}
	traceID := childIdentity.TraceID
	if traceID == "" {
		traceID = childIdentity.Request.TraceID
	}
	event := agent.Event{
		Version:     agent.EventSchemaVersion,
		ID:          fmt.Sprintf("%s-child-resumed-%s-%d", parent.RunID, child.RunID, child.AttemptCount),
		RunID:       parent.RunID,
		Type:        agent.EventChildResult,
		ToolCallID:  toolCallID,
		ExecutionID: child.ExecutionID,
		TraceID:     traceID,
		Data: map[string]any{
			"child_run_id":     child.RunID,
			"parent_run_id":    parent.RunID,
			"child_event_type": "result_ready",
			"phase":            "result",
			"child_status":     string(child.Status),
			"result_available": true,
			"parent_resumed":   true,
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := sink(event); err != nil {
		slog.WarnContext(ctx, "agent_parent_resume_event_publish_failed", "child_run_id", child.RunID, "parent_run_id", parent.RunID, "error", err)
	}
}

func (r *Runner) renewLease(ctx context.Context, run Run, cancel context.CancelCauseFunc) {
	interval := r.heartbeatInterval
	if interval <= 0 {
		interval = leaseHeartbeatInterval
	}
	r.renewLeaseWithInterval(ctx, run, interval, cancel)
}

func (r *Runner) renewLeaseWithInterval(ctx context.Context, run Run, interval time.Duration, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.store.RenewLease(context.WithoutCancel(ctx), run.ID, run.LeaseToken); err != nil {
				slog.WarnContext(ctx, "agent_run_lease_renew_failed", "run_id", run.RunID, "error", err)
				cancel(ErrLeaseLost)
				return
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
