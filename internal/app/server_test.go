package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type modelProviderStoreStub struct {
	provider modelprovider.Provider
}

func (s modelProviderStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.provider, nil
}

func (modelProviderStoreStub) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	return modelprovider.Provider{}, nil
}

type connectionCheckerStub struct{}

func (connectionCheckerStub) Check(context.Context, string, string) error { return nil }

func TestServerServesHealthCheck(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(nil, nil, "OPENAI_API_KEY").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}

	if body := response.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("response body = %q, want %q", body, `{"status":"ok"}`)
	}
}

func TestServerRoutesConnectionTest(t *testing.T) {
	t.Setenv("TEST_MODEL_PROVIDER_API_KEY", "test-secret")
	response := httptest.NewRecorder()
	server := app.New(modelProviderStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
	}}, connectionCheckerStub{}, "TEST_MODEL_PROVIDER_API_KEY")

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}
