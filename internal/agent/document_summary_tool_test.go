package agent_test

import (
	"context"
	"encoding/json"
	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"testing"
)

type summarySource struct{}

func (summarySource) ReadSummaryContent(context.Context, int64, int64) (documentsummary.Document, error) {
	return documentsummary.Document{Chunks: []string{"正文"}}, nil
}

type summaryStore struct{ summary documentsummary.Summary }

func (s *summaryStore) GetSummary(context.Context, int64, int64) (documentsummary.Summary, error) {
	return s.summary, nil
}
func (*summaryStore) MarkSummaryProcessing(context.Context, int64, int64) error { return nil }
func (s *summaryStore) SaveSummary(_ context.Context, _, _ int64, content string) error {
	s.summary = documentsummary.Summary{Status: "succeeded", Content: content}
	return nil
}
func (*summaryStore) SaveSummaryError(context.Context, int64, int64, string) error { return nil }

type summaryChat struct{}

func (summaryChat) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{Message: "全文摘要"}, nil
}
func TestDocumentSummaryToolUsesDedicatedService(t *testing.T) {
	service := documentsummary.NewService(summarySource{}, &summaryStore{}, summaryChat{}, 1000)
	tool, err := agent.NewDocumentSummaryToolForKnowledgeBase(service, 7)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"document_id":9}`))
	if err != nil || result.Content != "全文摘要" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
