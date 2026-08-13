package retrieval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type cacheCountingEmbedder struct {
	mu         sync.Mutex
	calls      int
	started    chan struct{}
	continueCh chan struct{}
	err        error
}

func (s *cacheCountingEmbedder) Embed(ctx context.Context, _ []string) (modelclient.EmbeddingResponse, error) {
	s.mu.Lock()
	s.calls++
	started, continueCh := s.started, s.continueCh
	err := s.err
	s.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if continueCh != nil {
		select {
		case <-continueCh:
		case <-ctx.Done():
			return modelclient.EmbeddingResponse{}, ctx.Err()
		}
	}
	if err != nil {
		return modelclient.EmbeddingResponse{}, err
	}
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Vector: []float32{0.1}}}}, nil
}

func (s *cacheCountingEmbedder) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type cacheCountingChunkStore struct {
	mu    sync.Mutex
	calls int
}

func (s *cacheCountingChunkStore) Search(context.Context, int64, []float32, int) ([]documentchunk.SearchResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return []documentchunk.SearchResult{{DocumentID: 1, Position: 0, Content: "缓存结果"}}, nil
}

func (s *cacheCountingChunkStore) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestServiceCachesSuccessfulRetrievalResults(t *testing.T) {
	embedder := &cacheCountingEmbedder{}
	chunks := &cacheCountingChunkStore{}
	service := NewService(embedder, chunks)

	first, err := service.Search(context.Background(), 7, "  Go   后端 ", 5)
	if err != nil {
		t.Fatalf("first Search() error = %v", err)
	}
	first[0].Content = "调用方修改"
	second, err := service.Search(context.Background(), 7, "Go 后端", 5)
	if err != nil {
		t.Fatalf("second Search() error = %v", err)
	}
	if embedder.Calls() != 1 || chunks.Calls() != 1 {
		t.Fatalf("provider calls = embedding %d, chunks %d; want one each", embedder.Calls(), chunks.Calls())
	}
	if second[0].Content != "缓存结果" {
		t.Fatalf("cached result was mutated by caller: %#v", second)
	}
}

func TestServiceClearCacheForcesFreshRetrieval(t *testing.T) {
	embedder := &cacheCountingEmbedder{}
	chunks := &cacheCountingChunkStore{}
	service := NewService(embedder, chunks)

	if _, err := service.Search(context.Background(), 7, "问题", 5); err != nil {
		t.Fatal(err)
	}
	service.ClearCache(7)
	if _, err := service.Search(context.Background(), 7, "问题", 5); err != nil {
		t.Fatal(err)
	}
	if embedder.Calls() != 2 || chunks.Calls() != 2 {
		t.Fatalf("calls after invalidation = embedding %d, chunks %d; want two each", embedder.Calls(), chunks.Calls())
	}
}

func TestServiceDoesNotCacheErrors(t *testing.T) {
	wantErr := errors.New("embedding unavailable")
	embedder := &cacheCountingEmbedder{err: wantErr}
	service := NewService(embedder, &cacheCountingChunkStore{})

	for range 2 {
		_, err := service.Search(context.Background(), 7, "问题", 5)
		if !errors.Is(err, wantErr) {
			t.Fatalf("Search() error = %v, want %v", err, wantErr)
		}
	}
	if embedder.Calls() != 2 {
		t.Fatalf("embedding calls = %d, want two after failed searches", embedder.Calls())
	}
}

func TestServiceSharesConcurrentRetrieval(t *testing.T) {
	embedder := &cacheCountingEmbedder{started: make(chan struct{}, 1), continueCh: make(chan struct{})}
	service := NewService(embedder, &cacheCountingChunkStore{})
	results := make(chan error, 2)
	go func() {
		_, err := service.Search(context.Background(), 7, "问题", 5)
		results <- err
	}()
	select {
	case <-embedder.started:
	case <-time.After(time.Second):
		t.Fatal("first search did not start")
	}
	go func() {
		_, err := service.Search(context.Background(), 7, "问题", 5)
		results <- err
	}()
	close(embedder.continueCh)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Search() error = %v", err)
		}
	}
	if embedder.Calls() != 1 {
		t.Fatalf("embedding calls = %d, want one shared call", embedder.Calls())
	}
}

func TestResultCacheExpiresAndEvictsEntries(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newResultCacheWithClock(CacheConfig{MaxEntries: 1, TTL: time.Minute}, func() time.Time { return now })
	firstKey := makeResultCacheKey(7, "第一个", 5, nil, false)
	secondKey := makeResultCacheKey(7, "第二个", 5, nil, false)
	cache.finish(firstKey, mustBegin(cache, firstKey), cachedResult{results: []Result{{Content: "first"}}}, nil)
	if _, ok := cache.get(firstKey); !ok {
		t.Fatal("first cache entry was not stored")
	}
	cache.finish(secondKey, mustBegin(cache, secondKey), cachedResult{results: []Result{{Content: "second"}}}, nil)
	if _, ok := cache.get(firstKey); ok {
		t.Fatal("oldest entry was not evicted")
	}
	now = now.Add(time.Minute)
	if _, ok := cache.get(secondKey); ok {
		t.Fatal("expired entry was returned")
	}
}

func mustBegin(cache *resultCache, key resultCacheKey) *cacheFlight {
	flight, owner := cache.begin(key)
	if !owner {
		panic("test cache key already in flight")
	}
	return flight
}
