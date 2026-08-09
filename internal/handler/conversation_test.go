package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type conversationStoreStub struct {
	records  []conversation.Conversation
	messages []conversation.Message
}

func (s *conversationStoreStub) Create(_ context.Context, input conversation.CreateInput) (conversation.Conversation, error) {
	record := conversation.Conversation{ID: int64(len(s.records) + 1), KnowledgeBaseID: input.KnowledgeBaseID, Title: input.Title, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.records = append(s.records, record)
	return record, nil
}
func (s *conversationStoreStub) Get(_ context.Context, id int64) (conversation.Conversation, error) {
	for _, record := range s.records {
		if record.ID == id {
			return record, nil
		}
	}
	return conversation.Conversation{}, conversation.ErrNotFound
}
func (s *conversationStoreStub) List(context.Context, int64) ([]conversation.Conversation, error) {
	return s.records, nil
}
func (s *conversationStoreStub) ListMessages(_ context.Context, id int64) ([]conversation.Message, error) {
	result := make([]conversation.Message, 0)
	for _, message := range s.messages {
		if message.ConversationID == id {
			result = append(result, message)
		}
	}
	return result, nil
}
func (s *conversationStoreStub) GetSummary(context.Context, int64) (conversation.Summary, error) {
	return conversation.Summary{}, conversation.ErrNotFound
}
func (s *conversationStoreStub) SaveSummary(context.Context, int64, int64, string) error { return nil }
func (s *conversationStoreStub) AppendExchange(_ context.Context, id int64, user, assistant string) error {
	s.messages = append(s.messages,
		conversation.Message{ID: int64(len(s.messages) + 1), ConversationID: id, Role: "user", Content: user},
		conversation.Message{ID: int64(len(s.messages) + 2), ConversationID: id, Role: "assistant", Content: assistant})
	return nil
}

func (s *conversationStoreStub) UpdateTitle(_ context.Context, id int64, title string) (conversation.Conversation, error) {
	for index := range s.records {
		if s.records[index].ID == id {
			s.records[index].Title = title
			return s.records[index], nil
		}
	}
	return conversation.Conversation{}, conversation.ErrNotFound
}

func (s *conversationStoreStub) Delete(_ context.Context, id int64) error {
	for index := range s.records {
		if s.records[index].ID == id {
			s.records = append(s.records[:index], s.records[index+1:]...)
			return nil
		}
	}
	return conversation.ErrNotFound
}

func TestConversationHandlerCreatesAndListsConversation(t *testing.T) {
	store := &conversationStoreStub{}
	endpoint := handler.NewConversations(conversation.NewService(store))
	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/conversations", strings.NewReader(`{"title":"  产品资料  "}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(create, request)
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), `"title":"产品资料"`) {
		t.Fatalf("create response: status=%d body=%s", create.Code, create.Body.String())
	}

	list := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations", nil)
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(list, request)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"knowledgeBaseId":7`) {
		t.Fatalf("list response: status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestConversationHandlerReturnsMessages(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	store.messages = []conversation.Message{{ID: 1, ConversationID: 3, Role: "user", Content: "问题"}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations/3/messages", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"问题"`) {
		t.Fatalf("messages response: status=%d body=%s", response.Code, response.Body.String())
	}
}
