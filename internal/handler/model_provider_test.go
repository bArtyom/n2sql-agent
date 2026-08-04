package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type modelProviderStoreStub struct {
	provider modelprovider.Provider
	err      error
}

func (s *modelProviderStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.provider, s.err
}

func (s *modelProviderStoreStub) Save(_ context.Context, provider modelprovider.Provider) (modelprovider.Provider, error) {
	s.provider = provider
	return provider, nil
}

func TestModelProviderReturnsNotFoundWhenUnconfigured(t *testing.T) {
	endpoint := handler.NewModelProvider(&modelProviderStoreStub{err: modelprovider.ErrNotFound}, "CUSTOM_MODEL_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-provider", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
	if !strings.Contains(response.Body.String(), `"apiKeyEnvVar":"CUSTOM_MODEL_KEY"`) {
		t.Fatalf("response should expose the configured environment variable name: %s", response.Body.String())
	}
}

func TestModelProviderSavesAndReturnsConfiguration(t *testing.T) {
	store := &modelProviderStoreStub{err: errors.New("not used")}
	endpoint := handler.NewModelProvider(store, "OPENAI_API_KEY")
	body := `{"name":"OpenAI Compatible","baseUrl":"https://example.com/v1","apiKeyEnvVar":"OPENAI_API_KEY","chatModel":"chat","embeddingModel":"embedding","enabled":true}`

	update := httptest.NewRecorder()
	endpoint.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/api/model-provider", strings.NewReader(body)))

	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d", update.Code, http.StatusOK)
	}

	store.err = nil
	read := httptest.NewRecorder()
	endpoint.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/model-provider", nil))

	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d, want %d", read.Code, http.StatusOK)
	}
	if strings.Contains(read.Body.String(), `"apiKey":`) {
		t.Fatalf("response must not include an API key: %s", read.Body.String())
	}
}

func TestModelProviderRejectsUnexpectedAPIKeyEnvironmentVariable(t *testing.T) {
	endpoint := handler.NewModelProvider(&modelProviderStoreStub{}, "OPENAI_API_KEY")
	body := `{"name":"OpenAI Compatible","baseUrl":"https://example.com/v1","apiKeyEnvVar":"DATABASE_URL","chatModel":"chat","embeddingModel":"embedding","enabled":true}`
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/model-provider", strings.NewReader(body)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
