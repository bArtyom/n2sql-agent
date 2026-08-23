package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
)

type safeAgentRunAttempt struct {
	AttemptCount int             `json:"attempt_count"`
	Status       agentrun.Status `json:"status"`
	Error        string          `json:"error,omitempty"`
	StopReason   string          `json:"stop_reason,omitempty"`
	StartedAt    interface{}     `json:"started_at"`
	FinishedAt   interface{}     `json:"finished_at,omitempty"`
	UpdatedAt    interface{}     `json:"updated_at"`
}

func safeAgentRunAttempts(attempts []agentrun.Attempt) []safeAgentRunAttempt {
	result := make([]safeAgentRunAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		result = append(result, safeAgentRunAttempt{
			AttemptCount: attempt.AttemptCount,
			Status:       attempt.Status,
			Error:        attempt.ErrorMessage,
			StopReason:   attempt.StopReason,
			StartedAt:    attempt.StartedAt,
			FinishedAt:   attempt.FinishedAt,
			UpdatedAt:    attempt.UpdatedAt,
		})
	}
	return result
}

// NewAgentRunAttempts returns safe retry history for one durable Agent Run.
// It intentionally omits request snapshots and checkpoint payloads.
func NewAgentRunAttempts(reader agentrun.Reader) http.Handler {
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
			http.Error(w, `{"error":"agent run attempts are unavailable"}`, http.StatusBadRequest)
			return
		}
		parent, err := reader.Get(r.Context(), runID, knowledgeBaseID)
		if err != nil {
			writeAgentRunAttemptsError(w, err)
			return
		}
		attemptReader, ok := reader.(agentrun.AttemptReader)
		if !ok {
			http.Error(w, `{"error":"agent run attempts are unavailable"}`, http.StatusNotImplemented)
			return
		}
		attempts, err := attemptReader.ListAttempts(r.Context(), parent.ID)
		if err != nil {
			http.Error(w, `{"error":"unable to load agent run attempts"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			RunID    string                `json:"run_id"`
			Attempts []safeAgentRunAttempt `json:"attempts"`
		}{RunID: parent.RunID, Attempts: safeAgentRunAttempts(attempts)})
	})
}

func writeAgentRunAttemptsError(w http.ResponseWriter, err error) {
	if errors.Is(err, agentrun.ErrRunNotFound) {
		http.Error(w, `{"error":"agent run not found"}`, http.StatusNotFound)
		return
	}
	http.Error(w, `{"error":"unable to load agent run"}`, http.StatusInternalServerError)
}
