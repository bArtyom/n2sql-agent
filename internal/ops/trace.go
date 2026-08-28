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

// TraceIdentity ties operational stages to a durable run and one concrete
// Worker execution without putting prompts, tool arguments, or secrets into
// logs. The fields are intentionally scalar and bounded for safe aggregation.
type TraceIdentity struct {
	TraceID     string
	RunID       string
	TaskID      string
	ExecutionID string
	Attempt     int
}

type traceIdentityContextKey struct{}

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
	if id == "" {
		identity, _ := ctx.Value(traceIdentityContextKey{}).(TraceIdentity)
		return identity.TraceID
	}
	return id
}

func WithTraceIdentity(ctx context.Context, identity TraceIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity.TraceID = boundedTraceValue(identity.TraceID)
	identity.RunID = boundedTraceValue(identity.RunID)
	identity.TaskID = boundedTraceValue(identity.TaskID)
	identity.ExecutionID = boundedTraceValue(identity.ExecutionID)
	return context.WithValue(ctx, traceIdentityContextKey{}, identity)
}

func TraceIdentityFromContext(ctx context.Context) TraceIdentity {
	if ctx == nil {
		return TraceIdentity{}
	}
	identity, _ := ctx.Value(traceIdentityContextKey{}).(TraceIdentity)
	return identity
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
	identity := TraceIdentityFromContext(ctx)
	if identity.RunID != "" {
		fields = append(fields, "run_id", identity.RunID)
	}
	if identity.TaskID != "" {
		fields = append(fields, "task_id", identity.TaskID)
	}
	if identity.ExecutionID != "" {
		fields = append(fields, "execution_id", identity.ExecutionID)
	}
	if identity.Attempt > 0 {
		fields = append(fields, "attempt", identity.Attempt)
	}
	attrs = boundedTraceAttrs(attrs)
	fields = append(fields, attrs...)
	slog.InfoContext(ctx, "trace_stage", fields...)
}

func boundedTraceAttrs(attrs []any) []any {
	if len(attrs) == 0 {
		return nil
	}
	result := make([]any, 0, len(attrs))
	for index := 0; index < len(attrs); index++ {
		value := attrs[index]
		if key, ok := value.(string); ok && index+1 < len(attrs) {
			result = append(result, boundedTraceValue(key))
			index++
			result = append(result, boundedTraceAttrValue(attrs[index]))
			continue
		}
		result = append(result, boundedTraceAttrValue(value))
	}
	return result
}

func boundedTraceAttrValue(value any) any {
	switch value := value.(type) {
	case string:
		return boundedTraceValue(value)
	case error:
		return boundedTraceValue(value.Error())
	default:
		return value
	}
}

func boundedTraceValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
