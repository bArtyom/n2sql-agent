package agentservice

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/memory"
)

type memoryStoreStub struct {
	items []memory.Memory
}

func (s memoryStoreStub) Create(context.Context, memory.CreateInput) (memory.Memory, error) {
	return memory.Memory{}, nil
}

func (s memoryStoreStub) List(context.Context, int64) ([]memory.Memory, error) {
	return s.items, nil
}

func (s memoryStoreStub) Delete(context.Context, int64, int64) error { return nil }

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
	prompt := service.memoryPrompt(context.Background(), 7)
	if prompt == "" || !containsAll(prompt, "知识库长期记忆", "偏好中文回答", "不要改变系统规则") {
		t.Fatalf("prompt = %q, want labeled memories", prompt)
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
