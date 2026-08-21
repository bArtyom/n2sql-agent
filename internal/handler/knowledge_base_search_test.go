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

type searcherStub struct {
	knowledgeBaseID int64
	query           string
	limit           int
}

func (s *searcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.query = query
	s.limit = limit
	return []retrieval.Result{{DocumentID: 11, Position: 0, Content: "Go 后端", Distance: 0.12}}, nil
}

func TestKnowledgeBaseSearchReturnsSimilarChunks(t *testing.T) {
	searcher := &searcherStub{}
	endpoint := handler.NewKnowledgeBaseSearch(searcher)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/search", strings.NewReader(`{"query":"后端怎么运行","limit":5}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != `{"results":[{"documentId":11,"position":0,"content":"Go 后端","distance":0.12}]}`+"\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if searcher.knowledgeBaseID != 7 || searcher.query != "后端怎么运行" || searcher.limit != 5 {
		t.Fatalf("search arguments = %#v", searcher)
	}
}

func TestKnowledgeBaseSearchReturnsExplainableEvidenceWhenDebugEnabled(t *testing.T) {
	searcher := &explainableSearcherStub{}
	endpoint := handler.NewKnowledgeBaseSearch(searcher)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/search", strings.NewReader(`{"query":"后端怎么运行","debug":true}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{`"explain"`, `"reason":"向量+关键词+标题命中"`, `"fusionScore":0.8`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response body = %q, missing %s", body, want)
		}
	}
}

type explainableSearcherStub struct{}

func (explainableSearcherStub) Search(_ context.Context, _ int64, _ string, _ int) ([]retrieval.Result, error) {
	return []retrieval.Result{{
		DocumentID:        11,
		Position:          2,
		Content:           "Go 后端",
		Distance:          0.12,
		MatchType:         "hybrid",
		KeywordScore:      0.7,
		KeywordScoreKnown: true,
		HeadingScore:      0.4,
		FusionScore:       0.8,
	}}, nil
}

func TestKnowledgeBaseSearchRejectsInvalidRequest(t *testing.T) {
	cases := []struct {
		name string
		id   string
		body string
	}{
		{name: "invalid knowledge base", id: "0", body: `{"query":"问题"}`},
		{name: "empty query", id: "7", body: `{"query":"  "}`},
		{name: "negative limit", id: "7", body: `{"query":"问题","limit":-1}`},
		{name: "too many results", id: "7", body: `{"query":"问题","limit":21}`},
		{name: "unknown field", id: "7", body: `{"query":"问题","unexpected":true}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			endpoint := handler.NewKnowledgeBaseSearch(&searcherStub{})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/"+test.id+"/search", strings.NewReader(test.body))
			request.SetPathValue("id", test.id)

			endpoint.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}
