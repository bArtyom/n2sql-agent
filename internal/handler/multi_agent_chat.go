package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

// NewMultiAgentChat exposes the minimal in-process Supervisor workflow. It is
// intentionally non-streaming; the existing Agent SSE endpoint remains the
// primary interactive chat path.
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
