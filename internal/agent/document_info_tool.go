package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

var ErrInvalidDocumentInfoInput = errors.New("invalid document info input")

var documentInfoParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "document_id": {
      "type": "integer",
      "minimum": 1,
      "description": "当前知识库中的文档 ID"
    }
  },
  "required": ["document_id"],
  "additionalProperties": false
}`)

type documentInfoInput struct {
	DocumentID int64 `json:"document_id"`
}

// DocumentInfoTool returns metadata for one document in the current
// knowledge-base scope. It deliberately does not return document content;
// content questions should go through knowledge_search.
type DocumentInfoTool struct {
	reader          document.Reader
	knowledgeBaseID int64
	maxResultBytes  int
	folderPath      *string
	folderRecursive bool
}

var _ Tool = (*DocumentInfoTool)(nil)

func NewDocumentInfoToolForKnowledgeBase(reader document.Reader, knowledgeBaseID int64, maxResultBytes int) (*DocumentInfoTool, error) {
	if reader == nil {
		return nil, ErrDocumentReaderUnavailable
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBaseScope
	}
	if maxResultBytes < 2 {
		return nil, ErrInvalidMaxResultBytes
	}
	return &DocumentInfoTool{
		reader:          reader,
		knowledgeBaseID: knowledgeBaseID,
		maxResultBytes:  maxResultBytes,
	}, nil
}

func (t *DocumentInfoTool) Name() string { return "document_info" }

func (t *DocumentInfoTool) Description() string {
	return "查询当前知识库中指定文档的文件类型、大小和处理状态"
}

func (t *DocumentInfoTool) SetFolderScope(folderPath *string, recursive bool) {
	if folderPath == nil {
		t.folderPath = nil
		t.folderRecursive = false
		return
	}
	copyOfPath := *folderPath
	t.folderPath = &copyOfPath
	t.folderRecursive = recursive
}

func (t *DocumentInfoTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), documentInfoParameters...)
}

func (t *DocumentInfoTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.reader == nil {
		return ToolResult{}, ErrDocumentReaderUnavailable
	}
	var input documentInfoInput
	if err := decodeToolArguments(raw, &input); err != nil {
		return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidDocumentInfoInput, err)
	}
	if input.DocumentID <= 0 {
		return ToolResult{}, ErrInvalidDocumentInfoInput
	}

	documents, err := t.reader.List(ctx, t.knowledgeBaseID)
	if err != nil {
		return ToolResult{}, fmt.Errorf("list documents for info: %w", err)
	}
	for _, item := range documents {
		if item.ID != input.DocumentID {
			continue
		}
		if t.folderPath != nil && !document.FolderPathInScope(item.FolderPath, *t.folderPath, t.folderRecursive) {
			return ToolResult{}, fmt.Errorf("document %d: %w", input.DocumentID, document.ErrDocumentNotFound)
		}
		info := documentListItem{
			ID:               item.ID,
			OriginalFilename: item.OriginalFilename,
			FolderPath:       item.FolderPath,
			ContentType:      item.ContentType,
			SizeBytes:        item.SizeBytes,
			ProcessingStatus: item.ProcessingStatus,
		}
		payload, err := json.Marshal(struct {
			Document documentListItem `json:"document"`
		}{Document: info})
		if err != nil {
			return ToolResult{}, fmt.Errorf("marshal document info: %w", err)
		}
		if len(payload) > t.maxResultBytes {
			return ToolResult{}, fmt.Errorf("document info result exceeds %d bytes", t.maxResultBytes)
		}
		// Structured metadata lets the UI render a key-value card instead of
		// the raw text summary.
		return ToolResult{Content: string(payload), Metadata: map[string]any{"document_info": info}}, nil
	}
	return ToolResult{}, fmt.Errorf("document %d: %w", input.DocumentID, document.ErrDocumentNotFound)
}
