package agentruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

type retrievalStatsTool struct{}

func (retrievalStatsTool) Name() string                { return "knowledge_search" }
func (retrievalStatsTool) Description() string         { return "搜索知识库" }
func (retrievalStatsTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (retrievalStatsTool) Call(ctx context.Context, _ json.RawMessage) (agent.ToolResult, error) {
	if observer := usage.RetrievalObserverFromContext(ctx); observer != nil {
		observer.ObserveRetrieval(usage.RetrievalObservation{
			VectorCandidates: 8, KeywordCandidates: 6, KeywordAfterThreshold: 4,
			DeduplicatedCandidates: 5, RerankBefore: 5, RerankAfter: 3,
			FinalResults: 3, FinalFiltered: 2,
		})
	}
	return agent.ToolResult{Content: "资料"}, nil
}

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

func TestEngineRunWithEventsEmitsReasoningBeforeAnswer(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ReasoningContent: "先检索资料，再组织答案", Message: "最终答案"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event

	_, err = engine.RunWithEvents(context.Background(), "run-reasoning", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventReasoningDelta, agent.EventMessageDelta, agent.EventRunFinished)
	reasoning, ok := events[1].Data.(map[string]any)
	if !ok || reasoning["content"] != "先检索资料，再组织答案" {
		t.Fatalf("reasoning event = %#v, want bounded content", events[1].Data)
	}
}

func TestEngineRunWithEventsBoundsReasoningContent(t *testing.T) {
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ReasoningContent: strings.Repeat("思", 20_000), Message: "最终答案"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, agent.NewToolRegistry(), 1)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event
	if _, err := engine.RunWithEvents(context.Background(), "run-reasoning-limit", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	reasoning, ok := events[1].Data.(map[string]any)
	if !ok || len(reasoning["content"].(string)) > 12*1024 {
		t.Fatalf("reasoning content bytes = %d, want at most 12288", len(reasoning["content"].(string)))
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

func TestEngineRunWithEventsIncludesRetrievalPipelineStats(t *testing.T) {
	registry := agent.NewToolRegistry()
	if err := registry.Register(retrievalStatsTool{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	callCount := 0
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
				ID: "call-stats", Type: "function",
				Function: modelclient.ToolCallFunction{Name: "knowledge_search", Arguments: `{}`},
			}}}, nil
		}
		return modelclient.ChatResponse{Message: "最终答案"}, nil
	}}
	engine, err := agentruntime.NewEngine(chat, registry, 2)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	var events []agent.Event
	if _, err := engine.RunWithEvents(context.Background(), "run-retrieval-stats", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("RunWithEvents() error = %v", err)
	}
	toolFinished := events[2].Data.(map[string]any)
	stats, ok := toolFinished["retrieval"].(usage.RetrievalObservation)
	if !ok || stats.VectorCandidates != 8 || stats.FinalFiltered != 2 || stats.RerankAfter != 3 {
		t.Fatalf("tool_finished retrieval stats = %#v, want bounded pipeline stats", toolFinished["retrieval"])
	}
}

func TestEngineRunWithEventsWaitsForApprovalBeforeToolCall(t *testing.T) {
	tool := &toolStub{}
	registry := agent.NewToolRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	chat := chatStub{call: func(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID:       "call-approval",
			Type:     "function",
			Function: modelclient.ToolCallFunction{Name: "knowledge_search", Arguments: `{"query":"审批"}`},
		}}}, nil
	}}
	approvalRequested := make(chan struct{})
	allow := make(chan bool)
	engine, err := agentruntime.NewEngineWithOptions(chat, registry, 1, agentruntime.EngineOptions{
		ApprovalGate: func(context.Context, string, json.RawMessage) (bool, error) {
			close(approvalRequested)
			return <-allow, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := engine.RunWithEvents(context.Background(), "run-approval", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(agent.Event) error { return nil })
		done <- runErr
	}()
	select {
	case <-approvalRequested:
	case <-time.After(time.Second):
		t.Fatal("approval gate was not called")
	}
	if tool.args != nil {
		t.Fatal("tool executed before approval")
	}
	allow <- true
	if err := <-done; err == nil {
		t.Fatal("RunWithEvents() unexpectedly succeeded with maxSteps=1")
	}
	if string(tool.args) != `{"query":"审批"}` {
		t.Fatalf("tool args = %s, want approval args", tool.args)
	}
}

func TestEngineRunWithEventsEmitsApprovalExpired(t *testing.T) {
	registry := agent.NewToolRegistry()
	if err := registry.Register(&toolStub{}); err != nil {
		t.Fatal(err)
	}
	chat := chatStub{call: func(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID: "call-expired", Type: "function",
			Function: modelclient.ToolCallFunction{Name: "knowledge_search", Arguments: `{}`},
		}}}, nil
	}}
	engine, err := agentruntime.NewEngineWithOptions(chat, registry, 1, agentruntime.EngineOptions{
		ApprovalGate: func(context.Context, string, json.RawMessage) (bool, error) {
			return false, context.DeadlineExceeded
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.Event
	_, err = engine.RunWithEvents(context.Background(), "run-expired", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunWithEvents() error = %v, want deadline exceeded", err)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventToolCalled, agent.EventApprovalRequired, agent.EventApprovalExpired)
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
	if !errors.Is(err, agentruntime.ErrMaxStepsExceeded) {
		t.Fatalf("RunWithEvents() error = %v, want ErrMaxStepsExceeded", err)
	}
	assertEventTypes(t, failedEvents, agent.EventRunStarted, agent.EventToolCalled, agent.EventToolFinished, agent.EventRunFailed)

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
	if !errors.Is(err, agentruntime.ErrMaxStepsExceeded) {
		t.Fatalf("RunWithEvents() error = %v, want ErrMaxStepsExceeded", err)
	}
	assertEventTypes(t, toolFailureEvents, agent.EventRunStarted, agent.EventToolCalled, agent.EventToolFinished, agent.EventRunFailed)
	toolFailureData, ok := toolFailureEvents[2].Data.(map[string]any)
	if !ok || toolFailureData["failed"] != true || toolFailureData["result_summary"] != "工具调用失败，已反馈给模型。" {
		t.Fatalf("tool failure event data = %#v, want failed tool summary", toolFailureEvents[2].Data)
	}
	if strings.Contains(fmt.Sprint(toolFailureData), toolErr.Error()) {
		t.Fatalf("tool failure event leaked internal error: %#v", toolFailureData)
	}

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
	modelFailureData, ok := modelFailureEvents[len(modelFailureEvents)-1].Data.(map[string]any)
	if !ok || modelFailureData["error"] != "模型服务暂时不可用，请稍后重试。" {
		t.Fatalf("model failure event data = %#v, want safe public error", modelFailureEvents[len(modelFailureEvents)-1].Data)
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
