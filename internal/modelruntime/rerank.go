package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
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
}

func NewRerankService(providers modelprovider.Store, client modelclient.Reranker, apiKeyEnvVar string, lookupAPIKey APIKeyLookup) *RerankService {
	return &RerankService{providers: providers, client: client, apiKeyEnvVar: apiKeyEnvVar, lookupAPIKey: lookupAPIKey}
}

func (s *RerankService) Rerank(ctx context.Context, query string, candidates []retrieval.Result, topN int) ([]retrieval.Result, error) {
	if strings.TrimSpace(query) == "" || len(candidates) == 0 || topN <= 0 || topN > len(candidates) {
		return nil, fmt.Errorf("invalid rerank request")
	}
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(provider.RerankBaseURL) == "" || strings.TrimSpace(provider.RerankModel) == "" {
		// Rerank is an optional second pass. A provider without rerank settings
		// keeps the hybrid recall order instead of breaking normal search.
		return append([]retrieval.Result(nil), candidates[:topN]...), nil
	}
	if provider.APIKeyEnvVar != s.apiKeyEnvVar {
		return nil, ErrAPIKeyEnvironmentMismatch
	}
	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || strings.TrimSpace(apiKey) == "" {
		return nil, ErrAPIKeyNotConfigured
	}
	documents := make([]string, len(candidates))
	for index, candidate := range candidates {
		documents[index] = candidate.Content
	}
	started := time.Now()
	response, err := s.client.Rerank(ctx, provider.RerankBaseURL, apiKey, modelclient.RerankRequest{
		Model:     provider.RerankModel,
		Query:     query,
		Documents: documents,
		TopN:      topN,
	})
	observeModelCall(ctx, usage.ModelKindRerank, provider.Name, provider.RerankModel, started, response.Usage, err)
	if err != nil {
		return nil, fmt.Errorf("rerank candidates: %w", err)
	}
	if observer := usage.ObserverFromContext(ctx); observer != nil && response.Usage != nil {
		if _, alreadyObserved := observer.(usage.CallObserver); !alreadyObserved {
			observer.ObserveChatTokens(*response.Usage)
		}
	}
	results := make([]retrieval.Result, 0, len(response.Results))
	for _, ranked := range response.Results {
		candidate := candidates[ranked.Index]
		candidate.RerankScore = ranked.RelevanceScore
		results = append(results, candidate)
	}
	return results, nil
}
