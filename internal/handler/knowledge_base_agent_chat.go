package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

func NewKnowledgeBaseAgentChat(answerer agentservice.Answerer) http.Handler {
	return NewKnowledgeBaseAgentChatWithLimits(answerer, agent.DefaultMaxHistoryBytes)
}

func NewKnowledgeBaseAgentChatWithLimits(answerer agentservice.Answerer, maxHistoryBytes int) http.Handler {
	return NewKnowledgeBaseAgentChatWithConversation(answerer, nil, maxHistoryBytes)
}

func NewKnowledgeBaseAgentChatWithConversation(answerer agentservice.Answerer, conversations *conversation.Service, maxHistoryBytes int) http.Handler {
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
		if err := loadConversationHistory(r.Context(), conversations, knowledgeBaseID, &request); err != nil {
			writeKnowledgeBaseAgentChatError(w, err)
			return
		}

		response, err := answerer.Answer(r.Context(), knowledgeBaseID, request)
		if err != nil {
			writeKnowledgeBaseAgentChatError(w, err)
			return
		}
		if err := saveConversationExchange(r.Context(), conversations, request, response.Answer); err != nil {
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
	case errors.Is(err, conversation.ErrInvalidConversation), errors.Is(err, conversation.ErrInvalidKnowledgeBase), errors.Is(err, conversation.ErrInvalidMessage):
		return "invalid conversation request", http.StatusBadRequest
	case errors.Is(err, conversation.ErrNotFound):
		return "conversation not found", http.StatusNotFound
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

func loadConversationHistory(ctx context.Context, conversations *conversation.Service, knowledgeBaseID int64, request *agentservice.ChatRequest) error {
	if request.ConversationID == 0 {
		return nil
	}
	if conversations == nil {
		return errors.New("conversation service is unavailable")
	}
	history, err := conversations.History(ctx, request.ConversationID, knowledgeBaseID)
	if err != nil {
		return fmt.Errorf("load conversation history: %w", err)
	}
	request.History = history
	return nil
}

func saveConversationExchange(ctx context.Context, conversations *conversation.Service, request agentservice.ChatRequest, answer string) error {
	if request.ConversationID == 0 || conversations == nil {
		return nil
	}
	if err := conversations.SaveExchange(ctx, request.ConversationID, request.Message, answer); err != nil {
		return fmt.Errorf("save conversation exchange: %w", err)
	}
	return nil
}
