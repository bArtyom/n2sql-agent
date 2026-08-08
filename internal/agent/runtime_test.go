package agent_test

import (
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

func TestAgentRunStartsAndCompletes(t *testing.T) {
	run, err := agent.NewAgentRun("run-1")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if run.Status() != agent.RunPending {
		t.Fatalf("new run status = %q, want %q", run.Status(), agent.RunPending)
	}

	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if run.Status() != agent.RunRunning {
		t.Fatalf("started run status = %q, want %q", run.Status(), agent.RunRunning)
	}

	if err := run.Complete("最终回答"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if run.Status() != agent.RunSucceeded {
		t.Fatalf("completed run status = %q, want %q", run.Status(), agent.RunSucceeded)
	}
	if run.FinalAnswer() != "最终回答" {
		t.Fatalf("FinalAnswer() = %q, want %q", run.FinalAnswer(), "最终回答")
	}
}

func TestAgentRunTracksRuntimeStats(t *testing.T) {
	run, err := agent.NewAgentRun("run-stats")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := run.RecordModelCall(); err != nil {
		t.Fatalf("RecordModelCall() error = %v", err)
	}
	run.ObserveChatTokens(usage.TokenUsage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14})
	run.ObserveEmbeddingTokens(usage.TokenUsage{PromptTokens: 7, TotalTokens: 7})
	if err := run.RecordToolCall(true); err != nil {
		t.Fatalf("RecordToolCall(success) error = %v", err)
	}
	if err := run.RecordToolCall(false); err != nil {
		t.Fatalf("RecordToolCall(failure) error = %v", err)
	}
	if err := run.Complete("答案"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}

	stats := run.Stats()
	if stats.Status != agent.RunSucceeded || stats.ModelCalls != 1 || stats.ToolCalls != 2 || stats.SuccessfulToolCalls != 1 || stats.FailedToolCalls != 1 || stats.PromptTokens != 11 || stats.CompletionTokens != 3 || stats.EmbeddingTokens != 7 || stats.TotalTokens != 21 || stats.FailureCategory != "" {
		t.Fatalf("stats = %#v, want completed call counts", stats)
	}
	if stats.StartedAt.IsZero() || stats.FinishedAt.IsZero() || stats.FinishedAt.Before(stats.StartedAt) {
		t.Fatalf("stats timestamps = %#v, want ordered timestamps", stats)
	}
	if stats.DurationMS < 0 || stats.DurationMS > 60_000 {
		t.Fatalf("stats duration = %dms, want a recent non-negative duration", stats.DurationMS)
	}

	failedRun, err := agent.NewAgentRun("run-stats-failed")
	if err != nil {
		t.Fatalf("NewAgentRun() failed run error = %v", err)
	}
	if err := failedRun.Start(); err != nil {
		t.Fatalf("failed run Start() error = %v", err)
	}
	if err := failedRun.SetFailureCategory(agent.FailureTool); err != nil {
		t.Fatalf("failed run SetFailureCategory() error = %v", err)
	}
	if err := failedRun.Fail(errors.New("tool failed")); err != nil {
		t.Fatalf("failed run Fail() error = %v", err)
	}
	if got := failedRun.Stats().FailureCategory; got != agent.FailureTool {
		t.Fatalf("failed run failure category = %q, want %q", got, agent.FailureTool)
	}
}

func TestAgentRunRejectsInvalidLifecycleTransitions(t *testing.T) {
	if _, err := agent.NewAgentRun("  "); !errors.Is(err, agent.ErrInvalidRunID) {
		t.Fatalf("NewAgentRun() error = %v, want %v", err, agent.ErrInvalidRunID)
	}
	if _, err := agent.NewAgentRun(" run-7 "); !errors.Is(err, agent.ErrInvalidRunID) {
		t.Fatalf("NewAgentRun() with padded ID error = %v, want %v", err, agent.ErrInvalidRunID)
	}

	run, err := agent.NewAgentRun("run-2")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := run.Complete("回答"); !errors.Is(err, agent.ErrInvalidRunTransition) {
		t.Fatalf("Complete() before Start error = %v, want %v", err, agent.ErrInvalidRunTransition)
	}
	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := run.Start(); !errors.Is(err, agent.ErrInvalidRunTransition) {
		t.Fatalf("second Start() error = %v, want %v", err, agent.ErrInvalidRunTransition)
	}
	if err := run.Complete("回答"); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := run.Complete("第二个回答"); !errors.Is(err, agent.ErrInvalidRunTransition) {
		t.Fatalf("second Complete() error = %v, want %v", err, agent.ErrInvalidRunTransition)
	}
}

func TestAgentRunRecordsFailureAndCancellation(t *testing.T) {
	run, err := agent.NewAgentRun("run-3")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := run.Fail(errors.New("model timeout")); err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if run.Status() != agent.RunFailed {
		t.Fatalf("failed run status = %q, want %q", run.Status(), agent.RunFailed)
	}
	if run.ErrorMessage() != "model timeout" {
		t.Fatalf("ErrorMessage() = %q, want %q", run.ErrorMessage(), "model timeout")
	}

	canceled, err := agent.NewAgentRun("run-4")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := canceled.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := canceled.Cancel("user stopped"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if canceled.Status() != agent.RunCanceled {
		t.Fatalf("canceled run status = %q, want %q", canceled.Status(), agent.RunCanceled)
	}
	if canceled.ErrorMessage() != "user stopped" {
		t.Fatalf("canceled ErrorMessage() = %q, want %q", canceled.ErrorMessage(), "user stopped")
	}
}

func TestAgentRunAppendsOrderedSteps(t *testing.T) {
	run, err := agent.NewAgentRun("run-5")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := run.AddStep(agent.Step{
		Kind:     agent.StepToolCall,
		Status:   agent.StepSucceeded,
		ToolName: "knowledge_search",
	}); err != nil {
		t.Fatalf("AddStep() error = %v", err)
	}
	if err := run.AddStep(agent.Step{
		Kind:   agent.StepFinalAnswer,
		Status: agent.StepSucceeded,
	}); err != nil {
		t.Fatalf("AddStep() error = %v", err)
	}

	steps := run.Steps()
	if len(steps) != 2 {
		t.Fatalf("Steps() length = %d, want 2", len(steps))
	}
	if steps[0].Number != 1 || steps[1].Number != 2 {
		t.Fatalf("step numbers = %d, %d, want 1, 2", steps[0].Number, steps[1].Number)
	}
	if steps[0].ToolName != "knowledge_search" || steps[1].Kind != agent.StepFinalAnswer {
		t.Fatalf("steps = %#v", steps)
	}
}

func TestAgentRunRejectsInvalidSteps(t *testing.T) {
	run, err := agent.NewAgentRun("run-6")
	if err != nil {
		t.Fatalf("NewAgentRun() error = %v", err)
	}
	if err := run.AddStep(agent.Step{Kind: agent.StepToolCall}); !errors.Is(err, agent.ErrInvalidRunTransition) {
		t.Fatalf("AddStep() before Start error = %v, want %v", err, agent.ErrInvalidRunTransition)
	}
	if err := run.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	cases := []agent.Step{
		{Kind: agent.StepKind("unknown"), Status: agent.StepSucceeded},
		{Kind: agent.StepToolCall, Status: agent.StepStatus("unknown")},
	}
	for _, step := range cases {
		if err := run.AddStep(step); !errors.Is(err, agent.ErrInvalidStep) {
			t.Fatalf("AddStep(%#v) error = %v, want %v", step, err, agent.ErrInvalidStep)
		}
	}
}

func TestNewEventValidatesIdentityAndType(t *testing.T) {
	event, err := agent.NewEvent(
		"event-1",
		"run-6",
		agent.EventToolCalled,
		map[string]string{"tool": "knowledge_search"},
	)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}
	if event.ID != "event-1" || event.RunID != "run-6" || event.Type != agent.EventToolCalled {
		t.Fatalf("event identity = %#v", event)
	}
	if event.Data == nil || event.CreatedAt.IsZero() {
		t.Fatalf("event data/time = %#v", event)
	}

	cases := []struct {
		name    string
		id      string
		runID   string
		typ     agent.EventType
		wantErr error
	}{
		{name: "empty event ID", id: "", runID: "run-6", typ: agent.EventToolCalled, wantErr: agent.ErrInvalidEvent},
		{name: "empty run ID", id: "event-1", runID: "", typ: agent.EventToolCalled, wantErr: agent.ErrInvalidEvent},
		{name: "unknown event type", id: "event-1", runID: "run-6", typ: agent.EventType("unknown"), wantErr: agent.ErrInvalidEvent},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := agent.NewEvent(test.id, test.runID, test.typ, nil)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NewEvent() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
