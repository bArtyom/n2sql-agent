package retrieval

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/usage"
)

// CacheConfig controls the small process-local retrieval result cache.
// Results are intentionally not shared between server instances; a restart
// clears them and a document update can explicitly invalidate them.
type CacheConfig struct {
	MaxEntries int
	TTL        time.Duration
}

const (
	DefaultCacheEntries = 128
	DefaultCacheTTL     = 2 * time.Minute
)

func DefaultCacheConfig() CacheConfig {
	return CacheConfig{MaxEntries: DefaultCacheEntries, TTL: DefaultCacheTTL}
}

type resultCacheKey struct {
	knowledgeBaseID  int64
	query            string
	limit            int
	documentIDs      string
	queryRewrite     bool
	keywordThreshold float64
}

type cachedResult struct {
	results      []Result
	rewriteState usage.QueryRewriteObservation
	observation  usage.RetrievalObservation
}

type cacheEntry struct {
	value            cachedResult
	expiresAt        time.Time
	lastUsed         uint64
	generation       uint64
	globalGeneration uint64
}

type cacheFlight struct {
	done             chan struct{}
	value            cachedResult
	err              error
	generation       uint64
	globalGeneration uint64
}

type resultCache struct {
	mu               sync.Mutex
	maxEntries       int
	ttl              time.Duration
	clock            func() time.Time
	sequence         uint64
	entries          map[resultCacheKey]cacheEntry
	inFlight         map[resultCacheKey]*cacheFlight
	generations      map[int64]uint64
	globalGeneration uint64
}

func newResultCache(config CacheConfig) *resultCache {
	return newResultCacheWithClock(config, time.Now)
}

func newResultCacheWithClock(config CacheConfig, clock func() time.Time) *resultCache {
	if config.MaxEntries <= 0 {
		config.MaxEntries = DefaultCacheEntries
	}
	if config.TTL <= 0 {
		config.TTL = DefaultCacheTTL
	}
	return &resultCache{
		maxEntries:  config.MaxEntries,
		ttl:         config.TTL,
		clock:       clock,
		entries:     make(map[resultCacheKey]cacheEntry),
		inFlight:    make(map[resultCacheKey]*cacheFlight),
		generations: make(map[int64]uint64),
	}
}

func (c *resultCache) get(key resultCacheKey) (cachedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cachedResult{}, false
	}
	if !c.clock().Before(entry.expiresAt) || entry.generation != c.generations[key.knowledgeBaseID] || entry.globalGeneration != c.globalGeneration {
		delete(c.entries, key)
		return cachedResult{}, false
	}
	c.sequence++
	entry.lastUsed = c.sequence
	c.entries[key] = entry
	return cloneCachedResult(entry.value), true
}

// begin returns the flight and whether the caller owns the underlying load.
// Only the owner calls the embedding/keyword/rerank pipeline; other requests
// wait for the same result instead of issuing duplicate provider calls.
func (c *resultCache) begin(key resultCacheKey) (*cacheFlight, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if flight, ok := c.inFlight[key]; ok {
		if flight.generation == c.generations[key.knowledgeBaseID] && flight.globalGeneration == c.globalGeneration {
			return flight, false
		}
		// A knowledge-base invalidation happened while the old request was
		// running. Let that request finish for its existing waiters, but do
		// not make new requests wait for stale results.
		delete(c.inFlight, key)
	}
	flight := &cacheFlight{
		done:             make(chan struct{}),
		generation:       c.generations[key.knowledgeBaseID],
		globalGeneration: c.globalGeneration,
	}
	c.inFlight[key] = flight
	return flight, true
}

func (c *resultCache) finish(key resultCacheKey, flight *cacheFlight, value cachedResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flight.value = cloneCachedResult(value)
	flight.err = err
	if err == nil && flight.generation == c.generations[key.knowledgeBaseID] && flight.globalGeneration == c.globalGeneration {
		c.sequence++
		c.entries[key] = cacheEntry{
			value:            cloneCachedResult(value),
			expiresAt:        c.clock().Add(c.ttl),
			lastUsed:         c.sequence,
			generation:       flight.generation,
			globalGeneration: flight.globalGeneration,
		}
		c.evictIfNeeded()
	}
	if current, ok := c.inFlight[key]; ok && current == flight {
		delete(c.inFlight, key)
	}
	close(flight.done)
}

func (c *resultCache) clear(knowledgeBaseID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if knowledgeBaseID <= 0 {
		c.globalGeneration++
		for key := range c.entries {
			delete(c.entries, key)
		}
		return
	}
	c.generations[knowledgeBaseID]++
	for key := range c.entries {
		if key.knowledgeBaseID == knowledgeBaseID {
			delete(c.entries, key)
		}
	}
}

func (c *resultCache) evictIfNeeded() {
	for len(c.entries) > c.maxEntries {
		var oldestKey resultCacheKey
		var oldestSequence uint64
		first := true
		for key, entry := range c.entries {
			if first || entry.lastUsed < oldestSequence {
				oldestKey, oldestSequence, first = key, entry.lastUsed, false
			}
		}
		delete(c.entries, oldestKey)
	}
}

func (c *resultCache) wait(ctx context.Context, flight *cacheFlight) (cachedResult, error) {
	select {
	case <-flight.done:
		return cloneCachedResult(flight.value), flight.err
	case <-ctx.Done():
		return cachedResult{}, ctx.Err()
	}
}

func cloneCachedResult(value cachedResult) cachedResult {
	value.results = append([]Result(nil), value.results...)
	return value
}

func makeResultCacheKey(knowledgeBaseID int64, query string, limit int, documentIDs []int64, queryRewrite bool, thresholds ...float64) resultCacheKey {
	var keywordThreshold float64
	if len(thresholds) > 0 {
		keywordThreshold = thresholds[0]
	}
	var builder strings.Builder
	for index, documentID := range documentIDs {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatInt(documentID, 10))
	}
	return resultCacheKey{
		knowledgeBaseID:  knowledgeBaseID,
		query:            normalizedCacheQuery(query),
		limit:            limit,
		documentIDs:      builder.String(),
		queryRewrite:     queryRewrite,
		keywordThreshold: keywordThreshold,
	}
}

func normalizedCacheQuery(query string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(query)), " ")
}
