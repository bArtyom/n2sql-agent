package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/requestid"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func NewKnowledgeBaseAgentChat(answerer agentservice.Answerer) http.Handler {
	return NewKnowledgeBaseAgentChatWithLimits(answerer, agent.DefaultMaxHistoryBytes)
}

func NewKnowledgeBaseAgentChatWithLimits(answerer agentservice.Answerer, maxHistoryBytes int) http.Handler {
	return NewKnowledgeBaseAgentChatWithConversation(answerer, nil, maxHistoryBytes)
}

func NewKnowledgeBaseAgentChatWithConversation(answerer agentservice.Answerer, conversations *conversation.Service, maxHistoryBytes int) http.Handler {
	return NewKnowledgeBaseAgentChatWithConversationAndMetrics(answerer, conversations, maxHistoryBytes, nil)
}

func NewKnowledgeBaseAgentChatWithConversationAndMetrics(answerer agentservice.Answerer, conversations *conversation.Service, maxHistoryBytes int, registry *metrics.Registry) http.Handler {
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
		started := time.Now()
		idempotencyKey, ok := decodeIdempotencyKey(w, r, request.ConversationID)
		if !ok {
			return
		}
		requestHash, err := idempotencyRequestHash(knowledgeBaseID, request)
		if err != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("hash idempotency request: %w", err))
			return
		}
		var response agentservice.Response
		replayed := false
		err = withConversationSummaryLock(r.Context(), conversations, knowledgeBaseID, request.ConversationID, func() error {
			if idempotencyKey != "" {
				storedResponse, found, err := loadIdempotentResponse(r.Context(), conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash)
				if err != nil {
					return err
				}
				if found {
					replayed = true
					response = storedResponse
					return nil
				}
			}
			if err := loadConversationHistory(r.Context(), conversations, knowledgeBaseID, &request); err != nil {
				return err
			}
			var err error
			response, err = answerer.Answer(r.Context(), knowledgeBaseID, request)
			if err != nil {
				return err
			}
			if err := saveConversationExchange(r.Context(), conversations, request, response.Answer); err != nil {
				return err
			}
			if err := saveConversationSummary(r.Context(), conversations, knowledgeBaseID, request, response); err != nil {
				slog.WarnContext(r.Context(), "conversation_summary_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
			}
			if idempotencyKey != "" {
				if err := saveIdempotentResponse(r.Context(), conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash, response); err != nil {
					slog.WarnContext(r.Context(), "conversation_idempotent_response_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
				}
			}
			return nil
		})
		if err != nil {
			logAgentRequest(r.Context(), started, request, response, err, registry, !replayed)
			writeKnowledgeBaseAgentChatError(w, err)
			return
		}
		logAgentRequest(r.Context(), started, request, response, nil, registry, !replayed)
		writeJSON(w, response)
	})
}

func logAgentRequest(ctx context.Context, started time.Time, request agentservice.ChatRequest, response agentservice.Response, requestErr error, registry *metrics.Registry, countRun bool) {
	fields := []any{
		"request_id", requestid.FromContext(ctx),
		"conversation_id", request.ConversationID,
		"run_id", response.RunID,
		"status", response.Status,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if requestErr != nil {
		fields = append(fields, "error", requestErr)
		slog.ErrorContext(ctx, "agent_request_failed", fields...)
	} else {
		slog.InfoContext(ctx, "agent_request_completed", fields...)
	}
	if registry == nil || !countRun {
		return
	}
	observation := metrics.AgentObservation{
		Outcome:  agentOutcome(response, requestErr),
		Duration: time.Since(started),
	}
	if response.Stats != nil {
		observation.Steps = response.Stats.StepCount
		observation.ToolCalls = response.Stats.ToolCalls
		observation.ToolFailures = response.Stats.FailedToolCalls
		observation.TotalTokens = response.Stats.TotalTokens
	}
	registry.ObserveAgent(observation)
}

func agentOutcome(response agentservice.Response, requestErr error) string {
	switch response.Status {
	case agent.RunSucceeded:
		return metrics.AgentOutcomeSucceeded
	case agent.RunCanceled:
		return metrics.AgentOutcomeCanceled
	case agent.RunFailed:
		return metrics.AgentOutcomeFailed
	}
	switch {
	case errors.Is(requestErr, context.DeadlineExceeded):
		return metrics.AgentOutcomeTimeout
	case errors.Is(requestErr, context.Canceled):
		return metrics.AgentOutcomeCanceled
	default:
		return metrics.AgentOutcomeFailed
	}
}

func decodeIdempotencyKey(w http.ResponseWriter, r *http.Request, conversationID int64) (string, bool) {
	if conversationID == 0 {
		return "", true
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", true
	}
	if !conversation.ValidateIdempotencyKey(key) {
		http.Error(w, `{"error":"invalid idempotency key"}`, http.StatusBadRequest)
		return "", false
	}
	return key, true
}

func idempotencyRequestHash(knowledgeBaseID int64, request agentservice.ChatRequest) (string, error) {
	payload, err := json.Marshal(struct {
		KnowledgeBaseID int64   `json:"knowledge_base_id"`
		ConversationID  int64   `json:"conversation_id"`
		Message         string  `json:"message"`
		TopK            int     `json:"top_k"`
		Threshold       float64 `json:"similarity_threshold"`
	}{KnowledgeBaseID: knowledgeBaseID, ConversationID: request.ConversationID, Message: request.Message, TopK: request.TopK, Threshold: request.SimilarityThreshold})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func withConversationSummaryLock(ctx context.Context, conversations *conversation.Service, knowledgeBaseID, conversationID int64, fn func() error) error {
	if conversationID == 0 || conversations == nil {
		return fn()
	}
	return conversations.WithSummaryLock(ctx, conversationID, knowledgeBaseID, fn)
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
	if request.TopK == 0 {
		request.TopK = retrieval.DefaultResults
	}
	if request.TopK < 1 || request.TopK > retrieval.MaxResults {
		http.Error(w, `{"error":"invalid agent chat top_k"}`, http.StatusBadRequest)
		return 0, agentservice.ChatRequest{}, false
	}
	if request.SimilarityThreshold != 0 {
		if err := agent.ValidateMaxKnowledgeDistance(request.SimilarityThreshold); err != nil {
			http.Error(w, `{"error":"invalid agent chat similarity_threshold"}`, http.StatusBadRequest)
			return 0, agentservice.ChatRequest{}, false
		}
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
	case errors.Is(err, conversation.ErrInvalidConversation), errors.Is(err, conversation.ErrInvalidKnowledgeBase), errors.Is(err, conversation.ErrInvalidMessage), errors.Is(err, conversation.ErrInvalidIdempotencyKey):
		return "invalid conversation request", http.StatusBadRequest
	case errors.Is(err, conversation.ErrIdempotencyConflict):
		return "idempotency key was reused with a different request", http.StatusConflict
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

func loadIdempotentResponse(ctx context.Context, conversations *conversation.Service, knowledgeBaseID, conversationID int64, key, requestHash string) (agentservice.Response, bool, error) {
	if conversations == nil {
		return agentservice.Response{}, false, errors.New("conversation service is unavailable")
	}
	data, err := conversations.GetIdempotentResponse(ctx, conversationID, knowledgeBaseID, key, requestHash)
	if errors.Is(err, conversation.ErrNotFound) {
		return agentservice.Response{}, false, nil
	}
	if err != nil {
		return agentservice.Response{}, false, fmt.Errorf("load conversation idempotent response: %w", err)
	}
	var response agentservice.Response
	if err := json.Unmarshal(data, &response); err != nil {
		return agentservice.Response{}, false, fmt.Errorf("decode conversation idempotent response: %w", err)
	}
	return response, true, nil
}

func saveIdempotentResponse(ctx context.Context, conversations *conversation.Service, knowledgeBaseID, conversationID int64, key, requestHash string, response agentservice.Response) error {
	if conversationID == 0 || conversations == nil {
		return nil
	}
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode conversation idempotent response: %w", err)
	}
	if err := conversations.SaveIdempotentResponse(ctx, conversationID, knowledgeBaseID, key, requestHash, data); err != nil {
		return fmt.Errorf("save conversation idempotent response: %w", err)
	}
	return nil
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
	summary, err := conversations.Summary(ctx, request.ConversationID, knowledgeBaseID)
	if err == nil {
		request.CachedSummary = &agentservice.CachedHistorySummary{
			ThroughMessageID: summary.ThroughMessageID,
			Content:          summary.Content,
		}
	} else if !errors.Is(err, conversation.ErrNotFound) {
		return fmt.Errorf("load conversation summary: %w", err)
	}
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

func saveConversationSummary(ctx context.Context, conversations *conversation.Service, knowledgeBaseID int64, request agentservice.ChatRequest, response agentservice.Response) error {
	if request.ConversationID == 0 || conversations == nil || response.HistorySummary == nil || !response.HistorySummary.Used || response.HistorySummary.ThroughMessageID == 0 {
		return nil
	}
	if err := conversations.SaveSummary(ctx, request.ConversationID, knowledgeBaseID, response.HistorySummary.ThroughMessageID, response.HistorySummary.Content); err != nil {
		return fmt.Errorf("save conversation summary: %w", err)
	}
	return nil
}
