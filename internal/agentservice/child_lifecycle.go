package agentservice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
)

// PersistentChildRunLifecycle records the synchronous child Agent as a
// regular agent_runs row linked to its parent. It deliberately does not put
// the child on the root Worker queue: the child is currently executed inside
// the parent's tool call.
type PersistentChildRunLifecycle struct {
	store agentrun.ChildRunStore
}

func NewPersistentChildRunLifecycle(store agentrun.ChildRunStore) *PersistentChildRunLifecycle {
	return &PersistentChildRunLifecycle{store: store}
}

var _ agentruntime.ChildRunLifecycle = (*PersistentChildRunLifecycle)(nil)

func (l *PersistentChildRunLifecycle) StartChild(ctx context.Context, spec agentruntime.ChildRunSpec) (string, error) {
	if l == nil || l.store == nil || spec.ParentRunID <= 0 || spec.KnowledgeBaseID <= 0 || strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.Question) == "" {
		return "", agentrun.ErrInvalidRun
	}
	snapshot, err := json.Marshal(map[string]any{
		"message":           spec.Question,
		"parent_run_id":     spec.ParentRunID,
		"knowledge_base_id": spec.KnowledgeBaseID,
	})
	if err != nil {
		return "", fmt.Errorf("encode child run snapshot: %w", err)
	}
	run, err := l.store.CreateChild(ctx, agentrun.ChildCreateInput{
		RunID:           spec.RunID,
		ParentRunID:     spec.ParentRunID,
		KnowledgeBaseID: spec.KnowledgeBaseID,
		Request:         snapshot,
	})
	if err != nil {
		return "", err
	}
	return run.RunID, nil
}

func (l *PersistentChildRunLifecycle) FinishChild(ctx context.Context, spec agentruntime.ChildRunSpec, result agent.ToolResult, runErr error) error {
	if l == nil || l.store == nil || strings.TrimSpace(spec.RunID) == "" || spec.KnowledgeBaseID <= 0 {
		return agentrun.ErrInvalidRun
	}
	// The lifecycle only receives the public child run ID. The store adapter
	// resolves it through the concrete implementation before updating status.
	store, ok := l.store.(interface {
		Get(context.Context, string, int64) (agentrun.Run, error)
	})
	if !ok {
		return agentrun.ErrInvalidRun
	}
	run, err := store.Get(ctx, spec.RunID, spec.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if runErr != nil {
		return l.store.MarkChildFailed(ctx, run.ID, runErr.Error())
	}
	response, err := json.Marshal(map[string]any{
		"answer":  result.Content,
		"sources": result.Metadata["sources"],
		"stats":   result.Metadata["child_steps"],
	})
	if err != nil {
		return err
	}
	if err := l.store.SaveChildResponse(ctx, run.ID, response); err != nil {
		return err
	}
	return l.store.MarkChildSucceeded(ctx, run.ID)
}
