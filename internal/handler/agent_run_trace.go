package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// NewAgentRunTrace returns a display-safe execution timeline for a durable run.
// It deliberately omits request snapshots, model reasoning, message deltas and
// raw tool output. The final answer remains available from the run status API.
func NewAgentRunTrace(store agentrun.EventStore) http.Handler {
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
		if runID == "" {
			http.Error(w, `{"error":"invalid agent run ID"}`, http.StatusBadRequest)
			return
		}
		if store == nil {
			http.Error(w, `{"error":"agent run trace is unavailable"}`, http.StatusNotImplemented)
			return
		}
		events, err := store.List(r.Context(), runID, knowledgeBaseID)
		if err != nil {
			if errors.Is(err, agentstream.ErrRunNotFound) {
				http.Error(w, `{"error":"agent run not found"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"unable to load agent run trace"}`, http.StatusInternalServerError)
			return
		}

		response := struct {
			RunID  string              `json:"run_id"`
			Events []safeAgentRunEvent `json:"events"`
		}{RunID: runID, Events: safeAgentRunEvents(events)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

type safeAgentRunEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Step      int            `json:"step,omitempty"`
	CreatedAt interface{}    `json:"created_at"`
	Summary   map[string]any `json:"summary,omitempty"`
}

func safeAgentRunEvents(events []agentstream.Event) []safeAgentRunEvent {
	result := make([]safeAgentRunEvent, 0, len(events))
	for _, event := range events {
		item := safeAgentRunEvent{ID: event.ID, Type: event.Type, Step: event.StepNumber, CreatedAt: event.CreatedAt}
		switch event.Type {
		case string(agent.EventRunStarted), string(agent.EventRunFinished), string(agent.EventRunFailed), string(agent.EventRunCanceled):
			// Lifecycle type and timing are enough for the diagnostic timeline.
		case string(agent.EventToolCalled), string(agent.EventToolFinished), string(agent.EventChildEvent):
			item.Summary = safeToolEventSummary(event)
		default:
			// Reasoning and message delta events are intentionally not exposed.
			continue
		}
		result = append(result, item)
	}
	return result
}

func safeToolEventSummary(event agentstream.Event) map[string]any {
	data, _ := event.Data.(map[string]any)
	summary := make(map[string]any, 3)
	if event.Type == string(agent.EventChildEvent) {
		for _, key := range []string{"child_run_id", "parent_run_id", "child_event_type", "child_step", "tool_name", "result_summary", "failed"} {
			if value, ok := data[key]; ok {
				summary[key] = value
			}
		}
		return summary
	}
	if toolName, ok := data["tool_name"].(string); ok && strings.TrimSpace(toolName) != "" {
		summary["tool_name"] = toolName
	}
	if event.Type == string(agent.EventToolCalled) {
		summary["status"] = "running"
		return summary
	}
	failed, _ := data["failed"].(bool)
	if failed {
		summary["status"] = "failed"
	} else {
		summary["status"] = "succeeded"
	}
	if resultSummary, ok := data["result_summary"].(string); ok && strings.TrimSpace(resultSummary) != "" {
		summary["result_summary"] = resultSummary
	}
	if retrievalStats, ok := data["retrieval"]; ok {
		summary["retrieval"] = retrievalStats
	}
	return summary
}
