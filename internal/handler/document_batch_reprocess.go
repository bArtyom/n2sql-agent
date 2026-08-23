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

const maxBatchReprocessBodyBytes = 128 << 10

type batchDocumentReprocessRequest struct {
	DocumentIDs   []int64         `json:"document_ids"`
	ProcessConfig json.RawMessage `json:"process_config"`
}

// NewDocumentBatchReprocess enqueues one processing task per selected document.
// The request uses the same process_config shape as upload and single-document
// reprocess, so an explicit configuration is captured as a task snapshot.
func NewDocumentBatchReprocess(reprocessor document.BatchReprocessor) http.Handler {
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

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBatchReprocessBodyBytes))
		if err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		request, err := parseBatchDocumentReprocessRequest(body)
		if err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		processConfig, err := parseRawProcessConfig(request.ProcessConfig)
		if err != nil {
			http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
			return
		}

		count, err := reprocessor.ReprocessMany(r.Context(), knowledgeBaseID, request.DocumentIDs, processConfig)
		if err != nil {
			switch {
			case errors.Is(err, document.ErrKnowledgeBaseNotFound), errors.Is(err, document.ErrDocumentNotFound):
				http.Error(w, `{"error":"document not found"}`, http.StatusNotFound)
			case errors.Is(err, document.ErrNoDocumentsSelected):
				http.Error(w, `{"error":"no documents selected"}`, http.StatusBadRequest)
			case errors.Is(err, document.ErrDocumentProcessing):
				http.Error(w, `{"error":"one or more documents are already processing"}`, http.StatusConflict)
			case errors.Is(err, document.ErrDeleteUnavailable):
				http.Error(w, `{"error":"document batch reprocess is unavailable"}`, http.StatusNotImplemented)
			case errors.Is(err, document.ErrInvalidProcessConfig):
				http.Error(w, `{"error":"invalid process_config"}`, http.StatusBadRequest)
			default:
				http.Error(w, `{"error":"unable to reprocess documents"}`, http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, struct {
			DocumentIDs []int64 `json:"document_ids"`
			Count       int     `json:"count"`
			Status      string  `json:"status"`
		}{DocumentIDs: request.DocumentIDs, Count: count, Status: "pending"})
	})
}

func parseBatchDocumentReprocessRequest(body []byte) (batchDocumentReprocessRequest, error) {
	if strings.TrimSpace(string(body)) == "" {
		return batchDocumentReprocessRequest{}, errors.New("request body is empty")
	}
	var request batchDocumentReprocessRequest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return batchDocumentReprocessRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		if err == nil {
			return batchDocumentReprocessRequest{}, errors.New("request must contain one JSON object")
		}
		return batchDocumentReprocessRequest{}, err
	}
	return request, nil
}

func parseRawProcessConfig(raw json.RawMessage) (*documentextractor.ProcessConfig, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	return parseProcessConfig(string(raw))
}
