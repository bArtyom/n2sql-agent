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
	"github.com/bArtyom/n2sql-agent/internal/ops"
)

// persistedAgentRequest is the request snapshot stored in agent_runs. Headers
// and transport state are copied into the snapshot because the Worker no
// longer has access to the original HTTP request.
type persistedAgentRequest struct {
	Request        agentservice.ChatRequest `json:"request"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
	RequestHash    string                   `json:"request_hash,omitempty"`
	ToolCallID     string                   `json:"tool_call_id,omitempty"`
	TraceID        string                   `json:"trace_id,omitempty"`
}

// NewPersistentAgentRunSubmission accepts a chat request, stores a durable
// pending run, and returns immediately. The caller then opens the separate SSE
// stream endpoint with the returned run_id.
func NewPersistentAgentRunSubmission(maxHistoryBytes int, store agentrun.Store, conversations *conversation.Service, hub *agentstream.Hub) http.Handler {
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
		idempotencyKey, ok := decodeIdempotencyKey(w, r, request.ConversationID)
		if !ok {
			return
		}
		requestHash, err := idempotencyRequestHash(knowledgeBaseID, request)
		if err != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("hash idempotency request: %w", err))
			return
		}
		runID := agentstream.NewRunID()
		request.RunID = runID
		if hub == nil || store == nil {
			writeKnowledgeBaseAgentChatError(w, agentrun.ErrInvalidRun)
			return
		}
		if err := hub.Start(runID, knowledgeBaseID); err != nil {
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("start agent stream: %w", err))
			return
		}
		snapshot, err := json.Marshal(persistedAgentRequest{
			Request:        request,
			IdempotencyKey: idempotencyKey,
			RequestHash:    requestHash,
		})
		if err != nil {
			_ = hub.Finish(runID)
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("encode agent request snapshot: %w", err))
			return
		}
		if _, err := store.Create(r.Context(), agentrun.CreateInput{
			RunID:           runID,
			KnowledgeBaseID: knowledgeBaseID,
			ConversationID:  request.ConversationID,
			Request:         snapshot,
		}); err != nil {
			_ = hub.Finish(runID)
			writeKnowledgeBaseAgentChatError(w, fmt.Errorf("create agent run: %w", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Agent-Run-ID", runID)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run_id":     runID,
			"status":     agentrun.StatusPending,
			"stream_url": fmt.Sprintf("/api/knowledge-bases/%d/agent-runs/%s/stream", knowledgeBaseID, runID),
		})
	})
}

// NewPersistentAgentExecutor adapts the Agent service to the durable Worker.
// It owns all work that used to happen inside the HTTP handler: loading
// history, waiting for approvals, saving the exchange, and publishing
// transport-only events.
func NewPersistentAgentExecutor(answerer agentservice.EventAnswerer, conversations *conversation.Service, hub *agentstream.Hub, registry *metrics.Registry, resultWriter agentrun.ResultWriter, eventStore agentrun.EventStore) agentrun.Executor {
	return NewPersistentAgentExecutorWithCheckpoint(answerer, conversations, hub, registry, resultWriter, eventStore, nil)
}

// NewPersistentAgentExecutorWithCheckpoint wires one DeerFlow-style durable
// checkpoint store into the Agent engine. The same snapshot is used for an
// interrupted run and for the next turn of a conversation.
func NewPersistentAgentExecutorWithCheckpoint(answerer agentservice.EventAnswerer, conversations *conversation.Service, hub *agentstream.Hub, registry *metrics.Registry, resultWriter agentrun.ResultWriter, eventStore agentrun.EventStore, checkpointStore agentrun.CheckpointStore) agentrun.Executor {
	return agentrun.ExecutorFunc(func(ctx context.Context, run agentrun.Run, sink agentrun.EventSink) error {
		if answerer == nil || hub == nil {
			return agentrun.ErrInvalidRun
		}
		var snapshot persistedAgentRequest
		if err := json.Unmarshal(run.Request, &snapshot); err != nil {
			return fmt.Errorf("decode agent request snapshot: %w", err)
		}
		request := snapshot.Request
		request.RunID = run.RunID
		request.ParentRunDatabaseID = run.ID
		request.ToolCallID = snapshot.ToolCallID
		request.TraceID = snapshot.TraceID
		request.ExecutionID = run.ExecutionID
		if request.TraceID == "" {
			if request.ParentRunPublicID != "" {
				request.TraceID = request.ParentRunPublicID
			} else {
				request.TraceID = run.RunID
			}
		}
		if checkpointStore != nil {
			if run.Checkpoint != nil {
				var state agentruntime.CheckpointState
				if err := json.Unmarshal(run.Checkpoint.State, &state); err != nil {
					slog.WarnContext(ctx, "agent_checkpoint_decode_failed", "run_id", run.RunID, "error", err)
				} else {
					request.Checkpoint = &state
					request.ResumeCurrentRun = true
				}
			} else if run.RunKind == agentrun.KindRoot && run.ConversationID > 0 {
				checkpoint, checkpointErr := checkpointStore.GetLatestThreadCheckpoint(ctx, run.ConversationID)
				if checkpointErr != nil {
					slog.WarnContext(ctx, "agent_thread_checkpoint_load_failed", "run_id", run.RunID, "conversation_id", run.ConversationID, "error", checkpointErr)
				} else if checkpoint != nil {
					var state agentruntime.CheckpointState
					if err := json.Unmarshal(checkpoint.State, &state); err != nil {
						slog.WarnContext(ctx, "agent_thread_checkpoint_decode_failed", "run_id", run.RunID, "conversation_id", run.ConversationID, "error", err)
					} else {
						request.Checkpoint = &state
					}
				}
			}
			checkpointSequence := 0
			request.CheckpointSink = func(ctx context.Context, state agentruntime.CheckpointState) error {
				payload, err := json.Marshal(state)
				if err != nil {
					return fmt.Errorf("encode agent checkpoint state: %w", err)
				}
				checkpointSequence++
				return checkpointStore.SaveCheckpoint(ctx, agentrun.Checkpoint{
					AgentRunID: run.ID, ConversationID: run.ConversationID,
					AttemptCount: run.AttemptCount, StepNumber: state.LastStep,
					CheckpointID: fmt.Sprintf("%s-attempt-%d-%d", run.RunID, run.AttemptCount, checkpointSequence),
					State:        payload,
				})
			}
		}

		executionContext, stopRun := context.WithCancel(ctx)
		executionContext = ops.WithTraceID(executionContext, request.TraceID)
		defer stopRun()
		keepStreamOpen := false
		if request.ChildMode {
			if err := hub.Start(run.RunID, run.KnowledgeBaseID); err != nil && !errors.Is(err, agentstream.ErrRunAlreadyStarted) {
				return fmt.Errorf("start child agent stream: %w", err)
			}
		}
		if err := hub.RegisterCancel(run.RunID, stopRun); err != nil {
			return fmt.Errorf("register agent run cancel: %w", err)
		}
		defer func() {
			if keepStreamOpen {
				return
			}
			if err := hub.Finish(run.RunID); err != nil && !errors.Is(err, agentstream.ErrRunNotFound) {
				slog.WarnContext(ctx, "agent_stream_finish_failed", "run_id", run.RunID, "error", err)
			}
		}()

		transportEventNumber := 0
		publish := func(eventType string, value any) error {
			transportEventNumber++
			eventRunID := run.RunID
			eventIDPrefix := run.RunID
			if request.ChildMode && request.ParentRunPublicID != "" {
				eventRunID = request.ParentRunPublicID
			}
			event := agentstream.Event{
				ID:          fmt.Sprintf("%s-transport-%s-%d", eventIDPrefix, request.ExecutionID, transportEventNumber),
				RunID:       eventRunID,
				Type:        eventType,
				Category:    eventCategory(eventType),
				ToolCallID:  request.ToolCallID,
				ExecutionID: request.ExecutionID,
				TraceID:     request.TraceID,
				Data:        value,
				CreatedAt:   time.Now().UTC(),
			}
			if eventStore != nil {
				if err := eventStore.Append(executionContext, run, event); err != nil {
					return err
				}
			}
			return hub.Publish(event)
		}
		emit := func(event agent.Event) error {
			event.Version = agent.EventSchemaVersion
			if request.ChildMode {
				event = wrapChildAgentEvent(run, request, event)
			}
			if sink != nil {
				return sink(event)
			}
			if request.ChildMode {
				return nil
			}
			return hub.PublishAgent(event)
		}

		executionContext = agentruntime.WithApprovalGate(executionContext, func(ctx context.Context, toolName string, arguments json.RawMessage) (bool, error) {
			approvalContext, cancelApproval := context.WithTimeout(ctx, agentApprovalTimeout)
			defer cancelApproval()
			return hub.WaitApproval(approvalContext, run.RunID, run.KnowledgeBaseID, toolName, arguments)
		})

		started := time.Now()
		var response agentservice.Response
		var replayed bool
		var err error
		if request.ChildMode {
			response, err = answerer.AnswerWithEvents(executionContext, run.KnowledgeBaseID, request, emit)
		} else {
			err = withConversationSummaryLock(executionContext, conversations, run.KnowledgeBaseID, request.ConversationID, func() error {
				if snapshot.IdempotencyKey != "" {
					stored, found, err := loadIdempotentResponse(executionContext, conversations, run.KnowledgeBaseID, request.ConversationID, snapshot.IdempotencyKey, snapshot.RequestHash)
					if err != nil {
						return err
					}
					if found {
						replayed = true
						response = stored
						return publish("conversation_replayed", map[string]any{"response": response})
					}
				}
				if err := loadConversationHistory(executionContext, conversations, run.KnowledgeBaseID, &request); err != nil {
					return err
				}
				var err error
				response, err = answerer.AnswerWithEvents(executionContext, run.KnowledgeBaseID, request, emit)
				if err != nil {
					return err
				}
				assistantMessageID, err := saveConversationExchange(executionContext, conversations, run.KnowledgeBaseID, request, response)
				if err != nil {
					return err
				}
				if err := saveConversationSummary(executionContext, conversations, run.KnowledgeBaseID, request, response); err != nil {
					slog.WarnContext(executionContext, "conversation_summary_save_failed", "run_id", run.RunID, "error", err)
				}
				if snapshot.IdempotencyKey != "" {
					if err := saveIdempotentResponse(executionContext, conversations, run.KnowledgeBaseID, request.ConversationID, snapshot.IdempotencyKey, snapshot.RequestHash, response); err != nil {
						slog.WarnContext(executionContext, "conversation_idempotent_response_save_failed", "run_id", run.RunID, "error", err)
					}
				}
				if request.ConversationID != 0 {
					if err := publish("conversation_saved", map[string]any{
						"conversation_id":      request.ConversationID,
						"assistant_message_id": assistantMessageID,
					}); err != nil {
						return err
					}
				}
				return nil
			})
		}
		if err != nil {
			if errors.Is(err, agentruntime.ErrAgentWaitingChildren) {
				keepStreamOpen = true
				_ = publish("waiting_children", map[string]any{"run_id": run.RunID})
				return err
			}
			if reason := stopReasonFromFailureCategory(response.Stats); reason != "" {
				err = &agentrun.StoppedError{Err: err, Reason: reason}
			}
			if !replayed {
				message, _ := knowledgeBaseAgentChatError(err)
				_ = publish("error", map[string]string{"error": message})
			}
			logAgentRequest(ctx, started, request, response, err, registry, !replayed)
			return err
		}
		if resultWriter != nil {
			response.ExecutionID = run.ExecutionID
			data, marshalErr := json.Marshal(response)
			if marshalErr != nil {
				return fmt.Errorf("encode agent response: %w", marshalErr)
			}
			if writeErr := resultWriter.SaveResponse(executionContext, run.ID, data); writeErr != nil {
				return writeErr
			}
		}
		logAgentRequest(ctx, started, request, response, nil, registry, !replayed)
		return nil
	})
}

func stopReasonFromFailureCategory(stats *agent.RunStats) string {
	if stats == nil {
		return ""
	}
	switch stats.FailureCategory {
	case agent.FailureModel:
		return agentrun.StopReasonModelError
	case agent.FailureTool:
		return agentrun.StopReasonToolError
	case agent.FailureTimeout:
		return agentrun.StopReasonTimeout
	case agent.FailureCanceled:
		return agentrun.StopReasonCanceled
	case agent.FailureStepLimit:
		return agentrun.StopReasonStepLimit
	case agent.FailureValidation:
		return agentrun.StopReasonValidationError
	case agent.FailureInternal:
		return agentrun.StopReasonInternalError
	default:
		return ""
	}
}

func eventCategory(eventType string) string {
	if eventType == "error" || eventType == "waiting_children" ||
		eventType == "conversation_saved" || eventType == "conversation_save_failed" ||
		eventType == "conversation_replayed" {
		return "control"
	}
	return agent.EventCategory(agent.EventType(eventType))
}

// wrapChildAgentEvent keeps asynchronous child progress on the parent's SSE
// stream while preserving the child identity and original event type. Only a
// bounded display summary crosses the boundary; raw child model/tool payloads
// stay inside the child run and its checkpoint/result storage.
func wrapChildAgentEvent(run agentrun.Run, request agentservice.ChatRequest, event agent.Event) agent.Event {
	parentRunID := request.ParentRunPublicID
	if parentRunID == "" {
		parentRunID = run.RunID
	}
	data := map[string]any{
		"child_run_id":     run.RunID,
		"parent_run_id":    parentRunID,
		"child_event_type": string(event.Type),
		"phase":            "progress",
		"child_step":       event.StepNumber,
		"execution_id":     run.ExecutionID,
		"trace_id":         request.TraceID,
	}
	if values, ok := event.Data.(map[string]any); ok {
		for _, key := range []string{"tool_name", "result_summary", "failed"} {
			if value, exists := values[key]; exists {
				data[key] = value
			}
		}
	}
	return agent.Event{
		Version:     agent.EventSchemaVersion,
		ID:          fmt.Sprintf("%s-child-%s", parentRunID, event.ID),
		RunID:       parentRunID,
		Type:        agent.EventChildEvent,
		ToolCallID:  request.ToolCallID,
		ExecutionID: run.ExecutionID,
		TraceID:     request.TraceID,
		StepNumber:  event.StepNumber,
		Data:        data,
		CreatedAt:   event.CreatedAt,
	}
}
