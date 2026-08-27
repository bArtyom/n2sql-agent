package modelruntime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/ops"
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

type MultimodalEmbeddingRunner interface {
	Embed(context.Context, []string) (modelclient.EmbeddingResponse, error)
	EmbedImage(context.Context, string, []byte) (modelclient.EmbeddingResponse, error)
}

type MultimodalEmbeddingService struct {
	embedder modelclient.MultimodalEmbedder
	baseURL  string
	model    string
	apiKey   string
}

func NewMultimodalEmbeddingService(embedder modelclient.MultimodalEmbedder, baseURL, model, apiKey string) *MultimodalEmbeddingService {
	return &MultimodalEmbeddingService{embedder: embedder, baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), model: strings.TrimSpace(model), apiKey: strings.TrimSpace(apiKey)}
}

func (s *MultimodalEmbeddingService) ModelName() string {
	if s == nil {
		return ""
	}
	return s.model
}

func (s *MultimodalEmbeddingService) Embed(ctx context.Context, input []string) (modelclient.EmbeddingResponse, error) {
	if s == nil || s.embedder == nil || s.baseURL == "" || s.model == "" {
		return modelclient.EmbeddingResponse{}, errors.New("multimodal embedding service is not configured")
	}
	items := make([]modelclient.MultimodalEmbeddingInput, len(input))
	for index, text := range input {
		items[index] = modelclient.MultimodalEmbeddingInput{Text: text}
	}
	return s.call(ctx, items)
}

func (s *MultimodalEmbeddingService) EmbedImage(ctx context.Context, mimeType string, data []byte) (modelclient.EmbeddingResponse, error) {
	return s.EmbedImageWithModel(ctx, s.model, mimeType, data)
}

func (s *MultimodalEmbeddingService) EmbedTextWithModel(ctx context.Context, model string, input []string) (modelclient.EmbeddingResponse, error) {
	items := make([]modelclient.MultimodalEmbeddingInput, len(input))
	for index, text := range input {
		items[index] = modelclient.MultimodalEmbeddingInput{Text: text}
	}
	return s.callWithModel(ctx, model, items)
}

func (s *MultimodalEmbeddingService) EmbedImageWithModel(ctx context.Context, model, mimeType string, data []byte) (modelclient.EmbeddingResponse, error) {
	if len(data) == 0 {
		return modelclient.EmbeddingResponse{}, errors.New("image data is empty")
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	image := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	return s.callWithModel(ctx, model, []modelclient.MultimodalEmbeddingInput{{Image: image}})
}

func (s *MultimodalEmbeddingService) call(ctx context.Context, input []modelclient.MultimodalEmbeddingInput) (modelclient.EmbeddingResponse, error) {
	return s.callWithModel(ctx, s.model, input)
}

func (s *MultimodalEmbeddingService) callWithModel(ctx context.Context, model string, input []modelclient.MultimodalEmbeddingInput) (modelclient.EmbeddingResponse, error) {
	if s == nil || s.embedder == nil || s.baseURL == "" || strings.TrimSpace(model) == "" {
		return modelclient.EmbeddingResponse{}, errors.New("multimodal embedding service is not configured")
	}
	started := time.Now()
	response, err := s.embedder.EmbedMultimodal(ctx, s.baseURL, s.apiKey, modelclient.MultimodalEmbeddingRequest{Model: model, Input: input})
	observeModelCall(ctx, usage.ModelKindEmbedding, "multimodal", model, started, response.Usage, err)
	if err != nil {
		return modelclient.EmbeddingResponse{}, &EmbeddingCallError{Err: fmt.Errorf("multimodal embed input: %w", err)}
	}
	return response, nil
}

type EmbeddingService struct {
	providers    modelprovider.Store
	embedder     modelclient.Embedder
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
	localBaseURL string
	localModel   string
	localAPIKey  string
	breaker      *ops.CircuitBreaker
}

func NewEmbeddingService(providers modelprovider.Store, embedder modelclient.Embedder, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, breakerConfig ...CircuitBreakerConfig) *EmbeddingService {
	return &EmbeddingService{
		providers:    providers,
		embedder:     embedder,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
		breaker:      newCircuitBreaker(breakerConfig),
	}
}

// NewEmbeddingServiceWithLocalFallback keeps chat on the configured provider
// while optionally routing embeddings to a local OpenAI-compatible server.
func NewEmbeddingServiceWithLocalFallback(providers modelprovider.Store, embedder modelclient.Embedder, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, localBaseURL, localModel, localAPIKey string, breakerConfig ...CircuitBreakerConfig) *EmbeddingService {
	service := NewEmbeddingService(providers, embedder, apiKeyEnvVar, lookupAPIKey, breakerConfig...)
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
	current, err := s.providers.Current(ctx)
	if err != nil {
		return modelclient.EmbeddingResponse{}, err
	}

	providers := []modelprovider.Provider{current}
	if enabledStore, ok := s.providers.(modelprovider.EnabledStore); ok {
		if enabled, listErr := enabledStore.Enabled(ctx); listErr == nil && len(enabled) > 0 {
			providers = enabled
		}
	}
	filtered := make([]modelprovider.Provider, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, candidate := range providers {
		if candidate.APIKeyEnvVar != s.apiKeyEnvVar {
			continue
		}
		if _, exists := seen[candidate.Name]; exists {
			continue
		}
		seen[candidate.Name] = struct{}{}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return modelclient.EmbeddingResponse{}, ErrAPIKeyEnvironmentMismatch
	}
	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return modelclient.EmbeddingResponse{}, ErrAPIKeyNotConfigured
	}

	var lastErr error
	for index, candidate := range filtered {
		if !s.breaker.Allow(candidate.Name, capabilityEmbedding) {
			observeCircuitBreaker(ctx, candidate.Name, capabilityEmbedding, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", candidate.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		response, callErr := s.embedder.Embed(ctx, candidate.BaseURL, apiKey, modelclient.EmbeddingRequest{
			Model: candidate.EmbeddingModel,
			Input: input,
		})
		observeModelCall(ctx, usage.ModelKindEmbedding, candidate.Name, candidate.EmbeddingModel, started, response.Usage, callErr)
		if callErr == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, candidate.Name, capabilityEmbedding, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(candidate.Name, capabilityEmbedding)
			return response, nil
		}
		lastErr = callErr
		if !ops.IsRetryableFailure(callErr) {
			break
		}
		s.breaker.RecordFailure(candidate.Name, capabilityEmbedding)
	}
	if lastErr == nil {
		lastErr = modelprovider.ErrNotFound
	}
	return modelclient.EmbeddingResponse{}, &EmbeddingCallError{Err: fmt.Errorf("embed input: %w", lastErr)}
}
