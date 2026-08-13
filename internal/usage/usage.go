package usage

import (
	"context"
	"sync"
)

// TokenUsage contains provider-reported token counts for one model request.
// Embedding responses normally populate PromptTokens and TotalTokens while
// CompletionTokens remains zero.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// EffectiveTotal returns a useful total when a provider omits total_tokens.
func (u TokenUsage) EffectiveTotal() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return nonNegative(u.PromptTokens) + nonNegative(u.CompletionTokens)
}

// Observer receives usage without exposing model content or credentials.
type Observer interface {
	ObserveChatTokens(TokenUsage)
	ObserveEmbeddingTokens(TokenUsage)
}

// QueryRewriteObservation describes how one retrieval request used optional
// query rewriting. It contains only bounded status data, never user content.
type QueryRewriteObservation struct {
	Enabled      bool `json:"enabled"`
	Applied      bool `json:"applied"`
	Fallback     bool `json:"fallback"`
	VariantCount int  `json:"variant_count"`
}

// QueryRewriteObserver is an optional request observer. Keeping it separate
// from Observer lets existing token observers remain source-compatible.
type QueryRewriteObserver interface {
	ObserveQueryRewrite(QueryRewriteObservation)
}

// QueryRewriteSnapshotter is implemented by observers that can expose the
// bounded status to a tool event or HTTP response after retrieval finishes.
type QueryRewriteSnapshotter interface {
	QueryRewriteSnapshot() QueryRewriteObservation
}

// QueryRewriteTracker is useful for non-Agent RAG requests, where there is no
// AgentRun to hold request statistics. It is safe for parallel query workers.
type QueryRewriteTracker struct {
	mu          sync.Mutex
	observation QueryRewriteObservation
}

func NewQueryRewriteTracker() *QueryRewriteTracker {
	return &QueryRewriteTracker{}
}

func (t *QueryRewriteTracker) ObserveQueryRewrite(observation QueryRewriteObservation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observation.Enabled = t.observation.Enabled || observation.Enabled
	t.observation.Applied = t.observation.Applied || observation.Applied
	t.observation.Fallback = t.observation.Fallback || observation.Fallback
	t.observation.VariantCount += observation.VariantCount
}

func (t *QueryRewriteTracker) QueryRewriteSnapshot() QueryRewriteObservation {
	if t == nil {
		return QueryRewriteObservation{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observation
}

type observerContextKey struct{}
type queryRewriteObserverContextKey struct{}

// WithObserver associates a usage observer with a request context.
func WithObserver(ctx context.Context, observer Observer) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	return context.WithValue(ctx, observerContextKey{}, observer)
}

// ObserverFromContext returns the usage observer associated with ctx.
func ObserverFromContext(ctx context.Context) Observer {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(observerContextKey{}).(Observer)
	return observer
}

func WithQueryRewriteObserver(ctx context.Context, observer QueryRewriteObserver) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	if existing := QueryRewriteObserverFromContext(ctx); existing != nil {
		observer = combinedQueryRewriteObserver{first: existing, second: observer}
	}
	return context.WithValue(ctx, queryRewriteObserverContextKey{}, observer)
}

type combinedQueryRewriteObserver struct {
	first  QueryRewriteObserver
	second QueryRewriteObserver
}

func (o combinedQueryRewriteObserver) ObserveQueryRewrite(observation QueryRewriteObservation) {
	o.first.ObserveQueryRewrite(observation)
	o.second.ObserveQueryRewrite(observation)
}

func (o combinedQueryRewriteObserver) QueryRewriteSnapshot() QueryRewriteObservation {
	var result QueryRewriteObservation
	if first, ok := o.first.(QueryRewriteSnapshotter); ok {
		result = mergeQueryRewriteObservation(result, first.QueryRewriteSnapshot())
	}
	if second, ok := o.second.(QueryRewriteSnapshotter); ok {
		result = mergeQueryRewriteObservation(result, second.QueryRewriteSnapshot())
	}
	return result
}

func mergeQueryRewriteObservation(current, next QueryRewriteObservation) QueryRewriteObservation {
	current.Enabled = current.Enabled || next.Enabled
	current.Applied = current.Applied || next.Applied
	current.Fallback = current.Fallback || next.Fallback
	current.VariantCount += nonNegative(next.VariantCount)
	return current
}

func QueryRewriteObserverFromContext(ctx context.Context) QueryRewriteObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(queryRewriteObserverContextKey{}).(QueryRewriteObserver)
	return observer
}

func QueryRewriteSnapshotFromContext(ctx context.Context) (QueryRewriteObservation, bool) {
	observer := QueryRewriteObserverFromContext(ctx)
	snapshotter, ok := observer.(QueryRewriteSnapshotter)
	if !ok {
		return QueryRewriteObservation{}, false
	}
	return snapshotter.QueryRewriteSnapshot(), true
}

func ObserveQueryRewrite(ctx context.Context, observation QueryRewriteObservation) {
	if observer := QueryRewriteObserverFromContext(ctx); observer != nil {
		observer.ObserveQueryRewrite(observation)
	}
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
