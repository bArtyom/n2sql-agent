package handler

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

// NewDocumentAsset serves an original image for a citation. The document
// service performs the knowledge-base ownership check before opening the file.
func NewDocumentAsset(reader document.AssetReader) http.Handler {
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
		asset, err := reader.OpenAsset(r.Context(), knowledgeBaseID, documentID)
		if errors.Is(err, document.ErrDocumentNotFound) {
			http.Error(w, `{"error":"document asset not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, document.ErrUnsupportedFile) {
			http.Error(w, `{"error":"document asset is not an image"}`, http.StatusUnsupportedMediaType)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to read document asset"}`, http.StatusInternalServerError)
			return
		}
		defer asset.Close()
		w.Header().Set("Content-Type", asset.ContentType)
		w.Header().Set("Content-Disposition", `inline; filename="`+safeAssetFilename(asset.OriginalFilename)+`"`)
		http.ServeContent(w, r, asset.OriginalFilename, time.Time{}, asset.Content)
	})
}

func safeAssetFilename(filename string) string {
	filename = filepath.Base(filename)
	filename = strings.ReplaceAll(filename, `"`, "")
	filename = strings.ReplaceAll(filename, "\r", "")
	filename = strings.ReplaceAll(filename, "\n", "")
	if filename == "." || filename == "" {
		return "image"
	}
	return filename
}
