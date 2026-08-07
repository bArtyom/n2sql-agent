package handler

import (
	"fmt"
	"log"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
)

func NewKnowledgeBaseAgentChatStream(answerer agentservice.EventAnswerer) http.Handler {
	return NewKnowledgeBaseAgentChatStreamWithLimits(answerer, agent.DefaultMaxHistoryBytes)
}

func NewKnowledgeBaseAgentChatStreamWithLimits(answerer agentservice.EventAnswerer, maxHistoryBytes int) http.Handler {
	if maxHistoryBytes <= 0 {
		maxHistoryBytes = agent.DefaultMaxHistoryBytes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, request, ok := decodeKnowledgeBaseAgentChatRequest(w, r, maxHistoryBytes)
		if !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming is not supported"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		emit := func(event agent.Event) error {
			return writeAgentSSEEvent(w, flusher, string(event.Type), event)
		}
		if _, err := answerer.AnswerWithEvents(r.Context(), knowledgeBaseID, request, emit); err != nil {
			if r.Context().Err() != nil {
				return
			}
			message, _ := knowledgeBaseAgentChatError(err)
			if writeErr := writeAgentSSEEvent(w, flusher, "error", struct {
				Error string `json:"error"`
			}{Error: message}); writeErr != nil {
				log.Printf("agent SSE error event write failed: %v", writeErr)
			}
		}
	})
}

func writeAgentSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, value any) error {
	switch eventType {
	case "error",
		string(agent.EventRunStarted),
		string(agent.EventStepStarted),
		string(agent.EventToolCalled),
		string(agent.EventToolFinished),
		string(agent.EventMessageDelta),
		string(agent.EventRunFinished),
		string(agent.EventRunFailed),
		string(agent.EventRunCanceled):
	default:
		return fmt.Errorf("invalid agent SSE event type %q", eventType)
	}

	return writeSSEMessage(w, flusher, eventType, value)
}
