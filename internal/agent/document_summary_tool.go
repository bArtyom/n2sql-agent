package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
)

var ErrInvalidDocumentSummaryInput = errors.New("invalid document summary input")

var documentSummaryParameters = json.RawMessage(`{
  "type": "object",
  "properties": {"document_id": {"type": "integer", "minimum": 1, "description": "要总结的当前知识库文档 ID"}},
  "required": ["document_id"],
  "additionalProperties": false
}`)

type documentSummaryInput struct {
	DocumentID int64 `json:"document_id"`
}

type DocumentSummaryTool struct {
	service         *documentsummary.Service
	knowledgeBaseID int64
}

var _ Tool = (*DocumentSummaryTool)(nil)

func NewDocumentSummaryToolForKnowledgeBase(service *documentsummary.Service, knowledgeBaseID int64) (*DocumentSummaryTool, error) {
	if service == nil {
		return nil, ErrDocumentReaderUnavailable
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBaseScope
	}
	return &DocumentSummaryTool{service: service, knowledgeBaseID: knowledgeBaseID}, nil
}

func (t *DocumentSummaryTool) Name() string { return "document_summary" }
func (t *DocumentSummaryTool) Description() string {
	return "当用户要求总结某个完整文档时调用；不要改用 document_read 逐段读取。"
}
func (t *DocumentSummaryTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), documentSummaryParameters...)
}
func (t *DocumentSummaryTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.service == nil {
		return ToolResult{}, ErrDocumentReaderUnavailable
	}
	var input documentSummaryInput
	if err := decodeToolArguments(raw, &input); err != nil || input.DocumentID <= 0 {
		return ToolResult{}, fmt.Errorf("%w: decode document_id", ErrInvalidDocumentSummaryInput)
	}
	result, err := t.service.Summarize(ctx, t.knowledgeBaseID, input.DocumentID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("summarize document %d: %w", input.DocumentID, err)
	}
	return ToolResult{Content: result.Content, Metadata: map[string]any{"document_summary": true, "cached": result.Cached}}, nil
}
