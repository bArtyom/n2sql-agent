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

type kindChunkReaderStub struct {
	chunk documentchunk.SearchResult
	kind  string
}

func (s *kindChunkReaderStub) Read(_ context.Context, _, _ int64, _ int) (documentchunk.SearchResult, error) {
	return documentchunk.SearchResult{}, documentchunk.ErrChunkNotFound
}

func (s *kindChunkReaderStub) ReadKind(_ context.Context, _, _ int64, _ int, kind string) (documentchunk.SearchResult, error) {
	s.kind = kind
	return s.chunk, nil
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

func TestDocumentChunkReadsSummaryCitation(t *testing.T) {
	reader := &kindChunkReaderStub{chunk: documentchunk.SearchResult{
		DocumentID: 7, OriginalFilename: "guide.md", Position: 0, ChunkKind: "summary", Content: "文档摘要",
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/chunks/0?kind=summary", nil)
	request.SetPathValue("id", "3")
	request.SetPathValue("documentID", "7")
	request.SetPathValue("position", "0")
	response := httptest.NewRecorder()

	handler.NewDocumentChunk(reader).ServeHTTP(response, request)

	if response.Code != http.StatusOK || reader.kind != "summary" || !strings.Contains(response.Body.String(), "文档摘要") {
		t.Fatalf("response = %d %s, kind=%q", response.Code, response.Body.String(), reader.kind)
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

func TestDocumentPreviewReturnsSeveralChunks(t *testing.T) {
	reader := &previewChunkReaderStub{chunks: map[int]documentchunk.SearchResult{
		0: {DocumentID: 7, OriginalFilename: "guide.md", Position: 0, Content: "第一段"},
		1: {DocumentID: 7, OriginalFilename: "guide.md", Position: 1, Content: "第二段"},
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/preview?limit=2", nil)
	request.SetPathValue("id", "3")
	request.SetPathValue("documentID", "7")
	response := httptest.NewRecorder()

	handler.NewDocumentPreview(reader).ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "第一段") || !strings.Contains(response.Body.String(), "第二段") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestDocumentPreviewRejectsInvalidRangeAndHidesReaderError(t *testing.T) {
	reader := &previewChunkReaderStub{err: errors.New("database password detail")}
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/preview?limit=99", nil)
	invalidRequest.SetPathValue("id", "3")
	invalidRequest.SetPathValue("documentID", "7")
	handler.NewDocumentPreview(reader).ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest || reader.calls != 0 {
		t.Fatalf("invalid response = %d, calls = %d", invalid.Code, reader.calls)
	}

	failed := httptest.NewRecorder()
	failureRequest := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/3/documents/7/preview", nil)
	failureRequest.SetPathValue("id", "3")
	failureRequest.SetPathValue("documentID", "7")
	handler.NewDocumentPreview(reader).ServeHTTP(failed, failureRequest)
	if failed.Code != http.StatusInternalServerError || strings.Contains(failed.Body.String(), "password detail") {
		t.Fatalf("failure response = %d %s", failed.Code, failed.Body.String())
	}
}

type previewChunkReaderStub struct {
	chunks map[int]documentchunk.SearchResult
	err    error
	calls  int
}

func (s *previewChunkReaderStub) Read(_ context.Context, _, _ int64, position int) (documentchunk.SearchResult, error) {
	s.calls++
	if s.err != nil {
		return documentchunk.SearchResult{}, s.err
	}
	chunk, ok := s.chunks[position]
	if !ok {
		return documentchunk.SearchResult{}, documentchunk.ErrChunkNotFound
	}
	return chunk, nil
}
