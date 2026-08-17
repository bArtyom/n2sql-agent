package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

var ErrInvalidDocumentReadInput = errors.New("invalid document read input")

const documentReadToolName = "document_read"

var documentReadParameters = json.RawMessage(`{
  "type": "object",
  "properties": {
    "document_id": {
      "type": "integer",
      "minimum": 1,
      "description": "当前知识库中的文档 ID"
    },
    "start_position": {
      "type": "integer",
      "minimum": 0,
      "default": 0,
      "description": "从第几个文档片段开始读取"
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 8,
      "default": 4,
      "description": "最多读取的片段数量"
    }
  },
  "required": ["document_id"],
  "additionalProperties": false
}`)

type documentReadInput struct {
	DocumentID    int64 `json:"document_id"`
	StartPosition int   `json:"start_position,omitempty"`
	Limit         int   `json:"limit,omitempty"`
}

type documentReadChunk struct {
	Position int    `json:"position"`
	Content  string `json:"content"`
}

type documentReadResponse struct {
	DocumentID    int64               `json:"document_id"`
	Filename      string              `json:"filename,omitempty"`
	StartPosition int                 `json:"start_position"`
	Chunks        []documentReadChunk `json:"chunks"`
	NextPosition  int                 `json:"next_position"`
	Truncated     bool                `json:"truncated"`
}

// DocumentReadTool reads a bounded window of already-processed chunks. It
// never accepts a filesystem path and its knowledge-base scope is fixed when
// the Agent request creates the tool.
type DocumentReadTool struct {
	reader          documentchunk.Reader
	knowledgeBaseID int64
	maxResultBytes  int
	maxChunks       int
}

var _ Tool = (*DocumentReadTool)(nil)

func NewDocumentReadToolForKnowledgeBase(reader documentchunk.Reader, knowledgeBaseID int64, maxResultBytes, maxChunks int) (*DocumentReadTool, error) {
	if reader == nil {
		return nil, ErrDocumentReaderUnavailable
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBaseScope
	}
	if maxResultBytes < 2 {
		return nil, ErrInvalidMaxResultBytes
	}
	if maxChunks < 1 || maxChunks > 8 {
		return nil, ErrInvalidDocumentReadInput
	}
	return &DocumentReadTool{reader: reader, knowledgeBaseID: knowledgeBaseID, maxResultBytes: maxResultBytes, maxChunks: maxChunks}, nil
}

func (t *DocumentReadTool) Name() string { return documentReadToolName }

func (t *DocumentReadTool) Description() string {
	return "读取当前知识库中指定文档的有限正文片段；需要更多内容时再分段读取"
}

func (t *DocumentReadTool) Parameters() json.RawMessage {
	return append(json.RawMessage(nil), documentReadParameters...)
}

func (t *DocumentReadTool) Call(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t == nil || t.reader == nil {
		return ToolResult{}, ErrDocumentReaderUnavailable
	}
	var input documentReadInput
	if err := decodeToolArguments(raw, &input); err != nil {
		return ToolResult{}, fmt.Errorf("%w: decode arguments: %v", ErrInvalidDocumentReadInput, err)
	}
	if input.DocumentID <= 0 || input.StartPosition < 0 {
		return ToolResult{}, ErrInvalidDocumentReadInput
	}
	if input.Limit == 0 {
		input.Limit = t.maxChunks
	}
	if input.Limit < 1 {
		return ToolResult{}, ErrInvalidDocumentReadInput
	}
	// The schema advertises the global maximum of eight chunks, while the
	// runtime may apply a smaller per-request bound (for example top_k=5).
	// Clamp an oversized model request to that safe bound instead of failing
	// the entire Agent run.
	if input.Limit > t.maxChunks {
		input.Limit = t.maxChunks
	}

	result, err := readDocumentChunks(ctx, t.reader, t.knowledgeBaseID, input.DocumentID, input.StartPosition, input.Limit, t.maxResultBytes)
	if err != nil {
		return ToolResult{}, fmt.Errorf("read document content: %w", err)
	}
	content, err := json.Marshal(documentReadResponse{
		DocumentID:    input.DocumentID,
		Filename:      result.OriginalFilename,
		StartPosition: input.StartPosition,
		Chunks:        result.Chunks,
		NextPosition:  result.NextPosition,
		Truncated:     result.Truncated,
	})
	if err != nil {
		return ToolResult{}, fmt.Errorf("marshal document content: %w", err)
	}
	if len(content) > t.maxResultBytes {
		return ToolResult{}, fmt.Errorf("document content result exceeds %d bytes", t.maxResultBytes)
	}
	metadata := map[string]any{"sources": documentReadSources(result)}
	if result.Truncated {
		metadata["truncated"] = true
	}
	return ToolResult{Content: string(content), Metadata: metadata}, nil
}

type readDocumentResult struct {
	DocumentID       int64
	Chunks           []documentReadChunk
	NextPosition     int
	Truncated        bool
	OriginalFilename string
}

// documentReadSources converts the ordered chunks into the same bounded source
// shape used by knowledge_search. A zero distance is intentional here: these
// are explicitly requested positions, not similarity-ranked results.
func documentReadSources(result readDocumentResult) []retrieval.Result {
	sources := make([]retrieval.Result, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		sources = append(sources, retrieval.Result{
			DocumentID:       result.DocumentID,
			OriginalFilename: result.OriginalFilename,
			Position:         chunk.Position,
			Content:          chunk.Content,
			MatchType:        documentReadToolName,
		})
	}
	return sources
}

func readDocumentChunks(ctx context.Context, reader documentchunk.Reader, knowledgeBaseID, documentID int64, startPosition, limit, maxBytes int) (readDocumentResult, error) {
	if rangeReader, ok := reader.(documentchunk.RangeReader); ok {
		result, err := rangeReader.ReadRange(ctx, knowledgeBaseID, documentID, startPosition, limit, maxBytes)
		if err != nil {
			return readDocumentResult{}, err
		}
		converted := readDocumentResult{DocumentID: documentID, NextPosition: result.NextPosition, Truncated: result.Truncated}
		for _, chunk := range result.Chunks {
			if converted.OriginalFilename == "" {
				converted.OriginalFilename = chunk.OriginalFilename
			}
			converted.Chunks = append(converted.Chunks, documentReadChunk{Position: chunk.Position, Content: chunk.Content})
		}
		return converted, nil
	}

	result := readDocumentResult{DocumentID: documentID, Chunks: make([]documentReadChunk, 0, limit), NextPosition: startPosition}
	bytesUsed := 0
	for len(result.Chunks) < limit && bytesUsed < maxBytes {
		chunk, err := reader.Read(ctx, knowledgeBaseID, documentID, startPosition+len(result.Chunks))
		if err != nil {
			if errors.Is(err, documentchunk.ErrChunkNotFound) && len(result.Chunks) > 0 {
				break
			}
			return readDocumentResult{}, err
		}
		if result.OriginalFilename == "" {
			result.OriginalFilename = chunk.OriginalFilename
		}
		remaining := maxBytes - bytesUsed
		if len(chunk.Content) > remaining {
			chunk.Content = truncateToolText(chunk.Content, remaining)
			result.Truncated = true
		}
		result.Chunks = append(result.Chunks, documentReadChunk{Position: chunk.Position, Content: chunk.Content})
		bytesUsed += len(chunk.Content)
		result.NextPosition = chunk.Position + 1
		if result.Truncated {
			break
		}
	}
	return result, nil
}

func truncateToolText(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}
