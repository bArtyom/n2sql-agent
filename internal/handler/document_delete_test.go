package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type documentDeleterStub struct {
	err             error
	knowledgeBaseID int64
	documentID      int64
}

func (s *documentDeleterStub) Delete(_ context.Context, knowledgeBaseID, documentID int64) error {
	s.knowledgeBaseID = knowledgeBaseID
	s.documentID = documentID
	return s.err
}

func TestDocumentDeleteReturnsSuccess(t *testing.T) {
	deleter := &documentDeleterStub{}
	endpoint := handler.NewDocumentDelete(deleter)
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/4/documents/8", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "8")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != "{\"deleted\":true}\n" {
		t.Fatalf("response body = %q, want deleted response", response.Body.String())
	}
	if deleter.knowledgeBaseID != 4 || deleter.documentID != 8 {
		t.Fatalf("delete arguments = %d/%d, want 4/8", deleter.knowledgeBaseID, deleter.documentID)
	}
}

func TestDocumentDeleteRejectsInvalidIDs(t *testing.T) {
	endpoint := handler.NewDocumentDelete(&documentDeleterStub{})
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/nope/documents/8", nil)
	request.SetPathValue("id", "nope")
	request.SetPathValue("documentID", "8")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDocumentDeleteReportsProcessingConflict(t *testing.T) {
	endpoint := handler.NewDocumentDelete(&documentDeleterStub{err: document.ErrDocumentProcessing})
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/4/documents/8", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "8")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusConflict)
	}
}
