package modelruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
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
}

func NewEmbeddingService(providers modelprovider.Store, embedder modelclient.Embedder, apiKeyEnvVar string, lookupAPIKey APIKeyLookup) *EmbeddingService {
	return &EmbeddingService{
		providers:    providers,
		embedder:     embedder,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
	}
}

func (s *EmbeddingService) Embed(ctx context.Context, input []string) (modelclient.EmbeddingResponse, error) {
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

	response, err := s.embedder.Embed(ctx, provider.BaseURL, apiKey, modelclient.EmbeddingRequest{
		Model: provider.EmbeddingModel,
		Input: input,
	})
	if err != nil {
		return modelclient.EmbeddingResponse{}, &EmbeddingCallError{Err: fmt.Errorf("embed input: %w", err)}
	}
	return response, nil
}
