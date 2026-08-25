package knowledgegraph

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type queryEntityStub struct {
	entities []string
}

func (s queryEntityStub) ExtractQueryEntities(context.Context, string) ([]string, *modelclient.TokenUsage, error) {
	return s.entities, nil, nil
}

type graphStoreStub struct {
	entities []string
}

func (s *graphStoreStub) UpsertChunk(context.Context, int64, ChunkRef, ChunkGraph) error { return nil }
func (s *graphStoreStub) DeleteDocument(context.Context, int64, int64) error             { return nil }
func (s *graphStoreStub) Search(_ context.Context, _ int64, entities []string, _ []int64) (SearchResult, error) {
	s.entities = append([]string(nil), entities...)
	return SearchResult{Chunks: []ChunkRef{{DocumentID: 9, Position: 2}, {DocumentID: 9, Position: 2}, {DocumentID: 4, Position: 1}}}, nil
}

type chunkReaderStub struct{}

func (chunkReaderStub) Read(_ context.Context, _ int64, documentID int64, position int) (documentchunk.SearchResult, error) {
	return documentchunk.SearchResult{DocumentID: documentID, Position: position, Content: "图谱命中"}, nil
}

func TestRetrieverExtractsEntitiesReadsAndDeduplicatesChunkRefs(t *testing.T) {
	store := &graphStoreStub{}
	retriever := NewRetriever(store, queryEntityStub{entities: []string{"员工手册", "员工手册", "年假"}}, chunkReaderStub{})

	results, err := retriever.SearchGraph(context.Background(), 7, "年假怎么计算", 10, nil)
	if err != nil {
		t.Fatalf("SearchGraph() error = %v", err)
	}
	if len(results) != 2 || results[0].MatchType != "graph" || results[0].FusionScore != 0.25 {
		t.Fatalf("results = %#v", results)
	}
	if strings.Join(store.entities, ",") != "员工手册,年假" {
		t.Fatalf("entities = %#v", store.entities)
	}
}
