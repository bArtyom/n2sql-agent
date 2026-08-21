package retrieval_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
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

type recordingEmbedder struct {
	mu      sync.Mutex
	queries []string
}

func (s *recordingEmbedder) Embed(_ context.Context, input []string) (modelclient.EmbeddingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, input...)
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1, 0.2}}}}, nil
}

type queryRewriterStub struct {
	queries []string
}

func (s *queryRewriterStub) Rewrite(_ context.Context, query string, maxVariants int) ([]string, error) {
	s.queries = append(s.queries, query)
	if maxVariants != retrieval.MaxQueryVariants {
		return nil, errors.New("unexpected rewrite limit")
	}
	return []string{"启动服务命令", "启动服务命令"}, nil
}

type multiQueryChunkStore struct {
	mu      sync.Mutex
	queries int
}

func (s *multiQueryChunkStore) Search(_ context.Context, _ int64, _ []float32, _ int) ([]retrieval.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries++
	return []retrieval.Result{{DocumentID: int64(s.queries), Position: 0, Content: "候选"}}, nil
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

type filteredChunkStoreStub struct {
	documentIDs []int64
}

func (s *filteredChunkStoreStub) Search(context.Context, int64, []float32, int) ([]retrieval.Result, error) {
	return nil, nil
}

func (s *filteredChunkStoreStub) SearchWithDocuments(_ context.Context, _ int64, _ []float32, _ int, documentIDs []int64) ([]retrieval.Result, error) {
	s.documentIDs = append([]int64(nil), documentIDs...)
	return []retrieval.Result{{DocumentID: documentIDs[0], Position: 0, Content: "范围内资料", Distance: 0.1}}, nil
}

type hybridChunkStoreStub struct{}

type neighborChunkStoreStub struct{}

type parentChunkStoreStub struct{}

func (neighborChunkStoreStub) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	return []documentchunk.SearchResult{{DocumentID: 9, Position: 1, Content: "命中片段", Distance: 0.2}}, nil
}

func (neighborChunkStoreStub) SearchNeighbors(context.Context, int64, int64, int, int, int) ([]retrieval.Result, error) {
	return []retrieval.Result{
		{Position: 0, Content: "前一个片段"},
		{Position: 1, Content: "命中片段"},
		{Position: 2, Content: "后一个片段"},
	}, nil
}

func (parentChunkStoreStub) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	return []documentchunk.SearchResult{{DocumentID: 9, Position: 1, Content: "命中子块", Distance: 0.2}}, nil
}

func (parentChunkStoreStub) ParentForChunk(context.Context, int64, int64, int) (documentchunk.ParentChunk, bool, error) {
	return documentchunk.ParentChunk{Position: 3, Content: "完整父块上下文"}, true, nil
}

type rerankerStub struct {
	candidates []retrieval.Result
	query      string
	topN       int
}

type failingReranker struct{}

func (failingReranker) Rerank(context.Context, string, []retrieval.Result, int) ([]retrieval.Result, error) {
	return nil, errors.New("rerank provider unavailable")
}

func (s *rerankerStub) Rerank(_ context.Context, query string, candidates []retrieval.Result, topN int) ([]retrieval.Result, error) {
	s.query, s.candidates, s.topN = query, candidates, topN
	return []retrieval.Result{{DocumentID: 2, Content: "重排第一"}, {DocumentID: 1, Content: "重排第二"}}, nil
}

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

func TestServiceExpandsNearbyChunkContext(t *testing.T) {
	service := retrieval.NewService(&embeddingStub{}, neighborChunkStoreStub{})

	results, err := service.Search(context.Background(), 7, "问题", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || len(results[0].ContextBefore) != 1 || len(results[0].ContextAfter) != 1 {
		t.Fatalf("expanded result = %#v", results)
	}
	if got := retrieval.ContextContent(results[0]); !strings.Contains(got, "前一个片段") || !strings.Contains(got, "后一个片段") {
		t.Fatalf("context content = %q", got)
	}
}

func TestServiceUsesParentContextBeforeLegacyNeighbors(t *testing.T) {
	service := retrieval.NewService(&embeddingStub{}, parentChunkStoreStub{})

	results, err := service.Search(context.Background(), 7, "问题", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].ParentPosition != 3 || results[0].ParentContent != "完整父块上下文" {
		t.Fatalf("parent result = %#v", results)
	}
	if len(results[0].ContextBefore) != 0 || len(results[0].ContextAfter) != 0 {
		t.Fatalf("parent result unexpectedly used neighbors = %#v", results)
	}
	content := retrieval.ContextContent(results[0])
	if !strings.Contains(content, "完整父块上下文") || !strings.Contains(content, "命中子块") {
		t.Fatalf("context content = %q", content)
	}
}

func TestServicePassesNormalizedDocumentFilter(t *testing.T) {
	store := &filteredChunkStoreStub{}
	service := retrieval.NewService(&embeddingStub{}, store)

	results, err := service.SearchWithOptions(context.Background(), 7, "问题", 5, retrieval.SearchOptions{DocumentIDs: []int64{9, 3, 9}})
	if err != nil {
		t.Fatalf("SearchWithOptions() error = %v", err)
	}
	if len(results) != 1 || results[0].DocumentID != 3 {
		t.Fatalf("results = %#v", results)
	}
	if !reflect.DeepEqual(store.documentIDs, []int64{3, 9}) {
		t.Fatalf("document IDs = %#v, want [3 9]", store.documentIDs)
	}
}

func TestServiceExpandsAndDeduplicatesQueryRewriteVariants(t *testing.T) {
	embedder := &recordingEmbedder{}
	rewriter := &queryRewriterStub{}
	chunks := &multiQueryChunkStore{}
	service := retrieval.NewHybridServiceWithRerankerAndRewriter(embedder, chunks, nil, nil, rewriter)

	results, err := service.SearchWithOptions(context.Background(), 7, "如何启动服务", 5, retrieval.SearchOptions{QueryRewrite: true})
	if err != nil {
		t.Fatalf("SearchWithOptions() error = %v", err)
	}
	if len(rewriter.queries) != 1 || len(embedder.queries) != 2 || chunks.queries != 2 {
		t.Fatalf("rewrite calls=%d embedded=%v chunk queries=%d, want one rewrite and two searches", len(rewriter.queries), embedder.queries, chunks.queries)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want two unique candidates", results)
	}
}

func TestServiceFallsBackToOriginalQueryWhenRewriterIsUnavailable(t *testing.T) {
	service := retrieval.NewService(&embeddingStub{}, &chunkStoreStub{})
	results, err := service.SearchWithOptions(context.Background(), 7, "问题", 5, retrieval.SearchOptions{QueryRewrite: true})
	if err != nil {
		t.Fatalf("SearchWithOptions() error = %v, want original-query fallback", err)
	}
	if len(results) != 1 || results[0].Content != "Go 后端" {
		t.Fatalf("fallback results = %#v", results)
	}
}

type failingQueryRewriter struct{}

func (failingQueryRewriter) Rewrite(context.Context, string, int) ([]string, error) {
	return nil, errors.New("rewrite provider unavailable")
}

func TestServiceFallsBackWhenQueryRewriteFails(t *testing.T) {
	embedder := &recordingEmbedder{}
	service := retrieval.NewHybridServiceWithRerankerAndRewriter(embedder, &chunkStoreStub{}, nil, nil, failingQueryRewriter{})
	results, err := service.SearchWithOptions(context.Background(), 7, "原始问题", 5, retrieval.SearchOptions{QueryRewrite: true})
	if err != nil {
		t.Fatalf("SearchWithOptions() error = %v, want fallback", err)
	}
	if len(results) != 1 || len(embedder.queries) != 1 || embedder.queries[0] != "原始问题" {
		t.Fatalf("fallback results=%#v embedded=%#v", results, embedder.queries)
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

type summaryCollisionStore struct{}

func (summaryCollisionStore) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	return []documentchunk.SearchResult{{DocumentID: 1, Position: 0, Content: "正文"}}, nil
}

func (summaryCollisionStore) SearchKeyword(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: 1, Position: 0, ChunkKind: "summary", Content: "摘要"}}, nil
}

func TestHybridServiceKeepsSummaryAndTextAtSamePositionDistinct(t *testing.T) {
	service := retrieval.NewHybridService(&embeddingStub{}, summaryCollisionStore{}, summaryCollisionStore{})
	results, err := service.Search(context.Background(), 7, "问题", 5)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 || results[0].ChunkKind == results[1].ChunkKind {
		t.Fatalf("results = %#v, want separate text and summary results", results)
	}
}

func TestHybridServiceExpandsCandidatesBeforeReranking(t *testing.T) {
	store := &candidateChunkStoreStub{}
	reranker := &rerankerStub{}
	service := retrieval.NewHybridServiceWithReranker(&embeddingStub{}, store, nil, reranker)

	results, err := service.Search(context.Background(), 7, "服务", 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 || results[0].DocumentID != 2 || store.limit != 6 || reranker.query != "服务" || reranker.topN != 2 {
		t.Fatalf("results=%#v store=%#v reranker=%#v", results, store, reranker)
	}
}

func TestHybridServiceFallsBackToFusedResultsWhenRerankFails(t *testing.T) {
	service := retrieval.NewHybridServiceWithReranker(&embeddingStub{}, hybridChunkStoreStub{}, hybridChunkStoreStub{}, failingReranker{})

	results, err := service.Search(context.Background(), 7, "错误码", 2)
	if err != nil {
		t.Fatalf("Search() error = %v, want hybrid fallback", err)
	}
	if len(results) != 2 || results[0].FusionScore <= 0 {
		t.Fatalf("fallback results = %#v, want fused candidates", results)
	}
}

func TestHybridServiceReportsRetrievalCounts(t *testing.T) {
	tracker := usage.NewRetrievalTracker()
	service := retrieval.NewHybridService(&embeddingStub{}, hybridChunkStoreStub{}, hybridChunkStoreStub{})

	if _, err := service.Search(usage.WithRetrievalObserver(context.Background(), tracker), 7, "错误码", 2); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	stats := tracker.RetrievalSnapshot()
	if stats.VectorCandidates != 1 || stats.KeywordCandidates != 2 || stats.DeduplicatedCandidates != 2 || stats.FinalResults != 2 {
		t.Fatalf("retrieval stats = %#v", stats)
	}
}

type candidateChunkStoreStub struct{ limit int }

func (s *candidateChunkStoreStub) Search(_ context.Context, _ int64, _ []float32, limit int) ([]retrieval.Result, error) {
	s.limit = limit
	return []retrieval.Result{
		{DocumentID: 1, Position: 1, Content: "候选一", Distance: 0.1},
		{DocumentID: 2, Position: 2, Content: "候选二", Distance: 0.2},
		{DocumentID: 3, Position: 3, Content: "候选三", Distance: 0.3},
	}, nil
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
