package modelruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type providerStoreStub struct {
	provider modelprovider.Provider
	err      error
}

func (s providerStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.provider, s.err
}

func (providerStoreStub) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	return modelprovider.Provider{}, nil
}

type embedderStub struct {
	baseURL string
	apiKey  string
	request modelclient.EmbeddingRequest
	err     error
}

func (s *embedderStub) Embed(_ context.Context, baseURL, apiKey string, request modelclient.EmbeddingRequest) (modelclient.EmbeddingResponse, error) {
	s.baseURL = baseURL
	s.apiKey = apiKey
	s.request = request
	if s.err != nil {
		return modelclient.EmbeddingResponse{}, s.err
	}
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1, 0.2}}}}, nil
}

func TestEmbeddingServiceReturnsProviderAndAPIKeyErrors(t *testing.T) {
	tests := []struct {
		name    string
		store   providerStoreStub
		keyName string
		lookup  modelruntime.APIKeyLookup
		wantErr error
	}{
		{
			name:    "provider not configured",
			store:   providerStoreStub{err: modelprovider.ErrNotFound},
			keyName: "TEST_MODEL_PROVIDER_API_KEY",
			lookup:  func(string) (string, bool) { return "test-secret", true },
			wantErr: modelprovider.ErrNotFound,
		},
		{
			name: "unexpected key environment variable",
			store: providerStoreStub{provider: modelprovider.Provider{
				APIKeyEnvVar: "ANOTHER_KEY",
			}},
			keyName: "TEST_MODEL_PROVIDER_API_KEY",
			lookup:  func(string) (string, bool) { return "test-secret", true },
			wantErr: modelruntime.ErrAPIKeyEnvironmentMismatch,
		},
		{
			name: "key is missing",
			store: providerStoreStub{provider: modelprovider.Provider{
				APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
			}},
			keyName: "TEST_MODEL_PROVIDER_API_KEY",
			lookup:  func(string) (string, bool) { return "", false },
			wantErr: modelruntime.ErrAPIKeyNotConfigured,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := modelruntime.NewEmbeddingService(test.store, &embedderStub{}, test.keyName, test.lookup)
			_, err := service.Embed(context.Background(), []string{"document chunk"})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Embed() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestEmbeddingServiceUsesConfiguredProviderAndEnvironmentKey(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:        "https://api.example.com/v1",
		APIKeyEnvVar:   "TEST_MODEL_PROVIDER_API_KEY",
		EmbeddingModel: "test-embedding-model",
	}}
	embedder := &embedderStub{}
	service := modelruntime.NewEmbeddingService(store, embedder, "TEST_MODEL_PROVIDER_API_KEY", func(name string) (string, bool) {
		if name != "TEST_MODEL_PROVIDER_API_KEY" {
			t.Fatalf("environment lookup name = %q", name)
		}
		return "test-secret", true
	})

	response, err := service.Embed(context.Background(), []string{"document chunk"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(response.Data) != 1 || len(response.Data[0].Vector) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if embedder.baseURL != store.provider.BaseURL {
		t.Fatalf("base URL = %q, want %q", embedder.baseURL, store.provider.BaseURL)
	}
	if embedder.apiKey != "test-secret" {
		t.Fatalf("API key = %q, want environment value", embedder.apiKey)
	}
	if embedder.request.Model != store.provider.EmbeddingModel {
		t.Fatalf("model = %q, want %q", embedder.request.Model, store.provider.EmbeddingModel)
	}
}
