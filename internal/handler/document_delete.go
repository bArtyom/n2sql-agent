package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

// NewDocumentDelete creates the destructive document lifecycle endpoint.
// The service performs the knowledge-base ownership check before deleting.
func NewDocumentDelete(deleter document.Deleter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}
		documentID, err := strconv.ParseInt(r.PathValue("documentID"), 10, 64)
		if err != nil || documentID <= 0 {
			http.Error(w, `{"error":"invalid document ID"}`, http.StatusBadRequest)
			return
		}

		if err := deleter.Delete(r.Context(), knowledgeBaseID, documentID); err != nil {
			switch {
			case errors.Is(err, document.ErrDocumentNotFound), errors.Is(err, document.ErrKnowledgeBaseNotFound):
				http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			case errors.Is(err, document.ErrDocumentProcessing):
				http.Error(w, `{"error":"document is still processing"}`, http.StatusConflict)
			case errors.Is(err, document.ErrDeleteUnavailable):
				http.Error(w, `{"error":"document deletion is unavailable"}`, http.StatusNotImplemented)
			default:
				http.Error(w, `{"error":"unable to delete document"}`, http.StatusInternalServerError)
			}
			return
		}

		writeJSON(w, struct {
			Deleted bool `json:"deleted"`
		}{Deleted: true})
	})
}
