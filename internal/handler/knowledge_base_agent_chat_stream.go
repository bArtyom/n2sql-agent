package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/requestid"
)

func NewKnowledgeBaseAgentChatStream(answerer agentservice.EventAnswerer) http.Handler {
	return NewKnowledgeBaseAgentChatStreamWithLimits(answerer, agent.DefaultMaxHistoryBytes)
}

func NewKnowledgeBaseAgentChatStreamWithLimits(answerer agentservice.EventAnswerer, maxHistoryBytes int) http.Handler {
	return NewKnowledgeBaseAgentChatStreamWithConversation(answerer, nil, maxHistoryBytes)
}

func NewKnowledgeBaseAgentChatStreamWithConversation(answerer agentservice.EventAnswerer, conversations *conversation.Service, maxHistoryBytes int) http.Handler {
	return NewKnowledgeBaseAgentChatStreamWithConversationAndMetrics(answerer, conversations, maxHistoryBytes, nil)
}

func NewKnowledgeBaseAgentChatStreamWithConversationAndMetrics(answerer agentservice.EventAnswerer, conversations *conversation.Service, maxHistoryBytes int, registry *metrics.Registry) http.Handler {
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
		requestHash, hashErr := idempotencyRequestHash(knowledgeBaseID, request)
		if hashErr != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("hash idempotency request: %w", hashErr))
			return
		}
		var preloadedResponse agentservice.Response
		var preloaded bool
		replayed := false
		var err error
		if idempotencyKey != "" {
			preloadedResponse, preloaded, err = loadIdempotentResponse(r.Context(), conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash)
			if err != nil {
				writeKnowledgeBaseAgentChatError(w, err)
				return
			}
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
		var response agentservice.Response
		var conversationSaveErr error
		err = withConversationSummaryLock(r.Context(), conversations, knowledgeBaseID, request.ConversationID, func() error {
			if idempotencyKey != "" {
				if preloaded {
					replayed = true
					response = preloadedResponse
					return writeAgentSSEEvent(w, flusher, "conversation_replayed", struct {
						Response agentservice.Response `json:"response"`
					}{Response: response})
				}
				storedResponse, found, err := loadIdempotentResponse(r.Context(), conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash)
				if err != nil {
					return err
				}
				if found {
					replayed = true
					response = storedResponse
					return writeAgentSSEEvent(w, flusher, "conversation_replayed", struct {
						Response agentservice.Response `json:"response"`
					}{Response: response})
				}
			}
			if err := loadConversationHistory(r.Context(), conversations, knowledgeBaseID, &request); err != nil {
				return err
			}
			var err error
			response, err = answerer.AnswerWithEvents(r.Context(), knowledgeBaseID, request, emit)
			if err != nil {
				return err
			}
			if err := saveConversationExchange(r.Context(), conversations, request, response.Answer); err != nil {
				conversationSaveErr = err
				message, _ := knowledgeBaseAgentChatError(err)
				if writeErr := writeAgentSSEEvent(w, flusher, "conversation_save_failed", struct {
					Error string `json:"error"`
				}{Error: message}); writeErr != nil {
					slog.ErrorContext(r.Context(), "agent_sse_conversation_save_error_event_write_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", writeErr)
				}
				return nil
			}
			if err := saveConversationSummary(r.Context(), conversations, knowledgeBaseID, request, response); err != nil {
				slog.WarnContext(r.Context(), "agent_sse_conversation_summary_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
			}
			if idempotencyKey != "" {
				if err := saveIdempotentResponse(r.Context(), conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash, response); err != nil {
					slog.WarnContext(r.Context(), "agent_sse_conversation_idempotent_response_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
				}
			}
			if request.ConversationID != 0 {
				if writeErr := writeAgentSSEEvent(w, flusher, "conversation_saved", struct {
					ConversationID int64 `json:"conversation_id"`
				}{ConversationID: request.ConversationID}); writeErr != nil {
					slog.ErrorContext(r.Context(), "agent_sse_conversation_saved_event_write_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", writeErr)
				}
			}
			return nil
		})
		if err != nil {
			logAgentRequest(r.Context(), started, request, response, err, registry, !replayed)
			if r.Context().Err() != nil {
				return
			}
			message, _ := knowledgeBaseAgentChatError(err)
			if writeErr := writeAgentSSEEvent(w, flusher, "error", struct {
				Error string `json:"error"`
			}{Error: message}); writeErr != nil {
				slog.ErrorContext(r.Context(), "agent_sse_error_event_write_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", writeErr)
			}
		}
		if conversationSaveErr != nil {
			logAgentRequest(r.Context(), started, request, response, conversationSaveErr, registry, !replayed)
			return
		}
		if err == nil {
			logAgentRequest(r.Context(), started, request, response, nil, registry, !replayed)
		}
	})
}

func writeAgentSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, value any) error {
	switch eventType {
	case "error",
		"conversation_saved",
		"conversation_save_failed",
		"conversation_replayed",
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
