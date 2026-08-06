package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
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
	return []retrieval.Result{
		{
			DocumentID:       11,
			OriginalFilename: "employee-handbook.md",
			Position:         2,
			Content:          "工作满一年可享受五天年假。",
			Distance:         0.12,
		},
	}, nil
}

func TestKnowledgeSearchToolCallsSearcherAndReturnsJSON(t *testing.T) {
	searcher := &searcherStub{}
	tool := agent.NewKnowledgeSearchTool(searcher)

	if tool.Name() != "knowledge_search" {
		t.Fatalf("Name() = %q, want knowledge_search", tool.Name())
	}
	if tool.Description() == "" {
		t.Fatal("Description() is empty")
	}

	result, err := tool.Call(context.Background(), json.RawMessage(`{
		"knowledge_base_id": 7,
		"query": "  年假怎么计算  ",
		"limit": 0
	}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if searcher.knowledgeBaseID != 7 || searcher.query != "年假怎么计算" || searcher.limit != retrieval.DefaultResults {
		t.Fatalf("search arguments = %#v, want knowledge base 7, trimmed query and default limit", searcher)
	}

	var results []retrieval.Result
	if err := json.Unmarshal([]byte(result.Content), &results); err != nil {
		t.Fatalf("decode tool result = %v", err)
	}
	if len(results) != 1 || results[0].Content != "工作满一年可享受五天年假。" {
		t.Fatalf("tool results = %#v", results)
	}
}

func TestKnowledgeSearchToolRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{name: "malformed JSON", args: `{"knowledge_base_id":`},
		{name: "missing knowledge base", args: `{"query":"问题"}`},
		{name: "empty query", args: `{"knowledge_base_id":7,"query":"  "}`},
		{name: "negative limit", args: `{"knowledge_base_id":7,"query":"问题","limit":-1}`},
		{name: "too many results", args: `{"knowledge_base_id":7,"query":"问题","limit":21}`},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool := agent.NewKnowledgeSearchTool(&searcherStub{})
			_, err := tool.Call(context.Background(), json.RawMessage(test.args))
			if !errors.Is(err, agent.ErrInvalidKnowledgeSearchInput) {
				t.Fatalf("Call() error = %v, want %v", err, agent.ErrInvalidKnowledgeSearchInput)
			}
		})
	}
}

type failingSearcher struct {
	err error
}

func (s failingSearcher) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return nil, s.err
}

func TestKnowledgeSearchToolPropagatesSearcherFailure(t *testing.T) {
	expected := errors.New("embedding service unavailable")
	tool := agent.NewKnowledgeSearchTool(failingSearcher{err: expected})

	_, err := tool.Call(context.Background(), json.RawMessage(`{
		"knowledge_base_id": 7,
		"query": "问题"
	}`))
	if !errors.Is(err, expected) {
		t.Fatalf("Call() error = %v, want %v", err, expected)
	}
}

func TestKnowledgeSearchToolRequiresSearcher(t *testing.T) {
	tool := agent.NewKnowledgeSearchTool(nil)

	_, err := tool.Call(context.Background(), json.RawMessage(`{
		"knowledge_base_id": 7,
		"query": "问题"
	}`))
	if !errors.Is(err, agent.ErrKnowledgeSearcherUnavailable) {
		t.Fatalf("Call() error = %v, want %v", err, agent.ErrKnowledgeSearcherUnavailable)
	}
}
