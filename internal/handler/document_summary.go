package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
)

// NewDocumentSummary handles an explicit full-document summary request. The
// work is performed outside the Agent/ReAct loop and is cached by the service.
func NewDocumentSummary(service *documentsummary.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
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
		result, err := service.Summarize(r.Context(), knowledgeBaseID, documentID)
		if errors.Is(err, document.ErrDocumentNotFound) {
			http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, documentsummary.ErrEmptyDocument) {
			http.Error(w, `{"error":"document has no processed content"}`, http.StatusConflict)
			return
		}
		if errors.Is(err, documentsummary.ErrSummaryRunning) {
			http.Error(w, `{"error":"document summary is already processing"}`, http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"document summary failed"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, result)
	})
}
