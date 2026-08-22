package documentsummary_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type sourceStub struct {
	document documentsummary.Document
	err      error
}

func (s sourceStub) ReadSummaryContent(context.Context, int64, int64) (documentsummary.Document, error) {
	return s.document, s.err
}

type storeStub struct {
	summary documentsummary.Summary
	status  string
	err     error
}

func (s *storeStub) GetSummary(context.Context, int64, int64) (documentsummary.Summary, error) {
	return s.summary, s.err
}
func (s *storeStub) MarkSummaryProcessing(context.Context, int64, int64) error {
	s.status = "processing"
	return nil
}
func (s *storeStub) MarkSummaryIndexProcessing(context.Context, int64, int64) (bool, error) {
	if s.summary.IndexStatus == "processing" || s.summary.IndexStatus == "succeeded" {
		return false, nil
	}
	s.summary.IndexStatus = "processing"
	return true, nil
}
func (s *storeStub) SaveSummary(_ context.Context, _, _ int64, summary string) error {
	s.status, s.summary = "succeeded", documentsummary.Summary{Content: summary, Status: "succeeded", IndexStatus: "none"}
	return nil
}
func (s *storeStub) SaveSummaryError(context.Context, int64, int64, string) error {
	s.status = "failed"
	return nil
}
func (s *storeStub) SaveSummaryIndexSuccess(context.Context, int64, int64) error {
	s.summary.IndexStatus = "succeeded"
	return nil
}
func (s *storeStub) SaveSummaryIndexError(_ context.Context, _, _ int64, message string) error {
	s.summary.IndexStatus, s.summary.IndexError = "failed", message
	return nil
}

type chatStub struct{ calls int }

func (s *chatStub) ChatMessages(_ context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	s.calls++
	if len(messages) != 2 || (!strings.Contains(messages[1].Content, "正文") && !strings.Contains(messages[1].Content, "局部摘要")) {
		return modelclient.ChatResponse{}, errors.New("unexpected summary prompt")
	}
	return modelclient.ChatResponse{Message: "这是文档摘要。"}, nil
}

func TestServiceGeneratesAndCachesDocumentSummary(t *testing.T) {
	store := &storeStub{}
	chat := &chatStub{}
	service := documentsummary.NewService(sourceStub{document: documentsummary.Document{Filename: "guide.md", Chunks: []string{"正文内容"}}}, store, chat, 1000)

	result, err := service.Summarize(context.Background(), 7, 9)
	if err != nil || result.Content != "这是文档摘要。" || result.Cached {
		t.Fatalf("first summary = %#v, error = %v", result, err)
	}
	if store.status != "succeeded" || chat.calls != 1 {
		t.Fatalf("status=%q calls=%d", store.status, chat.calls)
	}

	store.summary = documentsummary.Summary{Content: "缓存摘要", Status: "succeeded"}
	result, err = service.Summarize(context.Background(), 7, 9)
	if err != nil || result.Content != "缓存摘要" || !result.Cached || chat.calls != 1 {
		t.Fatalf("cached summary = %#v, error = %v, calls=%d", result, err, chat.calls)
	}
}

func TestServiceIndexesGeneratedSummaryAfterSavingIt(t *testing.T) {
	store := &storeStub{}
	service := documentsummary.NewService(sourceStub{document: documentsummary.Document{Chunks: []string{"正文内容"}}}, store, &chatStub{}, 1000)
	var indexedKB, indexedDocument int64
	var indexedContent string
	service.SetSummaryIndexer(func(_ context.Context, knowledgeBaseID, documentID int64, content string) error {
		indexedKB, indexedDocument, indexedContent = knowledgeBaseID, documentID, content
		return nil
	})

	if _, err := service.Summarize(context.Background(), 7, 9); err != nil {
		t.Fatalf("summarize error = %v", err)
	}
	if indexedKB != 7 || indexedDocument != 9 || indexedContent != "这是文档摘要。" {
		t.Fatalf("index callback = (%d, %d, %q)", indexedKB, indexedDocument, indexedContent)
	}
}

func TestServiceUsesBoundedMapReduceForLongDocuments(t *testing.T) {
	store := &storeStub{}
	chat := &chatStub{}
	service := documentsummary.NewService(sourceStub{document: documentsummary.Document{Chunks: []string{"第一段正文", "第二段正文", "第三段正文"}}}, store, chat, 12)

	result, err := service.Summarize(context.Background(), 7, 9)
	if err != nil || result.Content == "" || chat.calls < 2 {
		t.Fatalf("long summary = %#v, error = %v, calls=%d", result, err, chat.calls)
	}
}

func TestAsyncServiceQueuesSummaryWithoutWaitingForModel(t *testing.T) {
	store := &storeStub{}
	service := documentsummary.NewService(sourceStub{document: documentsummary.Document{Chunks: []string{"正文"}}}, store, &chatStub{}, 1000)
	async := documentsummary.NewAsyncService(service, 1)
	workerContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	async.Run(workerContext)

	result, err := async.Start(context.Background(), 7, 9)
	if err != nil || !result.Pending || result.TaskID == "" {
		t.Fatalf("queued result = %#v, error = %v", result, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && store.status != "succeeded" {
		time.Sleep(time.Millisecond)
	}
	if store.status != "succeeded" {
		t.Fatalf("summary status = %q, want succeeded", store.status)
	}
}

func TestAsyncServiceBackfillSkipsUnreadyAndCompletedDocuments(t *testing.T) {
	store := &storeStub{}
	service := documentsummary.NewService(sourceStub{document: documentsummary.Document{Chunks: []string{"正文"}}}, store, &chatStub{}, 1000)
	async := documentsummary.NewAsyncService(service, 1)
	workerContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	async.Run(workerContext)

	scheduled := async.Backfill(context.Background(), []documentsummary.BackfillCandidate{
		{KnowledgeBaseID: 7, DocumentID: 1, ProcessingStatus: "processing", SummaryStatus: "none"},
		{KnowledgeBaseID: 7, DocumentID: 2, ProcessingStatus: "succeeded", SummaryStatus: "processing"},
		{KnowledgeBaseID: 7, DocumentID: 3, ProcessingStatus: "succeeded", SummaryStatus: "succeeded", SummaryIndexStatus: "succeeded"},
		{KnowledgeBaseID: 7, DocumentID: 4, ProcessingStatus: "succeeded", SummaryStatus: "none"},
	})
	if scheduled != 1 {
		t.Fatalf("scheduled = %d, want one pending summary", scheduled)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && store.status != "succeeded" {
		time.Sleep(time.Millisecond)
	}
	if store.status != "succeeded" {
		t.Fatalf("backfilled summary status = %q, want succeeded", store.status)
	}
}

func TestAsyncServiceReindexesCachedSummaryWithoutCallingChat(t *testing.T) {
	store := &storeStub{summary: documentsummary.Summary{Content: "缓存摘要", Status: "succeeded", IndexStatus: "failed"}}
	chat := &chatStub{}
	service := documentsummary.NewService(sourceStub{}, store, chat, 1000)
	indexed := 0
	service.SetSummaryIndexer(func(_ context.Context, _, _ int64, content string) error {
		indexed++
		if content != "缓存摘要" {
			t.Fatalf("indexed content = %q", content)
		}
		return nil
	})
	async := documentsummary.NewAsyncService(service, 1)
	workerContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	async.Run(workerContext)

	result, err := async.Start(context.Background(), 7, 9)
	if err != nil || result.Content != "缓存摘要" || !result.Cached {
		t.Fatalf("cached result = %#v, error = %v", result, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && store.summary.IndexStatus != "succeeded" {
		time.Sleep(time.Millisecond)
	}
	if indexed != 1 || chat.calls != 0 || store.summary.IndexStatus != "succeeded" {
		t.Fatalf("indexed=%d chat_calls=%d index_status=%q", indexed, chat.calls, store.summary.IndexStatus)
	}
}
