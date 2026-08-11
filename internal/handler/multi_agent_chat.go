package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

// NewMultiAgentChat exposes the non-streaming in-process Supervisor workflow.
func NewMultiAgentChat(answerer multiagent.Answerer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerer == nil {
			http.Error(w, `{"error":"multi-agent service unavailable"}`, http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, request, ok := decodeKnowledgeBaseChatRequest(w, r)
		if !ok {
			return
		}
		response, err := answerer.Answer(r.Context(), knowledgeBaseID, request.Message, request.TopK)
		if err != nil {
			writeMultiAgentChatError(w, err)
			return
		}
		writeJSON(w, response)
	})
}

// NewMultiAgentChatStream exposes the same Supervisor workflow as SSE. The
// non-streaming endpoint remains available for callers that only need the
// final structured response.
func NewMultiAgentChatStream(answerer multiagent.EventAnswerer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerer == nil {
			http.Error(w, `{"error":"multi-agent service unavailable"}`, http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, request, ok := decodeKnowledgeBaseChatRequest(w, r)
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

		emit := func(event multiagent.Event) error {
			return writeMultiAgentSSEEvent(w, flusher, event)
		}
		_, err := answerer.AnswerWithEvents(r.Context(), knowledgeBaseID, request.Message, request.TopK, emit)
		if err == nil || r.Context().Err() != nil {
			return
		}
		message, _ := multiAgentChatError(err)
		if writeErr := writeSSEMessage(w, flusher, "error", struct {
			Error string `json:"error"`
		}{Error: message}); writeErr != nil {
			slog.ErrorContext(r.Context(), "multi_agent_sse_error_event_write_failed", "error", writeErr)
		}
	})
}

func writeMultiAgentSSEEvent(w http.ResponseWriter, flusher http.Flusher, event multiagent.Event) error {
	switch event.Type {
	case multiagent.EventRunStarted,
		multiagent.EventResearchStarted,
		multiagent.EventResearchToolCalled,
		multiagent.EventResearchToolFinished,
		multiagent.EventResearchSummary,
		multiagent.EventResearchFinished,
		multiagent.EventAnswererStarted,
		multiagent.EventAnswererFinished,
		multiagent.EventAnswererSkipped,
		multiagent.EventRunFinished,
		multiagent.EventRunFailed,
		multiagent.EventRunCanceled:
		return writeSSEMessage(w, flusher, string(event.Type), event)
	default:
		return errors.New("invalid multi-agent SSE event type")
	}
}

func writeMultiAgentChatError(w http.ResponseWriter, err error) {
	message, status := multiAgentChatError(err)
	http.Error(w, `{"error":`+strconv.Quote(message)+`}`, status)
}

func multiAgentChatError(err error) (string, int) {
	switch {
	case errors.Is(err, multiagent.ErrInvalidRequest):
		return "invalid multi-agent chat request", http.StatusBadRequest
	case errors.Is(err, multiagent.ErrInvalidResearchReport), errors.Is(err, multiagent.ErrEmptyFinalAnswer):
		return "multi-agent answer was invalid", http.StatusBadGateway
	case errors.Is(err, context.DeadlineExceeded):
		return "multi-agent chat timed out", http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		return "multi-agent chat canceled", http.StatusRequestTimeout
	case errors.Is(err, modelprovider.ErrNotFound):
		return "model provider not configured", http.StatusNotFound
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch), errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		return "model provider API key is not configured", http.StatusBadRequest
	default:
		var callError *modelruntime.ChatCallError
		if errors.As(err, &callError) {
			return "multi-agent model request failed", http.StatusBadGateway
		}
		return "multi-agent chat failed", http.StatusBadGateway
	}
}
