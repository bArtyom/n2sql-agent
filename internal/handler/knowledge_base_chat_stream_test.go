package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type streamAnswererStub struct{}

func (streamAnswererStub) Stream(_ context.Context, _ int64, _ string, _ int, emit func(rag.StreamEvent) error) error {
	if err := emit(rag.StreamEvent{
		Type: "sources",
		Sources: []retrieval.Result{{
			DocumentID:       10,
			OriginalFilename: "guide.md",
			Position:         2,
			Content:          "执行命令",
		}},
	}); err != nil {
		return err
	}
	return emit(rag.StreamEvent{Type: "delta", Delta: "回答片段"})
}

func TestKnowledgeBaseChatStreamWritesSSEEvents(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChatStream(streamAnswererStub{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat/stream", strings.NewReader(`{"message":"问题","topK":5}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", contentType)
	}
	want := "event: sources\ndata: {\"type\":\"sources\",\"sources\":[{\"documentId\":10,\"originalFilename\":\"guide.md\",\"position\":2,\"content\":\"执行命令\",\"distance\":0}]}\n\nevent: delta\ndata: {\"type\":\"delta\",\"delta\":\"回答片段\"}\n\nevent: done\ndata: {}\n\n"
	if response.Body.String() != want {
		t.Fatalf("body = %q, want %q", response.Body.String(), want)
	}
}

func TestKnowledgeBaseChatStreamRejectsInvalidRequestBeforeStreaming(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChatStream(streamAnswererStub{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/invalid/chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "invalid")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("invalid request must not start SSE")
	}
}

type failingStreamAnswererStub struct{}

func (failingStreamAnswererStub) Stream(_ context.Context, _ int64, _ string, _ int, emit func(rag.StreamEvent) error) error {
	if err := emit(rag.StreamEvent{Type: "sources", Sources: []retrieval.Result{{Content: "资料"}}}); err != nil {
		return err
	}
	return errors.New("model unavailable")
}

func TestKnowledgeBaseChatStreamWritesErrorAfterStreamingStarts(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChatStream(failingStreamAnswererStub{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "event: error\ndata: {\"error\":\"unable to answer question\"}") {
		t.Fatalf("body = %q, want SSE error event", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "event: done") {
		t.Fatal("failed stream must not write done event")
	}
}
