package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type connectionCheckerStub struct {
	baseURL string
	apiKey  string
	calls   int
	err     error
}

func (s *connectionCheckerStub) Check(_ context.Context, baseURL, apiKey string) error {
	s.calls++
	s.baseURL = baseURL
	s.apiKey = apiKey
	return s.err
}

func TestModelProviderConnectionTestReturnsOK(t *testing.T) {
	t.Setenv("TEST_MODEL_PROVIDER_API_KEY", "test-secret")
	store := &modelProviderStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
	}}
	checker := &connectionCheckerStub{}
	endpoint := handler.NewModelProviderConnectionTest(store, checker, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("response body = %q, want success response", response.Body.String())
	}
	if checker.baseURL != store.provider.BaseURL {
		t.Fatalf("base URL = %q, want %q", checker.baseURL, store.provider.BaseURL)
	}
	if checker.apiKey != "test-secret" {
		t.Fatalf("API key = %q, want environment value", checker.apiKey)
	}
}

func TestModelProviderConnectionTestRejectsNonPostRequests(t *testing.T) {
	endpoint := handler.NewModelProviderConnectionTest(&modelProviderStoreStub{}, &connectionCheckerStub{}, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func TestModelProviderConnectionTestRejectsMissingAPIKey(t *testing.T) {
	checker := &connectionCheckerStub{}
	endpoint := handler.NewModelProviderConnectionTest(&modelProviderStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
	}}, checker, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if checker.calls != 0 {
		t.Fatalf("checker calls = %d, want 0", checker.calls)
	}
}

func TestModelProviderConnectionTestReportsUpstreamFailure(t *testing.T) {
	t.Setenv("TEST_MODEL_PROVIDER_API_KEY", "test-secret")
	checker := &connectionCheckerStub{err: errors.New("upstream unavailable")}
	endpoint := handler.NewModelProviderConnectionTest(&modelProviderStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
	}}, checker, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
}

func TestModelProviderConnectionTestReturnsNotFoundWhenUnconfigured(t *testing.T) {
	endpoint := handler.NewModelProviderConnectionTest(&modelProviderStoreStub{err: modelprovider.ErrNotFound}, &connectionCheckerStub{}, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestModelProviderConnectionTestReturnsServerErrorWhenConfigurationLoadFails(t *testing.T) {
	endpoint := handler.NewModelProviderConnectionTest(&modelProviderStoreStub{err: errors.New("database unavailable")}, &connectionCheckerStub{}, "TEST_MODEL_PROVIDER_API_KEY")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
