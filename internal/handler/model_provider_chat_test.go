package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type chatRunnerStub struct {
	message string
	err     error
}

func (s *chatRunnerStub) Chat(_ context.Context, message string) (modelclient.ChatResponse, error) {
	s.message = message
	if s.err != nil {
		return modelclient.ChatResponse{}, s.err
	}
	return modelclient.ChatResponse{Message: "OK"}, nil
}

func TestModelProviderChatTestReturnsMessage(t *testing.T) {
	runner := &chatRunnerStub{}
	endpoint := handler.NewModelProviderChatTest(runner)
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"reply with OK"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != `{"message":"OK"}`+"\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if runner.message != "reply with OK" {
		t.Fatalf("message = %q, want %q", runner.message, "reply with OK")
	}
}

func TestModelProviderChatTestRejectsInvalidMessage(t *testing.T) {
	for _, body := range []string{
		`{"message":" "}`,
		`{"message":"hello","unexpected":true}`,
		`{"message":"hello"}{"message":"again"}`,
	} {
		response := httptest.NewRecorder()
		handler.NewModelProviderChatTest(&chatRunnerStub{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(body)))

		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status code = %d, want %d", body, response.Code, http.StatusBadRequest)
		}
	}
}

func TestModelProviderChatTestEnforcesMessageSizeLimit(t *testing.T) {
	runner := &chatRunnerStub{}
	endpoint := handler.NewModelProviderChatTest(runner)

	validResponse := httptest.NewRecorder()
	validMessage := strings.Repeat("a", 8000)
	endpoint.ServeHTTP(validResponse, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"`+validMessage+`"}`)))
	if validResponse.Code != http.StatusOK {
		t.Fatalf("8,000-byte message status code = %d, want %d", validResponse.Code, http.StatusOK)
	}

	invalidResponse := httptest.NewRecorder()
	invalidMessage := strings.Repeat("a", 8001)
	endpoint.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"`+invalidMessage+`"}`)))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("8,001-byte message status code = %d, want %d", invalidResponse.Code, http.StatusBadRequest)
	}
}

func TestModelProviderChatTestRejectsOversizedRequest(t *testing.T) {
	response := httptest.NewRecorder()
	body := `{"message":"` + strings.Repeat("a", 13000) + `"}`

	handler.NewModelProviderChatTest(&chatRunnerStub{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(body)))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestModelProviderChatTestReportsModelFailure(t *testing.T) {
	endpoint := handler.NewModelProviderChatTest(&chatRunnerStub{err: &modelruntime.ChatCallError{Err: errors.New("model unavailable")}})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"hello"}`)))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
}
