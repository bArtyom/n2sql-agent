package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const HeaderName = "X-Request-ID"

type contextKey struct{}

var fallbackSequence atomic.Uint64

func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

func Valid(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for index := 0; index < len(id); index++ {
		character := id[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func New() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), fallbackSequence.Add(1))
}

func NewMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(HeaderName))
		if !Valid(id) {
			id = New()
		}
		ctx := WithContext(r.Context(), id)
		w.Header().Set(HeaderName, id)
		started := time.Now()
		response := &statusWriter{ResponseWriter: w}
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(ctx, "http_request",
				"request_id", id,
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"bytes", response.bytes,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		}()
		next.ServeHTTP(response, r.WithContext(ctx))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	count, err := w.ResponseWriter.Write(data)
	w.bytes += count
	return count, err
}

func (w *statusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
