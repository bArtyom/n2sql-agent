package retrieval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type graphSearcherStub struct{}

func (graphSearcherStub) SearchGraph(context.Context, int64, string, int, []int64) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: 33, Position: 4, Content: "图谱召回", MatchType: "graph", FusionScore: 0.25}}, nil
}

type failingGraphSearcherStub struct{}

func (failingGraphSearcherStub) SearchGraph(context.Context, int64, string, int, []int64) ([]retrieval.Result, error) {
	return nil, errors.New("neo4j unavailable")
}

type graphEmbeddingStub struct{}

func (graphEmbeddingStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1}}}}, nil
}

type graphChunkStoreStub struct{}

func (graphChunkStoreStub) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	return []documentchunk.SearchResult{{DocumentID: 1, Position: 0, Content: "向量召回", Distance: 0.2}}, nil
}

func (graphChunkStoreStub) SearchKeyword(context.Context, int64, string, int) ([]documentchunk.SearchResult, error) {
	return nil, nil
}

func TestGraphRecallIsMergedIntoHybridResults(t *testing.T) {
	service := retrieval.NewHybridService(graphEmbeddingStub{}, graphChunkStoreStub{}, graphChunkStoreStub{})
	service.SetGraphSearcher(graphSearcherStub{})

	results, err := service.Search(context.Background(), 7, "年假", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	found := false
	for _, result := range results {
		if result.DocumentID == 33 && result.MatchType == "graph" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("results = %#v, want graph candidate", results)
	}
}

func TestGraphRecallFailureFallsBackToHybridResults(t *testing.T) {
	service := retrieval.NewHybridService(graphEmbeddingStub{}, graphChunkStoreStub{}, graphChunkStoreStub{})
	service.SetGraphSearcher(failingGraphSearcherStub{})

	results, err := service.Search(context.Background(), 7, "年假", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].Content != "向量召回" {
		t.Fatalf("results = %#v, want Hybrid fallback", results)
	}
}
