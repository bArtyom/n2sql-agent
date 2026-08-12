package retrieval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

var (
	ErrInvalidKnowledgeBase = errors.New("invalid knowledge base ID")
	ErrInvalidQuery         = errors.New("invalid search query")
	ErrInvalidLimit         = errors.New("invalid search result limit")
	ErrInvalidMaxDistance   = errors.New("search distance threshold must be greater than 0 and at most 1")
)

const (
	DefaultResults     = 5
	MaxResults         = 20
	DefaultMaxDistance = 0.65
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

func ValidateMaxDistance(maxDistance float64) error {
	if maxDistance <= 0 || maxDistance > 1 {
		return ErrInvalidMaxDistance
	}
	return nil
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
}

func NewService(embedder Embedder, chunks ChunkSearcher) *Service {
	return &Service{embedder: embedder, chunks: chunks}
}

func NewHybridService(embedder Embedder, chunks ChunkSearcher, keyword KeywordSearcher) *Service {
	return &Service{embedder: embedder, chunks: chunks, keyword: keyword}
}

func (s *Service) Search(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]Result, error) {
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
	response, err := s.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed search query: %w", err)
	}
	if observer := usage.ObserverFromContext(ctx); observer != nil && response.Usage != nil {
		observer.ObserveEmbeddingTokens(*response.Usage)
	}
	if len(response.Data) != 1 || len(response.Data[0].Vector) == 0 {
		return nil, errors.New("embedding response does not contain one non-empty query vector")
	}
	results, err := s.chunks.Search(ctx, knowledgeBaseID, response.Data[0].Vector, limit)
	if err != nil {
		return nil, fmt.Errorf("search document chunks: %w", err)
	}
	if s.keyword == nil {
		return results, nil
	}
	keywordResults, err := s.keyword.SearchKeyword(ctx, knowledgeBaseID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search keyword document chunks: %w", err)
	}
	return mergeResults(results, keywordResults, limit), nil
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
