package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/metrics"
)

func TestRegistryExposesHTTPAgentAndWorkerMetrics(t *testing.T) {
	registry := metrics.New()
	registry.ObserveHTTP(metrics.HTTPObservation{StatusCode: http.StatusOK, Duration: 120 * time.Millisecond})
	registry.ObserveHTTP(metrics.HTTPObservation{StatusCode: http.StatusBadRequest, Duration: 30 * time.Millisecond})
	registry.ObserveHTTP(metrics.HTTPObservation{StatusCode: http.StatusBadGateway, Duration: 220 * time.Millisecond})
	registry.ObserveAgent(metrics.AgentObservation{
		Outcome:      metrics.AgentOutcomeSucceeded,
		Duration:     400 * time.Millisecond,
		Steps:        2,
		ToolCalls:    1,
		ToolFailures: 0,
		TotalTokens:  70,
	})
	registry.ObserveAgent(metrics.AgentObservation{
		Outcome:      metrics.AgentOutcomeFailed,
		Duration:     100 * time.Millisecond,
		Steps:        1,
		ToolCalls:    1,
		ToolFailures: 1,
		TotalTokens:  20,
	})
	registry.ObserveWorker(metrics.WorkerObservation{Status: metrics.WorkerStatusStarted, Duration: 10 * time.Millisecond})
	registry.ObserveWorker(metrics.WorkerObservation{Status: metrics.WorkerStatusSucceeded, Duration: 50 * time.Millisecond})
	registry.ObserveWorker(metrics.WorkerObservation{Status: metrics.WorkerStatusFailed, Duration: 20 * time.Millisecond})
	registry.ObserveWorker(metrics.WorkerObservation{Status: metrics.WorkerStatusClaimFailed})

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; version=0.0.4" {
		t.Fatalf("content type = %q, want Prometheus text content type", contentType)
	}
	for _, line := range []string{
		"http_requests_total 3",
		"http_requests_4xx_total 1",
		"http_requests_5xx_total 1",
		"http_request_duration_ms_total 370",
		"agent_runs_total 2",
		"agent_runs_succeeded_total 1",
		"agent_runs_failed_total 1",
		"agent_steps_total 3",
		"agent_tool_calls_total 2",
		"agent_tool_failures_total 1",
		"agent_tokens_total 90",
		"agent_run_duration_ms_total 500",
		"worker_tasks_started_total 1",
		"worker_tasks_succeeded_total 1",
		"worker_tasks_failed_total 1",
		"worker_task_claim_failures_total 1",
		"worker_task_duration_ms_total 70",
	} {
		if !strings.Contains(response.Body.String(), line+"\n") {
			t.Fatalf("metrics body = %q, want line %q", response.Body.String(), line)
		}
	}
}

func TestMiddlewareRecordsStatusAndPreservesFlusher(t *testing.T) {
	registry := metrics.New()
	endpoint := registry.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapped response writer does not implement http.Flusher")
		}
		w.WriteHeader(http.StatusBadGateway)
		flusher.Flush()
	}))
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/agent-chat/stream", nil))

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if !response.Flushed {
		t.Fatal("response was not flushed")
	}
	metricsResponse := httptest.NewRecorder()
	registry.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), "http_requests_5xx_total 1\n") {
		t.Fatalf("metrics body = %q, want 5xx counter", metricsResponse.Body.String())
	}
}
