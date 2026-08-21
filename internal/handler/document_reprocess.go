package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

func NewDocumentReprocess(reprocessor document.Reprocessor) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
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
		if err := reprocessor.Reprocess(r.Context(), knowledgeBaseID, documentID); err != nil {
			switch {
			case errors.Is(err, document.ErrDocumentNotFound), errors.Is(err, document.ErrKnowledgeBaseNotFound):
				http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			case errors.Is(err, document.ErrDocumentProcessing):
				http.Error(w, `{"error":"document is already processing"}`, http.StatusConflict)
			case errors.Is(err, document.ErrDeleteUnavailable):
				http.Error(w, `{"error":"document reprocess is unavailable"}`, http.StatusNotImplemented)
			default:
				http.Error(w, `{"error":"unable to reprocess document"}`, http.StatusInternalServerError)
			}
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, struct {
			DocumentID int64  `json:"documentId"`
			Status     string `json:"status"`
		}{DocumentID: documentID, Status: "pending"})
	})
}
