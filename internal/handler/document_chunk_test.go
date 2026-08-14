package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type chunkReaderStub struct {
	chunk documentchunk.SearchResult
	err   error
	calls int
}

func (s *chunkReaderStub) Read(_ context.Context, _, _ int64, _ int) (documentchunk.SearchResult, error) {
	s.calls++
	return s.chunk, s.err
}

func TestDocumentChunkReturnsDetail(t *testing.T) {
	reader := &chunkReaderStub{chunk: documentchunk.SearchResult{
		DocumentID: 7, OriginalFilename: "guide.md", Position: 2,
		Content: "完整的子块内容", ParentContent: "父块内容", ParentPosition: 1,
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/chunks/2", nil)
	request.SetPathValue("id", "3")
	request.SetPathValue("documentID", "7")
	request.SetPathValue("position", "2")
	response := httptest.NewRecorder()

	handler.NewDocumentChunk(reader).ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "完整的子块内容") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if reader.calls != 1 {
		t.Fatalf("reader calls = %d, want 1", reader.calls)
	}
}

func TestDocumentChunkRejectsInvalidRoute(t *testing.T) {
	reader := &chunkReaderStub{}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/no/chunks/2", nil)
	request.SetPathValue("id", "3")
	request.SetPathValue("documentID", "no")
	request.SetPathValue("position", "2")
	response := httptest.NewRecorder()

	handler.NewDocumentChunk(reader).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || reader.calls != 0 {
		t.Fatalf("response = %d, calls = %d", response.Code, reader.calls)
	}
}

func TestDocumentChunkMapsNotFoundAndStorageErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "not found", err: documentchunk.ErrChunkNotFound, want: http.StatusNotFound},
		{name: "storage failure", err: errors.New("database unavailable"), want: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &chunkReaderStub{err: test.err}
			request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/chunks/2", nil)
			request.SetPathValue("id", "3")
			request.SetPathValue("documentID", "7")
			request.SetPathValue("position", "2")
			response := httptest.NewRecorder()

			handler.NewDocumentChunk(reader).ServeHTTP(response, request)

			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
			if strings.Contains(response.Body.String(), "database unavailable") {
				t.Fatal("storage detail leaked in response")
			}
		})
	}
}
