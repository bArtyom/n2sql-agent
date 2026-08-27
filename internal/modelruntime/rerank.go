package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/ops"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var ErrRerankNotConfigured = errors.New("rerank model is not configured")

// RerankService adapts the provider-specific HTTP API to the retrieval layer.
// It only receives the small candidate set produced by vector/keyword recall.
type RerankService struct {
	providers    modelprovider.Store
	client       modelclient.Reranker
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
	breaker      *ops.CircuitBreaker
}

func NewRerankService(providers modelprovider.Store, client modelclient.Reranker, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, breakerConfig ...CircuitBreakerConfig) *RerankService {
	return &RerankService{providers: providers, client: client, apiKeyEnvVar: apiKeyEnvVar, lookupAPIKey: lookupAPIKey, breaker: newCircuitBreaker(breakerConfig)}
}

func (s *RerankService) Rerank(ctx context.Context, query string, retrievalCandidates []retrieval.Result, topN int) ([]retrieval.Result, error) {
	if strings.TrimSpace(query) == "" || len(retrievalCandidates) == 0 || topN <= 0 || topN > len(retrievalCandidates) {
		return nil, fmt.Errorf("invalid rerank request")
	}
	current, err := s.providers.Current(ctx)
	if err != nil {
		return nil, err
	}
	providers := []modelprovider.Provider{current}
	if enabledStore, ok := s.providers.(modelprovider.EnabledStore); ok {
		if enabled, listErr := enabledStore.Enabled(ctx); listErr == nil && len(enabled) > 0 {
			providers = enabled
		}
	}
	providersWithRerank := make([]modelprovider.Provider, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if _, exists := seen[provider.Name]; exists {
			continue
		}
		seen[provider.Name] = struct{}{}
		if provider.APIKeyEnvVar == s.apiKeyEnvVar && strings.TrimSpace(provider.RerankBaseURL) != "" && strings.TrimSpace(provider.RerankModel) != "" {
			providersWithRerank = append(providersWithRerank, provider)
		}
	}
	if len(providersWithRerank) == 0 {
		// Rerank is an optional second pass. A provider without rerank settings
		// keeps the hybrid recall order instead of breaking normal search.
		if strings.TrimSpace(current.RerankBaseURL) != "" && current.APIKeyEnvVar != s.apiKeyEnvVar {
			return nil, ErrAPIKeyEnvironmentMismatch
		}
		return append([]retrieval.Result(nil), retrievalCandidates[:topN]...), nil
	}
	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || strings.TrimSpace(apiKey) == "" {
		return nil, ErrAPIKeyNotConfigured
	}
	documents := make([]string, len(retrievalCandidates))
	for index, candidate := range retrievalCandidates {
		documents[index] = candidate.Content
	}
	var lastErr error
	var response modelclient.RerankResponse
	for index, provider := range providersWithRerank {
		if !s.breaker.Allow(provider.Name, capabilityRerank) {
			observeCircuitBreaker(ctx, provider.Name, capabilityRerank, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", provider.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		response, err = s.client.Rerank(ctx, provider.RerankBaseURL, apiKey, modelclient.RerankRequest{
			Model: provider.RerankModel, Query: query, Documents: documents, TopN: topN,
		})
		observeModelCall(ctx, usage.ModelKindRerank, provider.Name, provider.RerankModel, started, response.Usage, err)
		if err == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, provider.Name, capabilityRerank, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(provider.Name, capabilityRerank)
			lastErr = nil
			break
		}
		lastErr = err
		if !ops.IsRetryableFailure(err) {
			break
		}
		s.breaker.RecordFailure(provider.Name, capabilityRerank)
	}
	if lastErr != nil && response.Results == nil {
		return nil, fmt.Errorf("rerank candidates: %w", lastErr)
	}
	if observer := usage.ObserverFromContext(ctx); observer != nil && response.Usage != nil {
		if _, alreadyObserved := observer.(usage.CallObserver); !alreadyObserved {
			observer.ObserveChatTokens(*response.Usage)
		}
	}
	results := make([]retrieval.Result, 0, len(response.Results))
	for _, ranked := range response.Results {
		candidate := retrievalCandidates[ranked.Index]
		candidate.RerankScore = ranked.RelevanceScore
		results = append(results, candidate)
	}
	return results, nil
}
