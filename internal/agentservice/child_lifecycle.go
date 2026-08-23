package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
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

var _ agentruntime.AsyncChildRunLifecycle = (*PersistentChildRunLifecycle)(nil)

type persistentChildCheckpointStore interface {
	Get(context.Context, string, int64) (agentrun.Run, error)
	ListToolCheckpoints(context.Context, int64) ([]agentrun.ToolCheckpoint, error)
	SaveToolCheckpoint(context.Context, agentrun.ToolCheckpoint) error
}

func (l *PersistentChildRunLifecycle) StartChild(ctx context.Context, spec agentruntime.ChildRunSpec) (string, error) {
	if l == nil || l.store == nil || spec.ParentRunID <= 0 || spec.KnowledgeBaseID <= 0 || strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.Question) == "" {
		return "", agentrun.ErrInvalidRun
	}
	snapshot, err := json.Marshal(map[string]any{
		"message":           spec.Question,
		"parent_run_id":     spec.ParentRunID,
		"knowledge_base_id": spec.KnowledgeBaseID,
		"tag_ids":           spec.TagIDs,
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

func (l *PersistentChildRunLifecycle) EnqueueChild(ctx context.Context, spec agentruntime.ChildRunSpec) (string, bool, agent.ToolResult, error) {
	if l == nil || l.store == nil {
		return "", false, agent.ToolResult{}, agentrun.ErrInvalidRun
	}
	store, ok := l.store.(agentrun.AsyncChildRunStore)
	if !ok || spec.ParentRunID <= 0 || spec.KnowledgeBaseID <= 0 || strings.TrimSpace(spec.RunID) == "" || strings.TrimSpace(spec.Question) == "" {
		return "", false, agent.ToolResult{}, agentrun.ErrInvalidRun
	}
	snapshot, err := json.Marshal(struct {
		Request ChatRequest `json:"request"`
	}{Request: ChatRequest{
		Message: spec.Question, RunID: spec.RunID, ParentRunDatabaseID: spec.ParentRunID,
		ParentRunPublicID: spec.ParentRunPublicID, DocumentIDs: append([]int64(nil), spec.DocumentIDs...),
		TagIDs:     append([]int64(nil), spec.TagIDs...),
		FolderPath: spec.FolderPath, FolderRecursive: spec.FolderRecursive,
		QueryRewrite: spec.QueryRewrite, TopK: spec.TopK, KeywordThreshold: spec.KeywordThreshold, ChildMode: true,
	}})
	if err != nil {
		return "", false, agent.ToolResult{}, fmt.Errorf("encode async child run snapshot: %w", err)
	}
	run, err := store.CreatePendingChild(ctx, agentrun.ChildCreateInput{RunID: spec.RunID, ParentRunID: spec.ParentRunID, KnowledgeBaseID: spec.KnowledgeBaseID, Request: snapshot})
	if err != nil {
		return "", false, agent.ToolResult{}, err
	}
	if !agentrun.IsTerminalStatus(run.Status) {
		return run.RunID, false, agent.ToolResult{}, nil
	}
	result := agent.ToolResult{Metadata: map[string]any{"child_run_id": run.RunID, "child_status": string(run.Status)}}
	if len(run.Response) > 0 {
		var payload struct {
			Answer  string             `json:"answer"`
			Sources []retrieval.Result `json:"sources"`
			Partial bool               `json:"partial"`
		}
		if err := json.Unmarshal(run.Response, &payload); err != nil {
			return "", false, agent.ToolResult{}, fmt.Errorf("decode async child response: %w", err)
		}
		result.Content = payload.Answer
		result.Metadata["sources"] = payload.Sources
		result.Metadata["partial_result"] = payload.Partial
	}
	if run.Status == agentrun.StatusFailed && strings.TrimSpace(result.Content) == "" {
		result.Content = fmt.Sprintf("子 Agent 已失败：%s", run.ErrorMessage)
	}
	return run.RunID, true, result, nil
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
		if errors.Is(runErr, context.Canceled) {
			return l.store.MarkChildCanceled(ctx, run.ID)
		}
		if strings.TrimSpace(result.Content) != "" {
			response, responseErr := marshalChildResponse(result)
			if responseErr != nil {
				return responseErr
			}
			if saveErr := l.store.SaveChildResponse(ctx, run.ID, response); saveErr != nil {
				return saveErr
			}
		}
		return l.store.MarkChildFailed(ctx, run.ID, runErr.Error())
	}
	response, err := marshalChildResponse(result)
	if err != nil {
		return err
	}
	if err := l.store.SaveChildResponse(ctx, run.ID, response); err != nil {
		return err
	}
	return l.store.MarkChildSucceeded(ctx, run.ID)
}

func marshalChildResponse(result agent.ToolResult) ([]byte, error) {
	response, err := json.Marshal(map[string]any{
		"answer":  result.Content,
		"sources": result.Metadata["sources"],
		"stats":   result.Metadata["child_steps"],
		"partial": result.Metadata["partial_result"] == true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode child response: %w", err)
	}
	return response, nil
}

func (l *PersistentChildRunLifecycle) LoadChildCheckpoints(ctx context.Context, spec agentruntime.ChildRunSpec) ([]agentruntime.ResumeCheckpoint, error) {
	store, ok := l.store.(persistentChildCheckpointStore)
	if !ok {
		return nil, nil
	}
	run, err := store.Get(ctx, spec.RunID, spec.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	checkpoints, err := store.ListToolCheckpoints(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return resumeChildCheckpoints(checkpoints), nil
}

func (l *PersistentChildRunLifecycle) SaveChildCheckpoint(ctx context.Context, spec agentruntime.ChildRunSpec, checkpoint agentruntime.ToolCheckpoint) error {
	store, ok := l.store.(persistentChildCheckpointStore)
	if !ok {
		return nil
	}
	run, err := store.Get(ctx, spec.RunID, spec.KnowledgeBaseID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(checkpoint.Payload)
	if err != nil {
		return fmt.Errorf("encode child checkpoint: %w", err)
	}
	return store.SaveToolCheckpoint(ctx, agentrun.ToolCheckpoint{
		AgentRunID: run.ID, AttemptCount: run.AttemptCount, StepNumber: checkpoint.StepNumber,
		ToolCallID: checkpoint.ToolCallID, DecisionID: checkpoint.DecisionID, ToolName: checkpoint.ToolName,
		Arguments: checkpoint.Arguments, ArgumentsHash: checkpoint.ArgumentsHash, Content: checkpoint.Content, Payload: payload,
	})
}

func resumeChildCheckpoints(checkpoints []agentrun.ToolCheckpoint) []agentruntime.ResumeCheckpoint {
	latestAttempt := 0
	for _, checkpoint := range checkpoints {
		if checkpoint.AttemptCount > latestAttempt {
			latestAttempt = checkpoint.AttemptCount
		}
	}
	result := make([]agentruntime.ResumeCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		if latestAttempt > 0 && checkpoint.AttemptCount != latestAttempt {
			continue
		}
		result = append(result, agentruntime.ResumeCheckpoint{
			ToolCallID: checkpoint.ToolCallID, DecisionID: checkpoint.DecisionID, ToolName: checkpoint.ToolName,
			Arguments: checkpoint.Arguments, ArgumentsHash: checkpoint.ArgumentsHash, StepNumber: checkpoint.StepNumber, Content: checkpoint.Content,
		})
	}
	return result
}
