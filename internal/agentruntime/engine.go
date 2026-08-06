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

// Engine runs a bounded, non-streaming Agent loop.
type Engine struct {
	chat     modelruntime.ToolChatRunner
	registry *agent.ToolRegistry
	maxSteps int
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
	conversation := append([]modelclient.ChatMessage(nil), messages...)
	definitions := e.registry.FunctionDefinitions()

	for step := 0; step < e.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return cancelResult(result, err)
		}

		response, err := e.chat.ChatMessagesWithTools(ctx, conversation, definitions)
		if err != nil {
			if stepErr := run.AddStep(agent.Step{Kind: agent.StepModelDecision, Status: agent.StepFailed}); stepErr != nil {
				return failResult(result, stepErr)
			}
			return finishError(result, err)
		}
		if err := run.AddStep(agent.Step{Kind: agent.StepModelDecision, Status: agent.StepSucceeded}); err != nil {
			return failResult(result, err)
		}

		if len(response.ToolCalls) == 0 {
			if strings.TrimSpace(response.Message) == "" {
				return failResult(result, ErrEmptyFinalAnswer)
			}
			if err := run.AddStep(agent.Step{Kind: agent.StepFinalAnswer, Status: agent.StepSucceeded}); err != nil {
				return failResult(result, err)
			}
			if err := run.Complete(response.Message); err != nil {
				return failResult(result, err)
			}
			result.Response = response
			return result, nil
		}

		conversation = append(conversation, modelclient.ChatMessage{
			Role:      "assistant",
			Content:   response.Message,
			ToolCalls: response.ToolCalls,
		})
		for _, toolCall := range response.ToolCalls {
			if err := validateToolCall(toolCall); err != nil {
				return addToolFailure(result, toolCall.Function.Name, err)
			}

			tool, err := e.registry.Find(toolCall.Function.Name)
			if err != nil {
				return addToolFailure(result, toolCall.Function.Name, err)
			}
			toolResult, err := tool.Call(ctx, json.RawMessage(toolCall.Function.Arguments))
			if err != nil {
				return addToolFailure(result, toolCall.Function.Name, fmt.Errorf("execute tool: %w", err))
			}
			if strings.TrimSpace(toolResult.Content) == "" {
				return addToolFailure(result, toolCall.Function.Name, ErrInvalidToolResult)
			}
			if err := result.Run.AddStep(agent.Step{
				Kind:     agent.StepToolCall,
				Status:   agent.StepSucceeded,
				ToolName: toolCall.Function.Name,
			}); err != nil {
				return failResult(result, err)
			}
			conversation = append(conversation, modelclient.ChatMessage{
				Role:       "tool",
				ToolCallID: toolCall.ID,
				Content:    toolResult.Content,
			})
		}
	}

	return failResult(result, ErrMaxStepsExceeded)
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

func addToolFailure(result Result, toolName string, err error) (Result, error) {
	if stepErr := result.Run.AddStep(agent.Step{Kind: agent.StepToolCall, Status: agent.StepFailed, ToolName: toolName}); stepErr != nil {
		return failResult(result, stepErr)
	}
	return finishError(result, fmt.Errorf("tool %q: %w", toolName, err))
}

func cancelResult(result Result, err error) (Result, error) {
	if cancelErr := result.Run.Cancel(err.Error()); cancelErr != nil {
		return result, cancelErr
	}
	return result, err
}

func failResult(result Result, err error) (Result, error) {
	return finishError(result, err)
}

func finishError(result Result, err error) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cancelResult(result, err)
	}
	if failErr := result.Run.Fail(err); failErr != nil {
		return result, failErr
	}
	return result, err
}
