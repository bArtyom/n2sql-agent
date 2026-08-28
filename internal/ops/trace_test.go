package ops_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/ops"
)

func TestTraceStageRequiresTraceID(t *testing.T) {
	// The helper is intentionally a no-op without a correlation ID. This keeps
	// background logs from pretending to belong to an unrelated request.
	ops.TraceStage(context.Background(), "answer_started")
	ops.TraceStage(ops.WithTraceID(context.Background(), "trace-1"), "answer_started", "steps", 1)
}

func TestTraceIdentityAddsBoundedRunFields(t *testing.T) {
	longID := "run-" + strings.Repeat("x", 300)
	ctx := ops.WithTraceIdentity(context.Background(), ops.TraceIdentity{
		TraceID: longID, RunID: "run-1", TaskID: "child-1", ExecutionID: "exec-1", Attempt: 2,
	})
	if got := ops.TraceID(ctx); len(got) != 256 {
		t.Fatalf("TraceID() length = %d, want bounded length 256", len(got))
	}
	identity := ops.TraceIdentityFromContext(ctx)
	if len(identity.TraceID) != 256 || identity.RunID != "run-1" || identity.Attempt != 2 {
		t.Fatalf("trace identity = %#v, want bounded trace/run metadata", identity)
	}
	if got := ops.TraceIdentityFromContext(context.Background()); got != (ops.TraceIdentity{}) {
		t.Fatalf("empty trace identity = %#v", got)
	}
}
