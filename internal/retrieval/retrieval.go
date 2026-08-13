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
	ErrInvalidKeywordThreshold   = errors.New("keyword score threshold must be between 0 and 1")
	ErrInvalidDocumentIDs        = errors.New("invalid document filter")
	ErrDocumentFilterUnavailable = errors.New("document filter is unavailable")
	ErrQueryRewriteUnavailable   = errors.New("query rewrite is unavailable")
)

const (
	DefaultResults          = 5
	MaxResults              = 20
	MaxDocumentIDs          = 100
	MaxQueryVariants        = 2
	MaxConcurrentQueries    = MaxQueryVariants + 1
	DefaultMaxDistance      = 0.65
	DefaultKeywordThreshold = 0.10
	rrfConstant             = 60
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

// NeighborSearcher is an optional store capability. Existing custom stores
// continue to work; PostgreSQL stores can additionally provide nearby chunks.
type NeighborSearcher interface {
	SearchNeighbors(context.Context, int64, int64, int, int, int) ([]Result, error)
}

type ParentSearcher interface {
	ParentForChunk(context.Context, int64, int64, int) (documentchunk.ParentChunk, bool, error)
}

// BatchParentSearcher is an optional store capability. It avoids one SQL
// query per hit when a retrieval response contains several child chunks.
type BatchParentSearcher interface {
	ParentsForChunks(context.Context, int64, []documentchunk.ChunkReference) (map[documentchunk.ChunkReference]documentchunk.ParentChunk, error)
}

const (
	DefaultContextBefore = 1
	DefaultContextAfter  = 1
)

type SearchOptions struct {
	DocumentIDs      []int64
	QueryRewrite     bool
	KeywordThreshold float64
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

func ValidateKeywordThreshold(threshold float64) error {
	if threshold < 0 || threshold > 1 {
		return ErrInvalidKeywordThreshold
	}
	return nil
}

func effectiveKeywordThreshold(threshold float64) float64 {
	if threshold == 0 {
		return DefaultKeywordThreshold
	}
	return threshold
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
	return FilterByMaxDistanceWithStats(context.Background(), results, maxDistance)
}

// FilterByMaxDistanceWithStats applies the final semantic-distance boundary
// and reports how many results survived it. Keyword-only results have no
// vector distance and are kept here; their keyword threshold is applied in
// the recall stage.
func FilterByMaxDistanceWithStats(ctx context.Context, results []Result, maxDistance float64) ([]Result, error) {
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
	usage.ObserveRetrieval(ctx, usage.RetrievalObservation{FinalFiltered: len(filtered)})
	return filtered, nil
}

type Service struct {
	embedder Embedder
	chunks   ChunkSearcher
	keyword  KeywordSearcher
	reranker Reranker
	rewriter QueryRewriter
	cache    *resultCache
}

func NewService(embedder Embedder, chunks ChunkSearcher) *Service {
	return newService(embedder, chunks, nil, nil, nil, DefaultCacheConfig())
}

func NewHybridService(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher) *Service {
	return newService(embedder, chunks, keyword, nil, nil, DefaultCacheConfig())
}

func NewHybridServiceWithReranker(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker) *Service {
	return newService(embedder, chunks, keyword, reranker, nil, DefaultCacheConfig())
}

func NewHybridServiceWithRerankerAndRewriter(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker, rewriter QueryRewriter) *Service {
	return NewHybridServiceWithRerankerAndRewriterAndCache(embedder, chunks, keyword, reranker, rewriter, DefaultCacheConfig())
}

func NewHybridServiceWithRerankerAndRewriterAndCache(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker, rewriter QueryRewriter, cacheConfig CacheConfig) *Service {
	return newService(embedder, chunks, keyword, reranker, rewriter, cacheConfig)
}

func newService(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher, reranker Reranker, rewriter QueryRewriter, cacheConfig CacheConfig) *Service {
	return &Service{
		embedder: embedder,
		chunks:   chunks,
		keyword:  keyword,
		reranker: reranker,
		rewriter: rewriter,
		cache:    newResultCache(cacheConfig),
	}
}

// ClearCache removes cached results for one knowledge base. Call it after a
// document is successfully re-indexed so the next question sees new chunks.
func (s *Service) ClearCache(knowledgeBaseID int64) {
	if s.cache != nil {
		s.cache.clear(knowledgeBaseID)
	}
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
	options.DocumentIDs = documentIDs
	if err := ValidateKeywordThreshold(options.KeywordThreshold); err != nil {
		return nil, err
	}
	options.KeywordThreshold = effectiveKeywordThreshold(options.KeywordThreshold)
	key := makeResultCacheKey(knowledgeBaseID, query, limit, documentIDs, options.QueryRewrite, options.KeywordThreshold)
	if s.cache != nil {
		if value, ok := s.cache.get(key); ok {
			usage.ObserveQueryRewrite(ctx, value.rewriteState)
			usage.ObserveRetrieval(ctx, value.observation)
			return value.results, nil
		}
		flight, owner := s.cache.begin(key)
		if !owner {
			value, waitErr := s.cache.wait(ctx, flight)
			if waitErr != nil {
				return nil, waitErr
			}
			usage.ObserveQueryRewrite(ctx, value.rewriteState)
			usage.ObserveRetrieval(ctx, value.observation)
			return value.results, nil
		}
		// The first get and begin are separate operations. Recheck after
		// claiming the flight so a request that raced with a just-finished
		// loader does not repeat the provider calls unnecessarily.
		if value, ok := s.cache.get(key); ok {
			s.cache.finish(key, flight, value, nil)
			usage.ObserveQueryRewrite(ctx, value.rewriteState)
			usage.ObserveRetrieval(ctx, value.observation)
			return value.results, nil
		}
		value, searchErr := s.searchUncached(ctx, knowledgeBaseID, query, limit, options, documentIDs)
		if searchErr == nil {
			value.results, searchErr = s.expandContext(ctx, knowledgeBaseID, value.results)
		}
		s.cache.finish(key, flight, value, searchErr)
		if searchErr != nil {
			return nil, searchErr
		}
		usage.ObserveRetrieval(ctx, value.observation)
		return value.results, nil
	}
	value, err := s.searchUncached(ctx, knowledgeBaseID, query, limit, options, documentIDs)
	if err != nil {
		return nil, err
	}
	value.results, err = s.expandContext(ctx, knowledgeBaseID, value.results)
	if err != nil {
		return nil, err
	}
	usage.ObserveRetrieval(ctx, value.observation)
	return value.results, nil
}

// ContextContent formats one hit and its nearby chunks for a model prompt.
// Ranking fields stay on the original result; only the prompt text expands.
func ContextContent(result Result) string {
	if result.ParentContent != "" {
		return "[父块上下文]\n" + result.ParentContent + "\n\n[命中片段]\n" + result.Content
	}
	if len(result.ContextBefore) == 0 && len(result.ContextAfter) == 0 {
		return result.Content
	}
	var builder strings.Builder
	for _, chunk := range result.ContextBefore {
		builder.WriteString("[前置上下文]\n")
		builder.WriteString(chunk.Content)
		builder.WriteString("\n")
	}
	builder.WriteString("[命中片段]\n")
	builder.WriteString(result.Content)
	for _, chunk := range result.ContextAfter {
		builder.WriteString("\n[后置上下文]\n")
		builder.WriteString(chunk.Content)
	}
	return builder.String()
}

// ResultForPrompt keeps citation metadata while putting the nearby text into
// the content field that a model already understands.
func ResultForPrompt(result Result) Result {
	result.Content = ContextContent(result)
	result.ContextBefore = nil
	result.ContextAfter = nil
	result.ParentContent = ""
	result.ParentPosition = 0
	return result
}

func (s *Service) expandContext(ctx context.Context, knowledgeBaseID int64, results []Result) ([]Result, error) {
	parentSearcher, hasParentSearch := s.chunks.(ParentSearcher)
	batchParentSearcher, hasBatchParentSearch := s.chunks.(BatchParentSearcher)
	neighborSearcher, hasNeighborSearch := s.chunks.(NeighborSearcher)
	if (!hasParentSearch && !hasBatchParentSearch && !hasNeighborSearch) || len(results) == 0 {
		return results, nil
	}
	expanded := append([]Result(nil), results...)
	parents := make(map[documentchunk.ChunkReference]documentchunk.ParentChunk)
	if hasBatchParentSearch {
		references := make([]documentchunk.ChunkReference, 0, len(results))
		seen := make(map[documentchunk.ChunkReference]struct{}, len(results))
		for _, result := range results {
			reference := documentchunk.ChunkReference{DocumentID: result.DocumentID, Position: result.Position}
			if _, exists := seen[reference]; exists {
				continue
			}
			seen[reference] = struct{}{}
			references = append(references, reference)
		}
		var err error
		parents, err = batchParentSearcher.ParentsForChunks(ctx, knowledgeBaseID, references)
		if err != nil {
			return nil, fmt.Errorf("expand parent contexts: %w", err)
		}
	}
	for index, result := range expanded {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("expand retrieval context: %w", err)
		}
		reference := documentchunk.ChunkReference{DocumentID: result.DocumentID, Position: result.Position}
		if parent, found := parents[reference]; found {
			expanded[index].ParentContent = parent.Content
			expanded[index].ParentPosition = parent.Position
			continue
		}
		if hasParentSearch && !hasBatchParentSearch {
			parent, found, err := parentSearcher.ParentForChunk(ctx, knowledgeBaseID, result.DocumentID, result.Position)
			if err != nil {
				return nil, fmt.Errorf("expand parent context for document %d position %d: %w", result.DocumentID, result.Position, err)
			}
			if found {
				expanded[index].ParentContent = parent.Content
				expanded[index].ParentPosition = parent.Position
				continue
			}
		}
		if !hasNeighborSearch {
			continue
		}
		neighbors, err := neighborSearcher.SearchNeighbors(ctx, knowledgeBaseID, result.DocumentID, result.Position, DefaultContextBefore, DefaultContextAfter)
		if err != nil {
			return nil, fmt.Errorf("expand retrieval context for document %d position %d: %w", result.DocumentID, result.Position, err)
		}
		for _, neighbor := range neighbors {
			switch {
			case neighbor.Position < result.Position:
				expanded[index].ContextBefore = append(expanded[index].ContextBefore, documentchunk.ContextChunk{Position: neighbor.Position, Content: neighbor.Content})
			case neighbor.Position > result.Position:
				expanded[index].ContextAfter = append(expanded[index].ContextAfter, documentchunk.ContextChunk{Position: neighbor.Position, Content: neighbor.Content})
			}
		}
	}
	return expanded, nil
}

func (s *Service) searchUncached(ctx context.Context, knowledgeBaseID int64, query string, limit int, options SearchOptions, documentIDs []int64) (cachedResult, error) {
	queries := []string{query}
	rewriteState := usage.QueryRewriteObservation{}
	if options.QueryRewrite {
		rewriteState.Enabled = true
		if s.rewriter == nil {
			rewriteState.Fallback = true
		} else {
			variants, rewriteErr := s.rewriter.Rewrite(ctx, query, MaxQueryVariants)
			if rewriteErr != nil {
				if errors.Is(rewriteErr, context.Canceled) || errors.Is(rewriteErr, context.DeadlineExceeded) {
					return cachedResult{}, fmt.Errorf("rewrite search query: %w", rewriteErr)
				}
				rewriteState.Fallback = true
			} else {
				variants = normalizeQueryVariants(query, variants)
				if len(variants) == 0 {
					rewriteState.Fallback = true
				} else {
					rewriteState.Applied = true
					rewriteState.VariantCount = len(variants)
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
			results, searchErr := s.searchOneQuery(queryContext, knowledgeBaseID, searchQuery, candidateLimit, documentIDs, options.KeywordThreshold)
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
		return cachedResult{}, firstErr
	}
	var merged []Result
	var observation usage.RetrievalObservation
	for _, queryResult := range queryResults {
		if queryResult.usage != nil {
			if observer := usage.ObserverFromContext(ctx); observer != nil {
				observer.ObserveEmbeddingTokens(*queryResult.usage)
			}
		}
		observation.VectorCandidates += queryResult.observation.VectorCandidates
		observation.KeywordCandidates += queryResult.observation.KeywordCandidates
		observation.KeywordAfterThreshold += queryResult.observation.KeywordAfterThreshold
		observation.KeywordRejected += queryResult.observation.KeywordRejected
		merged = mergeCandidateResults(merged, queryResult.results, MaxResults)
	}
	observation.DeduplicatedCandidates = len(merged)
	observation.FinalResults = min(len(merged), limit)
	if s.reranker == nil {
		results := merged[:min(len(merged), limit)]
		usage.ObserveQueryRewrite(ctx, rewriteState)
		observation.FinalResults = len(results)
		return cachedResult{results: results, rewriteState: rewriteState, observation: observation}, nil
	}
	if len(merged) == 0 {
		usage.ObserveQueryRewrite(ctx, rewriteState)
		return cachedResult{rewriteState: rewriteState, observation: observation}, nil
	}
	rankLimit := min(len(merged), limit)
	observation.RerankBefore = len(merged)
	ranked, err := s.reranker.Rerank(ctx, query, merged, rankLimit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return cachedResult{}, fmt.Errorf("rerank search results: %w", err)
		}
		// Rerank is an optional quality pass. A provider failure must not throw
		// away the already useful hybrid recall result.
		observation.RerankFallback = true
		observation.RerankAfter = rankLimit
		observation.FinalResults = rankLimit
		usage.ObserveQueryRewrite(ctx, rewriteState)
		return cachedResult{results: append([]Result(nil), merged[:rankLimit]...), rewriteState: rewriteState, observation: observation}, nil
	}
	if len(ranked) > rankLimit {
		ranked = ranked[:rankLimit]
	}
	usage.ObserveQueryRewrite(ctx, rewriteState)
	observation.RerankAfter = len(ranked)
	observation.FinalResults = len(ranked)
	return cachedResult{results: ranked, rewriteState: rewriteState, observation: observation}, nil
}

type querySearchResult struct {
	results     []Result
	usage       *modelclient.TokenUsage
	observation usage.RetrievalObservation
}

func (s *Service) searchOneQuery(ctx context.Context, knowledgeBaseID int64, searchQuery string, candidateLimit int, documentIDs []int64, keywordThreshold float64) (querySearchResult, error) {
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
	observation := usage.RetrievalObservation{VectorCandidates: len(vectorResults)}
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
		observation.KeywordCandidates = len(keywordResults)
		filteredKeywordResults := filterKeywordResults(keywordResults, keywordThreshold)
		observation.KeywordAfterThreshold = len(filteredKeywordResults)
		observation.KeywordRejected = len(keywordResults) - len(filteredKeywordResults)
		queryResults = mergeResults(vectorResults, filteredKeywordResults, candidateLimit)
	}
	return querySearchResult{results: queryResults, usage: response.Usage, observation: observation}, nil
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

func filterKeywordResults(results []Result, threshold float64) []Result {
	if threshold <= 0 {
		return append([]Result(nil), results...)
	}
	filtered := make([]Result, 0, len(results))
	for _, result := range results {
		if !result.KeywordScoreKnown || result.KeywordScore >= threshold {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func mergeResults(vectorResults, keywordResults []Result, limit int) []Result {
	merged := make([]Result, 0, limit)
	seen := make(map[string]int, limit)
	add := func(result Result, matchType string, rank int) {
		key := resultKey(result)
		contribution := 1 / float64(rrfConstant+rank+1)
		if index, ok := seen[key]; ok {
			merged[index].MatchType = "hybrid"
			merged[index].FusionScore += contribution
			if result.KeywordScoreKnown && !merged[index].KeywordScoreKnown {
				merged[index].KeywordScore = result.KeywordScore
				merged[index].KeywordScoreKnown = true
			}
			return
		}
		result.MatchType = matchType
		result.FusionScore = contribution
		seen[key] = len(merged)
		merged = append(merged, result)
	}
	for index, result := range vectorResults {
		add(result, "vector", index)
	}
	for index, result := range keywordResults {
		add(result, "keyword", index)
	}
	sort.SliceStable(merged, func(left, right int) bool { return merged[left].FusionScore > merged[right].FusionScore })
	if len(merged) > limit {
		merged = merged[:limit]
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
			merged[index].FusionScore += result.FusionScore
			continue
		}
		if len(merged) >= limit {
			break
		}
		seen[key] = len(merged)
		merged = append(merged, result)
	}
	sort.SliceStable(merged, func(left, right int) bool { return merged[left].FusionScore > merged[right].FusionScore })
	return merged
}

func resultKey(result Result) string {
	return fmt.Sprintf("%d:%d", result.DocumentID, result.Position)
}
