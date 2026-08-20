package agentservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

type childRunStoreStub struct {
	run         agentrun.Run
	response    json.RawMessage
	succeeded   bool
	failed      string
	canceled    bool
	checkpoints []agentrun.ToolCheckpoint
}

func (s *childRunStoreStub) CreateChild(_ context.Context, input agentrun.ChildCreateInput) (agentrun.Run, error) {
	s.run = agentrun.Run{ID: 9, RunID: input.RunID, ParentRunID: input.ParentRunID, KnowledgeBaseID: input.KnowledgeBaseID, RunKind: agentrun.KindChild, Status: agentrun.StatusRunning, AttemptCount: 1}
	return s.run, nil
}

func (s *childRunStoreStub) ListToolCheckpoints(_ context.Context, _ int64) ([]agentrun.ToolCheckpoint, error) {
	return s.checkpoints, nil
}

func (s *childRunStoreStub) SaveToolCheckpoint(_ context.Context, checkpoint agentrun.ToolCheckpoint) error {
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *childRunStoreStub) SaveChildResponse(_ context.Context, _ int64, response json.RawMessage) error {
	s.response = append([]byte(nil), response...)
	return nil
}

func TestPersistentChildRunLifecycleLoadsAndSavesCheckpoints(t *testing.T) {
	store := &childRunStoreStub{checkpoints: []agentrun.ToolCheckpoint{
		{AttemptCount: 1, StepNumber: 1, ToolCallID: "old", ToolName: "knowledge_search", Arguments: `{}`, ArgumentsHash: "old-hash", Content: "old", Payload: json.RawMessage(`{}`)},
		{AttemptCount: 2, StepNumber: 2, ToolCallID: "new", ToolName: "knowledge_search", Arguments: `{}`, ArgumentsHash: "new-hash", Content: "new", Payload: json.RawMessage(`{}`)},
	}}
	lifecycle := NewPersistentChildRunLifecycle(store)
	spec := agentruntime.ChildRunSpec{RunID: "child-1", ParentRunID: 42, KnowledgeBaseID: 7, Question: "研究年假"}
	store.run = agentrun.Run{ID: 9, RunID: "child-1", KnowledgeBaseID: 7, AttemptCount: 2, RunKind: agentrun.KindChild}
	checkpoints, err := lifecycle.LoadChildCheckpoints(context.Background(), spec)
	if err != nil {
		t.Fatalf("LoadChildCheckpoints() error = %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ToolCallID != "new" {
		t.Fatalf("checkpoints = %#v", checkpoints)
	}
	if err := lifecycle.SaveChildCheckpoint(context.Background(), spec, agentruntime.ToolCheckpoint{
		ToolCallID: "saved", ToolName: "knowledge_search", StepNumber: 3, Arguments: `{}`, ArgumentsHash: "saved-hash", Content: "saved", Payload: map[string]any{"ok": true},
	}); err != nil {
		t.Fatalf("SaveChildCheckpoint() error = %v", err)
	}
	if len(store.checkpoints) != 3 || store.checkpoints[2].ToolCallID != "saved" {
		t.Fatalf("saved checkpoints = %#v", store.checkpoints)
	}
}

func (s *childRunStoreStub) MarkChildSucceeded(_ context.Context, _ int64) error {
	s.succeeded = true
	return nil
}

func (s *childRunStoreStub) MarkChildFailed(_ context.Context, _ int64, message string) error {
	s.failed = message
	return nil
}

func (s *childRunStoreStub) MarkChildCanceled(_ context.Context, _ int64) error {
	s.canceled = true
	return nil
}

func (s *childRunStoreStub) Get(_ context.Context, runID string, knowledgeBaseID int64) (agentrun.Run, error) {
	if s.run.RunID != runID || s.run.KnowledgeBaseID != knowledgeBaseID {
		return agentrun.Run{}, agentrun.ErrRunNotFound
	}
	return s.run, nil
}

func (s *childRunStoreStub) CreatePendingChild(_ context.Context, input agentrun.ChildCreateInput) (agentrun.Run, error) {
	if s.run.RunID == "" {
		s.run = agentrun.Run{ID: 9, RunID: input.RunID, ParentRunID: input.ParentRunID, KnowledgeBaseID: input.KnowledgeBaseID, RunKind: agentrun.KindChild, Status: agentrun.StatusPending}
	}
	if s.run.Status == agentrun.StatusFailed && s.run.AttemptCount < 3 {
		s.run.Status = agentrun.StatusPending
		s.run.Response = nil
		s.run.ErrorMessage = ""
	}
	return s.run, nil
}

func TestPersistentChildRunLifecycleRequeuesFailedChildBelowAttemptLimit(t *testing.T) {
	store := &childRunStoreStub{run: agentrun.Run{ID: 9, RunID: "child-1", KnowledgeBaseID: 7, RunKind: agentrun.KindChild, Status: agentrun.StatusFailed, AttemptCount: 1, ErrorMessage: "temporary model error"}}
	lifecycle := NewPersistentChildRunLifecycle(store)
	runID, ready, _, err := lifecycle.EnqueueChild(context.Background(), agentruntime.ChildRunSpec{
		RunID: "child-1", ParentRunID: 42, KnowledgeBaseID: 7, Question: "研究年假",
	})
	if err != nil || runID != "child-1" || ready {
		t.Fatalf("EnqueueChild() = (%q, %v, %v), want pending retry", runID, ready, err)
	}
	if store.run.Status != agentrun.StatusPending || store.run.ErrorMessage != "" {
		t.Fatalf("requeued child = %#v", store.run)
	}
}

func TestPersistentChildRunLifecycleReturnsFinalFailedChildAtAttemptLimit(t *testing.T) {
	store := &childRunStoreStub{run: agentrun.Run{ID: 9, RunID: "child-1", KnowledgeBaseID: 7, RunKind: agentrun.KindChild, Status: agentrun.StatusFailed, AttemptCount: 3, ErrorMessage: "permanent failure"}}
	lifecycle := NewPersistentChildRunLifecycle(store)
	_, ready, result, err := lifecycle.EnqueueChild(context.Background(), agentruntime.ChildRunSpec{
		RunID: "child-1", ParentRunID: 42, KnowledgeBaseID: 7, Question: "研究年假",
	})
	if err != nil || !ready {
		t.Fatalf("EnqueueChild() = (ready=%v, err=%v), want final result", ready, err)
	}
	if result.Content != "子 Agent 已失败：permanent failure" {
		t.Fatalf("result content = %q", result.Content)
	}
	if store.run.Status != agentrun.StatusFailed {
		t.Fatalf("status = %s, want failed", store.run.Status)
	}
}

func TestPersistentChildRunLifecycleStoresParentAndResult(t *testing.T) {
	store := &childRunStoreStub{}
	lifecycle := NewPersistentChildRunLifecycle(store)
	spec := agentruntime.ChildRunSpec{RunID: "child-1", ParentRunID: 42, KnowledgeBaseID: 7, Question: "研究年假"}
	if _, err := lifecycle.StartChild(context.Background(), spec); err != nil {
		t.Fatalf("StartChild() error = %v", err)
	}
	if store.run.ParentRunID != 42 || store.run.RunKind != agentrun.KindChild {
		t.Fatalf("child run = %#v", store.run)
	}
	if err := lifecycle.FinishChild(context.Background(), spec, agent.ToolResult{Content: "结论", Metadata: map[string]any{"child_steps": 2}}, nil); err != nil {
		t.Fatalf("FinishChild() error = %v", err)
	}
	if !store.succeeded || len(store.response) == 0 {
		t.Fatalf("stored response = %s, succeeded = %v", store.response, store.succeeded)
	}
}
