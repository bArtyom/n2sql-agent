package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

func NewDocumentList(reader document.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		documents, err := reader.List(r.Context(), knowledgeBaseID)
		if errors.Is(err, document.ErrKnowledgeBaseNotFound) {
			http.Error(w, `{"error":"knowledge base not found"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to list documents"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, documents)
	})
}
