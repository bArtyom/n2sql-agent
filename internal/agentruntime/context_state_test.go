package agentruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type contextSummaryStub struct{}

func (contextSummaryStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{Message: "早期工具结果已经确认：年假按入职年限计算；仍需核对试用期规则。"}, nil
}

func TestCompactContextStatePersistsSummarySeparately(t *testing.T) {
	state := ContextState{Messages: []modelclient.ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: strings.Repeat("第一轮问题 ", 20)},
		{Role: "assistant", Content: strings.Repeat("第一轮回答 ", 20)},
		{Role: "tool", Content: "UNTRUSTED_TOOL_RESULT\n{\"trusted\":false,\"content\":\"" + strings.Repeat("年假按入职年限计算；试用期规则待核对 ", 20) + "\"}"},
		{Role: "user", Content: "当前问题：请给我最终结论"},
	}}

	got := compactContextState(context.Background(), state, 230, contextSummaryStub{})
	if got.SummaryText == "" {
		t.Fatal("SummaryText is empty, want compressed durable memory")
	}
	if got.LastStep != state.LastStep {
		t.Fatalf("LastStep = %d, want %d", got.LastStep, state.LastStep)
	}
	if len(got.Messages) == 0 || got.Messages[len(got.Messages)-1].Content != "当前问题：请给我最终结论" {
		t.Fatalf("current user message was not retained: %#v", got.Messages)
	}
	if messageBytes(got.Messages) > 230 {
		t.Fatalf("compacted messages = %d bytes, want <= 230", messageBytes(got.Messages))
	}
}

func TestRestoreContextStateDoesNotDuplicateSummaryMessage(t *testing.T) {
	state := ContextState{
		Messages: []modelclient.ChatMessage{
			{Role: "system", Content: "system"},
			{Role: "user", Content: "当前问题"},
		},
		SummaryText: "早期结论",
	}

	got := restoreContextState(state)
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(got.Messages))
	}
	if got.Messages[1].Role != "system" || got.Messages[1].Content != "Agent 短记忆（较早工具结果摘要）：\n早期结论" {
		t.Fatalf("summary message = %#v", got.Messages[1])
	}

	got = restoreContextState(got)
	if len(got.Messages) != 3 {
		t.Fatalf("restored messages = %d, want no duplicate summary", len(got.Messages))
	}
}

func TestBuildTurnContextRebuildsSystemPromptAndKeepsHiddenThreadState(t *testing.T) {
	state := ContextState{
		SummaryText: "上一轮已确认年假按工龄计算。",
		Messages: []modelclient.ChatMessage{
			{Role: "system", Content: "旧系统提示"},
			{Role: "user", Content: "上一轮问题"},
			{Role: "tool", ToolCallID: "search-1", Content: "检索结果"},
			{Role: "assistant", Content: "上一轮答案"},
		},
	}

	got := BuildTurnContext("新系统提示", state)
	if len(got) != 5 {
		t.Fatalf("turn context messages = %#v, want fresh system, summary, and three non-system messages", got)
	}
	if got[0].Role != "system" || got[0].Content != "新系统提示" {
		t.Fatalf("system prompt = %#v, want rebuilt prompt", got[0])
	}
	if got[1].Role != "system" || !strings.Contains(got[1].Content, state.SummaryText) {
		t.Fatalf("summary message = %#v, want hidden durable summary", got[1])
	}
	if got[2].Role != "user" || got[3].Role != "tool" || got[4].Role != "assistant" {
		t.Fatalf("non-system thread state = %#v", got[2:])
	}
}
