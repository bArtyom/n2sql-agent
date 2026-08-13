package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

func NewDocumentReprocess(reprocessor document.Reprocessor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		documentID, documentErr := strconv.ParseInt(r.PathValue("documentId"), 10, 64)
		if err != nil || documentErr != nil || knowledgeBaseID <= 0 || documentID <= 0 {
			http.Error(w, `{"error":"invalid document ID"}`, http.StatusBadRequest)
			return
		}
		item, err := reprocessor.Reprocess(r.Context(), knowledgeBaseID, documentID)
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) || errors.Is(err, document.ErrDocumentNotFound) {
			http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to reprocess document"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, item)
	})
}
