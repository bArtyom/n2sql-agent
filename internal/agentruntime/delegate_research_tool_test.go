package agentruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type delegateChatStub struct {
	calls       int
	definitions [][]agent.FunctionDefinition
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
