package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
)

// NewAgentRunStatus returns durable metadata for a run. The request snapshot
// is deliberately omitted because it may contain attachments or user input.
func NewAgentRunStatus(reader agentrun.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		runID := r.PathValue("runID")
		if runID == "" {
			http.Error(w, `{"error":"invalid agent run ID"}`, http.StatusBadRequest)
			return
		}
		if reader == nil {
			http.Error(w, `{"error":"agent run status is unavailable"}`, http.StatusNotImplemented)
			return
		}
		run, err := reader.Get(r.Context(), runID, knowledgeBaseID)
		if err != nil {
			if errors.Is(err, agentrun.ErrRunNotFound) {
				http.Error(w, `{"error":"agent run not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"unable to load agent run"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id":        run.RunID,
			"status":        run.Status,
			"attempt_count": run.AttemptCount,
			"error":         run.ErrorMessage,
			"created_at":    run.CreatedAt,
			"started_at":    run.StartedAt,
			"finished_at":   run.FinishedAt,
			"updated_at":    run.UpdatedAt,
		})
	})
}
