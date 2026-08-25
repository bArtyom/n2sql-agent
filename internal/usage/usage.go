package usage

import (
	"context"
	"sync"
	"time"
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

// ModelKind identifies a provider call without recording prompts, documents,
// images, or credentials. Keeping this small and bounded makes it safe for
// metrics labels and durable evaluation summaries.
type ModelKind string

const (
	ModelKindChat      ModelKind = "chat"
	ModelKindEmbedding ModelKind = "embedding"
	ModelKindRerank    ModelKind = "rerank"
	ModelKindOCR       ModelKind = "ocr"
)

// ModelCallObservation is the common operational record for one model call.
// EstimatedCostMicros is supplied by the caller's configured price snapshot;
// the runtime never invents provider prices.
type ModelCallObservation struct {
	Kind                ModelKind     `json:"kind"`
	Provider            string        `json:"provider,omitempty"`
	Model               string        `json:"model,omitempty"`
	TraceID             string        `json:"trace_id,omitempty"`
	Duration            time.Duration `json:"-"`
	Success             bool          `json:"success"`
	ErrorClass          string        `json:"error_class,omitempty"`
	Usage               TokenUsage    `json:"usage"`
	EstimatedCostMicros int64         `json:"estimated_cost_micros,omitempty"`
}

// CallObserver is deliberately additive to Observer so existing AgentRun
// usage observers remain source-compatible.
type CallObserver interface {
	ObserveModelCall(ModelCallObservation)
}

type callObserverContextKey struct{}

func WithCallObserver(ctx context.Context, observer CallObserver) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	if existing := CallObserverFromContext(ctx); existing != nil {
		observer = combinedCallObserver{first: existing, second: observer}
	}
	return context.WithValue(ctx, callObserverContextKey{}, observer)
}

func CallObserverFromContext(ctx context.Context) CallObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(callObserverContextKey{}).(CallObserver)
	return observer
}

func ObserveModelCall(ctx context.Context, observation ModelCallObservation) {
	if observer := CallObserverFromContext(ctx); observer != nil {
		observer.ObserveModelCall(observation)
	}
}

type combinedCallObserver struct {
	first  CallObserver
	second CallObserver
}

func (o combinedCallObserver) ObserveModelCall(observation ModelCallObservation) {
	o.first.ObserveModelCall(observation)
	o.second.ObserveModelCall(observation)
}

// ModelCallSnapshot is a bounded aggregate suitable for evaluation results
// and operational dashboards.
type ModelCallSnapshot struct {
	Calls               int   `json:"calls"`
	Failures            int   `json:"failures"`
	DurationMS          int64 `json:"duration_ms"`
	PromptTokens        int   `json:"prompt_tokens"`
	CompletionTokens    int   `json:"completion_tokens"`
	TotalTokens         int   `json:"total_tokens"`
	EstimatedCostMicros int64 `json:"estimated_cost_micros"`
}

// CallTracker implements both token Observer and CallObserver. It is safe to
// share across parallel retrieval or image tasks within one request/run.
type CallTracker struct {
	mu     sync.Mutex
	byKind map[ModelKind]ModelCallSnapshot
}

func NewCallTracker() *CallTracker {
	return &CallTracker{byKind: make(map[ModelKind]ModelCallSnapshot)}
}

func (t *CallTracker) ObserveChatTokens(value TokenUsage) {
	if t != nil {
		t.observeTokens(ModelKindChat, value)
	}
}

func (t *CallTracker) ObserveEmbeddingTokens(value TokenUsage) {
	if t != nil {
		t.observeTokens(ModelKindEmbedding, value)
	}
}

func (t *CallTracker) observeTokens(kind ModelKind, value TokenUsage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.byKind[kind]
	current.PromptTokens += nonNegative(value.PromptTokens)
	current.CompletionTokens += nonNegative(value.CompletionTokens)
	current.TotalTokens += nonNegative(value.EffectiveTotal())
	t.byKind[kind] = current
}

func (t *CallTracker) ObserveModelCall(observation ModelCallObservation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.byKind[observation.Kind]
	current.Calls++
	if !observation.Success {
		current.Failures++
	}
	current.DurationMS += observation.Duration.Milliseconds()
	current.PromptTokens += nonNegative(observation.Usage.PromptTokens)
	current.CompletionTokens += nonNegative(observation.Usage.CompletionTokens)
	total := observation.Usage.EffectiveTotal()
	if total > 0 {
		current.TotalTokens += total
	}
	if observation.EstimatedCostMicros > 0 {
		current.EstimatedCostMicros += observation.EstimatedCostMicros
	}
	t.byKind[observation.Kind] = current
}

func (t *CallTracker) ModelCallSnapshot(kind ModelKind) ModelCallSnapshot {
	if t == nil {
		return ModelCallSnapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byKind[kind]
}

func (t *CallTracker) ModelCallSnapshots() map[ModelKind]ModelCallSnapshot {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make(map[ModelKind]ModelCallSnapshot, len(t.byKind))
	for kind, snapshot := range t.byKind {
		result[kind] = snapshot
	}
	return result
}

// QueryRewriteObservation describes how one retrieval request used optional
// query rewriting. It contains only bounded status data, never user content.
type QueryRewriteObservation struct {
	Enabled      bool `json:"enabled"`
	Applied      bool `json:"applied"`
	Fallback     bool `json:"fallback"`
	VariantCount int  `json:"variant_count"`
}

// RetrievalObservation is a bounded summary of one or more retrieval calls.
// It deliberately contains counts and status only, never query text or source
// content, so it is safe to expose in Agent/RAG responses.
type RetrievalObservation struct {
	VectorCandidates       int  `json:"vector_candidates"`
	KeywordCandidates      int  `json:"keyword_candidates"`
	KeywordAfterThreshold  int  `json:"keyword_after_threshold"`
	KeywordRejected        int  `json:"keyword_rejected"`
	SummaryCandidates      int  `json:"summary_candidates"`
	GraphCandidates        int  `json:"graph_candidates"`
	DeduplicatedCandidates int  `json:"deduplicated_candidates"`
	RerankBefore           int  `json:"rerank_before"`
	RerankAfter            int  `json:"rerank_after"`
	FinalResults           int  `json:"final_results"`
	FinalFiltered          int  `json:"final_filtered"`
	RerankFallback         bool `json:"rerank_fallback"`
}

func (o RetrievalObservation) HasData() bool {
	return o.VectorCandidates > 0 || o.KeywordCandidates > 0 || o.KeywordAfterThreshold > 0 || o.KeywordRejected > 0 ||
		o.SummaryCandidates > 0 || o.GraphCandidates > 0 || o.DeduplicatedCandidates > 0 || o.RerankBefore > 0 || o.RerankAfter > 0 ||
		o.FinalResults > 0 || o.FinalFiltered > 0 || o.RerankFallback
}

// RetrievalObserver receives bounded retrieval pipeline statistics.
type RetrievalObserver interface {
	ObserveRetrieval(RetrievalObservation)
}

type RetrievalSnapshotter interface {
	RetrievalSnapshot() RetrievalObservation
}

// RetrievalTracker collects retrieval observations for a non-Agent request.
// AgentRun implements the same interface for Agent requests.
type RetrievalTracker struct {
	mu          sync.Mutex
	observation RetrievalObservation
}

func NewRetrievalTracker() *RetrievalTracker { return &RetrievalTracker{} }

func (t *RetrievalTracker) ObserveRetrieval(observation RetrievalObservation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observation.VectorCandidates += nonNegative(observation.VectorCandidates)
	t.observation.KeywordCandidates += nonNegative(observation.KeywordCandidates)
	t.observation.KeywordAfterThreshold += nonNegative(observation.KeywordAfterThreshold)
	t.observation.KeywordRejected += nonNegative(observation.KeywordRejected)
	t.observation.SummaryCandidates += nonNegative(observation.SummaryCandidates)
	t.observation.GraphCandidates += nonNegative(observation.GraphCandidates)
	t.observation.DeduplicatedCandidates += nonNegative(observation.DeduplicatedCandidates)
	t.observation.RerankBefore += nonNegative(observation.RerankBefore)
	t.observation.RerankAfter += nonNegative(observation.RerankAfter)
	t.observation.FinalResults += nonNegative(observation.FinalResults)
	t.observation.FinalFiltered += nonNegative(observation.FinalFiltered)
	t.observation.RerankFallback = t.observation.RerankFallback || observation.RerankFallback
}

func (t *RetrievalTracker) RetrievalSnapshot() RetrievalObservation {
	if t == nil {
		return RetrievalObservation{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observation
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
type retrievalObserverContextKey struct{}

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

func WithRetrievalObserver(ctx context.Context, observer RetrievalObserver) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	if existing := RetrievalObserverFromContext(ctx); existing != nil {
		observer = combinedRetrievalObserver{first: existing, second: observer}
	}
	return context.WithValue(ctx, retrievalObserverContextKey{}, observer)
}

type combinedRetrievalObserver struct {
	first  RetrievalObserver
	second RetrievalObserver
}

func (o combinedRetrievalObserver) ObserveRetrieval(observation RetrievalObservation) {
	o.first.ObserveRetrieval(observation)
	o.second.ObserveRetrieval(observation)
}

func (o combinedRetrievalObserver) RetrievalSnapshot() RetrievalObservation {
	var result RetrievalObservation
	if first, ok := o.first.(RetrievalSnapshotter); ok {
		result = mergeRetrievalObservation(result, first.RetrievalSnapshot())
	}
	if second, ok := o.second.(RetrievalSnapshotter); ok {
		result = mergeRetrievalObservation(result, second.RetrievalSnapshot())
	}
	return result
}

func mergeRetrievalObservation(current, next RetrievalObservation) RetrievalObservation {
	current.VectorCandidates += nonNegative(next.VectorCandidates)
	current.KeywordCandidates += nonNegative(next.KeywordCandidates)
	current.KeywordAfterThreshold += nonNegative(next.KeywordAfterThreshold)
	current.KeywordRejected += nonNegative(next.KeywordRejected)
	current.SummaryCandidates += nonNegative(next.SummaryCandidates)
	current.GraphCandidates += nonNegative(next.GraphCandidates)
	current.DeduplicatedCandidates += nonNegative(next.DeduplicatedCandidates)
	current.RerankBefore += nonNegative(next.RerankBefore)
	current.RerankAfter += nonNegative(next.RerankAfter)
	current.FinalResults += nonNegative(next.FinalResults)
	current.FinalFiltered += nonNegative(next.FinalFiltered)
	current.RerankFallback = current.RerankFallback || next.RerankFallback
	return current
}

func RetrievalObserverFromContext(ctx context.Context) RetrievalObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(retrievalObserverContextKey{}).(RetrievalObserver)
	return observer
}

func ObserveRetrieval(ctx context.Context, observation RetrievalObservation) {
	if observer := RetrievalObserverFromContext(ctx); observer != nil {
		observer.ObserveRetrieval(observation)
	}
}

func RetrievalSnapshotFromContext(ctx context.Context) (RetrievalObservation, bool) {
	observer := RetrievalObserverFromContext(ctx)
	snapshotter, ok := observer.(RetrievalSnapshotter)
	if !ok {
		return RetrievalObservation{}, false
	}
	return snapshotter.RetrievalSnapshot(), true
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
