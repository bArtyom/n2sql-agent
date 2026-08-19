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
)

// persistedAgentRequest is the request snapshot stored in agent_runs. Headers
// and transport state are copied into the snapshot because the Worker no
// longer has access to the original HTTP request.
type persistedAgentRequest struct {
	Request        agentservice.ChatRequest `json:"request"`
	IdempotencyKey string                   `json:"idempotency_key,omitempty"`
	RequestHash    string                   `json:"request_hash,omitempty"`
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

// NewPersistentAgentExecutorWithCheckpoint adds a durable boundary after a
// completed tool event. The event payload is already bounded by the Agent
// runtime, so the checkpoint stores only a safe summary and metadata.
func NewPersistentAgentExecutorWithCheckpoint(answerer agentservice.EventAnswerer, conversations *conversation.Service, hub *agentstream.Hub, registry *metrics.Registry, resultWriter agentrun.ResultWriter, eventStore agentrun.EventStore, checkpointStore agentrun.ToolCheckpointStore) agentrun.Executor {
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
		request.RecoveryCheckpoints = run.Checkpoints
		if checkpointStore != nil {
			request.CheckpointSink = func(ctx context.Context, checkpoint agentruntime.ToolCheckpoint) error {
				payload, err := json.Marshal(checkpoint.Payload)
				if err != nil {
					return fmt.Errorf("encode tool checkpoint event: %w", err)
				}
				return checkpointStore.SaveToolCheckpoint(ctx, agentrun.ToolCheckpoint{
					AgentRunID: run.ID, AttemptCount: run.AttemptCount, StepNumber: checkpoint.StepNumber,
					ToolCallID: checkpoint.ToolCallID, ToolName: checkpoint.ToolName, Arguments: checkpoint.Arguments,
					ArgumentsHash: checkpoint.ArgumentsHash, Content: checkpoint.Content, Payload: payload,
				})
			}
		}

		executionContext, stopRun := context.WithCancel(ctx)
		defer stopRun()
		if err := hub.RegisterCancel(run.RunID, stopRun); err != nil {
			return fmt.Errorf("register agent run cancel: %w", err)
		}
		defer func() {
			if err := hub.Finish(run.RunID); err != nil && !errors.Is(err, agentstream.ErrRunNotFound) {
				slog.WarnContext(ctx, "agent_stream_finish_failed", "run_id", run.RunID, "error", err)
			}
		}()

		transportEventNumber := 0
		publish := func(eventType string, value any) error {
			transportEventNumber++
			event := agentstream.Event{
				ID:        fmt.Sprintf("%s-transport-%d", run.RunID, transportEventNumber),
				RunID:     run.RunID,
				Type:      eventType,
				Data:      value,
				CreatedAt: time.Now().UTC(),
			}
			if eventStore != nil {
				if err := eventStore.Append(executionContext, run, event); err != nil {
					return err
				}
			}
			return hub.Publish(event)
		}
		emit := func(event agent.Event) error {
			event.RunID = run.RunID
			event.Version = agent.EventSchemaVersion
			if sink != nil {
				return sink(event)
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
		err := withConversationSummaryLock(executionContext, conversations, run.KnowledgeBaseID, request.ConversationID, func() error {
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
		if err != nil {
			if !replayed {
				message, _ := knowledgeBaseAgentChatError(err)
				_ = publish("error", map[string]string{"error": message})
			}
			logAgentRequest(ctx, started, request, response, err, registry, !replayed)
			return err
		}
		if resultWriter != nil {
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
