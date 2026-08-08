package conversation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
)

type storeStub struct {
	createdInput conversation.CreateInput
	conversation conversation.Conversation
	messages     []conversation.Message
	exchangeUser string
	exchangeBot  string
	exchangeErr  error
}

func (s *storeStub) Create(_ context.Context, input conversation.CreateInput) (conversation.Conversation, error) {
	s.createdInput = input
	return s.conversation, nil
}

func (s *storeStub) Get(context.Context, int64) (conversation.Conversation, error) {
	return s.conversation, nil
}

func (s *storeStub) List(context.Context, int64) ([]conversation.Conversation, error) {
	return []conversation.Conversation{s.conversation}, nil
}

func (s *storeStub) ListMessages(context.Context, int64) ([]conversation.Message, error) {
	return s.messages, nil
}

func (s *storeStub) AppendExchange(_ context.Context, _ int64, user, assistant string) error {
	s.exchangeUser = user
	s.exchangeBot = assistant
	return s.exchangeErr
}

func (s *storeStub) UpdateTitle(_ context.Context, _ int64, title string) (conversation.Conversation, error) {
	s.conversation.Title = title
	return s.conversation, nil
}

func (s *storeStub) Delete(context.Context, int64) error { return nil }

func TestServiceCreatesConversationWithTrimmedTitle(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7, Title: "年假"}}
	service := conversation.NewService(store)

	created, err := service.Create(context.Background(), 7, "  年假  ")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if store.createdInput.KnowledgeBaseID != 7 || store.createdInput.Title != "年假" || created.ID != 9 {
		t.Fatalf("created = %#v, input = %#v", created, store.createdInput)
	}
}

func TestServiceLoadsScopedHistoryAsAgentMessages(t *testing.T) {
	createdAt := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	store := &storeStub{
		conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7},
		messages: []conversation.Message{
			{ID: 1, ConversationID: 9, Role: "user", Content: "年假有几天？", CreatedAt: createdAt},
			{ID: 2, ConversationID: 9, Role: "assistant", Content: "五天。", CreatedAt: createdAt.Add(time.Second)},
		},
	}
	service := conversation.NewService(store)

	history, err := service.History(context.Background(), 9, 7)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	want := []agentservice.HistoryMessage{
		{Role: "user", Content: "年假有几天？"},
		{Role: "assistant", Content: "五天。"},
	}
	if len(history) != len(want) || history[0] != want[0] || history[1] != want[1] {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
}

func TestServiceSavesCompletedExchange(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)

	if err := service.SaveExchange(context.Background(), 9, "  问题  ", "  答案  "); err != nil {
		t.Fatalf("SaveExchange() error = %v", err)
	}
	if store.exchangeUser != "问题" || store.exchangeBot != "答案" {
		t.Fatalf("saved exchange = %q / %q, want trimmed messages", store.exchangeUser, store.exchangeBot)
	}
}

func TestServiceRejectsInvalidConversationInputs(t *testing.T) {
	service := conversation.NewService(&storeStub{})
	if _, err := service.Create(context.Background(), 0, "标题"); !errors.Is(err, conversation.ErrInvalidKnowledgeBase) {
		t.Fatalf("Create() error = %v, want invalid knowledge base", err)
	}
	if err := service.SaveExchange(context.Background(), 0, "问题", "答案"); !errors.Is(err, conversation.ErrInvalidConversation) {
		t.Fatalf("SaveExchange() error = %v, want invalid conversation", err)
	}
}
