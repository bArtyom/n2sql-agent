package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/security"
)

var (
	ErrInvalidEngine     = errors.New("invalid agent engine")
	ErrInvalidContext    = errors.New("agent context is required")
	ErrInvalidMessages   = errors.New("agent messages are required")
	ErrInvalidMaxSteps   = errors.New("agent max steps must be positive")
	ErrMaxStepsExceeded  = errors.New("agent max steps exceeded")
	ErrEmptyFinalAnswer  = errors.New("agent final answer is empty")
	ErrInvalidToolCall   = errors.New("invalid agent tool call")
	ErrInvalidToolResult = errors.New("invalid agent tool result")
)

const untrustedToolResultPrefix = "UNTRUSTED_TOOL_RESULT\n"

type untrustedToolResultEnvelope struct {
	Trusted bool   `json:"trusted"`
	Content string `json:"content"`
}

// Engine runs a bounded, non-streaming Agent loop.
type Engine struct {
	chat     modelruntime.ToolChatRunner
	registry *agent.ToolRegistry
	maxSteps int
}

// EventSink receives lifecycle events emitted while an Agent run executes.
// A nil sink disables event emission and preserves the non-streaming Run path.
type EventSink func(agent.Event) error

type eventEmitter struct {
	runID  string
	sink   EventSink
	nextID int
}

type Result struct {
	Run      *agent.AgentRun
	Response modelclient.ChatResponse
}

func NewEngine(chat modelruntime.ToolChatRunner, registry *agent.ToolRegistry, maxSteps int) (*Engine, error) {
	if chat == nil || registry == nil {
		return nil, ErrInvalidEngine
	}
	if maxSteps <= 0 {
		return nil, ErrInvalidMaxSteps
	}
	return &Engine{chat: chat, registry: registry, maxSteps: maxSteps}, nil
}

func (e *Engine) Run(ctx context.Context, runID string, messages []modelclient.ChatMessage) (Result, error) {
	return e.run(ctx, runID, messages, nil)
}

// RunWithEvents executes the Agent loop and reports lifecycle events to sink.
// Events are emitted in execution order; a sink error stops the run.
func (e *Engine) RunWithEvents(ctx context.Context, runID string, messages []modelclient.ChatMessage, sink EventSink) (Result, error) {
	return e.run(ctx, runID, messages, sink)
}

func (e *Engine) run(ctx context.Context, runID string, messages []modelclient.ChatMessage, sink EventSink) (Result, error) {
	if e == nil || e.chat == nil || e.registry == nil {
		return Result{}, ErrInvalidEngine
	}
	if ctx == nil {
		return Result{}, ErrInvalidContext
	}
	if len(messages) == 0 {
		return Result{}, ErrInvalidMessages
	}

	run, err := agent.NewAgentRun(runID)
	if err != nil {
		return Result{}, err
	}
	if err := run.Start(); err != nil {
		return Result{Run: run}, err
	}

	result := Result{Run: run}
	emitter := newEventEmitter(runID, sink)
	if err := emitter.emit(agent.EventRunStarted, 0, map[string]any{
		"status": string(agent.RunRunning),
	}); err != nil {
		return finishError(result, err)
	}
	conversation := append([]modelclient.ChatMessage(nil), messages...)
	definitions := e.registry.FunctionDefinitions()

	for step := 0; step < e.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return finishErrorWithEvents(result, err, emitter)
		}

		if err := run.RecordModelCall(); err != nil {
			return finishErrorWithEvents(result, err, emitter)
		}
		response, err := e.chat.ChatMessagesWithTools(ctx, conversation, definitions)
		if err != nil {
			if stepErr := run.AddStep(agent.Step{Kind: agent.StepModelDecision, Status: agent.StepFailed}); stepErr != nil {
				return finishErrorWithEvents(result, stepErr, emitter)
			}
			return finishErrorWithCategory(result, err, agent.FailureModel, emitter)
		}
		if err := run.AddStep(agent.Step{Kind: agent.StepModelDecision, Status: agent.StepSucceeded}); err != nil {
			return finishErrorWithEvents(result, err, emitter)
		}

		if len(response.ToolCalls) == 0 {
			safeMessage := security.RedactText(response.Message)
			if strings.TrimSpace(safeMessage) == "" {
				return finishErrorWithCategory(result, ErrEmptyFinalAnswer, agent.FailureValidation, emitter)
			}
			if err := run.AddStep(agent.Step{Kind: agent.StepFinalAnswer, Status: agent.StepSucceeded}); err != nil {
				return finishErrorWithEvents(result, err, emitter)
			}
			if err := emitter.emit(agent.EventMessageDelta, len(run.Steps()), map[string]any{
				"content": safeMessage,
			}); err != nil {
				return finishError(result, err)
			}
			if err := run.Complete(safeMessage); err != nil {
				return finishErrorWithEvents(result, err, emitter)
			}
			response.Message = safeMessage
			result.Response = response
			if err := emitter.emit(agent.EventRunFinished, len(run.Steps()), map[string]any{
				"answer": safeMessage,
				"stats":  run.Stats(),
			}); err != nil {
				return result, err
			}
			return result, nil
		}

		conversation = append(conversation, modelclient.ChatMessage{
			Role:      "assistant",
			Content:   response.Message,
			ToolCalls: response.ToolCalls,
		})
		for _, toolCall := range response.ToolCalls {
			if err := emitter.emit(agent.EventToolCalled, len(run.Steps())+1, map[string]any{
				"tool_call_id": toolCall.ID,
				"tool_name":    toolCall.Function.Name,
			}); err != nil {
				return finishError(result, err)
			}
			if err := validateToolCall(toolCall); err != nil {
				return addToolFailureWithEvents(result, toolCall.Function.Name, err, emitter)
			}

			tool, err := e.registry.Find(toolCall.Function.Name)
			if err != nil {
				return addToolFailureWithEvents(result, toolCall.Function.Name, err, emitter)
			}
			toolResult, err := tool.Call(ctx, json.RawMessage(toolCall.Function.Arguments))
			if err != nil {
				return addToolFailureWithEvents(result, toolCall.Function.Name, fmt.Errorf("execute tool: %w", err), emitter)
			}
			if strings.TrimSpace(toolResult.Content) == "" {
				return addToolFailureWithEvents(result, toolCall.Function.Name, ErrInvalidToolResult, emitter)
			}
			toolResult = security.RedactToolResult(toolResult)
			if err := result.Run.AddStep(agent.Step{
				Kind:     agent.StepToolCall,
				Status:   agent.StepSucceeded,
				ToolName: toolCall.Function.Name,
			}); err != nil {
				return finishErrorWithEvents(result, err, emitter)
			}
			if err := result.Run.RecordToolCall(true); err != nil {
				return finishErrorWithEvents(result, err, emitter)
			}
			toolFinishedData := map[string]any{
				"tool_call_id": toolCall.ID,
				"tool_name":    toolCall.Function.Name,
			}
			if sources, ok := toolResult.Metadata["sources"]; ok {
				toolFinishedData["sources"] = sources
			}
			if truncated, ok := toolResult.Metadata["truncated"].(bool); ok {
				toolFinishedData["truncated"] = truncated
			}
			if err := emitter.emit(agent.EventToolFinished, len(run.Steps()), toolFinishedData); err != nil {
				return finishError(result, err)
			}
			toolContent, err := markUntrustedToolResult(toolResult.Content)
			if err != nil {
				return finishErrorWithEvents(result, err, emitter)
			}
			conversation = append(conversation, modelclient.ChatMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    toolContent,
			})
		}
	}

	return finishErrorWithCategory(result, ErrMaxStepsExceeded, agent.FailureStepLimit, emitter)
}

func markUntrustedToolResult(content string) (string, error) {
	payload, err := json.Marshal(untrustedToolResultEnvelope{Content: content})
	if err != nil {
		return "", fmt.Errorf("encode untrusted tool result: %w", err)
	}
	return untrustedToolResultPrefix + string(payload), nil
}

func newEventEmitter(runID string, sink EventSink) *eventEmitter {
	return &eventEmitter{runID: runID, sink: sink}
}

func (e *eventEmitter) emit(eventType agent.EventType, stepNumber int, data any) error {
	if e == nil || e.sink == nil {
		return nil
	}
	e.nextID++
	event, err := agent.NewEvent(fmt.Sprintf("%s-event-%d", e.runID, e.nextID), e.runID, eventType, data)
	if err != nil {
		return fmt.Errorf("create agent event: %w", err)
	}
	event.StepNumber = stepNumber
	if err := e.sink(event); err != nil {
		return fmt.Errorf("deliver agent event: %w", err)
	}
	return nil
}

func validateToolCall(toolCall modelclient.ToolCall) error {
	if strings.TrimSpace(toolCall.ID) == "" || toolCall.Type != "function" || strings.TrimSpace(toolCall.Function.Name) == "" {
		return ErrInvalidToolCall
	}
	if len(toolCall.Function.Arguments) == 0 || !json.Valid([]byte(toolCall.Function.Arguments)) {
		return fmt.Errorf("%w: arguments must be valid JSON", ErrInvalidToolCall)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &object); err != nil || object == nil {
		return fmt.Errorf("%w: arguments must be an object", ErrInvalidToolCall)
	}
	return nil
}

func addToolFailureWithEvents(result Result, toolName string, err error, emitter *eventEmitter) (Result, error) {
	if categoryErr := result.Run.SetFailureCategory(agent.FailureTool); categoryErr != nil {
		return finishErrorWithEvents(result, categoryErr, emitter)
	}
	if recordErr := result.Run.RecordToolCall(false); recordErr != nil {
		return finishErrorWithEvents(result, recordErr, emitter)
	}
	if stepErr := result.Run.AddStep(agent.Step{Kind: agent.StepToolCall, Status: agent.StepFailed, ToolName: toolName}); stepErr != nil {
		return finishErrorWithEvents(result, stepErr, emitter)
	}
	return finishErrorWithEvents(result, fmt.Errorf("tool %q: %w", toolName, err), emitter)
}

func finishErrorWithCategory(result Result, err error, category agent.FailureCategory, emitter *eventEmitter) (Result, error) {
	if categoryErr := result.Run.SetFailureCategory(category); categoryErr != nil {
		return finishErrorWithEvents(result, categoryErr, emitter)
	}
	return finishErrorWithEvents(result, err, emitter)
}

func cancelResult(result Result, err error) (Result, error) {
	if cancelErr := result.Run.Cancel(err.Error()); cancelErr != nil {
		return result, cancelErr
	}
	return result, err
}

func finishErrorWithEvents(result Result, err error, emitter *eventEmitter) (Result, error) {
	result, finishErr := finishError(result, err)
	if finishErr != nil && result.Run.Status() != agent.RunFailed && result.Run.Status() != agent.RunCanceled {
		return result, finishErr
	}
	if emitter == nil {
		return result, finishErr
	}
	eventType := agent.EventRunFailed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		eventType = agent.EventRunCanceled
	}
	if emitErr := emitter.emit(eventType, len(result.Run.Steps()), map[string]any{
		"error": err.Error(),
		"stats": result.Run.Stats(),
	}); emitErr != nil {
		return result, errors.Join(finishErr, emitErr)
	}
	return result, finishErr
}

func finishError(result Result, err error) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		category := agent.FailureCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			category = agent.FailureTimeout
		}
		if categoryErr := result.Run.SetFailureCategory(category); categoryErr != nil {
			return result, categoryErr
		}
		return cancelResult(result, err)
	}
	if failErr := result.Run.Fail(err); failErr != nil {
		return result, failErr
	}
	return result, err
}
