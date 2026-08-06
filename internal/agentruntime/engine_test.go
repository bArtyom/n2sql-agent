package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type chatStub struct {
	call func(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error)
}

func (s chatStub) ChatMessagesWithTools(ctx context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return s.call(ctx, messages, definitions)
}

type toolStub struct {
	args json.RawMessage
	err  error
}

func (t *toolStub) Name() string { return "knowledge_search" }

func (t *toolStub) Description() string { return "search the knowledge base" }

func (t *toolStub) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
}

func (t *toolStub) Call(_ context.Context, args json.RawMessage) (agent.ToolResult, error) {
	t.args = append(t.args[:0], args...)
	if t.err != nil {
		return agent.ToolResult{}, t.err
	}
	return agent.ToolResult{Content: `[{"content":"annual leave policy"}]`}, nil
}

func TestEngineReturnsModelAnswerWithoutToolCall(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 1 || messages[0].Content != "年假怎么计算？" {
			t.Fatalf("messages = %#v", messages)
		}
		if len(definitions) != 0 {
			t.Fatalf("definitions = %#v, want no tools", definitions)
		}
		return modelclient.ChatResponse{Message: "请参考年假制度。"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 3)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-direct", []modelclient.ChatMessage{{Role: "user", Content: "年假怎么计算？"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Run.Status() != agent.RunSucceeded || result.Run.FinalAnswer() != "请参考年假制度。" {
		t.Fatalf("run = %#v, answer = %q", result.Run, result.Run.FinalAnswer())
	}
	if len(result.Run.Steps()) != 2 {
		t.Fatalf("steps = %#v, want model decision and final answer", result.Run.Steps())
	}
}

func TestEngineExecutesToolAndUsesResultForFinalAnswer(t *testing.T) {
	tool := &toolStub{}
	registry := agent.NewToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	callCount := 0
	chat := chatStub{call: func(_ context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		callCount++
		if len(definitions) != 1 || definitions[0].Name != "knowledge_search" {
			t.Fatalf("definitions = %#v", definitions)
		}
		if callCount == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: modelclient.ToolCallFunction{
					Name:      "knowledge_search",
					Arguments: `{"query":"年假"}`,
				},
			}}}, nil
		}
		wantMessages := []modelclient.ChatMessage{
			{Role: "user", Content: "年假怎么计算？"},
			{Role: "assistant", ToolCalls: []modelclient.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: modelclient.ToolCallFunction{
					Name:      "knowledge_search",
					Arguments: `{"query":"年假"}`,
				},
			}}},
			{Role: "tool", ToolCallID: "call-1", Content: `[{"content":"annual leave policy"}]`},
		}
		if !reflect.DeepEqual(messages, wantMessages) {
			t.Fatalf("messages = %#v, want %#v", messages, wantMessages)
		}
		return modelclient.ChatResponse{Message: "年假按入职年限计算。"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, registry, 3)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-tool", []modelclient.ChatMessage{{Role: "user", Content: "年假怎么计算？"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Run.Status() != agent.RunSucceeded || result.Run.FinalAnswer() != "年假按入职年限计算。" {
		t.Fatalf("run status = %s, answer = %q", result.Run.Status(), result.Run.FinalAnswer())
	}
	if string(tool.args) != `{"query":"年假"}` {
		t.Fatalf("tool args = %s", tool.args)
	}
	if callCount != 2 {
		t.Fatalf("model call count = %d, want 2", callCount)
	}
}

func TestEngineFailsWhenModelRequestsUnknownTool(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID:   "call-missing",
			Type: "function",
			Function: modelclient.ToolCallFunction{
				Name:      "missing_tool",
				Arguments: `{}`,
			},
		}}}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-unknown-tool", []modelclient.ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, agent.ErrToolNotFound) {
		t.Fatalf("Run() error = %v, want ErrToolNotFound", err)
	}
	if result.Run.Status() != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", result.Run.Status())
	}
}

func TestEngineStopsAtMaximumSteps(t *testing.T) {
	registry := agent.NewToolRegistry()
	if err := registry.Register(&toolStub{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID:   "call-loop",
			Type: "function",
			Function: modelclient.ToolCallFunction{
				Name:      "knowledge_search",
				Arguments: `{}`,
			},
		}}}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, registry, 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-max-steps", []modelclient.ChatMessage{{Role: "user", Content: "loop"}})
	if !errors.Is(err, agentruntime.ErrMaxStepsExceeded) {
		t.Fatalf("Run() error = %v, want ErrMaxStepsExceeded", err)
	}
	if result.Run.Status() != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", result.Run.Status())
	}
}

func TestEngineCancelsBeforeCallingModelWhenContextIsCanceled(t *testing.T) {
	chat := chatStub{call: func(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		t.Fatal("model should not be called after context cancellation")
		return modelclient.ChatResponse{}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := engine.Run(ctx, "run-canceled", []modelclient.ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result.Run.Status() != agent.RunCanceled {
		t.Fatalf("run status = %s, want canceled", result.Run.Status())
	}
}

func TestEngineFailsWhenToolExecutionFails(t *testing.T) {
	wantErr := errors.New("search unavailable")
	registry := agent.NewToolRegistry()
	if err := registry.Register(&toolStub{err: wantErr}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID:   "call-failed",
			Type: "function",
			Function: modelclient.ToolCallFunction{
				Name:      "knowledge_search",
				Arguments: `{}`,
			},
		}}}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, registry, 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-tool-failure", []modelclient.ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want wrapped tool error", err)
	}
	if result.Run.Status() != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", result.Run.Status())
	}
}

func TestEngineRejectsEmptyFinalAnswer(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	result, err := engine.Run(context.Background(), "run-empty-answer", []modelclient.ChatMessage{{Role: "user", Content: "hello"}})
	if !errors.Is(err, agentruntime.ErrEmptyFinalAnswer) {
		t.Fatalf("Run() error = %v, want ErrEmptyFinalAnswer", err)
	}
	if result.Run.Status() != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", result.Run.Status())
	}
}
