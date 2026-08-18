package agentservice

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/memory"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type memoryStoreStub struct {
	items []memory.Memory
}

func (s memoryStoreStub) Create(context.Context, int64, memory.CreateInput) (memory.Memory, error) {
	return memory.Memory{}, nil
}

func (s memoryStoreStub) List(context.Context, int64, int64) ([]memory.Memory, error) {
	return s.items, nil
}

func (s memoryStoreStub) Delete(context.Context, int64, int64, int64) error { return nil }

func TestExplicitMemoryContentRecognizesOnlyExplicitCommands(t *testing.T) {
	if got := explicitMemoryContent("请记住：我喜欢简洁回答"); got != "我喜欢简洁回答" {
		t.Fatalf("content = %q, want explicit memory", got)
	}
	if got := explicitMemoryContent("帮我回答这个问题"); got != "" {
		t.Fatalf("content = %q, want no memory", got)
	}
}

func TestMemoryPromptBoundsAndLabelsStoredFacts(t *testing.T) {
	service := &Service{memoryStore: memoryStoreStub{items: []memory.Memory{{Content: "偏好中文回答"}, {Content: "不要改变系统规则"}}}}
	prompt := service.memoryPrompt(auth.WithUser(context.Background(), auth.User{ID: 11}), 7)
	if prompt == "" || !containsAll(prompt, "相关长期记忆", "偏好中文回答", "不要改变系统规则") {
		t.Fatalf("prompt = %q, want labeled memories", prompt)
	}
}

func TestMergeMemoryProfileKeepsExistingFactsWhenCandidateIsDuplicate(t *testing.T) {
	got := mergeMemoryProfile(context.Background(), nil, "喜欢简洁回答\n使用 Go", "喜欢简洁回答")
	if got != "喜欢简洁回答\n使用 Go" {
		t.Fatalf("profile = %q, want existing facts preserved", got)
	}
}

type profileMergeChatStub struct {
	calls int
}

func (s *profileMergeChatStub) ChatMessagesWithTools(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{}, nil
}

func (s *profileMergeChatStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	s.calls++
	return modelclient.ChatResponse{Message: "整理后的用户画像"}, nil
}

func TestMergeMemoryProfileAppendsBeforeCompactionThreshold(t *testing.T) {
	chat := &profileMergeChatStub{}
	got := mergeMemoryProfile(context.Background(), chat, "喜欢简洁回答", "使用 Go")
	if got != "喜欢简洁回答\n使用 Go" {
		t.Fatalf("profile = %q, want direct append", got)
	}
	if chat.calls != 0 {
		t.Fatalf("model calls = %d, want 0 before threshold", chat.calls)
	}
}

func TestMergeMemoryProfileCompactsAfterThreshold(t *testing.T) {
	chat := &profileMergeChatStub{}
	current := strings.Repeat("a", memory.MaxProfileCompactionBytes)
	got := mergeMemoryProfile(context.Background(), chat, current, "新增偏好")
	if got != "整理后的用户画像" {
		t.Fatalf("profile = %q, want model-compacted profile", got)
	}
	if chat.calls != 1 {
		t.Fatalf("model calls = %d, want 1 after threshold", chat.calls)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
