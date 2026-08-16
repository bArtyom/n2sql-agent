package conversation_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

type storeStub struct {
	createdInput conversation.CreateInput
	conversation conversation.Conversation
	searchQuery  string
	searchLimit  int
	messages     []conversation.Message
	exchangeUser string
	exchangeBot  string
	exchangeMeta conversation.MessageMetadata
	exchangeErr  error
	summary      conversation.Summary
	summaryErr   error
	idempotency  map[string]conversation.IdempotentResponse
}

type distributedLockStoreStub struct {
	*storeStub
	lockCalls int
	lockErr   error
}

func (s *distributedLockStoreStub) WithConversationLock(_ context.Context, conversationID int64, fn func() error) error {
	s.lockCalls++
	if conversationID != 9 {
		return errors.New("unexpected conversation ID")
	}
	if s.lockErr != nil {
		return s.lockErr
	}
	return fn()
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

func (s *storeStub) Search(_ context.Context, _ int64, query string, limit int) ([]conversation.Conversation, error) {
	s.searchQuery = query
	s.searchLimit = limit
	return []conversation.Conversation{s.conversation}, nil
}

func (s *storeStub) SearchMessages(_ context.Context, _ int64, query string, limit int) ([]conversation.MessageSearchResult, error) {
	s.searchQuery = query
	s.searchLimit = limit
	return []conversation.MessageSearchResult{{MessageID: 3, Summary: "匹配内容"}}, nil
}

func (s *storeStub) ListMessages(context.Context, int64) ([]conversation.Message, error) {
	return s.messages, nil
}

func (s *storeStub) ListMessagesPage(_ context.Context, conversationID, beforeID int64, limit int) ([]conversation.Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	older := make([]conversation.Message, 0)
	for _, message := range s.messages {
		if message.ConversationID == conversationID && (beforeID <= 0 || message.ID < beforeID) {
			older = append(older, message)
		}
	}
	if len(older) > limit {
		return older[len(older)-limit:], true, nil
	}
	return older, false, nil
}

func (s *storeStub) GetSummary(context.Context, int64) (conversation.Summary, error) {
	return s.summary, s.summaryErr
}

func (s *storeStub) SaveSummary(_ context.Context, conversationID, throughMessageID int64, content string) error {
	s.summary = conversation.Summary{ConversationID: conversationID, ThroughMessageID: throughMessageID, Content: content}
	return nil
}

func (s *storeStub) AppendExchange(_ context.Context, _ int64, user, assistant string) error {
	s.exchangeUser = user
	s.exchangeBot = assistant
	return s.exchangeErr
}

func (s *storeStub) AppendExchangeWithMetadata(_ context.Context, _ int64, user, assistant string, metadata conversation.MessageMetadata) error {
	s.exchangeUser = user
	s.exchangeBot = assistant
	s.exchangeMeta = metadata
	return s.exchangeErr
}

func (s *storeStub) UpdateTitle(_ context.Context, _ int64, title string) (conversation.Conversation, error) {
	s.conversation.Title = title
	return s.conversation, nil
}

func (s *storeStub) SetPinned(_ context.Context, _ int64, pinned bool) (conversation.Conversation, error) {
	s.conversation.IsPinned = pinned
	return s.conversation, nil
}

func (s *storeStub) SetChatModel(_ context.Context, _ int64, model string) (conversation.Conversation, error) {
	s.conversation.ChatModel = model
	return s.conversation, nil
}

func (s *storeStub) ClearMessages(context.Context, int64) error { return nil }

func (s *storeStub) Delete(context.Context, int64) error { return nil }

func (s *storeStub) GetIdempotentResponse(_ context.Context, conversationID int64, key string) (conversation.IdempotentResponse, error) {
	if response, ok := s.idempotency[fmt.Sprintf("%d:%s", conversationID, key)]; ok {
		response.Response = append([]byte(nil), response.Response...)
		return response, nil
	}
	return conversation.IdempotentResponse{}, conversation.ErrNotFound
}

func (s *storeStub) SaveIdempotentResponse(_ context.Context, conversationID int64, key, requestHash string, response []byte) error {
	if s.idempotency == nil {
		s.idempotency = make(map[string]conversation.IdempotentResponse)
	}
	s.idempotency[fmt.Sprintf("%d:%s", conversationID, key)] = conversation.IdempotentResponse{RequestHash: requestHash, Response: append([]byte(nil), response...)}
	return nil
}

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

func TestServiceSearchTrimsQueryAndUsesDefaultLimit(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)
	results, err := service.Search(context.Background(), 7, "  年假  ", 0)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if store.searchQuery != "年假" || store.searchLimit != 50 || len(results) != 1 {
		t.Fatalf("search = query %q limit %d results %#v", store.searchQuery, store.searchLimit, results)
	}
}

func TestServiceSearchRejectsBlankOrOversizedQuery(t *testing.T) {
	service := conversation.NewService(&storeStub{})
	if _, err := service.Search(context.Background(), 7, "   ", 10); !errors.Is(err, conversation.ErrInvalidSearchQuery) {
		t.Fatalf("blank query error = %v", err)
	}
	if _, err := service.Search(context.Background(), 7, strings.Repeat("a", 201), 10); !errors.Is(err, conversation.ErrInvalidSearchQuery) {
		t.Fatalf("oversized query error = %v", err)
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
		{ID: 1, Role: "user", Content: "年假有几天？"},
		{ID: 2, Role: "assistant", Content: "五天。"},
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

func TestServiceSetsScopedChatModel(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)

	updated, err := service.SetChatModel(context.Background(), 9, 7, "  chat-fast  ")
	if err != nil {
		t.Fatalf("SetChatModel() error = %v", err)
	}
	if updated.ChatModel != "chat-fast" || store.conversation.ChatModel != "chat-fast" {
		t.Fatalf("chat model = %q / %q, want chat-fast", updated.ChatModel, store.conversation.ChatModel)
	}
	if _, err := service.SetChatModel(context.Background(), 9, 8, "chat-fast"); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("cross-knowledge-base SetChatModel() error = %v, want ErrNotFound", err)
	}
}

func TestServiceSavesBoundedExchangeMetadata(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)
	retrievalStats := &usage.RetrievalObservation{VectorCandidates: 12, FinalFiltered: 4}

	if err := service.SaveExchangeWithMetadata(context.Background(), 9, "问题", "答案", conversation.MessageMetadata{Retrieval: retrievalStats}); err != nil {
		t.Fatalf("SaveExchangeWithMetadata() error = %v", err)
	}
	if store.exchangeMeta.Retrieval == nil || store.exchangeMeta.Retrieval.VectorCandidates != 12 || store.exchangeMeta.Retrieval.FinalFiltered != 4 {
		t.Fatalf("saved metadata = %#v, want retrieval counts", store.exchangeMeta)
	}
}

func TestServiceLoadsAndSavesScopedSummary(t *testing.T) {
	store := &storeStub{
		conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7},
		summary:      conversation.Summary{ConversationID: 9, ThroughMessageID: 12, Content: "之前讨论过年假。"},
	}
	service := conversation.NewService(store)

	summary, err := service.Summary(context.Background(), 9, 7)
	if err != nil || summary.ThroughMessageID != 12 {
		t.Fatalf("Summary() = %#v, error = %v", summary, err)
	}
	if err := service.SaveSummary(context.Background(), 9, 7, 14, "  新摘要  "); err != nil {
		t.Fatalf("SaveSummary() error = %v", err)
	}
	if store.summary.ThroughMessageID != 14 || store.summary.Content != "新摘要" {
		t.Fatalf("saved summary = %#v", store.summary)
	}
}

func TestServiceSerializesSummaryWorkPerConversation(t *testing.T) {
	service := conversation.NewService(&storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}})
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- service.WithSummaryLock(context.Background(), 9, 7, func() error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()
	<-firstStarted
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- service.WithSummaryLock(context.Background(), 9, 7, func() error {
			close(secondStarted)
			return nil
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("second summary callback started before first released")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second lock error = %v", err)
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second summary callback did not start")
	}
}

func TestServiceUsesDistributedConversationLockWhenStoreProvidesIt(t *testing.T) {
	store := &distributedLockStoreStub{storeStub: &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}}
	service := conversation.NewService(store)
	called := false
	if err := service.WithSummaryLock(context.Background(), 9, 7, func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("WithSummaryLock() error = %v", err)
	}
	if !called || store.lockCalls != 1 {
		t.Fatalf("callback=%t distributed lock calls=%d, want callback and one external lock", called, store.lockCalls)
	}
}

func TestServicePropagatesDistributedLockFailure(t *testing.T) {
	wantErr := errors.New("database lock unavailable")
	store := &distributedLockStoreStub{
		storeStub: &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}},
		lockErr:   wantErr,
	}
	service := conversation.NewService(store)
	if err := service.WithSummaryLock(context.Background(), 9, 7, func() error { return nil }); !errors.Is(err, wantErr) {
		t.Fatalf("WithSummaryLock() error = %v, want distributed lock failure", err)
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

func TestServiceStoresScopedIdempotentResponse(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)

	requestHash := strings.Repeat("a", 64)
	if err := service.SaveIdempotentResponse(context.Background(), 9, 7, "request-1", requestHash, []byte(`{"answer":"五天"}`)); err != nil {
		t.Fatalf("SaveIdempotentResponse() error = %v", err)
	}
	response, err := service.GetIdempotentResponse(context.Background(), 9, 7, "request-1", requestHash)
	if err != nil {
		t.Fatalf("GetIdempotentResponse() error = %v", err)
	}
	if string(response) != `{"answer":"五天"}` {
		t.Fatalf("response = %q, want stored JSON", response)
	}
}

func TestServiceRejectsInvalidIdempotencyKey(t *testing.T) {
	service := conversation.NewService(&storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}})
	for _, key := range []string{"", "bad key", strings.Repeat("a", 129)} {
		if err := service.SaveIdempotentResponse(context.Background(), 9, 7, key, strings.Repeat("a", 64), []byte(`{"answer":"答案"}`)); !errors.Is(err, conversation.ErrInvalidIdempotencyKey) {
			t.Fatalf("SaveIdempotentResponse(%q) error = %v, want invalid key", key, err)
		}
	}
}

func TestServicePinsAndUnpinsOwnedConversation(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7}}
	service := conversation.NewService(store)

	pinned, err := service.SetPinned(context.Background(), 9, 7, true)
	if err != nil {
		t.Fatalf("SetPinned(true) error = %v", err)
	}
	if !pinned.IsPinned {
		t.Fatal("SetPinned(true) returned IsPinned = false, want true")
	}
	unpinned, err := service.SetPinned(context.Background(), 9, 7, false)
	if err != nil {
		t.Fatalf("SetPinned(false) error = %v", err)
	}
	if unpinned.IsPinned {
		t.Fatal("SetPinned(false) returned IsPinned = true, want false")
	}
	if _, err := service.SetPinned(context.Background(), 9, 8, true); !errors.Is(err, conversation.ErrNotFound) {
		t.Fatalf("cross knowledge base error = %v, want ErrNotFound", err)
	}
	if _, err := service.SetPinned(context.Background(), 0, 7, true); !errors.Is(err, conversation.ErrInvalidConversation) {
		t.Fatalf("invalid conversation error = %v, want ErrInvalidConversation", err)
	}
}

func TestServiceAutoTitlesDefaultConversation(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7, Title: conversation.DefaultTitle}}
	service := conversation.NewService(store)
	if err := service.AutoTitle(context.Background(), 9, 7, "请总结这篇文档的奖励函数设计，并且把记忆写入机制也一起说一下，最后讲讲原创性风险"); err != nil {
		t.Fatalf("AutoTitle() error = %v", err)
	}
	if !strings.HasPrefix(store.conversation.Title, "请总结这篇文档的奖励函数设计") || !strings.HasSuffix(store.conversation.Title, "…") {
		t.Fatalf("AutoTitle() title = %q, want capped question summary", store.conversation.Title)
	}
}

func TestServiceAutoTitleSkipsUserRenamedConversation(t *testing.T) {
	store := &storeStub{conversation: conversation.Conversation{ID: 9, KnowledgeBaseID: 7, Title: "我的笔记"}}
	service := conversation.NewService(store)
	if err := service.AutoTitle(context.Background(), 9, 7, "另一个问题"); err != nil {
		t.Fatalf("AutoTitle() error = %v", err)
	}
	if store.conversation.Title != "我的笔记" {
		t.Fatalf("AutoTitle() overwrote user title = %q, want untouched", store.conversation.Title)
	}
}
