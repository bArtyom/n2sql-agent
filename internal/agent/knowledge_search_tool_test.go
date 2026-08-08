package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type searcherStub struct {
	knowledgeBaseID int64
	query           string
	limit           int
	content         string
	distance        float64
}

type registryProbeTool struct{}

func (registryProbeTool) Name() string { return "document_delete" }

func (registryProbeTool) Description() string { return "删除文档" }

func (registryProbeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (registryProbeTool) Call(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "ok"}, nil
}

func (s *searcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.query = query
	s.limit = limit
	content := s.content
	if content == "" {
		content = "工作满一年可享受五天年假。"
	}
	distance := s.distance
	if distance == 0 {
		distance = 0.12
	}
	return []retrieval.Result{
		{
			DocumentID:       11,
			OriginalFilename: "employee-handbook.md",
			Position:         2,
			Content:          content,
			Distance:         distance,
		},
	}, nil
}

func TestKnowledgeSearchToolMarksEmptyAndDistantResultsAsIrrelevant(t *testing.T) {
	tool := agent.NewKnowledgeSearchTool(&searcherStub{distance: 0.9})

	result, err := tool.Call(context.Background(), json.RawMessage(`{"knowledge_base_id":7,"query":"量子计算"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.NoRelevantResults || result.FallbackAnswer == "" {
		t.Fatalf("result = %#v, want deterministic fallback metadata", result)
	}
	var visible []retrieval.Result
	if err := json.Unmarshal([]byte(result.Content), &visible); err != nil {
		t.Fatalf("result content = %q, error = %v", result.Content, err)
	}
	if len(visible) != 0 {
		t.Fatalf("visible results = %#v, want empty", visible)
	}
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
	var parameters map[string]any
	if err := json.Unmarshal(tool.Parameters(), &parameters); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	if parameters["type"] != "object" {
		t.Fatalf("Parameters() type = %#v, want object", parameters["type"])
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
	metadata, ok := result.Metadata["sources"].([]retrieval.Result)
	if !ok || len(metadata) != 1 || metadata[0].DocumentID != 11 {
		t.Fatalf("tool source metadata = %#v", result.Metadata)
	}
}

func TestKnowledgeSearchToolTruncatesStructuredResults(t *testing.T) {
	tool, err := agent.NewKnowledgeSearchToolWithMaxBytes(&searcherStub{content: strings.Repeat("年假制度 ", 200)}, 180)
	if err != nil {
		t.Fatalf("NewKnowledgeSearchToolWithMaxBytes() error = %v", err)
	}

	result, err := tool.Call(context.Background(), json.RawMessage(`{"knowledge_base_id":7,"query":"年假"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if len([]byte(result.Content)) > 180 {
		t.Fatalf("tool result bytes = %d, want at most 180", len([]byte(result.Content)))
	}
	var visible []retrieval.Result
	if err := json.Unmarshal([]byte(result.Content), &visible); err != nil {
		t.Fatalf("truncated result is invalid JSON: %v", err)
	}
	if len(visible) != 1 || len(visible[0].Content) >= 1000 {
		t.Fatalf("visible results = %#v, want one shortened result", visible)
	}
	truncated, ok := result.Metadata["truncated"].(bool)
	if !ok || !truncated {
		t.Fatalf("truncated metadata = %#v, want true", result.Metadata)
	}
	sources, ok := result.Metadata["sources"].([]retrieval.Result)
	if !ok || len(sources) != len(visible) || sources[0].Content != visible[0].Content {
		t.Fatalf("source metadata = %#v, want same visible results", result.Metadata)
	}
}

func TestNewKnowledgeSearchToolWithMaxBytesRejectsInvalidLimit(t *testing.T) {
	_, err := agent.NewKnowledgeSearchToolWithMaxBytes(&searcherStub{}, 1)
	if !errors.Is(err, agent.ErrInvalidMaxResultBytes) {
		t.Fatalf("error = %v, want %v", err, agent.ErrInvalidMaxResultBytes)
	}
}

func TestNewKnowledgeSearchRegistryRegistersSearchTool(t *testing.T) {
	registry, err := agent.NewKnowledgeSearchRegistry(&searcherStub{})
	if err != nil {
		t.Fatalf("NewKnowledgeSearchRegistry() error = %v", err)
	}

	tool, err := registry.Find("knowledge_search")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if tool.Name() != "knowledge_search" {
		t.Fatalf("registered tool name = %q, want knowledge_search", tool.Name())
	}
}

func TestKnowledgeSearchRegistriesAllowOnlyKnowledgeSearch(t *testing.T) {
	registries := []struct {
		name   string
		create func() (*agent.ToolRegistry, error)
	}{
		{name: "global", create: func() (*agent.ToolRegistry, error) {
			return agent.NewKnowledgeSearchRegistry(&searcherStub{})
		}},
		{name: "scoped", create: func() (*agent.ToolRegistry, error) {
			return agent.NewKnowledgeSearchRegistryForKnowledgeBase(&searcherStub{}, 7)
		}},
	}

	for _, test := range registries {
		t.Run(test.name, func(t *testing.T) {
			registry, err := test.create()
			if err != nil {
				t.Fatalf("create registry error = %v", err)
			}
			if err := registry.Register(registryProbeTool{}); !errors.Is(err, agent.ErrToolNotAllowed) {
				t.Fatalf("Register() unapproved tool error = %v, want %v", err, agent.ErrToolNotAllowed)
			}
		})
	}
}

func TestNewKnowledgeSearchRegistryForKnowledgeBaseScopesTool(t *testing.T) {
	searcher := &searcherStub{}
	registry, err := agent.NewKnowledgeSearchRegistryForKnowledgeBase(searcher, 7)
	if err != nil {
		t.Fatalf("NewKnowledgeSearchRegistryForKnowledgeBase() error = %v", err)
	}
	tool, err := registry.Find("knowledge_search")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	var parameters map[string]any
	if err := json.Unmarshal(tool.Parameters(), &parameters); err != nil {
		t.Fatalf("Parameters() is not valid JSON: %v", err)
	}
	properties := parameters["properties"].(map[string]any)
	if _, hasKnowledgeBaseID := properties["knowledge_base_id"]; hasKnowledgeBaseID {
		t.Fatal("scoped tool must not expose knowledge_base_id to the model")
	}

	if _, err := tool.Call(context.Background(), json.RawMessage(`{"query":"年假"}`)); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if searcher.knowledgeBaseID != 7 {
		t.Fatalf("knowledge base ID = %d, want 7", searcher.knowledgeBaseID)
	}
}

func TestScopedKnowledgeSearchToolRejectsKnowledgeBaseOverride(t *testing.T) {
	tool, err := agent.NewKnowledgeSearchToolForKnowledgeBase(&searcherStub{}, 7)
	if err != nil {
		t.Fatalf("NewKnowledgeSearchToolForKnowledgeBase() error = %v", err)
	}

	_, err = tool.Call(context.Background(), json.RawMessage(`{"knowledge_base_id":999,"query":"年假"}`))
	if !errors.Is(err, agent.ErrInvalidKnowledgeSearchInput) {
		t.Fatalf("Call() error = %v, want %v", err, agent.ErrInvalidKnowledgeSearchInput)
	}
}

func TestNewKnowledgeSearchRegistryForKnowledgeBaseRejectsInvalidScope(t *testing.T) {
	_, err := agent.NewKnowledgeSearchRegistryForKnowledgeBase(&searcherStub{}, 0)
	if !errors.Is(err, agent.ErrInvalidKnowledgeBaseScope) {
		t.Fatalf("error = %v, want %v", err, agent.ErrInvalidKnowledgeBaseScope)
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
