package modelruntime

import (
	"context"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/ops"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

func observeModelCall(ctx context.Context, kind usage.ModelKind, provider, model string, started time.Time, responseUsage *modelclient.TokenUsage, err error) {
	observation := usage.ModelCallObservation{
		Kind: kind, Provider: provider, Model: model, TraceID: ops.TraceID(ctx),
		Duration: time.Since(started), Success: err == nil,
		ErrorClass: modelErrorClass(err),
	}
	if responseUsage != nil {
		observation.Usage = *responseUsage
	}
	usage.ObserveModelCall(ctx, observation)
}

func modelErrorClass(err error) string {
	return string(ops.ClassifyFailure(err))
}

func observeCircuitBreaker(ctx context.Context, provider, capability, event string) {
	usage.ObserveCircuitBreaker(ctx, usage.CircuitBreakerObservation{
		Provider: provider, Capability: capability, Event: event,
	})
}
