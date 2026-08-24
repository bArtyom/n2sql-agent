package modelruntime

import (
	"context"
	"errors"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

func observeModelCall(ctx context.Context, kind usage.ModelKind, provider, model string, started time.Time, responseUsage *modelclient.TokenUsage, err error) {
	observation := usage.ModelCallObservation{
		Kind: kind, Provider: provider, Model: model,
		Duration: time.Since(started), Success: err == nil,
		ErrorClass: modelErrorClass(err),
	}
	if responseUsage != nil {
		observation.Usage = *responseUsage
	}
	usage.ObserveModelCall(ctx, observation)
}

func modelErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "provider_error"
}
