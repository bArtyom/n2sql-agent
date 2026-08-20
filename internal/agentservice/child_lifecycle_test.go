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
	run       agentrun.Run
	response  json.RawMessage
	succeeded bool
	failed    string
}

func (s *childRunStoreStub) CreateChild(_ context.Context, input agentrun.ChildCreateInput) (agentrun.Run, error) {
	s.run = agentrun.Run{ID: 9, RunID: input.RunID, ParentRunID: input.ParentRunID, KnowledgeBaseID: input.KnowledgeBaseID, RunKind: agentrun.KindChild, Status: agentrun.StatusRunning}
	return s.run, nil
}

func (s *childRunStoreStub) SaveChildResponse(_ context.Context, _ int64, response json.RawMessage) error {
	s.response = append([]byte(nil), response...)
	return nil
}

func (s *childRunStoreStub) MarkChildSucceeded(_ context.Context, _ int64) error {
	s.succeeded = true
	return nil
}

func (s *childRunStoreStub) MarkChildFailed(_ context.Context, _ int64, message string) error {
	s.failed = message
	return nil
}

func (s *childRunStoreStub) Get(_ context.Context, runID string, knowledgeBaseID int64) (agentrun.Run, error) {
	if s.run.RunID != runID || s.run.KnowledgeBaseID != knowledgeBaseID {
		return agentrun.Run{}, agentrun.ErrRunNotFound
	}
	return s.run, nil
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
