package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
)

type summaryRequester struct{}

func (summaryRequester) Start(context.Context, int64, int64) (documentsummary.AsyncResult, error) {
	return documentsummary.AsyncResult{Result: documentsummary.Result{Content: "全文摘要"}}, nil
}

func TestDocumentSummaryToolUsesDedicatedService(t *testing.T) {
	tool, err := agent.NewDocumentSummaryToolForKnowledgeBase(summaryRequester{}, 7)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Call(context.Background(), json.RawMessage(`{"document_id":9}`))
	if err != nil || result.Content != "全文摘要" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
