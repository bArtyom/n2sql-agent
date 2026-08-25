package worker

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/knowledgegraph"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/postprocess"
)

type graphChunkReaderStub struct{}

func (graphChunkReaderStub) Read(context.Context, int64, int64, int) (documentchunk.SearchResult, error) {
	return documentchunk.SearchResult{Content: "张三属于研发部。"}, nil
}

type graphExtractorStub struct{}

func (graphExtractorStub) ExtractChunk(context.Context, string) (knowledgegraph.ChunkGraph, *modelclient.TokenUsage, error) {
	return knowledgegraph.ChunkGraph{
		Entities: []knowledgegraph.Entity{{Name: "张三"}, {Name: "研发部"}},
		Relations: []knowledgegraph.Relation{{Source: "张三", Target: "研发部", Type: "属于"}},
	}, &modelclient.TokenUsage{PromptTokens: 20, CompletionTokens: 8}, nil
}

type graphStoreStub struct {
	knowledgeBaseID int64
	ref             knowledgegraph.ChunkRef
	graph           knowledgegraph.ChunkGraph
}

func (s *graphStoreStub) UpsertChunk(_ context.Context, knowledgeBaseID int64, ref knowledgegraph.ChunkRef, graph knowledgegraph.ChunkGraph) error {
	s.knowledgeBaseID, s.ref, s.graph = knowledgeBaseID, ref, graph
	return nil
}

func (*graphStoreStub) Search(context.Context, int64, []string, []int64) (knowledgegraph.SearchResult, error) {
	return knowledgegraph.SearchResult{}, nil
}

func (*graphStoreStub) DeleteDocument(context.Context, int64, int64) error { return nil }

func TestGraphPostprocessHandlerExtractsAndPersistsChunkGraph(t *testing.T) {
	store := &graphStoreStub{}
	handler := NewGraphPostprocessHandler(graphChunkReaderStub{}, graphExtractorStub{}, store)
	result, err := handler.Handle(context.Background(), postprocess.Task{KnowledgeBaseID: 7, DocumentID: 10, ChunkPosition: 2})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if store.knowledgeBaseID != 7 || store.ref != (knowledgegraph.ChunkRef{DocumentID: 10, Position: 2}) {
		t.Fatalf("stored scope = %d/%#v", store.knowledgeBaseID, store.ref)
	}
	if len(store.graph.Entities) != 2 || len(store.graph.Relations) != 1 {
		t.Fatalf("stored graph = %#v", store.graph)
	}
	if result.InputTokens != 20 || result.OutputTokens != 8 {
		t.Fatalf("usage = %d/%d", result.InputTokens, result.OutputTokens)
	}
}
