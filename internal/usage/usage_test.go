package usage_test

import (
	"context"
	"testing"

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
