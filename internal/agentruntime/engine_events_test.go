package agentruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

func TestEngineRunWithEventsEmitsDirectAnswerLifecycle(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{Message: "最终答案"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event

	result, err := engine.RunWithEvents(context.Background(), "run-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	if result.Run.Status() != agent.RunSucceeded {
		t.Fatalf("run status = %s, want succeeded", result.Run.Status())
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventMessageDelta, agent.EventRunFinished)
	finishedData, ok := events[len(events)-1].Data.(map[string]any)
	if !ok {
		t.Fatalf("run_finished data = %#v", events[len(events)-1].Data)
	}
	stats, ok := finishedData["stats"].(agent.RunStats)
	if !ok || stats.ModelCalls != 1 || stats.Status != agent.RunSucceeded {
		t.Fatalf("run_finished stats = %#v, want one successful model call", finishedData["stats"])
	}
	for _, event := range events {
		if event.RunID != "run-events" || event.ID == "" {
			t.Fatalf("event identity = %#v", event)
		}
	}
}

func TestEngineRunWithEventsEmitsToolLifecycle(t *testing.T) {
	tool := &toolStub{metadata: map[string]any{
		"sources": []map[string]any{{
			"documentId":       int64(11),
			"originalFilename": "employee-handbook.md",
			"position":         2,
			"content":          "工作满一年可享受五天年假。",
			"distance":         0.12,
		}},
		"truncated": true,
	}}
	registry := agent.NewToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	callCount := 0
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: modelclient.ToolCallFunction{
					Name:      "knowledge_search",
					Arguments: `{}`,
				},
			}}}, nil
		}
		return modelclient.ChatResponse{Message: "最终答案"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, registry, 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event

	_, err = engine.RunWithEvents(context.Background(), "run-tool-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventToolCalled, agent.EventToolFinished, agent.EventMessageDelta, agent.EventRunFinished)
	toolFinished := events[2].Data.(map[string]any)
	if _, ok := toolFinished["sources"]; !ok {
		t.Fatalf("tool_finished data = %#v, want sources", toolFinished)
	}
	if truncated, ok := toolFinished["truncated"].(bool); !ok || !truncated {
		t.Fatalf("tool_finished data = %#v, want truncated=true", toolFinished)
	}
}

func TestEngineRunWithEventsEmitsFailureAndCancellation(t *testing.T) {
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
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var failedEvents []agent.Event
	_, err = engine.RunWithEvents(context.Background(), "run-failed-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		failedEvents = append(failedEvents, event)
		return nil
	})
	if !errors.Is(err, agent.ErrToolNotFound) {
		t.Fatalf("RunWithEvents() error = %v, want ErrToolNotFound", err)
	}
	assertEventTypes(t, failedEvents, agent.EventRunStarted, agent.EventToolCalled, agent.EventRunFailed)

	toolErr := errors.New("search unavailable")
	toolRegistry := agent.NewToolRegistry()
	if err := toolRegistry.Register(&toolStub{err: toolErr}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	toolEngine, err := agentruntime.NewEngine(chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID:   "call-tool-error",
			Type: "function",
			Function: modelclient.ToolCallFunction{
				Name:      "knowledge_search",
				Arguments: `{}`,
			},
		}}}, nil
	}}, toolRegistry, 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var toolFailureEvents []agent.Event
	_, err = toolEngine.RunWithEvents(context.Background(), "run-tool-failed-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		toolFailureEvents = append(toolFailureEvents, event)
		return nil
	})
	if !errors.Is(err, toolErr) {
		t.Fatalf("RunWithEvents() error = %v, want tool error", err)
	}
	assertEventTypes(t, toolFailureEvents, agent.EventRunStarted, agent.EventToolCalled, agent.EventRunFailed)

	modelErr := errors.New("model unavailable")
	modelEngine, err := agentruntime.NewEngine(chatStub{call: func(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{}, modelErr
	}}, agent.NewToolRegistry(), 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var modelFailureEvents []agent.Event
	modelResult, err := modelEngine.RunWithEvents(context.Background(), "run-model-failed-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		modelFailureEvents = append(modelFailureEvents, event)
		return nil
	})
	if !errors.Is(err, modelErr) {
		t.Fatalf("RunWithEvents() error = %v, want model error", err)
	}
	assertEventTypes(t, modelFailureEvents, agent.EventRunStarted, agent.EventRunFailed)
	if got := modelResult.Run.Stats().FailureCategory; got != agent.FailureModel {
		t.Fatalf("model failure category = %q, want %q", got, agent.FailureModel)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var canceledEvents []agent.Event
	_, err = engine.RunWithEvents(ctx, "run-canceled-events", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		canceledEvents = append(canceledEvents, event)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithEvents() error = %v, want context.Canceled", err)
	}
	assertEventTypes(t, canceledEvents, agent.EventRunStarted, agent.EventRunCanceled)
}

func TestEngineRunWithEventsStopsWhenSinkFails(t *testing.T) {
	sinkErr := errors.New("client disconnected")
	engine, err := agentruntime.NewEngine(chatStub{call: func(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{Message: "最终答案"}, nil
	}}, agent.NewToolRegistry(), 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event
	result, err := engine.RunWithEvents(context.Background(), "run-sink-error", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		if event.Type == agent.EventMessageDelta {
			return sinkErr
		}
		return nil
	})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("RunWithEvents() error = %v, want sink error", err)
	}
	if result.Run.Status() != agent.RunFailed {
		t.Fatalf("run status = %s, want failed", result.Run.Status())
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventMessageDelta)
}

func assertEventTypes(t *testing.T, events []agent.Event, want ...agent.EventType) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d (%#v)", len(events), len(want), events)
	}
	for index, eventType := range want {
		if events[index].Type != eventType {
			t.Fatalf("event[%d] type = %s, want %s", index, events[index].Type, eventType)
		}
	}
}
