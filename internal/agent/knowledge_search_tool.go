package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

var (
	ErrInvalidKnowledgeSearchInput  = errors.New("invalid knowledge search input")
	ErrInvalidKnowledgeBaseScope    = errors.New("invalid knowledge base scope")
	ErrKnowledgeSearcherUnavailable = errors.New("knowledge searcher unavailable")
	ErrInvalidMaxResultBytes        = errors.New("knowledge search result byte limit must be at least 2")
	ErrInvalidMaxResults            = errors.New("knowledge search result limit must be between 1 and 20")
	ErrInvalidMaxKnowledgeDistance  = retrieval.ErrInvalidMaxDistance
)

const (
	DefaultMaxToolResultBytes = 32 * 1024
	// DefaultMaxKnowledgeDistance is the largest pgvector cosine distance
	// accepted as evidence for an Agent answer. Larger values are treated as
	// unrelated search hits rather than being passed to the model as facts.
	DefaultMaxKnowledgeDistance = retrieval.DefaultMaxDistance
)

const noRelevantKnowledgeAnswer = "当前知识库中没有找到足够相关的资料，无法根据现有文档可靠回答这个问题。"

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

var scopedKnowledgeSearchParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
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
  "required": ["query"],
  "additionalProperties": false
}`)

// KnowledgeSearchTool adapts the existing retrieval service to the Agent Tool interface.
type KnowledgeSearchTool struct {
	searcher         retrieval.Searcher
	knowledgeBaseID  int64
	maxResultBytes   int
	maxResults       int
	maxDistance      float64
	keywordThreshold float64
	documentIDs      []int64
	queryRewrite     bool
}

var _ Tool = (*KnowledgeSearchTool)(nil)

func NewKnowledgeSearchTool(searcher retrieval.Searcher) *KnowledgeSearchTool {
	return &KnowledgeSearchTool{searcher: searcher, maxResultBytes: DefaultMaxToolResultBytes, maxResults: retrieval.MaxResults, maxDistance: DefaultMaxKnowledgeDistance}
}

func NewKnowledgeSearchToolForKnowledgeBase(searcher retrieval.Searcher, knowledgeBaseID int64) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(searcher, knowledgeBaseID, DefaultMaxToolResultBytes)
}

func NewKnowledgeSearchToolWithMaxBytes(searcher retrieval.Searcher, maxResultBytes int) (*KnowledgeSearchTool, error) {
	if searcher == nil {
		return nil, ErrKnowledgeSearcherUnavailable
	}
	if maxResultBytes < 2 {
		return nil, ErrInvalidMaxResultBytes
	}
	return &KnowledgeSearchTool{searcher: searcher, maxResultBytes: maxResultBytes, maxResults: retrieval.MaxResults, maxDistance: DefaultMaxKnowledgeDistance}, nil
}

func NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes int) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithLimits(searcher, knowledgeBaseID, maxResultBytes, retrieval.MaxResults)
}

func NewKnowledgeSearchToolForKnowledgeBaseWithLimits(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistance(searcher, knowledgeBaseID, maxResultBytes, maxResults, DefaultMaxKnowledgeDistance)
}

func NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistance(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocuments(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, nil)
}

// NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocuments creates
// a read-only search tool scoped to one knowledge base and, optionally, a set
// of documents inside that knowledge base.
func NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocuments(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64, documentIDs []int64) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewrite(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, documentIDs, false)
}

func NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewrite(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64, documentIDs []int64, queryRewrite bool) (*KnowledgeSearchTool, error) {
	return NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, retrieval.DefaultKeywordThreshold, documentIDs, queryRewrite)
}

func NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance, keywordThreshold float64, documentIDs []int64, queryRewrite bool) (*KnowledgeSearchTool, error) {
	return newKnowledgeSearchToolForKnowledgeBase(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, keywordThreshold, documentIDs, queryRewrite)
}

func newKnowledgeSearchToolForKnowledgeBase(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance, keywordThreshold float64, documentIDs []int64, queryRewrite bool) (*KnowledgeSearchTool, error) {
	if searcher == nil {
		return nil, ErrKnowledgeSearcherUnavailable
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBaseScope
	}
	if maxResultBytes < 2 {
		return nil, ErrInvalidMaxResultBytes
	}
	if maxResults < 1 || maxResults > retrieval.MaxResults {
		return nil, ErrInvalidMaxResults
	}
	if err := ValidateMaxKnowledgeDistance(maxDistance); err != nil {
		return nil, err
	}
	if err := retrieval.ValidateKeywordThreshold(keywordThreshold); err != nil {
		return nil, err
	}
	normalizedDocumentIDs, err := retrieval.NormalizeDocumentIDs(documentIDs)
	if err != nil {
		return nil, err
	}
	return &KnowledgeSearchTool{
		searcher:         searcher,
		knowledgeBaseID:  knowledgeBaseID,
		maxResultBytes:   maxResultBytes,
		maxResults:       maxResults,
		maxDistance:      maxDistance,
		keywordThreshold: keywordThreshold,
		documentIDs:      normalizedDocumentIDs,
		queryRewrite:     queryRewrite,
	}, nil
}

func ValidateMaxKnowledgeDistance(maxDistance float64) error {
	if maxDistance == 0 {
		return nil
	}
	return retrieval.ValidateMaxDistance(maxDistance)
}

func (t *KnowledgeSearchTool) Name() string {
	return "knowledge_search"
}

func (t *KnowledgeSearchTool) Description() string {
	return "搜索指定知识库中与问题最相关的文档片段"
}

func (t *KnowledgeSearchTool) Parameters() json.RawMessage {
	if t != nil && t.knowledgeBaseID > 0 {
		return append(json.RawMessage(nil), scopedKnowledgeSearchParameters...)
	}
	return append(json.RawMessage(nil), knowledgeSearchParameters...)
}

func (t *KnowledgeSearchTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.searcher == nil {
		return ToolResult{}, ErrKnowledgeSearcherUnavailable
	}

	var input KnowledgeSearchInput
	if t.knowledgeBaseID > 0 {
		var scopedInput struct {
			Query string `json:"query"`
			Limit int    `json:"limit,omitempty"`
		}
		if err := decodeKnowledgeSearchArguments(raw, &scopedInput); err != nil {
			return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidKnowledgeSearchInput, err)
		}
		input.KnowledgeBaseID = t.knowledgeBaseID
		input.Query = scopedInput.Query
		input.Limit = scopedInput.Limit
	} else if err := decodeKnowledgeSearchArguments(raw, &input); err != nil {
		return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidKnowledgeSearchInput, err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.KnowledgeBaseID <= 0 || input.Query == "" {
		return ToolResult{}, ErrInvalidKnowledgeSearchInput
	}
	maxResults := t.maxResults
	if maxResults == 0 {
		maxResults = retrieval.MaxResults
	}
	if input.Limit == 0 {
		input.Limit = retrieval.DefaultResults
	}
	if input.Limit > maxResults {
		if t.maxResults < retrieval.MaxResults {
			input.Limit = maxResults
		} else {
			return ToolResult{}, ErrInvalidKnowledgeSearchInput
		}
	}
	if input.Limit < 1 || input.Limit > retrieval.MaxResults {
		return ToolResult{}, ErrInvalidKnowledgeSearchInput
	}

	var (
		results []retrieval.Result
		err     error
	)
	if len(t.documentIDs) == 0 && !t.queryRewrite && t.keywordThreshold <= 0 {
		results, err = t.searcher.Search(ctx, input.KnowledgeBaseID, input.Query, input.Limit)
	} else if filtered, ok := t.searcher.(retrieval.FilteredSearcher); ok {
		results, err = filtered.SearchWithOptions(ctx, input.KnowledgeBaseID, input.Query, input.Limit, retrieval.SearchOptions{DocumentIDs: t.documentIDs, QueryRewrite: t.queryRewrite, KeywordThreshold: t.keywordThreshold})
	} else if len(t.documentIDs) > 0 || t.queryRewrite {
		return ToolResult{}, retrieval.ErrDocumentFilterUnavailable
	} else {
		results, err = t.searcher.Search(ctx, input.KnowledgeBaseID, input.Query, input.Limit)
	}
	if err != nil {
		return ToolResult{}, fmt.Errorf("knowledge search: %w", err)
	}
	relevantResults, err := retrieval.FilterByMaxDistanceWithStats(ctx, results, t.maxDistance)
	if err != nil {
		return ToolResult{}, fmt.Errorf("filter knowledge search results: %w", err)
	}
	content, visibleResults, truncated, err := limitKnowledgeSearchResults(relevantResults, t.maxResultBytes)
	if err != nil {
		return ToolResult{}, fmt.Errorf("encode knowledge search results: %w", err)
	}
	metadata := map[string]any{"sources": visibleResults}
	if truncated {
		metadata["truncated"] = true
	}
	if len(relevantResults) == 0 {
		metadata["no_relevant_results"] = true
		return ToolResult{
			Content:           string(content),
			Metadata:          metadata,
			NoRelevantResults: true,
			FallbackAnswer:    noRelevantKnowledgeAnswer,
		}, nil
	}
	return ToolResult{
		Content:  string(content),
		Metadata: metadata,
	}, nil
}

func limitKnowledgeSearchResults(results []retrieval.Result, maxBytes int) ([]byte, []retrieval.Result, bool, error) {
	if maxBytes < 2 {
		return nil, nil, false, ErrInvalidMaxResultBytes
	}
	visible := make([]retrieval.Result, 0, len(results))
	for _, result := range results {
		candidate := appendSearchResult(visible, retrieval.ResultForPrompt(result))
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return nil, nil, false, err
		}
		if len(encoded) <= maxBytes {
			visible = candidate
			continue
		}

		shortened, fits, err := shortenSearchResult(visible, retrieval.ResultForPrompt(result), maxBytes)
		if err != nil {
			return nil, nil, false, err
		}
		if fits {
			visible = append(visible, shortened)
		}
		encoded, err = json.Marshal(visible)
		if err != nil {
			return nil, nil, false, err
		}
		return encoded, visible, true, nil
	}

	encoded, err := json.Marshal(visible)
	if err != nil {
		return nil, nil, false, err
	}
	return encoded, visible, false, nil
}

func shortenSearchResult(prefix []retrieval.Result, result retrieval.Result, maxBytes int) (retrieval.Result, bool, error) {
	runes := []rune(result.Content)
	low, high := 0, len(runes)
	best := -1
	var shortened retrieval.Result
	for low <= high {
		middle := low + (high-low)/2
		candidate := result
		candidate.Content = string(runes[:middle])
		encoded, err := json.Marshal(appendSearchResult(prefix, candidate))
		if err != nil {
			return retrieval.Result{}, false, err
		}
		if len(encoded) <= maxBytes {
			best = middle
			shortened = candidate
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return shortened, best >= 0, nil
}

func appendSearchResult(results []retrieval.Result, result retrieval.Result) []retrieval.Result {
	copyOfResults := make([]retrieval.Result, len(results), len(results)+1)
	copy(copyOfResults, results)
	return append(copyOfResults, result)
}

func decodeKnowledgeSearchArguments(raw json.RawMessage, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func NewKnowledgeSearchRegistry(searcher retrieval.Searcher) (*ToolRegistry, error) {
	if searcher == nil {
		return nil, ErrKnowledgeSearcherUnavailable
	}
	registry, err := NewToolRegistryWithAllowlist("knowledge_search")
	if err != nil {
		return nil, fmt.Errorf("create knowledge search allowlist: %w", err)
	}
	if err := registry.Register(NewKnowledgeSearchTool(searcher)); err != nil {
		return nil, fmt.Errorf("register knowledge search tool: %w", err)
	}
	return registry, nil
}

func NewKnowledgeSearchRegistryForKnowledgeBase(searcher retrieval.Searcher, knowledgeBaseID int64) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithMaxBytes(searcher, knowledgeBaseID, DefaultMaxToolResultBytes)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithMaxBytes(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes int) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithLimits(searcher, knowledgeBaseID, maxResultBytes, retrieval.MaxResults)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithLimits(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistance(searcher, knowledgeBaseID, maxResultBytes, maxResults, DefaultMaxKnowledgeDistance)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistance(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocuments(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, nil)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocuments(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64, documentIDs []int64) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewrite(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, documentIDs, false)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewrite(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance float64, documentIDs []int64, queryRewrite bool) (*ToolRegistry, error) {
	return NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, retrieval.DefaultKeywordThreshold, documentIDs, queryRewrite)
}

func NewKnowledgeSearchRegistryForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(searcher retrieval.Searcher, knowledgeBaseID int64, maxResultBytes, maxResults int, maxDistance, keywordThreshold float64, documentIDs []int64, queryRewrite bool) (*ToolRegistry, error) {
	tool, err := NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(searcher, knowledgeBaseID, maxResultBytes, maxResults, maxDistance, keywordThreshold, documentIDs, queryRewrite)
	if err != nil {
		return nil, err
	}
	registry, err := NewToolRegistryWithAllowlist("knowledge_search")
	if err != nil {
		return nil, fmt.Errorf("create scoped knowledge search allowlist: %w", err)
	}
	if err := registry.Register(tool); err != nil {
		return nil, fmt.Errorf("register scoped knowledge search tool: %w", err)
	}
	return registry, nil
}
