package usage

import "context"

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

type observerContextKey struct{}

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

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
