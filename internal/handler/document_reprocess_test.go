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

type documentReprocessorStub struct {
	knowledgeBaseID int64
	documentID      int64
	config          *documentextractor.ProcessConfig
	err             error
}

func (s *documentReprocessorStub) Reprocess(_ context.Context, knowledgeBaseID, documentID int64, config *documentextractor.ProcessConfig) error {
	s.knowledgeBaseID, s.documentID, s.config = knowledgeBaseID, documentID, config
	return s.err
}

func TestDocumentReprocessAcceptsProcessConfig(t *testing.T) {
	reprocessor := &documentReprocessorStub{}
	endpoint := handler.NewDocumentReprocess(reprocessor)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/9/reprocess", strings.NewReader(`{"process_config":{"chunking_config":{"chunk_size":600}}}`))
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || reprocessor.knowledgeBaseID != 4 || reprocessor.documentID != 9 {
		t.Fatalf("status=%d ids=%d/%d", response.Code, reprocessor.knowledgeBaseID, reprocessor.documentID)
	}
	if reprocessor.config == nil || reprocessor.config.ChunkingConfig == nil || reprocessor.config.ChunkingConfig.ChunkSize != 600 {
		t.Fatalf("process config = %#v", reprocessor.config)
	}
}

func TestDocumentReprocessRejectsUnknownProcessConfigField(t *testing.T) {
	endpoint := handler.NewDocumentReprocess(&documentReprocessorStub{})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/9/reprocess", strings.NewReader(`{"process_config":{"unknown":true}}`))
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDocumentReprocessMapsInvalidDocument(t *testing.T) {
	endpoint := handler.NewDocumentReprocess(&documentReprocessorStub{err: document.ErrDocumentNotFound})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/9/reprocess", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusNotFound)
	}
}
