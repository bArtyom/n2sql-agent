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

type answererStub struct {
	knowledgeBaseID int64
	question        string
	topK            int
	err             error
	options         *retrieval.SearchOptions
}

func (s *answererStub) Answer(_ context.Context, knowledgeBaseID int64, question string, topK int) (rag.Response, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.question = question
	s.topK = topK
	if s.err != nil {
		return rag.Response{}, s.err
	}
	return rag.Response{
		Answer: "可以执行 go run ./cmd/server。",
		Sources: []retrieval.Result{{
			DocumentID:       10,
			OriginalFilename: "guide.md",
			Position:         2,
			Content:          "执行 go run ./cmd/server 启动服务。",
		}},
	}, nil
}

func (s *answererStub) AnswerWithSearchOptions(_ context.Context, knowledgeBaseID int64, question string, topK int, _ float64, options retrieval.SearchOptions) (rag.Response, error) {
	s.options = &options
	return s.Answer(context.Background(), knowledgeBaseID, question, topK)
}

func TestKnowledgeBaseChatReturnsAnswerAndSources(t *testing.T) {
	answerer := &answererStub{}
	endpoint := handler.NewKnowledgeBaseChat(answerer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"如何启动服务？","topK":5}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != `{"answer":"可以执行 go run ./cmd/server。","sources":[{"documentId":10,"originalFilename":"guide.md","position":2,"content":"执行 go run ./cmd/server 启动服务。","distance":0}]}`+"\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if answerer.knowledgeBaseID != 7 || answerer.question != "如何启动服务？" || answerer.topK != 5 {
		t.Fatalf("answer arguments = %#v", answerer)
	}
}

func TestKnowledgeBaseChatPassesFolderScope(t *testing.T) {
	answerer := &answererStub{}
	endpoint := handler.NewKnowledgeBaseChat(answerer)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题","folder_path":"docs/api","folder_recursive":true}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || answerer.options == nil || answerer.options.FolderPath == nil || *answerer.options.FolderPath != "docs/api" || !answerer.options.FolderRecursive {
		t.Fatalf("status=%d options=%#v", response.Code, answerer.options)
	}
}

func TestKnowledgeBaseChatDefaultsTopKAndRejectsInvalidInput(t *testing.T) {
	answerer := &answererStub{}
	endpoint := handler.NewKnowledgeBaseChat(answerer)

	valid := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(valid, request)
	if valid.Code != http.StatusOK || answerer.topK != 5 {
		t.Fatalf("status = %d, topK = %d", valid.Code, answerer.topK)
	}

	invalid := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题","topK":21}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", invalid.Code, http.StatusBadRequest)
	}
}

func TestKnowledgeBaseChatReportsMissingSources(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChat(&answererStub{err: rag.ErrNoSources})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestKnowledgeBaseChatReportsUnexpectedFailure(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChat(&answererStub{err: errors.New("database unavailable")})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func TestKnowledgeBaseChatRejectsMalformedAndOversizedRequests(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseChat(&answererStub{})
	cases := []struct {
		name string
		id   string
		body string
	}{
		{name: "invalid ID", id: "invalid", body: `{"message":"问题"}`},
		{name: "malformed JSON", id: "7", body: `{"message":"问题"`},
		{name: "unknown field", id: "7", body: `{"message":"问题","unexpected":true}`},
		{name: "trailing JSON", id: "7", body: `{"message":"问题"}{"message":"另一个问题"}`},
		{name: "oversized message", id: "7", body: `{"message":"` + strings.Repeat("a", 8001) + `"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/"+test.id+"/chat", strings.NewReader(test.body))
			request.SetPathValue("id", test.id)
			endpoint.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}
