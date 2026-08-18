package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/requestid"
)

const agentApprovalTimeout = 30 * time.Second

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
	return NewKnowledgeBaseAgentChatStreamWithHub(answerer, conversations, maxHistoryBytes, registry, agentstream.NewHub())
}

// NewKnowledgeBaseAgentChatStreamWithHub injects the short-lived event hub so
// tests and future durable implementations can control the replay boundary.
func NewKnowledgeBaseAgentChatStreamWithHub(answerer agentservice.EventAnswerer, conversations *conversation.Service, maxHistoryBytes int, registry *metrics.Registry, hub *agentstream.Hub) http.Handler {
	if maxHistoryBytes <= 0 {
		maxHistoryBytes = agent.DefaultMaxHistoryBytes
	}
	if hub == nil {
		hub = agentstream.NewHub()
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
		runID := agentstream.NewRunID()
		request.RunID = runID
		if err := hub.Start(runID, knowledgeBaseID); err != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("start agent stream: %w", err))
			return
		}
		// The execution context outlives the request so a disconnected browser
		// does not cancel the Agent run (it can reconnect to the hub replay).
		// The stop endpoint cancels this same context via the registered
		// function; the engine maps the cancellation to a run_canceled event.
		executionContext, stopRun := context.WithCancel(context.WithoutCancel(r.Context()))
		executionContext = agentservice.WithAsyncRun(executionContext)
		if err := hub.RegisterCancel(runID, stopRun); err != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("register agent run cancel: %w", err))
			return
		}
		defer func() {
			if err := hub.Finish(runID); err != nil {
				slog.WarnContext(r.Context(), "agent_stream_finish_failed", "run_id", runID, "error", err)
			}
		}()
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming is not supported"}`, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("X-Agent-Run-ID", runID)
		w.WriteHeader(http.StatusOK)
		transportEventNumber := 0
		agentEventNumber := 0
		publishHandlerEvent := func(eventType string, value any) error {
			transportEventNumber++
			hubEvent := agentstream.Event{
				ID:        fmt.Sprintf("%s-transport-%d", runID, transportEventNumber),
				RunID:     runID,
				Type:      eventType,
				Data:      value,
				CreatedAt: time.Now().UTC(),
			}
			if err := hub.Publish(hubEvent); err != nil {
				return fmt.Errorf("publish transport event: %w", err)
			}
			if err := writeAgentSSEEvent(w, flusher, eventType, hubEvent); err != nil {
				slog.DebugContext(r.Context(), "agent_sse_client_write_failed", "run_id", runID, "event", eventType, "error", err)
			}
			return nil
		}

		emit := func(event agent.Event) error {
			// The real Agent Service uses request.RunID. Normalizing here keeps the
			// transport boundary safe for alternate EventAnswerer implementations.
			event.RunID = runID
			if event.ID == "" {
				agentEventNumber++
				event.ID = fmt.Sprintf("%s-agent-%d", runID, agentEventNumber)
			}
			hubEvent := agentstream.Event{
				ID:         event.ID,
				RunID:      event.RunID,
				Type:       string(event.Type),
				StepNumber: event.StepNumber,
				Data:       event.Data,
				CreatedAt:  event.CreatedAt,
			}
			if err := hub.Publish(hubEvent); err != nil {
				return fmt.Errorf("publish agent event: %w", err)
			}
			if err := writeAgentSSEEvent(w, flusher, string(event.Type), event); err != nil {
				// A disconnected browser must not cancel the underlying Agent run;
				// the event is already available to a reconnecting client in hub.
				slog.DebugContext(r.Context(), "agent_sse_client_write_failed", "run_id", runID, "error", err)
			}
			return nil
		}
		var response agentservice.Response
		var conversationSaveErr error
		executionContext = agentruntime.WithApprovalGate(executionContext, func(ctx context.Context, toolName string, arguments json.RawMessage) (bool, error) {
			approvalContext, cancelApproval := context.WithTimeout(ctx, agentApprovalTimeout)
			defer cancelApproval()
			return hub.WaitApproval(approvalContext, runID, knowledgeBaseID, toolName, arguments)
		})
		err = withConversationSummaryLock(executionContext, conversations, knowledgeBaseID, request.ConversationID, func() error {
			if idempotencyKey != "" {
				if preloaded {
					replayed = true
					response = preloadedResponse
					return publishHandlerEvent("conversation_replayed", struct {
						Response agentservice.Response `json:"response"`
					}{Response: response})
				}
				storedResponse, found, err := loadIdempotentResponse(executionContext, conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash)
				if err != nil {
					return err
				}
				if found {
					replayed = true
					response = storedResponse
					return publishHandlerEvent("conversation_replayed", struct {
						Response agentservice.Response `json:"response"`
					}{Response: response})
				}
			}
			if err := loadConversationHistory(executionContext, conversations, knowledgeBaseID, &request); err != nil {
				return err
			}
			var err error
			response, err = answerer.AnswerWithEvents(executionContext, knowledgeBaseID, request, emit)
			if err != nil {
				return err
			}
			assistantMessageID, err := saveConversationExchange(executionContext, conversations, knowledgeBaseID, request, response)
			if err != nil {
				conversationSaveErr = err
				message, _ := knowledgeBaseAgentChatError(err)
				if writeErr := publishHandlerEvent("conversation_save_failed", struct {
					Error string `json:"error"`
				}{Error: message}); writeErr != nil {
					slog.ErrorContext(r.Context(), "agent_sse_conversation_save_error_event_write_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", writeErr)
				}
				return nil
			}
			if err := saveConversationSummary(executionContext, conversations, knowledgeBaseID, request, response); err != nil {
				slog.WarnContext(r.Context(), "agent_sse_conversation_summary_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
			}
			if idempotencyKey != "" {
				if err := saveIdempotentResponse(executionContext, conversations, knowledgeBaseID, request.ConversationID, idempotencyKey, requestHash, response); err != nil {
					slog.WarnContext(r.Context(), "agent_sse_conversation_idempotent_response_save_failed", "request_id", requestid.FromContext(r.Context()), "conversation_id", request.ConversationID, "error", err)
				}
			}
			if request.ConversationID != 0 {
				if writeErr := publishHandlerEvent("conversation_saved", struct {
					ConversationID     int64 `json:"conversation_id"`
					AssistantMessageID int64 `json:"assistant_message_id,omitempty"`
				}{ConversationID: request.ConversationID, AssistantMessageID: assistantMessageID}); writeErr != nil {
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
			// A user-initiated stop cancels the execution context but not the
			// request context; the Agent engine already published a
			// run_canceled event with a safe message, so publishing the
			// generic handler error event here would override the stopped
			// state in the browser. Timeouts keep their dedicated error event.
			if errors.Is(err, context.Canceled) {
				return
			}
			message, _ := knowledgeBaseAgentChatError(err)
			if writeErr := publishHandlerEvent("error", struct {
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

// NewAgentRunStream serves a replay followed by live events from the same
// in-process Hub. It is intentionally scoped by knowledge base ID so a run ID
// cannot be used to read another knowledge base's events.
func NewAgentRunStream(hub *agentstream.Hub) http.Handler {
	return NewAgentRunStreamWithStore(hub, nil)
}

func NewAgentRunStreamWithStore(hub *agentstream.Hub, eventStore agentrun.EventStore) http.Handler {
	if hub == nil {
		hub = agentstream.NewHub()
	}
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
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming is not supported"}`, http.StatusInternalServerError)
			return
		}
		snapshot, live, cancel, done, err := hub.Subscribe(runID, knowledgeBaseID, r.Context().Done())
		if err != nil {
			if errors.Is(err, agentstream.ErrRunNotFound) && eventStore != nil {
				if liveStore, ok := eventStore.(agentrun.LiveEventStore); ok {
					storedSnapshot, storedLive, stop, storedDone, streamErr := liveStore.Subscribe(r.Context(), runID, knowledgeBaseID)
					if streamErr == nil {
						defer stop()
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Cache-Control", "no-cache")
						w.Header().Set("X-Accel-Buffering", "no")
						w.WriteHeader(http.StatusOK)
						for _, event := range storedSnapshot {
							if err := writeAgentSSEEvent(w, flusher, event.Type, event); err != nil {
								return
							}
						}
						if storedDone {
							return
						}
						for {
							select {
							case <-r.Context().Done():
								return
							case event, ok := <-storedLive:
								if !ok {
									return
								}
								if err := writeAgentSSEEvent(w, flusher, event.Type, event); err != nil {
									return
								}
							}
						}
					}
				}
				storedEvents, storeErr := eventStore.List(r.Context(), runID, knowledgeBaseID)
				if storeErr == nil && len(storedEvents) > 0 {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.WriteHeader(http.StatusOK)
					for _, event := range storedEvents {
						if err := writeAgentSSEEvent(w, flusher, event.Type, event); err != nil {
							return
						}
					}
					return
				}
			}
			if errors.Is(err, agentstream.ErrRunNotFound) {
				http.Error(w, `{"error":"agent run not found or expired"}`, http.StatusNotFound)
				return
			}
			http.Error(w, `{"error":"unable to resume agent run"}`, http.StatusInternalServerError)
			return
		}
		defer cancel()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		for _, event := range snapshot {
			if err := writeAgentSSEEvent(w, flusher, event.Type, event); err != nil {
				return
			}
		}
		if done {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case event, ok := <-live:
				if !ok {
					return
				}
				if err := writeAgentSSEEvent(w, flusher, event.Type, event); err != nil {
					return
				}
			}
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
		string(agent.EventReasoningDelta),
		string(agent.EventMessageDelta),
		string(agent.EventApprovalRequired),
		string(agent.EventApprovalResolved),
		string(agent.EventApprovalExpired),
		string(agent.EventRunFinished),
		string(agent.EventRunFailed),
		string(agent.EventRunCanceled):
	default:
		return fmt.Errorf("invalid agent SSE event type %q", eventType)
	}

	return writeSSEMessage(w, flusher, eventType, value)
}
