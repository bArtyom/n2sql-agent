package ops_test

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/ops"
)

func TestTraceStageRequiresTraceID(t *testing.T) {
	// The helper is intentionally a no-op without a correlation ID. This keeps
	// background logs from pretending to belong to an unrelated request.
	ops.TraceStage(context.Background(), "answer_started")
	ops.TraceStage(ops.WithTraceID(context.Background(), "trace-1"), "answer_started", "steps", 1)
}
