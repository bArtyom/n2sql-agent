package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

func NewKnowledgeBaseAgentChat(answerer agentservice.Answerer) http.Handler {
	return NewKnowledgeBaseAgentChatWithLimits(answerer, agent.DefaultMaxHistoryBytes)
}

func NewKnowledgeBaseAgentChatWithLimits(answerer agentservice.Answerer, maxHistoryBytes int) http.Handler {
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

		response, err := answerer.Answer(r.Context(), knowledgeBaseID, request)
		if err != nil {
			writeKnowledgeBaseAgentChatError(w, err)
			return
		}
		writeJSON(w, response)
	})
}

func decodeKnowledgeBaseAgentChatRequest(w http.ResponseWriter, r *http.Request, maxHistoryBytes int) (int64, agentservice.ChatRequest, bool) {
	knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
	if !ok {
		return 0, agentservice.ChatRequest{}, false
	}

	var request agentservice.ChatRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, int64(maxChatQuestion+maxHistoryBytes+4096)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAgentChatDecodeError(w, err)
		return 0, agentservice.ChatRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, `{"error":"invalid agent chat request"}`, http.StatusBadRequest)
		return 0, agentservice.ChatRequest{}, false
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" || len(request.Message) > maxChatQuestion {
		http.Error(w, `{"error":"invalid agent chat message"}`, http.StatusBadRequest)
		return 0, agentservice.ChatRequest{}, false
	}
	for index := range request.History {
		if request.History[index].Role != "user" && request.History[index].Role != "assistant" {
			http.Error(w, `{"error":"invalid agent chat history"}`, http.StatusBadRequest)
			return 0, agentservice.ChatRequest{}, false
		}
		request.History[index].Content = strings.TrimSpace(request.History[index].Content)
		if request.History[index].Content == "" {
			http.Error(w, `{"error":"invalid agent chat history"}`, http.StatusBadRequest)
			return 0, agentservice.ChatRequest{}, false
		}
	}
	return knowledgeBaseID, request, true
}

func writeAgentChatDecodeError(w http.ResponseWriter, err error) {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		http.Error(w, `{"error":"agent chat request is too large"}`, http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, `{"error":"invalid agent chat request"}`, http.StatusBadRequest)
}

func writeKnowledgeBaseAgentChatError(w http.ResponseWriter, err error) {
	message, status := knowledgeBaseAgentChatError(err)
	http.Error(w, `{"error":`+strconv.Quote(message)+`}`, status)
}

func knowledgeBaseAgentChatError(err error) (string, int) {
	switch {
	case errors.Is(err, agentservice.ErrInvalidRequest):
		return "invalid agent chat request", http.StatusBadRequest
	case errors.Is(err, context.DeadlineExceeded):
		return "agent chat timed out", http.StatusGatewayTimeout
	case errors.Is(err, modelprovider.ErrNotFound):
		return "model provider not configured", http.StatusNotFound
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch), errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		return "model provider API key is not configured", http.StatusBadRequest
	default:
		return "agent chat failed", http.StatusBadGateway
	}
}
