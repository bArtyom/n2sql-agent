package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

var (
	ErrInvalidKnowledgeSearchInput  = errors.New("invalid knowledge search input")
	ErrKnowledgeSearcherUnavailable = errors.New("knowledge searcher unavailable")
)

type KnowledgeSearchInput struct {
	KnowledgeBaseID int64  `json:"knowledge_base_id"`
	Query           string `json:"query"`
	Limit           int    `json:"limit,omitempty"`
}

var knowledgeSearchParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "knowledge_base_id": {
      "type": "integer",
      "minimum": 1,
      "description": "要搜索的知识库 ID"
    },
    "query": {
      "type": "string",
      "minLength": 1,
      "description": "要搜索的自然语言问题"
    },
    "limit": {
      "type": "integer",
      "minimum": 0,
      "maximum": 20,
      "default": 5,
      "description": "最多返回的文档片段数量；传 0 使用默认值 5"
    }
  },
  "required": ["knowledge_base_id", "query"],
  "additionalProperties": false
}`)

// KnowledgeSearchTool adapts the existing retrieval service to the Agent Tool interface.
type KnowledgeSearchTool struct {
	searcher retrieval.Searcher
}

var _ Tool = (*KnowledgeSearchTool)(nil)

func NewKnowledgeSearchTool(searcher retrieval.Searcher) *KnowledgeSearchTool {
	return &KnowledgeSearchTool{searcher: searcher}
}

func (t *KnowledgeSearchTool) Name() string {
	return "knowledge_search"
}

func (t *KnowledgeSearchTool) Description() string {
	return "搜索指定知识库中与问题最相关的文档片段"
}

func (t *KnowledgeSearchTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), knowledgeSearchParameters...)
}

func (t *KnowledgeSearchTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.searcher == nil {
		return ToolResult{}, ErrKnowledgeSearcherUnavailable
	}

	var input KnowledgeSearchInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidKnowledgeSearchInput, err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.KnowledgeBaseID <= 0 || input.Query == "" {
		return ToolResult{}, ErrInvalidKnowledgeSearchInput
	}
	if input.Limit == 0 {
		input.Limit = retrieval.DefaultResults
	}
	if input.Limit < 1 || input.Limit > retrieval.MaxResults {
		return ToolResult{}, ErrInvalidKnowledgeSearchInput
	}

	results, err := t.searcher.Search(ctx, input.KnowledgeBaseID, input.Query, input.Limit)
	if err != nil {
		return ToolResult{}, fmt.Errorf("knowledge search: %w", err)
	}
	content, err := json.Marshal(results)
	if err != nil {
		return ToolResult{}, fmt.Errorf("encode knowledge search results: %w", err)
	}
	return ToolResult{Content: string(content)}, nil
}

func NewKnowledgeSearchRegistry(searcher retrieval.Searcher) (*ToolRegistry, error) {
	if searcher == nil {
		return nil, ErrKnowledgeSearcherUnavailable
	}
	registry := NewToolRegistry()
	if err := registry.Register(NewKnowledgeSearchTool(searcher)); err != nil {
		return nil, fmt.Errorf("register knowledge search tool: %w", err)
	}
	return registry, nil
}
