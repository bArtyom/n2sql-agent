package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
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
		requestBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProcessConfigBytes+1))
		if err != nil {
			http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
			return
		}
		processConfig, err := parseReprocessProcessConfig(requestBody)
		if err != nil {
			http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
			return
		}
		if err := reprocessor.Reprocess(r.Context(), knowledgeBaseID, documentID, processConfig); err != nil {
			switch {
			case errors.Is(err, document.ErrDocumentNotFound), errors.Is(err, document.ErrKnowledgeBaseNotFound):
				http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			case errors.Is(err, document.ErrDocumentProcessing):
				http.Error(w, `{"error":"document is already processing"}`, http.StatusConflict)
			case errors.Is(err, document.ErrDeleteUnavailable):
				http.Error(w, `{"error":"document reprocess is unavailable"}`, http.StatusNotImplemented)
			case errors.Is(err, document.ErrInvalidProcessConfig):
				http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
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

func parseReprocessProcessConfig(body []byte) (*documentextractor.ProcessConfig, error) {
	if strings.TrimSpace(string(body)) == "" {
		return nil, nil
	}
	var request struct {
		ProcessConfig json.RawMessage `json:"process_config"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("reprocess request must contain one JSON object")
		}
		return nil, err
	}
	return parseRawProcessConfig(request.ProcessConfig)
}
