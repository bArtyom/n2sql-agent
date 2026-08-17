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
func (s *storeStub) SaveSummary(_ context.Context, _, _ int64, summary string) error {
	s.status, s.summary = "succeeded", documentsummary.Summary{Content: summary, Status: "succeeded"}
	return nil
}
func (s *storeStub) SaveSummaryError(context.Context, int64, int64, string) error {
	s.status = "failed"
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
