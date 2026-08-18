package agentservice

import (
	"context"
	"testing"
)

func TestAsyncRunContextMarker(t *testing.T) {
	ctx := context.Background()
	if isAsyncRun(ctx) {
		t.Fatal("ordinary context unexpectedly marked async")
	}
	if !isAsyncRun(WithAsyncRun(ctx)) {
		t.Fatal("async context marker was not preserved")
	}
}
