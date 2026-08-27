package ops_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/ops"
)

type statusError int

func (e statusError) Error() string   { return "provider error" }
func (e statusError) HTTPStatus() int { return int(e) }

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass ops.FailureClass
		retryable bool
	}{
		{name: "canceled", err: context.Canceled, wantClass: ops.FailureCanceled},
		{name: "timeout", err: context.DeadlineExceeded, wantClass: ops.FailureTimeout, retryable: true},
		{name: "rate limit", err: statusError(429), wantClass: ops.FailureRateLimited, retryable: true},
		{name: "auth", err: statusError(401), wantClass: ops.FailureAuthentication},
		{name: "invalid", err: statusError(400), wantClass: ops.FailureInvalidRequest},
		{name: "unavailable", err: statusError(503), wantClass: ops.FailureUnavailable, retryable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ops.ClassifyFailure(test.err)
			if got != test.wantClass || ops.IsRetryableFailure(test.err) != test.retryable {
				t.Fatalf("class=%q retryable=%v, want class=%q retryable=%v", got, ops.IsRetryableFailure(test.err), test.wantClass, test.retryable)
			}
		})
	}
	if ops.ClassifyFailure(errors.New("connection refused")) != ops.FailureUnavailable {
		t.Fatal("connection refused should be unavailable")
	}
}

func TestCircuitBreakerOpensAndAllowsHalfOpenProbe(t *testing.T) {
	breaker := ops.NewCircuitBreaker(2, 20*time.Millisecond)
	if !breaker.Allow("openai", "chat") {
		t.Fatal("first call should be allowed")
	}
	breaker.RecordFailure("openai", "chat")
	breaker.RecordFailure("openai", "chat")
	if breaker.Allow("openai", "chat") {
		t.Fatal("circuit should be open")
	}
	time.Sleep(25 * time.Millisecond)
	if !breaker.Allow("openai", "chat") || breaker.Allow("openai", "chat") {
		t.Fatal("only one half-open probe should be allowed")
	}
	breaker.RecordSuccess("openai", "chat")
	if !breaker.Allow("openai", "chat") {
		t.Fatal("success should close circuit")
	}
}

func TestCircuitBreakerScopesFailuresByProviderAndCapability(t *testing.T) {
	breaker := ops.NewCircuitBreaker(1, time.Minute)

	breaker.RecordFailure("openai", "embedding")

	if breaker.Allow("openai", "embedding") {
		t.Fatal("embedding circuit should be open after its failure limit")
	}
	if !breaker.Allow("openai", "chat") {
		t.Fatal("chat circuit should remain available when embedding is open")
	}
}
