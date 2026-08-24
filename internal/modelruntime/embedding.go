package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var (
	ErrAPIKeyEnvironmentMismatch = errors.New("model provider API key environment variable does not match server configuration")
	ErrAPIKeyNotConfigured       = errors.New("model provider API key environment variable is not set")
)

type APIKeyLookup func(string) (string, bool)

type EmbeddingCallError struct {
	Err error
}

func (e *EmbeddingCallError) Error() string { return e.Err.Error() }

func (e *EmbeddingCallError) Unwrap() error { return e.Err }

type EmbeddingRunner interface {
	Embed(context.Context, []string) (modelclient.EmbeddingResponse, error)
}

type EmbeddingService struct {
	providers    modelprovider.Store
	embedder     modelclient.Embedder
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
	localBaseURL string
	localModel   string
	localAPIKey  string
}

func NewEmbeddingService(providers modelprovider.Store, embedder modelclient.Embedder, apiKeyEnvVar string, lookupAPIKey APIKeyLookup) *EmbeddingService {
	return &EmbeddingService{
		providers:    providers,
		embedder:     embedder,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
	}
}

// NewEmbeddingServiceWithLocalFallback keeps chat on the configured provider
// while optionally routing embeddings to a local OpenAI-compatible server.
func NewEmbeddingServiceWithLocalFallback(providers modelprovider.Store, embedder modelclient.Embedder, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, localBaseURL, localModel, localAPIKey string) *EmbeddingService {
	service := NewEmbeddingService(providers, embedder, apiKeyEnvVar, lookupAPIKey)
	service.localBaseURL = strings.TrimRight(strings.TrimSpace(localBaseURL), "/")
	service.localModel = strings.TrimSpace(localModel)
	service.localAPIKey = strings.TrimSpace(localAPIKey)
	if service.localAPIKey == "" {
		service.localAPIKey = "ollama"
	}
	return service
}

func (s *EmbeddingService) Embed(ctx context.Context, input []string) (modelclient.EmbeddingResponse, error) {
	if s.localBaseURL != "" && s.localModel != "" {
		started := time.Now()
		response, err := s.embedder.Embed(ctx, s.localBaseURL, s.localAPIKey, modelclient.EmbeddingRequest{Model: s.localModel, Input: input})
		observeModelCall(ctx, usage.ModelKindEmbedding, "local", s.localModel, started, response.Usage, err)
		if err != nil {
			return modelclient.EmbeddingResponse{}, &EmbeddingCallError{Err: fmt.Errorf("embed with local model: %w", err)}
		}
		return response, nil
	}
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return modelclient.EmbeddingResponse{}, err
	}
	if provider.APIKeyEnvVar != s.apiKeyEnvVar {
		return modelclient.EmbeddingResponse{}, ErrAPIKeyEnvironmentMismatch
	}

	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return modelclient.EmbeddingResponse{}, ErrAPIKeyNotConfigured
	}

	started := time.Now()
	response, err := s.embedder.Embed(ctx, provider.BaseURL, apiKey, modelclient.EmbeddingRequest{
		Model: provider.EmbeddingModel,
		Input: input,
	})
	observeModelCall(ctx, usage.ModelKindEmbedding, provider.Name, provider.EmbeddingModel, started, response.Usage, err)
	if err != nil {
		return modelclient.EmbeddingResponse{}, &EmbeddingCallError{Err: fmt.Errorf("embed input: %w", err)}
	}
	return response, nil
}
