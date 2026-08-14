package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

var (
	ErrDocumentReaderUnavailable = errors.New("document reader unavailable")
	ErrInvalidDocumentListInput  = errors.New("invalid document list input")
)

const documentListToolName = "document_list"

var documentListParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "limit": {
      "type": "integer",
      "minimum": 0,
      "maximum": 20,
      "default": 20,
      "description": "最多返回的文档数量；传 0 使用默认值"
    }
  },
  "additionalProperties": false
}`)

type documentListInput struct {
	Limit int `json:"limit,omitempty"`
}

type documentListItem struct {
	ID               int64  `json:"id"`
	OriginalFilename string `json:"original_filename"`
	ContentType      string `json:"content_type"`
	SizeBytes        int64  `json:"size_bytes"`
	ProcessingStatus string `json:"processing_status"`
}

type documentListResponse struct {
	Documents []documentListItem `json:"documents"`
	Total     int                `json:"total"`
	Returned  int                `json:"returned"`
	Truncated bool               `json:"truncated"`
}

// DocumentListTool lists document metadata in one knowledge base. It never
// accepts a knowledge base ID from the model; the service binds the scope when
// it creates the tool for a request.
type DocumentListTool struct {
	reader          document.Reader
	knowledgeBaseID int64
	maxResultBytes  int
	maxResults      int
}

var _ Tool = (*DocumentListTool)(nil)

func NewDocumentListToolForKnowledgeBase(reader document.Reader, knowledgeBaseID int64, maxResultBytes, maxResults int) (*DocumentListTool, error) {
	if reader == nil {
		return nil, ErrDocumentReaderUnavailable
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
	return &DocumentListTool{
		reader:          reader,
		knowledgeBaseID: knowledgeBaseID,
		maxResultBytes:  maxResultBytes,
		maxResults:      maxResults,
	}, nil
}

func (t *DocumentListTool) Name() string { return documentListToolName }

func (t *DocumentListTool) Description() string {
	return "列出当前知识库中的文档名称、类型、大小和处理状态"
}

func (t *DocumentListTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), documentListParameters...)
}

func (t *DocumentListTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.reader == nil {
		return ToolResult{}, ErrDocumentReaderUnavailable
	}
	var input documentListInput
	if err := decodeToolArguments(raw, &input); err != nil {
		return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidDocumentListInput, err)
	}
	limit := input.Limit
	if limit == 0 {
		limit = t.maxResults
	}
	if limit < 1 || limit > t.maxResults {
		return ToolResult{}, ErrInvalidDocumentListInput
	}

	documents, err := t.reader.List(ctx, t.knowledgeBaseID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("list documents: %w", err)
	}
	content, err := marshalDocumentList(documents, limit, t.maxResultBytes)
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Content: content}, nil
}

func marshalDocumentList(documents []document.Document, limit, maxBytes int) (string, error) {
	selected := make([]documentListItem, 0, minInt(len(documents), limit))
	for _, item := range documents {
		if len(selected) >= limit {
			break
		}
		candidate := append(selected, documentListItem{
			ID:               item.ID,
			OriginalFilename: item.OriginalFilename,
			ContentType:      item.ContentType,
			SizeBytes:        item.SizeBytes,
			ProcessingStatus: item.ProcessingStatus,
		})
		payload, err := json.Marshal(documentListResponse{
			Documents: candidate,
			Total:     len(documents),
			Returned:  len(candidate),
			Truncated: len(candidate) < len(documents),
		})
		if err != nil {
			return "", fmt.Errorf("marshal document list: %w", err)
		}
		if len(payload) > maxBytes {
			break
		}
		selected = candidate
	}

	payload, err := json.Marshal(documentListResponse{
		Documents: selected,
		Total:     len(documents),
		Returned:  len(selected),
		Truncated: len(selected) < len(documents),
	})
	if err != nil {
		return "", fmt.Errorf("marshal document list: %w", err)
	}
	if len(payload) > maxBytes {
		return "", fmt.Errorf("document list result exceeds %d bytes", maxBytes)
	}
	return string(payload), nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// NewKnowledgeSearchAndDocumentListRegistry creates the safe, read-only
// document tools used by the standard Agent for one knowledge base.
func NewKnowledgeSearchAndDocumentListRegistry(
	searcher retrieval.Searcher,
	reader document.Reader,
	knowledgeBaseID int64,
	maxResultBytes, maxResults int,
	maxDistance, keywordThreshold float64,
	documentIDs []int64,
	queryRewrite bool,
) (*ToolRegistry, error) {
	searchTool, err := NewKnowledgeSearchToolForKnowledgeBaseWithLimitsAndDistanceAndDocumentsAndQueryRewriteAndKeywordThreshold(
		searcher,
		knowledgeBaseID,
		maxResultBytes,
		maxResults,
		maxDistance,
		keywordThreshold,
		documentIDs,
		queryRewrite,
	)
	if err != nil {
		return nil, fmt.Errorf("create knowledge search tool: %w", err)
	}
	listTool, err := NewDocumentListToolForKnowledgeBase(reader, knowledgeBaseID, maxResultBytes, retrieval.MaxResults)
	if err != nil {
		return nil, fmt.Errorf("create document list tool: %w", err)
	}
	infoTool, err := NewDocumentInfoToolForKnowledgeBase(reader, knowledgeBaseID, maxResultBytes)
	if err != nil {
		return nil, fmt.Errorf("create document info tool: %w", err)
	}
	registry, err := NewToolRegistryWithAllowlist("document_info", "document_list", "knowledge_search")
	if err != nil {
		return nil, fmt.Errorf("create read-only tool allowlist: %w", err)
	}
	if err := registry.Register(searchTool); err != nil {
		return nil, fmt.Errorf("register knowledge search tool: %w", err)
	}
	if err := registry.Register(listTool); err != nil {
		return nil, fmt.Errorf("register document list tool: %w", err)
	}
	if err := registry.Register(infoTool); err != nil {
		return nil, fmt.Errorf("register document info tool: %w", err)
	}
	return registry, nil
}
