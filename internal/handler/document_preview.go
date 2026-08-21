package handler

import (
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
)

const (
	defaultDocumentPreviewLimit = 8
	defaultDocumentPreviewBytes = 32 * 1024
)

type documentPreviewChunk struct {
	Position    int    `json:"position"`
	Content     string `json:"content"`
	HeadingPath string `json:"headingPath,omitempty"`
}

type documentPreviewResponse struct {
	DocumentID       int64                           `json:"documentId"`
	OriginalFilename string                          `json:"originalFilename,omitempty"`
	Chunks           []documentPreviewChunk          `json:"chunks"`
	NextPosition     int                             `json:"nextPosition"`
	Truncated        bool                            `json:"truncated"`
	Diagnostics      *documentchunk.SplitDiagnostics `json:"diagnostics,omitempty"`
}

// NewDocumentPreview exposes a bounded, read-only document window. It uses a
// batch reader when available and falls back to the existing single-chunk
// reader so older implementations remain compatible.
func NewDocumentPreview(reader documentchunk.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		documentID, err := strconv.ParseInt(r.PathValue("documentID"), 10, 64)
		if err != nil || documentID <= 0 {
			http.Error(w, `{"error":"invalid document ID"}`, http.StatusBadRequest)
			return
		}
		startPosition, limit, ok := previewQuery(r)
		if !ok {
			http.Error(w, `{"error":"invalid preview range"}`, http.StatusBadRequest)
			return
		}
		result, err := readPreview(r, reader, knowledgeBaseID, documentID, startPosition, limit, defaultDocumentPreviewBytes)
		if errors.Is(err, documentchunk.ErrChunkNotFound) {
			http.Error(w, `{"error":"document content not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to read document preview"}`, http.StatusInternalServerError)
			return
		}
		response := documentPreviewResponse{
			DocumentID:       documentID,
			OriginalFilename: result.OriginalFilename,
			Chunks:           make([]documentPreviewChunk, 0, len(result.Chunks)),
			NextPosition:     result.NextPosition,
			Truncated:        result.Truncated,
		}
		if diagnosticsReader, ok := reader.(documentchunk.DiagnosticsReader); ok {
			diagnostics, diagnosticsErr := diagnosticsReader.ChunkingDiagnostics(r.Context(), knowledgeBaseID, documentID)
			if diagnosticsErr == nil && diagnostics.ChunkCount > 0 {
				response.Diagnostics = &diagnostics
			}
		}
		for _, chunk := range result.Chunks {
			response.Chunks = append(response.Chunks, documentPreviewChunk{Position: chunk.Position, Content: chunk.Content, HeadingPath: chunk.HeadingPath})
		}
		writeJSON(w, response)
	})
}

func previewQuery(r *http.Request) (int, int, bool) {
	startPosition := 0
	limit := defaultDocumentPreviewLimit
	if raw := r.URL.Query().Get("start"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, false
		}
		startPosition = value
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 20 {
			return 0, 0, false
		}
		limit = value
	}
	return startPosition, limit, true
}

type previewResult struct {
	Chunks           []documentchunk.SearchResult
	NextPosition     int
	Truncated        bool
	OriginalFilename string
}

func readPreview(r *http.Request, reader documentchunk.Reader, knowledgeBaseID, documentID int64, startPosition, limit, maxBytes int) (previewResult, error) {
	if rangeReader, ok := reader.(documentchunk.RangeReader); ok {
		result, err := rangeReader.ReadRange(r.Context(), knowledgeBaseID, documentID, startPosition, limit, maxBytes)
		return previewResult{Chunks: result.Chunks, NextPosition: result.NextPosition, Truncated: result.Truncated, OriginalFilename: firstFilename(result.Chunks)}, err
	}
	result := previewResult{Chunks: make([]documentchunk.SearchResult, 0, limit), NextPosition: startPosition}
	bytesUsed := 0
	for len(result.Chunks) < limit && bytesUsed < maxBytes {
		chunk, err := reader.Read(r.Context(), knowledgeBaseID, documentID, startPosition+len(result.Chunks))
		if errors.Is(err, documentchunk.ErrChunkNotFound) {
			if len(result.Chunks) == 0 {
				return previewResult{}, err
			}
			break
		}
		if err != nil {
			return previewResult{}, err
		}
		remaining := maxBytes - bytesUsed
		if len(chunk.Content) > remaining {
			chunk.Content = truncatePreviewText(chunk.Content, remaining)
			result.Truncated = true
		}
		result.Chunks = append(result.Chunks, chunk)
		bytesUsed += len(chunk.Content)
		result.NextPosition = chunk.Position + 1
		if result.Truncated {
			break
		}
	}
	if len(result.Chunks) == limit {
		result.Truncated = true
	}
	result.OriginalFilename = firstFilename(result.Chunks)
	return result, nil
}

func firstFilename(chunks []documentchunk.SearchResult) string {
	if len(chunks) == 0 {
		return ""
	}
	return chunks[0].OriginalFilename
}

func truncatePreviewText(value string, maxBytes int) string {
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
