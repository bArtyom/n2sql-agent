package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
)

type documentChunkResponse struct {
	DocumentID       int64  `json:"documentId"`
	OriginalFilename string `json:"originalFilename,omitempty"`
	Position         int    `json:"position"`
	ChunkKind        string `json:"chunkKind,omitempty"`
	ImageInfo        any    `json:"imageInfo,omitempty"`
	Content          string `json:"content"`
	HeadingPath      string `json:"headingPath,omitempty"`
	ParentContent    string `json:"parentContent,omitempty"`
	ParentPosition   int    `json:"parentPosition,omitempty"`
}

// NewDocumentChunk serves the full content behind a stored citation preview.
// The Reader owns the authorization boundary; this handler only validates the
// route values and maps storage errors to a small public error vocabulary.
func NewDocumentChunk(reader documentchunk.Reader) http.Handler {
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
		position, err := strconv.Atoi(r.PathValue("position"))
		if err != nil || position < 0 {
			http.Error(w, `{"error":"invalid chunk position"}`, http.StatusBadRequest)
			return
		}

		kind := r.URL.Query().Get("kind")
		if kind == "" {
			kind = "text"
		}
		var chunk documentchunk.SearchResult
		if kindReader, ok := reader.(documentchunk.KindReader); ok {
			chunk, err = kindReader.ReadKind(r.Context(), knowledgeBaseID, documentID, position, kind)
		} else if kind == "text" {
			chunk, err = reader.Read(r.Context(), knowledgeBaseID, documentID, position)
		} else {
			http.Error(w, `{"error":"unsupported chunk kind"}`, http.StatusBadRequest)
			return
		}
		if errors.Is(err, documentchunk.ErrChunkNotFound) {
			http.Error(w, `{"error":"document chunk not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to read document chunk"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, documentChunkResponse{
			DocumentID:       chunk.DocumentID,
			OriginalFilename: chunk.OriginalFilename,
			Position:         chunk.Position,
			ChunkKind:        chunk.ChunkKind,
			ImageInfo:        chunk.ImageInfo,
			Content:          chunk.Content,
			HeadingPath:      chunk.HeadingPath,
			ParentContent:    chunk.ParentContent,
			ParentPosition:   chunk.ParentPosition,
		})
	})
}
