package retrieval_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

type embeddingStub struct {
	input []string
}

func (s *embeddingStub) Embed(_ context.Context, input []string) (modelclient.EmbeddingResponse, error) {
	s.input = input
	return modelclient.EmbeddingResponse{
		Data:  []modelclient.Embedding{{Index: 0, Vector: []float32{0.1, 0.2}}},
		Usage: &modelclient.TokenUsage{PromptTokens: 7, TotalTokens: 7},
	}, nil
}

type usageObserverStub struct {
	embedding usage.TokenUsage
}

func (s *usageObserverStub) ObserveChatTokens(usage.TokenUsage) {}

func (s *usageObserverStub) ObserveEmbeddingTokens(value usage.TokenUsage) {
	s.embedding = value
}

type chunkStoreStub struct {
	knowledgeBaseID int64
	embedding       []float32
	limit           int
}

type hybridChunkStoreStub struct{}

func (hybridChunkStoreStub) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	return []documentchunk.SearchResult{{DocumentID: 1, Position: 0, Content: "向量命中", Distance: 0.2}}, nil
}

func (hybridChunkStoreStub) SearchKeyword(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{
		{DocumentID: 1, Position: 0, Content: "重复命中", Distance: 0},
		{DocumentID: 2, Position: 1, Content: "关键词命中", Distance: 0},
	}, nil
}

func (s *chunkStoreStub) Search(_ context.Context, knowledgeBaseID int64, embedding []float32, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.embedding = embedding
	s.limit = limit
	return []retrieval.Result{{DocumentID: 11, Position: 0, Content: "Go 后端"}}, nil
}

func TestServiceEmbedsQueryAndSearchesKnowledgeBase(t *testing.T) {
	embedder := &embeddingStub{}
	store := &chunkStoreStub{}
	service := retrieval.NewService(embedder, store)

	results, err := service.Search(context.Background(), 7, "后端怎么运行", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Content != "Go 后端" {
		t.Fatalf("results = %#v", results)
	}
	if len(embedder.input) != 1 || embedder.input[0] != "后端怎么运行" {
		t.Fatalf("embedding input = %#v", embedder.input)
	}
	if store.knowledgeBaseID != 7 || store.limit != 5 || len(store.embedding) != 2 || store.embedding[1] != 0.2 {
		t.Fatalf("search arguments = %#v", store)
	}
}

func TestHybridServiceMergesVectorAndKeywordResultsWithoutDuplicates(t *testing.T) {
	service := retrieval.NewHybridService(&embeddingStub{}, hybridChunkStoreStub{}, hybridChunkStoreStub{})

	results, err := service.Search(context.Background(), 7, "错误码", 3)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 || results[0].DocumentID != 1 || results[1].DocumentID != 2 || results[0].MatchType != "hybrid" || results[1].MatchType != "keyword" {
		t.Fatalf("hybrid results = %#v, want vector ID 1 and keyword ID 2", results)
	}
}

func TestServiceReportsEmbeddingUsageToContextObserver(t *testing.T) {
	observer := &usageObserverStub{}
	service := retrieval.NewService(&embeddingStub{}, &chunkStoreStub{})

	if _, err := service.Search(usage.WithObserver(context.Background(), observer), 7, "问题", 5); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if observer.embedding.PromptTokens != 7 || observer.embedding.TotalTokens != 7 {
		t.Fatalf("embedding usage = %#v, want prompt=7 total=7", observer.embedding)
	}
}

func TestServiceReturnsEmbeddingError(t *testing.T) {
	expected := errors.New("model unavailable")
	embedder := embeddingErrorStub{err: expected}
	service := retrieval.NewService(embedder, &chunkStoreStub{})

	_, err := service.Search(context.Background(), 7, "问题", 5)
	if !errors.Is(err, expected) {
		t.Fatalf("Search() error = %v, want %v", err, expected)
	}
}

func TestServiceRejectsInvalidSearchArguments(t *testing.T) {
	cases := []struct {
		name            string
		knowledgeBaseID int64
		query           string
		limit           int
		want            error
	}{
		{name: "invalid knowledge base", knowledgeBaseID: 0, query: "问题", limit: 5, want: retrieval.ErrInvalidKnowledgeBase},
		{name: "empty query", knowledgeBaseID: 7, query: "  ", limit: 5, want: retrieval.ErrInvalidQuery},
		{name: "zero limit", knowledgeBaseID: 7, query: "问题", limit: 0, want: retrieval.ErrInvalidLimit},
		{name: "too many results", knowledgeBaseID: 7, query: "问题", limit: retrieval.MaxResults + 1, want: retrieval.ErrInvalidLimit},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := retrieval.NewService(&embeddingStub{}, &chunkStoreStub{})
			_, err := service.Search(context.Background(), test.knowledgeBaseID, test.query, test.limit)
			if !errors.Is(err, test.want) {
				t.Fatalf("Search() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestServiceRejectsEmptyQueryVector(t *testing.T) {
	service := retrieval.NewService(emptyEmbeddingStub{}, &chunkStoreStub{})

	_, err := service.Search(context.Background(), 7, "问题", 5)
	if err == nil || !strings.Contains(err.Error(), "one non-empty query vector") {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestFilterByMaxDistanceKeepsOnlyCloseResults(t *testing.T) {
	results, err := retrieval.FilterByMaxDistance([]retrieval.Result{
		{DocumentID: 1, Distance: 0.20},
		{DocumentID: 2, Distance: 0.65},
		{DocumentID: 3, Distance: 0.66},
	}, 0.65)
	if err != nil {
		t.Fatalf("FilterByMaxDistance() error = %v", err)
	}
	if len(results) != 2 || results[0].DocumentID != 1 || results[1].DocumentID != 2 {
		t.Fatalf("filtered results = %#v, want IDs 1 and 2", results)
	}
}

func TestFilterByMaxDistanceRejectsInvalidThreshold(t *testing.T) {
	if _, err := retrieval.FilterByMaxDistance(nil, 1.01); !errors.Is(err, retrieval.ErrInvalidMaxDistance) {
		t.Fatalf("FilterByMaxDistance() error = %v, want ErrInvalidMaxDistance", err)
	}
}

type embeddingErrorStub struct{ err error }

func (s embeddingErrorStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{}, s.err
}

type emptyEmbeddingStub struct{}

func (emptyEmbeddingStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{}, nil
}
