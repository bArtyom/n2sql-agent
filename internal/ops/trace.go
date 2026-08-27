package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
)

const TraceHeader = "X-Trace-ID"

type traceContextKey struct{}

func NewTraceID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "trace-unknown"
	}
	return hex.EncodeToString(value[:])
}

func WithTraceID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceContextKey{}, strings.TrimSpace(id))
}

func TraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceContextKey{}).(string)
	return id
}

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(TraceHeader))
		if id == "" || len(id) > 128 {
			id = NewTraceID()
		}
		ctx := WithTraceID(r.Context(), id)
		w.Header().Set(TraceHeader, id)
		logger.DebugContext(ctx, "trace_started", "trace_id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TraceStage records a bounded lifecycle marker. It intentionally stores only
// the stage name and small numeric/status attributes; prompts, documents and
// credentials stay out of operational logs.
func TraceStage(ctx context.Context, stage string, attrs ...any) {
	if strings.TrimSpace(stage) == "" || TraceID(ctx) == "" {
		return
	}
	fields := []any{"trace_id", TraceID(ctx), "stage", stage}
	fields = append(fields, attrs...)
	slog.InfoContext(ctx, "trace_stage", fields...)
}
