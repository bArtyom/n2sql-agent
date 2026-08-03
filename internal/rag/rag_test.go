package rag_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type searcherStub struct{}

func (searcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{
		DocumentID:       10,
		OriginalFilename: "guide.md",
		Position:         2,
		Content:          "执行 go run ./cmd/server 启动服务。",
		Distance:         0.12,
	}}, nil
}

type chatStub struct {
	messages []modelclient.ChatMessage
}

func (s *chatStub) ChatMessages(_ context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	s.messages = messages
	return modelclient.ChatResponse{Message: "可以执行 go run ./cmd/server。"}, nil
}

func TestServiceBuildsGroundedPromptAndReturnsSources(t *testing.T) {
	chat := &chatStub{}
	service := rag.NewService(searcherStub{}, chat)

	response, err := service.Answer(context.Background(), 7, "如何启动服务？", 5)
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "可以执行 go run ./cmd/server。" || len(response.Sources) != 1 || response.Sources[0].OriginalFilename != "guide.md" {
		t.Fatalf("response = %#v", response)
	}
	if len(chat.messages) != 2 || chat.messages[0].Role != "system" || chat.messages[1].Role != "user" {
		t.Fatalf("messages = %#v", chat.messages)
	}
	if !strings.Contains(chat.messages[1].Content, "如何启动服务？") || !strings.Contains(chat.messages[1].Content, "guide.md") || !strings.Contains(chat.messages[1].Content, "go run ./cmd/server") {
		t.Fatalf("prompt = %q", chat.messages[1].Content)
	}
}

type emptySearcherStub struct{}

func (emptySearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return nil, nil
}

func TestServiceRejectsQuestionsWithoutSources(t *testing.T) {
	service := rag.NewService(emptySearcherStub{}, &chatStub{})

	_, err := service.Answer(context.Background(), 7, "没有资料的问题", 5)
	if !errors.Is(err, rag.ErrNoSources) {
		t.Fatalf("Answer() error = %v, want ErrNoSources", err)
	}
}

type failingSearcherStub struct{ err error }

func (s failingSearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return nil, s.err
}

func TestServicePropagatesSearchFailure(t *testing.T) {
	expected := errors.New("database unavailable")
	service := rag.NewService(failingSearcherStub{err: expected}, &chatStub{})

	_, err := service.Answer(context.Background(), 7, "问题", 5)
	if !errors.Is(err, expected) {
		t.Fatalf("Answer() error = %v, want %v", err, expected)
	}
}

type failingChatStub struct{ err error }

func (s failingChatStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{}, s.err
}

func TestServicePropagatesChatFailure(t *testing.T) {
	expected := errors.New("model unavailable")
	service := rag.NewService(searcherStub{}, failingChatStub{err: expected})

	_, err := service.Answer(context.Background(), 7, "问题", 5)
	if !errors.Is(err, expected) {
		t.Fatalf("Answer() error = %v, want %v", err, expected)
	}
}

type emptyAnswerChatStub struct{}

func (emptyAnswerChatStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{}, nil
}

func TestServiceRejectsEmptyChatAnswer(t *testing.T) {
	service := rag.NewService(searcherStub{}, emptyAnswerChatStub{})

	_, err := service.Answer(context.Background(), 7, "问题", 5)
	if err == nil || !strings.Contains(err.Error(), "does not contain an answer") {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceCapsPromptContext(t *testing.T) {
	chat := &chatStub{}
	longContent := strings.Repeat("资料 ", 20000)
	service := rag.NewService(longSourceSearcherStub{content: longContent}, chat)

	if _, err := service.Answer(context.Background(), 7, "问题", 5); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if len(chat.messages) != 2 || len(chat.messages[1].Content) > rag.MaxPromptBytes {
		t.Fatalf("prompt size = %d, want <= %d", len(chat.messages[1].Content), rag.MaxPromptBytes)
	}
	if !strings.Contains(chat.messages[1].Content, "<reference_material>") || !strings.Contains(chat.messages[1].Content, "</reference_material>") {
		t.Fatalf("prompt delimiters missing: %q", chat.messages[1].Content[:min(len(chat.messages[1].Content), 100)])
	}
}

type longSourceSearcherStub struct{ content string }

func (s longSourceSearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{OriginalFilename: "long.md", Content: s.content}}, nil
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
