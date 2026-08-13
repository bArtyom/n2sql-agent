package retrieval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var (
	ErrInvalidKnowledgeBase      = errors.New("invalid knowledge base ID")
	ErrInvalidQuery              = errors.New("invalid search query")
	ErrInvalidLimit              = errors.New("invalid search result limit")
	ErrInvalidMaxDistance        = errors.New("search distance threshold must be greater than 0 and at most 1")
	ErrInvalidDocumentIDs        = errors.New("invalid document filter")
	ErrDocumentFilterUnavailable = errors.New("document filter is unavailable")
	ErrQueryRewriteUnavailable   = errors.New("query rewrite is unavailable")
)

const (
	DefaultResults       = 5
	MaxResults           = 20
	MaxDocumentIDs       = 100
	MaxQueryVariants     = 2
	MaxConcurrentQueries = MaxQueryVariants + 1
	DefaultMaxDistance   = 0.65
)

type Result = documentchunk.SearchResult

type Embedder interface {
	Embed(context.Context, []string) (modelclient.EmbeddingResponse, error)
}

type ChunkSearcher interface {
	Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error)
}

type Searcher interface {
	Search(context.Context, int64, string, int) ([]Result, error)
}

type KeywordSearcher interface {
	SearchKeyword(context.Context, int64, string, int) ([]Result, error)
}

type FilteredChunkSearcher interface {
	SearchWithDocuments(context.Context, int64, []float32, int, []int64) ([]documentchunk.SearchResult, error)
}

type FilteredKeywordSearcher interface {
	SearchKeywordWithDocuments(context.Context, int64, string, int, []int64) ([]Result, error)
}

type SearchOptions struct {
	DocumentIDs  []int64
	QueryRewrite bool
}

type FilteredSearcher interface {
	SearchWithOptions(context.Context, int64, string, int, SearchOptions) ([]Result, error)
}

// Reranker re-scores a small candidate set after the cheap recall stages.
type Reranker interface {
	Rerank(context.Context, string, []Result, int) ([]Result, error)
}

// QueryRewriter turns one user question into a small set of alternative
// search queries. The original question is always searched as well.
type QueryRewriter interface {
	Rewrite(context.Context, string, int) ([]string, error)
}

func ValidateMaxDistance(maxDistance float64) error {
	if maxDistance <= 0 || maxDistance > 1 {
		return ErrInvalidMaxDistance
	}
	return nil
}

func NormalizeDocumentIDs(documentIDs []int64) ([]int64, error) {
	if len(documentIDs) > MaxDocumentIDs {
		return nil, ErrInvalidDocumentIDs
	}
	unique := make(map[int64]struct{}, len(documentIDs))
	normalized := make([]int64, 0, len(documentIDs))
	for _, documentID := range documentIDs {
		if documentID <= 0 {
			return nil, ErrInvalidDocumentIDs
		}
		if _, exists := unique[documentID]; exists {
			continue
		}
		unique[documentID] = struct{}{}
		normalized = append(normalized, documentID)
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left] < normalized[right] })
	return normalized, nil
}

func (s *Service) SearchWithOptions(ctx context.Context, knowledgeBaseID int64, query string, limit int, options SearchOptions) ([]Result, error) {
	return s.searchWithOptions(ctx, knowledgeBaseID, query, limit, options)
}

// FilterByMaxDistance keeps only results close enough to the query. pgvector
// cosine distance is lower for more similar vectors, so this is an upper bound.
func FilterByMaxDistance(results []Result, maxDistance float64) ([]Result, error) {
	if maxDistance == 0 {
		maxDistance = DefaultMaxDistance
	}
	if maxDistance <= 0 || maxDistance > 1 {
		return nil, ErrInvalidMaxDistance
	}
	filtered := make([]Result, 0, len(results))
	for _, result := range results {
		// Keyword-only results use KeywordScore, not a pgvector distance. A
		// zero distance there means "not a vector result", so do not discard
		// an exact lexical hit because of the semantic threshold.
		if result.MatchType == "keyword" {
			filtered = append(filtered, result)
			continue
		}
		if result.Distance <= maxDistance {
			filtered = append(filtered, result)
		}
	}
	return filtered, nil
}

type Service struct {
	embedder Embedder
	chunks   ChunkSearcher
	keyword  KeywordSearcher
	reranker Reranker
	rewriter QueryRewriter
}

func NewService(embedder Embedder, chunks ChunkSearcher) *Service {
	return &Service{embedder: embedder, chunks: chunks}
}

func NewHybridService(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher) *Service {
	return &Service{embedder: embedder, chunks: chunks, keyword: keyword}
}

func NewHybridServiceWithReranker(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker) *Service {
	return &Service{embedder: embedder, chunks: chunks, keyword: keyword, reranker: reranker}
}

func NewHybridServiceWithRerankerAndRewriter(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker, rewriter QueryRewriter) *Service {
	return &Service{embedder: embedder, chunks: chunks, keyword: keyword, reranker: reranker, rewriter: rewriter}
}

func (s *Service) Search(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]Result, error) {
	return s.searchWithOptions(ctx, knowledgeBaseID, query, limit, SearchOptions{})
}

func (s *Service) searchWithOptions(ctx context.Context, knowledgeBaseID int64, query string, limit int, options SearchOptions) ([]Result, error) {
	if ctx == nil {
		return nil, errors.New("retrieval context is required")
	}
	if knowledgeBaseID <= 0 {
		return nil, ErrInvalidKnowledgeBase
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, ErrInvalidQuery
	}
	if limit <= 0 || limit > MaxResults {
		return nil, ErrInvalidLimit
	}
	documentIDs, err := NormalizeDocumentIDs(options.DocumentIDs)
	if err != nil {
		return nil, err
	}
	queries := []string{query}
	if options.QueryRewrite {
		if s.rewriter == nil {
			usage.ObserveQueryRewrite(ctx, usage.QueryRewriteObservation{Enabled: true, Fallback: true})
		} else {
			variants, rewriteErr := s.rewriter.Rewrite(ctx, query, MaxQueryVariants)
			if rewriteErr != nil {
				if errors.Is(rewriteErr, context.Canceled) || errors.Is(rewriteErr, context.DeadlineExceeded) {
					return nil, fmt.Errorf("rewrite search query: %w", rewriteErr)
				}
				usage.ObserveQueryRewrite(ctx, usage.QueryRewriteObservation{Enabled: true, Fallback: true})
			} else {
				variants = normalizeQueryVariants(query, variants)
				if len(variants) == 0 {
					usage.ObserveQueryRewrite(ctx, usage.QueryRewriteObservation{Enabled: true, Fallback: true})
				} else {
					usage.ObserveQueryRewrite(ctx, usage.QueryRewriteObservation{Enabled: true, Applied: true, VariantCount: len(variants)})
					queries = append(queries, variants...)
				}
			}
		}
	}
	candidateLimit := limit
	if s.reranker != nil && candidateLimit < MaxResults {
		candidateLimit *= 3
		if candidateLimit > MaxResults {
			candidateLimit = MaxResults
		}
	}
	queryResults := make([]querySearchResult, len(queries))
	queryContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var waitGroup sync.WaitGroup
	var firstErr error
	var firstErrOnce sync.Once
	for index, searchQuery := range queries {
		index, searchQuery := index, searchQuery
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			results, searchErr := s.searchOneQuery(queryContext, knowledgeBaseID, searchQuery, candidateLimit, documentIDs)
			if searchErr != nil {
				firstErrOnce.Do(func() {
					firstErr = searchErr
					cancel()
				})
				return
			}
			queryResults[index] = results
		}()
	}
	waitGroup.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	var merged []Result
	for _, queryResult := range queryResults {
		if queryResult.usage != nil {
			if observer := usage.ObserverFromContext(ctx); observer != nil {
				observer.ObserveEmbeddingTokens(*queryResult.usage)
			}
		}
		merged = mergeCandidateResults(merged, queryResult.results, MaxResults)
	}
	if s.reranker == nil {
		return merged[:min(len(merged), limit)], nil
	}
	if len(merged) == 0 {
		return nil, nil
	}
	rankLimit := min(len(merged), limit)
	ranked, err := s.reranker.Rerank(ctx, query, merged, rankLimit)
	if err != nil {
		return nil, fmt.Errorf("rerank search results: %w", err)
	}
	if len(ranked) > rankLimit {
		return ranked[:rankLimit], nil
	}
	return ranked, nil
}

type querySearchResult struct {
	results []Result
	usage   *modelclient.TokenUsage
}

func (s *Service) searchOneQuery(ctx context.Context, knowledgeBaseID int64, searchQuery string, candidateLimit int, documentIDs []int64) (querySearchResult, error) {
	response, embedErr := s.embedder.Embed(ctx, []string{searchQuery})
	if embedErr != nil {
		return querySearchResult{}, fmt.Errorf("embed search query: %w", embedErr)
	}
	if len(response.Data) != 1 || len(response.Data[0].Vector) == 0 {
		return querySearchResult{}, errors.New("embedding response does not contain one non-empty query vector")
	}
	var vectorResults []Result
	var err error
	if len(documentIDs) == 0 {
		vectorResults, err = s.chunks.Search(ctx, knowledgeBaseID, response.Data[0].Vector, candidateLimit)
	} else {
		filtered, ok := s.chunks.(FilteredChunkSearcher)
		if !ok {
			return querySearchResult{}, ErrDocumentFilterUnavailable
		}
		vectorResults, err = filtered.SearchWithDocuments(ctx, knowledgeBaseID, response.Data[0].Vector, candidateLimit, documentIDs)
	}
	if err != nil {
		return querySearchResult{}, fmt.Errorf("search document chunks: %w", err)
	}
	queryResults := vectorResults
	if s.keyword != nil {
		var keywordResults []Result
		if len(documentIDs) == 0 {
			keywordResults, err = s.keyword.SearchKeyword(ctx, knowledgeBaseID, searchQuery, candidateLimit)
		} else {
			filtered, ok := s.keyword.(FilteredKeywordSearcher)
			if !ok {
				return querySearchResult{}, ErrDocumentFilterUnavailable
			}
			keywordResults, err = filtered.SearchKeywordWithDocuments(ctx, knowledgeBaseID, searchQuery, candidateLimit, documentIDs)
		}
		if err != nil {
			return querySearchResult{}, fmt.Errorf("search keyword document chunks: %w", err)
		}
		queryResults = mergeResults(vectorResults, keywordResults, candidateLimit)
	}
	return querySearchResult{results: queryResults, usage: response.Usage}, nil
}

func normalizeQueryVariants(original string, variants []string) []string {
	seen := map[string]struct{}{strings.ToLower(strings.Join(strings.Fields(original), " ")): {}}
	result := make([]string, 0, MaxQueryVariants)
	for _, variant := range variants {
		variant = strings.TrimSpace(variant)
		if variant == "" {
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(variant), " "))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, variant)
		if len(result) == MaxQueryVariants {
			break
		}
	}
	return result
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func mergeResults(vectorResults, keywordResults []Result, limit int) []Result {
	merged := make([]Result, 0, limit)
	seen := make(map[string]struct{}, limit)
	add := func(result Result, matchType string) {
		key := fmt.Sprintf("%d:%d", result.DocumentID, result.Position)
		if _, ok := seen[key]; ok {
			for index := range merged {
				if fmt.Sprintf("%d:%d", merged[index].DocumentID, merged[index].Position) == key {
					merged[index].MatchType = "hybrid"
					break
				}
			}
			return
		}
		result.MatchType = matchType
		seen[key] = struct{}{}
		merged = append(merged, result)
	}
	for index := 0; len(merged) < limit && (index < len(vectorResults) || index < len(keywordResults)); index++ {
		if index < len(vectorResults) {
			add(vectorResults[index], "vector")
		}
		if len(merged) >= limit {
			break
		}
		if index < len(keywordResults) {
			add(keywordResults[index], "keyword")
		}
	}
	return merged
}

func mergeCandidateResults(existing, incoming []Result, limit int) []Result {
	merged := append([]Result(nil), existing...)
	seen := make(map[string]int, len(merged))
	for index, result := range merged {
		seen[resultKey(result)] = index
	}
	for _, result := range incoming {
		key := resultKey(result)
		if index, exists := seen[key]; exists {
			if merged[index].MatchType != result.MatchType {
				merged[index].MatchType = "hybrid"
			}
			continue
		}
		if len(merged) >= limit {
			break
		}
		seen[key] = len(merged)
		merged = append(merged, result)
	}
	return merged
}

func resultKey(result Result) string {
	return fmt.Sprintf("%d:%d", result.DocumentID, result.Position)
}
