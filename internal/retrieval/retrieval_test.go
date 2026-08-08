package retrieval_test

import (
	"context"
	"errors"
	"strings"
	"testing"

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

type embeddingErrorStub struct{ err error }

func (s embeddingErrorStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{}, s.err
}

type emptyEmbeddingStub struct{}

func (emptyEmbeddingStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{}, nil
}
