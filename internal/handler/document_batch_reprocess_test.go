package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type documentBatchReprocessorStub struct {
	knowledgeBaseID int64
	documentIDs     []int64
	config          *documentextractor.ProcessConfig
	count           int
	err             error
}

func (s *documentBatchReprocessorStub) ReprocessMany(_ context.Context, knowledgeBaseID int64, documentIDs []int64, config *documentextractor.ProcessConfig) (int, error) {
	s.knowledgeBaseID, s.documentIDs, s.config = knowledgeBaseID, documentIDs, config
	return s.count, s.err
}

func TestDocumentBatchReprocessAcceptsProcessConfig(t *testing.T) {
	reprocessor := &documentBatchReprocessorStub{count: 2}
	endpoint := handler.NewDocumentBatchReprocess(reprocessor)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/reprocess", strings.NewReader(`{"document_ids":[9,12],"process_config":{"chunking_config":{"chunk_size":600}}}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || reprocessor.knowledgeBaseID != 4 || len(reprocessor.documentIDs) != 2 {
		t.Fatalf("status=%d kb=%d ids=%v", response.Code, reprocessor.knowledgeBaseID, reprocessor.documentIDs)
	}
	if reprocessor.config == nil || reprocessor.config.ChunkingConfig == nil || reprocessor.config.ChunkingConfig.ChunkSize != 600 {
		t.Fatalf("process config = %#v", reprocessor.config)
	}
}

func TestDocumentBatchReprocessRejectsEmptySelection(t *testing.T) {
	endpoint := handler.NewDocumentBatchReprocess(&documentBatchReprocessorStub{err: document.ErrNoDocumentsSelected})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/reprocess", strings.NewReader(`{"document_ids":[]}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDocumentBatchReprocessMapsConflict(t *testing.T) {
	endpoint := handler.NewDocumentBatchReprocess(&documentBatchReprocessorStub{err: document.ErrDocumentProcessing})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/reprocess", strings.NewReader(`{"document_ids":[9]}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusConflict)
	}
}
