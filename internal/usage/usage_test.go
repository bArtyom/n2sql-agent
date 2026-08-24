package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/usage"
)

type observerStub struct{}

func (observerStub) ObserveChatTokens(usage.TokenUsage)      {}
func (observerStub) ObserveEmbeddingTokens(usage.TokenUsage) {}

func TestTokenUsageEffectiveTotalFallsBackToComponentCounts(t *testing.T) {
	if got := (usage.TokenUsage{PromptTokens: 11, CompletionTokens: 3}).EffectiveTotal(); got != 14 {
		t.Fatalf("EffectiveTotal() = %d, want 14", got)
	}
	if got := (usage.TokenUsage{PromptTokens: -2, CompletionTokens: 3}).EffectiveTotal(); got != 3 {
		t.Fatalf("EffectiveTotal() with negative input = %d, want 3", got)
	}
}

func TestCallTrackerAggregatesBoundedModelObservations(t *testing.T) {
	tracker := usage.NewCallTracker()
	ctx := usage.WithCallObserver(context.Background(), tracker)
	usage.ObserveModelCall(ctx, usage.ModelCallObservation{
		Kind: usage.ModelKindChat, Model: "chat-model", Success: true,
		Duration: 1500 * time.Millisecond,
		Usage:    usage.TokenUsage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
	})
	usage.ObserveModelCall(ctx, usage.ModelCallObservation{
		Kind: usage.ModelKindChat, Model: "chat-model", Success: false,
		Duration: 500 * time.Millisecond,
	})
	snapshot := tracker.ModelCallSnapshot(usage.ModelKindChat)
	if snapshot.Calls != 2 || snapshot.Failures != 1 || snapshot.DurationMS != 2000 || snapshot.TotalTokens != 10 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestObserverContextRoundTrip(t *testing.T) {
	observer := observerStub{}
	ctx := usage.WithObserver(context.Background(), observer)
	if got := usage.ObserverFromContext(ctx); got == nil {
		t.Fatal("ObserverFromContext() = nil, want observer")
	}
	if usage.ObserverFromContext(context.Background()) != nil {
		t.Fatal("ObserverFromContext() without observer is non-nil")
	}
}

func TestQueryRewriteTrackerAggregatesParallelObservations(t *testing.T) {
	tracker := usage.NewQueryRewriteTracker()
	tracker.ObserveQueryRewrite(usage.QueryRewriteObservation{Enabled: true, Applied: true, VariantCount: 2})
	tracker.ObserveQueryRewrite(usage.QueryRewriteObservation{Enabled: true, Fallback: true})
	got := tracker.QueryRewriteSnapshot()
	if !got.Enabled || !got.Applied || !got.Fallback || got.VariantCount != 2 {
		t.Fatalf("query rewrite snapshot = %#v", got)
	}
}
