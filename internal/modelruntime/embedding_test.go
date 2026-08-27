package modelruntime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
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

type multimodalEmbedderStub struct {
	baseURL string
	apiKey  string
	request modelclient.MultimodalEmbeddingRequest
}

type profileStoreStub struct {
	profile knowledgebase.EmbeddingProfile
}

func (s profileStoreStub) GetEmbeddingProfile(context.Context, int64) (knowledgebase.EmbeddingProfile, error) {
	return s.profile, nil
}

func (profileStoreStub) SaveEmbeddingProfile(context.Context, int64, knowledgebase.EmbeddingProfile) error {
	return nil
}

type standardEmbeddingRunnerStub struct{ calls int }

func (s *standardEmbeddingRunnerStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	s.calls++
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Vector: []float32{1}}}}, nil
}

func (s *multimodalEmbedderStub) EmbedMultimodal(_ context.Context, baseURL, apiKey string, request modelclient.MultimodalEmbeddingRequest) (modelclient.EmbeddingResponse, error) {
	s.baseURL = baseURL
	s.apiKey = apiKey
	s.request = request
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1, 0.2}}}}, nil
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

func TestEmbeddingServiceUsesOptionalLocalProvider(t *testing.T) {
	embedder := &embedderStub{}
	service := modelruntime.NewEmbeddingServiceWithLocalFallback(
		providerStoreStub{err: modelprovider.ErrNotFound},
		embedder,
		"TEST_MODEL_PROVIDER_API_KEY",
		func(string) (string, bool) { return "", false },
		"http://127.0.0.1:11434/v1",
		"qwen3-embedding:0.6b",
		"ollama",
	)
	if _, err := service.Embed(context.Background(), []string{"本地向量测试"}); err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if embedder.baseURL != "http://127.0.0.1:11434/v1" || embedder.apiKey != "ollama" || embedder.request.Model != "qwen3-embedding:0.6b" {
		t.Fatalf("local embedding request = %#v, baseURL=%q, apiKey=%q", embedder.request, embedder.baseURL, embedder.apiKey)
	}
}

func TestMultimodalEmbeddingServiceUsesConfiguredLocalProvider(t *testing.T) {
	embedder := &multimodalEmbedderStub{}
	service := modelruntime.NewMultimodalEmbeddingService(
		embedder,
		"http://127.0.0.1:8000/v1",
		"Qwen3-VL-Embedding-2B",
		"local-secret",
	)
	response, err := service.EmbedImage(context.Background(), "image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("EmbedImage() error = %v", err)
	}
	if len(response.Data) != 1 || len(response.Data[0].Vector) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if embedder.baseURL != "http://127.0.0.1:8000/v1" || embedder.apiKey != "local-secret" || embedder.request.Model != "Qwen3-VL-Embedding-2B" {
		t.Fatalf("request = %#v, baseURL=%q, apiKey=%q", embedder.request, embedder.baseURL, embedder.apiKey)
	}
	if !strings.HasPrefix(embedder.request.Input[0].Image, "data:image/png;base64,") {
		t.Fatalf("image input = %q", embedder.request.Input[0].Image)
	}
}

func TestKnowledgeBaseEmbeddingServiceRoutesMultimodalText(t *testing.T) {
	standard := &standardEmbeddingRunnerStub{}
	multimodal := modelruntime.NewMultimodalEmbeddingService(&multimodalEmbedderStub{}, "http://127.0.0.1:8000/v1", "qwen3-vl-embedding", "secret")
	router := modelruntime.NewKnowledgeBaseEmbeddingService(profileStoreStub{profile: knowledgebase.EmbeddingProfile{Mode: knowledgebase.EmbeddingModeMultimodal}}, standard, multimodal)

	response, err := router.EmbedForKnowledgeBase(context.Background(), 7, []string{"架构图"})
	if err != nil {
		t.Fatalf("EmbedForKnowledgeBase() error = %v", err)
	}
	if len(response.Data) != 1 || standard.calls != 0 {
		t.Fatalf("response=%#v standard calls=%d", response, standard.calls)
	}
}

func TestKnowledgeBaseEmbeddingServiceRoutesStandardText(t *testing.T) {
	standard := &standardEmbeddingRunnerStub{}
	router := modelruntime.NewKnowledgeBaseEmbeddingService(profileStoreStub{profile: knowledgebase.DefaultEmbeddingProfile()}, standard, nil)

	if _, err := router.EmbedForKnowledgeBase(context.Background(), 7, []string{"普通正文"}); err != nil {
		t.Fatalf("EmbedForKnowledgeBase() error = %v", err)
	}
	if standard.calls != 1 {
		t.Fatalf("standard calls = %d, want 1", standard.calls)
	}
}
