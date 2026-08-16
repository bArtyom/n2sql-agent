package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type conversationStoreStub struct {
	records      []conversation.Conversation
	messages     []conversation.Message
	searchQuery  string
	searchLimit  int
	idempotency  map[string]conversation.IdempotentResponse
	exchangeMeta conversation.MessageMetadata
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
func (s *conversationStoreStub) Search(_ context.Context, _ int64, query string, limit int) ([]conversation.Conversation, error) {
	s.searchQuery = query
	s.searchLimit = limit
	return s.records, nil
}
func (s *conversationStoreStub) SearchMessages(_ context.Context, _ int64, query string, limit int) ([]conversation.MessageSearchResult, error) {
	s.searchQuery = query
	s.searchLimit = limit
	return []conversation.MessageSearchResult{{MessageID: 3, Summary: "匹配内容"}}, nil
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

func (s *conversationStoreStub) ListMessagesPage(_ context.Context, id, beforeID int64, limit int) ([]conversation.Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	older := make([]conversation.Message, 0)
	for _, message := range s.messages {
		if message.ConversationID == id && (beforeID <= 0 || message.ID < beforeID) {
			older = append(older, message)
		}
	}
	if len(older) > limit {
		return older[len(older)-limit:], true, nil
	}
	return older, false, nil
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

func (s *conversationStoreStub) AppendExchangeWithMetadata(ctx context.Context, id int64, user, assistant string, metadata conversation.MessageMetadata) error {
	s.exchangeMeta = metadata
	return s.AppendExchange(ctx, id, user, assistant)
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

func (s *conversationStoreStub) SetPinned(_ context.Context, id int64, pinned bool) (conversation.Conversation, error) {
	for index := range s.records {
		if s.records[index].ID == id {
			s.records[index].IsPinned = pinned
			return s.records[index], nil
		}
	}
	return conversation.Conversation{}, conversation.ErrNotFound
}

func (s *conversationStoreStub) SetChatModel(_ context.Context, id int64, model string) (conversation.Conversation, error) {
	for index := range s.records {
		if s.records[index].ID == id {
			s.records[index].ChatModel = model
			return s.records[index], nil
		}
	}
	return conversation.Conversation{}, conversation.ErrNotFound
}

func (s *conversationStoreStub) ClearMessages(_ context.Context, id int64) error {
	kept := s.messages[:0]
	for _, message := range s.messages {
		if message.ConversationID != id {
			kept = append(kept, message)
		}
	}
	s.messages = kept
	return nil
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

func (s *conversationStoreStub) GetIdempotentResponse(_ context.Context, conversationID int64, key string) (conversation.IdempotentResponse, error) {
	response, ok := s.idempotency[fmt.Sprintf("%d:%s", conversationID, key)]
	if !ok {
		return conversation.IdempotentResponse{}, conversation.ErrNotFound
	}
	response.Response = append([]byte(nil), response.Response...)
	return response, nil
}

func (s *conversationStoreStub) SaveIdempotentResponse(_ context.Context, conversationID int64, key, requestHash string, response []byte) error {
	if s.idempotency == nil {
		s.idempotency = make(map[string]conversation.IdempotentResponse)
	}
	s.idempotency[fmt.Sprintf("%d:%s", conversationID, key)] = conversation.IdempotentResponse{RequestHash: requestHash, Response: append([]byte(nil), response...)}
	return nil
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

func TestConversationHandlerSearchesConversations(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 1, KnowledgeBaseID: 7, Title: "年假规则"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations?q=%E5%B9%B4%E5%81%87&limit=12", nil)
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.searchQuery != "年假" || store.searchLimit != 12 {
		t.Fatalf("search: status=%d query=%q limit=%d", response.Code, store.searchQuery, store.searchLimit)
	}
}

func TestConversationHandlerRejectsInvalidSearchLimit(t *testing.T) {
	endpoint := handler.NewConversations(conversation.NewService(&conversationStoreStub{}))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations?q=test&limit=101", nil)
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestConversationMessageSearchReturnsMatches(t *testing.T) {
	store := &conversationStoreStub{}
	endpoint := handler.NewConversationMessageSearch(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations/messages/search?q=%E5%B9%B4%E5%81%87&limit=8", nil)
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusOK || store.searchQuery != "年假" || store.searchLimit != 8 || !strings.Contains(response.Body.String(), `"messageId":3`) {
		t.Fatalf("message search: status=%d query=%q limit=%d body=%s", response.Code, store.searchQuery, store.searchLimit, response.Body.String())
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"问题"`) || !strings.Contains(response.Body.String(), `"has_more":false`) {
		t.Fatalf("messages response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationHandlerPaginatesMessages(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	for id := int64(1); id <= 5; id++ {
		store.messages = append(store.messages, conversation.Message{ID: id, ConversationID: 3, Role: "user", Content: "消息"})
	}
	endpoint := handler.NewConversations(conversation.NewService(store))

	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations/3/messages?limit=2", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(first, request)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"has_more":true`) {
		t.Fatalf("first page: status=%d body=%s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations/3/messages?limit=2&before_id=4", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(second, request)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"has_more":true`) {
		t.Fatalf("second page: status=%d body=%s", second.Code, second.Body.String())
	}

	last := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/conversations/3/messages?limit=2&before_id=2", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(last, request)
	if last.Code != http.StatusOK || !strings.Contains(last.Body.String(), `"has_more":false`) {
		t.Fatalf("last page: status=%d body=%s", last.Code, last.Body.String())
	}
}

func TestConversationHandlerPinsAndUnpinsConversation(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))

	pin := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/7/conversations/3", strings.NewReader(`{"is_pinned":true}`))
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(pin, request)
	if pin.Code != http.StatusOK || !strings.Contains(pin.Body.String(), `"isPinned":true`) {
		t.Fatalf("pin response: status=%d body=%s", pin.Code, pin.Body.String())
	}

	unpin := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/7/conversations/3", strings.NewReader(`{"is_pinned":false}`))
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(unpin, request)
	if unpin.Code != http.StatusOK || !strings.Contains(unpin.Body.String(), `"isPinned":false`) {
		t.Fatalf("unpin response: status=%d body=%s", unpin.Code, unpin.Body.String())
	}
}

func TestConversationHandlerUpdatesChatModel(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/7/conversations/3", strings.NewReader(`{"chat_model":"chat-fast"}`))
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"chatModel":"chat-fast"`) {
		t.Fatalf("chat model response: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestConversationHandlerRejectsUnconfiguredChatModel(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	providers := &modelProviderStoreStub{provider: modelprovider.Provider{
		ChatModel:  "chat-default",
		ChatModels: []string{"chat-default", "chat-fast"},
	}}
	endpoint := handler.NewConversationsWithModelProvider(conversation.NewService(store), providers)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/7/conversations/3", strings.NewReader(`{"chat_model":"unconfigured"}`))
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestConversationHandlerRejectsPinnedOnOtherKnowledgeBase(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/8/conversations/3", strings.NewReader(`{"is_pinned":true}`))
	request.SetPathValue("id", "8")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross knowledge base pin status = %d, want 404", response.Code)
	}
}

func TestConversationHandlerRejectsPatchWithoutFields(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/7/conversations/3", strings.NewReader(`{}`))
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty patch status = %d, want 400", response.Code)
	}
}

func TestConversationHandlerClearsMessages(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	store.messages = []conversation.Message{
		{ID: 1, ConversationID: 3, Role: "user", Content: "问题"},
		{ID: 2, ConversationID: 3, Role: "assistant", Content: "回答"},
		{ID: 3, ConversationID: 9, Role: "user", Content: "别的会话"},
	}
	endpoint := handler.NewConversations(conversation.NewService(store))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/7/conversations/3/messages", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("conversationId", "3")
	request.SetPathValue("messages", "messages")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("clear status = %d, want 204", response.Code)
	}
	if len(store.messages) != 1 || store.messages[0].ConversationID != 9 {
		t.Fatalf("messages after clear = %#v, want only other conversation", store.messages)
	}
}

func TestConversationHandlerRejectsClearOnOtherKnowledgeBase(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 3, KnowledgeBaseID: 7, Title: "对话"}}}
	endpoint := handler.NewConversations(conversation.NewService(store))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/8/conversations/3/messages", nil)
	request.SetPathValue("id", "8")
	request.SetPathValue("conversationId", "3")
	request.SetPathValue("messages", "messages")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross knowledge base clear status = %d, want 404", response.Code)
	}
}
