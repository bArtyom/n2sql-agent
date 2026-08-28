package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type delegateChatStub struct {
	calls       int
	definitions [][]agent.FunctionDefinition
}

type delegateFailingChatStub struct {
	calls int
}

type delegateBlockingChatStub struct{}

type delegateModelChatStub struct {
	delegateChatStub
	model string
}

func (s *delegateModelChatStub) ChatMessagesWithToolsForModel(_ context.Context, model string, _ []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	s.model = model
	s.definitions = append(s.definitions, definitions)
	return modelclient.ChatResponse{Message: "按指定子 Agent 模型完成研究。"}, nil
}

func (*delegateBlockingChatStub) ChatMessagesWithTools(ctx context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	<-ctx.Done()
	return modelclient.ChatResponse{}, ctx.Err()
}

func (s *delegateFailingChatStub) ChatMessagesWithTools(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	s.calls++
	if s.calls == 1 {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID: "child-search-1", Type: "function",
			Function: modelclient.ToolCallFunction{Name: "knowledge_search", Arguments: `{"query":"年假"}`},
		}}}, nil
	}
	return modelclient.ChatResponse{}, errors.New("child model unavailable")
}

func (s *delegateChatStub) ChatMessagesWithTools(_ context.Context, _ []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	s.calls++
	s.definitions = append(s.definitions, definitions)
	if s.calls == 1 {
		return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
			ID: "child-search-1", Type: "function",
			Function: modelclient.ToolCallFunction{Name: "knowledge_search", Arguments: `{"query":"年假"}`},
		}}}, nil
	}
	return modelclient.ChatResponse{Message: "年假按员工入职年限计算。"}, nil
}

type delegateSearcherStub struct{}

func (delegateSearcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: knowledgeBaseID + 1, OriginalFilename: "员工手册.md", Position: 3, Content: query + "规则"}}, nil
}

type delegateFolderSearcherStub struct {
	folderPath string
	recursive  bool
}

func (s *delegateFolderSearcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, _ int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: knowledgeBaseID + 1, Position: 3, Content: query}}, nil
}

func (s *delegateFolderSearcherStub) SearchWithOptions(_ context.Context, _ int64, _ string, _ int, options retrieval.SearchOptions) ([]retrieval.Result, error) {
	if options.FolderPath != nil {
		s.folderPath = *options.FolderPath
	}
	s.recursive = options.FolderRecursive
	return []retrieval.Result{{DocumentID: 8, Position: 3, Content: "目录内资料"}}, nil
}

type childLifecycleStub struct {
	started  ChildRunSpec
	finished bool
	err      error
}

type asyncChildLifecycleStub struct {
	childLifecycleStub
	ready  bool
	result agent.ToolResult
	called bool
}

func (s *asyncChildLifecycleStub) EnqueueChild(_ context.Context, _ ChildRunSpec) (string, bool, agent.ToolResult, error) {
	s.called = true
	return "child-async-1", s.ready, s.result, nil
}

func (s *childLifecycleStub) StartChild(_ context.Context, spec ChildRunSpec) (string, error) {
	s.started = spec
	return spec.RunID, nil
}

func (s *childLifecycleStub) FinishChild(_ context.Context, _ ChildRunSpec, _ agent.ToolResult, runErr error) error {
	s.finished = runErr == nil
	s.err = runErr
	return nil
}

func TestDelegateResearchToolEnqueuesAsyncChildInsteadOfRunningInline(t *testing.T) {
	lifecycle := &asyncChildLifecycleStub{}
	chat := &delegateChatStub{}
	tool, err := NewDelegateResearchTool(chat, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !lifecycle.called || chat.calls != 0 || result.Metadata["waiting_children"] != true {
		t.Fatalf("async result = %#v, called=%v chat_calls=%d", result, lifecycle.called, chat.calls)
	}
}

func TestDelegateResearchToolReusesCompletedAsyncChildResult(t *testing.T) {
	lifecycle := &asyncChildLifecycleStub{ready: true, result: agent.ToolResult{Content: "已完成研究", Metadata: map[string]any{"child_status": string(agent.RunSucceeded)}}}
	chat := &delegateChatStub{}
	tool, err := NewDelegateResearchTool(chat, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`))
	if err != nil || result.Content != "已完成研究" || chat.calls != 0 {
		t.Fatalf("result = %#v err=%v chat_calls=%d", result, err, chat.calls)
	}
}

func TestDelegateResearchToolTimesOutAndMarksChildTerminal(t *testing.T) {
	lifecycle := &childLifecycleStub{}
	tool, err := NewDelegateResearchTool(&delegateBlockingChatStub{}, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	tool.SetChildTimeout(10 * time.Millisecond)
	started := time.Now()
	_, err = tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Call() error = %v, want timeout", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(started))
	}
	if lifecycle.finished || lifecycle.err == nil || !strings.Contains(lifecycle.err.Error(), "timed out") {
		t.Fatalf("child lifecycle = finished:%v err:%v, want terminal failure", lifecycle.finished, lifecycle.err)
	}
}

func TestDelegateResearchToolRunsScopedReadOnlyChild(t *testing.T) {
	chat := &delegateChatStub{}
	tool, err := NewDelegateResearchTool(chat, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"question":"请研究年假规则"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !strings.Contains(result.Content, "年假按员工入职年限计算") {
		t.Fatalf("content = %q", result.Content)
	}
	if result.Metadata["child_run_id"] == "" || result.Metadata["child_status"] != string(agent.RunSucceeded) {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	sources, ok := result.Metadata["sources"].([]retrieval.Result)
	if !ok || len(sources) != 1 || sources[0].DocumentID != 8 {
		t.Fatalf("sources = %#v", result.Metadata["sources"])
	}
	if len(chat.definitions) != 2 || len(chat.definitions[0]) != 1 || chat.definitions[0][0].Name != "knowledge_search" {
		t.Fatalf("child definitions = %#v", chat.definitions)
	}
	if events, ok := result.Metadata["child_events"].([]map[string]any); !ok || len(events) == 0 {
		t.Fatalf("child events = %#v", result.Metadata["child_events"])
	}
}

func TestDelegateResearchToolUsesNamedSubagentModelAndParentToolAllowlist(t *testing.T) {
	chat := &delegateModelChatStub{}
	registry, err := NewSubagentRegistry([]SubagentConfig{
		{Name: "fast-research", SystemPrompt: "快速研究", Tools: []string{"knowledge_search", "parent_read"}, Model: "child-model", MaxSteps: 1, Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	parentTool := testNamedTool{name: "parent_read"}
	unsafeTool := testNamedTool{name: "delegate_research"}
	tool, err := NewDelegateResearchTool(chat, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatal(err)
	}
	tool.SetSubagentRegistry(registry)
	tool.SetChildTools(parentTool, unsafeTool)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假","subagent":"fast-research"}`))
	if err != nil {
		t.Fatal(err)
	}
	if chat.model != "child-model" || result.Content == "" {
		t.Fatalf("model=%q result=%#v", chat.model, result)
	}
	if len(chat.definitions) != 1 {
		t.Fatalf("definitions = %#v", chat.definitions)
	}
	var foundParent, foundUnsafe bool
	for _, definition := range chat.definitions[0] {
		foundParent = foundParent || definition.Name == "parent_read"
		foundUnsafe = foundUnsafe || definition.Name == "delegate_research"
	}
	if !foundParent || foundUnsafe {
		t.Fatalf("child tool definitions = %#v", chat.definitions[0])
	}
}

type testNamedTool struct{ name string }

func (t testNamedTool) Name() string        { return t.name }
func (t testNamedTool) Description() string { return "test tool" }
func (t testNamedTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t testNamedTool) Call(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "ok"}, nil
}

func TestDelegateResearchToolPropagatesFolderScopeToChildSearch(t *testing.T) {
	searcher := &delegateFolderSearcherStub{}
	chat := &delegateChatStub{}
	tool, err := NewDelegateResearchTool(chat, searcher, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	path := "docs/api"
	tool.SetFolderScope(&path, true)
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究目录内规则"}`)); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if searcher.folderPath != "docs/api" || !searcher.recursive {
		t.Fatalf("child search scope path=%q recursive=%v", searcher.folderPath, searcher.recursive)
	}
}

func TestDelegateResearchToolRejectsInvalidQuestion(t *testing.T) {
	tool, err := NewDelegateResearchTool(&delegateChatStub{}, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"question":""}`)); err == nil {
		t.Fatal("Call() error = nil, want invalid question")
	}
}

func TestDelegateResearchToolPersistsChildLifecycleWhenConfigured(t *testing.T) {
	lifecycle := &childLifecycleStub{}
	chat := &delegateChatStub{}
	tool, err := NewDelegateResearchTool(chat, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`)); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if lifecycle.started.ParentRunID != 42 || lifecycle.started.KnowledgeBaseID != 7 || lifecycle.started.Question != "研究年假" {
		t.Fatalf("started child = %#v", lifecycle.started)
	}
	if !lifecycle.finished {
		t.Fatal("child lifecycle was not completed")
	}
}

func TestDelegateResearchToolPassesToolCallAndTraceIdentityToChild(t *testing.T) {
	lifecycle := &childLifecycleStub{}
	tool, err := NewDelegateResearchTool(&delegateChatStub{}, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	ctx := WithExecutionIdentity(context.Background(), ExecutionIdentity{ToolCallID: "call-parent-1", TraceID: "trace-parent-1"})
	if _, err := tool.Call(ctx, json.RawMessage(`{"question":"研究年假"}`)); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if lifecycle.started.ToolCallID != "call-parent-1" || lifecycle.started.TraceID != "trace-parent-1" {
		t.Fatalf("child identity = %#v, want tool_call_id and trace_id", lifecycle.started)
	}
}

func TestDelegateResearchToolReturnsPartialResultWithoutAutomaticRetry(t *testing.T) {
	lifecycle := &childLifecycleStub{}
	tool, err := NewDelegateResearchTool(&delegateFailingChatStub{}, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	tool.SetChildRunLifecycle(lifecycle)
	result, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`))
	if err != nil {
		t.Fatalf("Call() error = %v, want partial result", err)
	}
	if !strings.Contains(result.Content, "已检索到 1 条资料") || result.Metadata["child_status"] != string(agent.RunFailed) {
		t.Fatalf("partial result = %#v", result)
	}
	if result.Metadata["partial_result"] != true || result.Metadata["checkpointable"] != false || result.Metadata["resume_available"] != true {
		t.Fatalf("partial metadata = %#v", result.Metadata)
	}
	if lifecycle.finished {
		t.Fatal("failed child was reported as succeeded")
	}
}

func TestDelegateResearchToolPublishesAssociatedChildEvents(t *testing.T) {
	tool, err := NewDelegateResearchTool(&delegateChatStub{}, delegateSearcherStub{}, 7, 4096, 3, nil, false, retrieval.DefaultKeywordThreshold)
	if err != nil {
		t.Fatalf("NewDelegateResearchTool() error = %v", err)
	}
	tool.SetParentRun(42, "parent-run")
	var events []agent.Event
	tool.SetChildEventSink(func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"question":"研究年假"}`)); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len(events) == 0 {
		t.Fatal("child event sink received no events")
	}
	seenResult := false
	for _, event := range events {
		if (event.Type != agent.EventChildEvent && event.Type != agent.EventChildResult) || event.RunID != "parent-run" {
			t.Fatalf("child event = %#v", event)
		}
		data, ok := event.Data.(map[string]any)
		if !ok || data["child_run_id"] == "" || data["parent_run_id"] != "parent-run" {
			t.Fatalf("child event data = %#v", event.Data)
		}
		if event.Type == agent.EventChildResult {
			seenResult = true
			if data["phase"] != "result" || data["result_available"] != true {
				t.Fatalf("child result data = %#v", event.Data)
			}
		}
	}
	if !seenResult {
		t.Fatal("child event sink received no separate child_result event")
	}
}
