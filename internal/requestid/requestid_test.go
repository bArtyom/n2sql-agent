package requestid_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/requestid"
)

func TestMiddlewarePreservesValidRequestIDAndAddsStructuredFields(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	endpoint := requestid.NewMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := requestid.FromContext(r.Context()); got != "req-client-1" {
			t.Errorf("request ID in context = %q, want req-client-1", got)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set(requestid.HeaderName, "req-client-1")

	endpoint.ServeHTTP(response, request)

	if got := response.Header().Get(requestid.HeaderName); got != "req-client-1" {
		t.Fatalf("response request ID = %q, want req-client-1", got)
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusAccepted)
	}
	for _, field := range []string{"request_id=req-client-1", "method=GET", "path=/health", "status=202", "duration_ms="} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("logs = %q, want field %q", logs.String(), field)
		}
	}
}

func TestMiddlewareGeneratesRequestIDWhenHeaderIsMissingOrInvalid(t *testing.T) {
	for _, header := range []string{"", "bad request id", strings.Repeat("a", 129)} {
		t.Run(header, func(t *testing.T) {
			endpoint := requestid.NewMiddleware(slog.Default(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := requestid.FromContext(r.Context()); got == "" || !requestid.Valid(got) {
					t.Errorf("generated request ID = %q, want valid ID", got)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/health", nil)
			if header != "" {
				request.Header.Set(requestid.HeaderName, header)
			}

			endpoint.ServeHTTP(response, request)

			if response.Code != http.StatusNoContent {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusNoContent)
			}
			if got := response.Header().Get(requestid.HeaderName); got == "" || !requestid.Valid(got) {
				t.Fatalf("response request ID = %q, want valid ID", got)
			}
		})
	}
}

func TestMiddlewarePreservesFlusherAndLogsFailureStatus(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	endpoint := requestid.NewMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped response writer does not implement http.Flusher")
		}
		w.WriteHeader(http.StatusBadGateway)
		flusher.Flush()
	}))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/agent-chat/stream", nil)

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if !response.Flushed {
		t.Fatal("response was not flushed")
	}
	if !strings.Contains(logs.String(), "status=502") {
		t.Fatalf("logs = %q, want status=502", logs.String())
	}
}

func TestFromContextReturnsEmptyForMissingID(t *testing.T) {
	if got := requestid.FromContext(context.Background()); got != "" {
		t.Fatalf("request ID = %q, want empty", got)
	}
}
