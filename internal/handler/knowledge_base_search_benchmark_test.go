package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type benchmarkSearcher struct{}

func (benchmarkSearcher) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: 11, Position: 0, Content: "固定 benchmark 文档片段", Distance: 0.12}}, nil
}

// BenchmarkKnowledgeBaseSearchHandler measures the HTTP/JSON boundary only.
// The search dependency is a fixed stub, so this benchmark does not call a
// database, embedding provider, chat model, or spend API tokens.
func BenchmarkKnowledgeBaseSearchHandler(b *testing.B) {
	endpoint := handler.NewKnowledgeBaseSearch(benchmarkSearcher{})
	body := `{"query":"如何启动服务？","limit":5}`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/search", strings.NewReader(body))
		request.SetPathValue("id", "7")
		endpoint.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
		}
	}
}
