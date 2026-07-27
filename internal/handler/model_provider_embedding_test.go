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

type embeddingRunnerStub struct {
	input []string
	err   error
}

func (s *embeddingRunnerStub) Embed(_ context.Context, input []string) (modelclient.EmbeddingResponse, error) {
	s.input = input
	if s.err != nil {
		return modelclient.EmbeddingResponse{}, s.err
	}
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1, 0.2}}}}, nil
}

func TestModelProviderEmbeddingTestReturnsVectors(t *testing.T) {
	runner := &embeddingRunnerStub{}
	endpoint := handler.NewModelProviderEmbeddingTest(runner)
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != `{"data":[{"index":0,"vector":[0.1,0.2]}]}`+"\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if len(runner.input) != 1 || runner.input[0] != "document chunk" {
		t.Fatalf("input = %#v", runner.input)
	}
}

func TestModelProviderEmbeddingTestRejectsInvalidInput(t *testing.T) {
	endpoint := handler.NewModelProviderEmbeddingTest(&embeddingRunnerStub{})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":[]}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestModelProviderEmbeddingTestRejectsTrailingJSON(t *testing.T) {
	endpoint := handler.NewModelProviderEmbeddingTest(&embeddingRunnerStub{})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}{"input":["another chunk"]}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestModelProviderEmbeddingTestRejectsOversizedInput(t *testing.T) {
	endpoint := handler.NewModelProviderEmbeddingTest(&embeddingRunnerStub{})
	response := httptest.NewRecorder()
	body := `{"input":["` + strings.Repeat("a", 300000) + `"]}`

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(body)))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestModelProviderEmbeddingTestReportsModelFailure(t *testing.T) {
	endpoint := handler.NewModelProviderEmbeddingTest(&embeddingRunnerStub{err: &modelruntime.EmbeddingCallError{Err: errors.New("model unavailable")}})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
}
