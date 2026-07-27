package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
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

type embeddingRunnerStub struct{}

func (embeddingRunnerStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1}}}}, nil
}

func TestServerServesHealthCheck(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(app.Dependencies{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

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
	server := app.New(app.Dependencies{
		Providers: modelProviderStoreStub{provider: modelprovider.Provider{
			BaseURL:      "https://example.com/v1",
			APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		}},
		ConnectionChecker: connectionCheckerStub{},
		APIKeyEnvVar:      "TEST_MODEL_PROVIDER_API_KEY",
	})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesEmbeddingTest(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Embeddings: embeddingRunnerStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerDoesNotRegisterEmbeddingRouteWithoutRunner(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(app.Dependencies{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}
