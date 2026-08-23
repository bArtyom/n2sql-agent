package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
)

// NewAgentRunChildren returns a safe execution tree rooted at one Agent Run.
// Request snapshots and lease tokens are never exposed.
func NewAgentRunChildren(reader agentrun.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		runID := strings.TrimSpace(r.PathValue("runID"))
		if runID == "" || reader == nil {
			http.Error(w, `{"error":"agent run children are unavailable"}`, http.StatusBadRequest)
			return
		}
		parent, err := reader.Get(r.Context(), runID, knowledgeBaseID)
		if err != nil {
			writeAgentRunChildrenError(w, err)
			return
		}
		childReader, ok := reader.(agentrun.ChildReader)
		if !ok {
			http.Error(w, `{"error":"agent run children are unavailable"}`, http.StatusNotImplemented)
			return
		}
		attemptReader, _ := reader.(agentrun.AttemptReader)
		node, err := buildAgentRunTree(r.Context(), childReader, attemptReader, parent, 0)
		if err != nil {
			http.Error(w, `{"error":"unable to load agent run children"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(node)
	})
}

type agentRunTreeNode struct {
	RunID        string                `json:"run_id"`
	ParentRunID  string                `json:"parent_run_id,omitempty"`
	RunKind      agentrun.Kind         `json:"run_kind"`
	Status       agentrun.Status       `json:"status"`
	AttemptCount int                   `json:"attempt_count"`
	Error        string                `json:"error,omitempty"`
	StopReason   string                `json:"stop_reason,omitempty"`
	CreatedAt    interface{}           `json:"created_at"`
	StartedAt    interface{}           `json:"started_at,omitempty"`
	FinishedAt   interface{}           `json:"finished_at,omitempty"`
	UpdatedAt    interface{}           `json:"updated_at"`
	Response     any                   `json:"response,omitempty"`
	Attempts     []safeAgentRunAttempt `json:"attempts,omitempty"`
	Children     []agentRunTreeNode    `json:"children,omitempty"`
}

func buildAgentRunTree(ctx context.Context, reader agentrun.ChildReader, attemptReader agentrun.AttemptReader, run agentrun.Run, depth int) (agentRunTreeNode, error) {
	node := agentRunTreeNode{
		RunID: run.RunID, RunKind: run.RunKind, Status: run.Status, AttemptCount: run.AttemptCount,
		Error: run.ErrorMessage, StopReason: run.StopReason, CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, UpdatedAt: run.UpdatedAt,
	}
	if len(run.Response) > 0 {
		_ = json.Unmarshal(run.Response, &node.Response)
	}
	if attemptReader != nil {
		attempts, err := attemptReader.ListAttempts(ctx, run.ID)
		if err != nil {
			return agentRunTreeNode{}, err
		}
		node.Attempts = safeAgentRunAttempts(attempts)
	}
	if depth >= 8 {
		return node, nil
	}
	children, err := reader.ListChildren(ctx, run.ID, run.KnowledgeBaseID)
	if err != nil {
		return agentRunTreeNode{}, err
	}
	node.Children = make([]agentRunTreeNode, 0, len(children))
	for _, child := range children {
		childNode, err := buildAgentRunTree(ctx, reader, attemptReader, child, depth+1)
		if err != nil {
			return agentRunTreeNode{}, err
		}
		childNode.ParentRunID = run.RunID
		node.Children = append(node.Children, childNode)
	}
	return node, nil
}

func writeAgentRunChildrenError(w http.ResponseWriter, err error) {
	if errors.Is(err, agentrun.ErrRunNotFound) {
		http.Error(w, `{"error":"agent run not found"}`, http.StatusNotFound)
		return
	}
	http.Error(w, `{"error":"unable to load agent run"}`, http.StatusInternalServerError)
}
