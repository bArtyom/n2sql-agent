package metrics

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	AgentOutcomeSucceeded = "succeeded"
	AgentOutcomeFailed    = "failed"
	AgentOutcomeCanceled  = "canceled"
	AgentOutcomeTimeout   = "timeout"

	WorkerStatusStarted            = "processing"
	WorkerStatusSucceeded          = "succeeded"
	WorkerStatusFailed             = "failed"
	WorkerStatusCanceled           = "canceled"
	WorkerStatusClaimFailed        = "claim_failed"
	WorkerStatusStatusUpdateFailed = "status_update_failed"
	WorkerStatusRetryScheduled     = "retry_scheduled"
	WorkerStatusDeadLetter         = "dead_letter"

	A2AStatusSubmitted = "submitted"
	A2AStatusStarted   = "working"
	A2AStatusCompleted = "completed"
	A2AStatusFailed    = "failed"
)

type HTTPObservation struct {
	StatusCode int
	Duration   time.Duration
}

type AgentObservation struct {
	Outcome      string
	Duration     time.Duration
	Steps        int
	ToolCalls    int
	ToolFailures int
	TotalTokens  int
}

type WorkerObservation struct {
	Status   string
	Duration time.Duration
}

// Registry stores process-local metrics. A scrape or process restart does not
// persist these values; a later Prometheus integration can provide retention.
type Registry struct {
	httpRequestsTotal       atomic.Uint64
	httpRequests4xxTotal    atomic.Uint64
	httpRequests5xxTotal    atomic.Uint64
	httpDurationMSTotal     atomic.Uint64
	agentRunsTotal          atomic.Uint64
	agentRunsSucceeded      atomic.Uint64
	agentRunsFailed         atomic.Uint64
	agentRunsCanceled       atomic.Uint64
	agentRunsTimeout        atomic.Uint64
	agentStepsTotal         atomic.Uint64
	agentToolCallsTotal     atomic.Uint64
	agentToolFailuresTotal  atomic.Uint64
	agentTokensTotal        atomic.Uint64
	agentDurationMSTotal    atomic.Uint64
	workerTasksStarted      atomic.Uint64
	workerTasksSucceeded    atomic.Uint64
	workerTasksFailed       atomic.Uint64
	workerTasksCanceled     atomic.Uint64
	workerClaimFailures     atomic.Uint64
	workerStatusUpdateFails atomic.Uint64
	workerRetries           atomic.Uint64
	workerDeadLetters       atomic.Uint64
	workerDurationMSTotal   atomic.Uint64
	a2aTasksSubmitted       atomic.Uint64
	a2aTasksStarted         atomic.Uint64
	a2aTasksCompleted       atomic.Uint64
	a2aTasksFailed          atomic.Uint64
	a2aTaskDurationMSTotal  atomic.Uint64
}

func New() *Registry { return &Registry{} }

func (r *Registry) ObserveHTTP(observation HTTPObservation) {
	if r == nil {
		return
	}
	r.httpRequestsTotal.Add(1)
	switch {
	case observation.StatusCode >= http.StatusInternalServerError:
		r.httpRequests5xxTotal.Add(1)
	case observation.StatusCode >= http.StatusBadRequest:
		r.httpRequests4xxTotal.Add(1)
	}
	r.httpDurationMSTotal.Add(durationMilliseconds(observation.Duration))
}

func (r *Registry) ObserveAgent(observation AgentObservation) {
	if r == nil {
		return
	}
	r.agentRunsTotal.Add(1)
	switch observation.Outcome {
	case AgentOutcomeSucceeded:
		r.agentRunsSucceeded.Add(1)
	case AgentOutcomeCanceled:
		r.agentRunsCanceled.Add(1)
	case AgentOutcomeTimeout:
		r.agentRunsTimeout.Add(1)
	default:
		r.agentRunsFailed.Add(1)
	}
	r.agentStepsTotal.Add(nonNegativeInt(observation.Steps))
	r.agentToolCallsTotal.Add(nonNegativeInt(observation.ToolCalls))
	r.agentToolFailuresTotal.Add(nonNegativeInt(observation.ToolFailures))
	r.agentTokensTotal.Add(nonNegativeInt(observation.TotalTokens))
	r.agentDurationMSTotal.Add(durationMilliseconds(observation.Duration))
}

func (r *Registry) ObserveWorker(observation WorkerObservation) {
	if r == nil {
		return
	}
	switch observation.Status {
	case WorkerStatusStarted:
		r.workerTasksStarted.Add(1)
	case WorkerStatusSucceeded:
		r.workerTasksSucceeded.Add(1)
		r.workerDurationMSTotal.Add(durationMilliseconds(observation.Duration))
	case WorkerStatusFailed:
		r.workerTasksFailed.Add(1)
		r.workerDurationMSTotal.Add(durationMilliseconds(observation.Duration))
	case WorkerStatusCanceled:
		r.workerTasksCanceled.Add(1)
		r.workerDurationMSTotal.Add(durationMilliseconds(observation.Duration))
	case WorkerStatusClaimFailed:
		r.workerClaimFailures.Add(1)
	case WorkerStatusStatusUpdateFailed:
		r.workerStatusUpdateFails.Add(1)
	case WorkerStatusRetryScheduled:
		r.workerRetries.Add(1)
	case WorkerStatusDeadLetter:
		r.workerDeadLetters.Add(1)
	}
}

func (r *Registry) ObserveWorkerDuration(duration time.Duration) {
	if r == nil {
		return
	}
	r.workerDurationMSTotal.Add(durationMilliseconds(duration))
}

// ObserveA2ATask records one A2A task lifecycle event.
func (r *Registry) ObserveA2ATask(status string, duration time.Duration) {
	if r == nil {
		return
	}
	switch status {
	case A2AStatusSubmitted:
		r.a2aTasksSubmitted.Add(1)
	case A2AStatusStarted:
		r.a2aTasksStarted.Add(1)
	case A2AStatusCompleted:
		r.a2aTasksCompleted.Add(1)
		r.a2aTaskDurationMSTotal.Add(durationMilliseconds(duration))
	case A2AStatusFailed:
		r.a2aTasksFailed.Add(1)
		r.a2aTaskDurationMSTotal.Add(durationMilliseconds(duration))
	}
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		if err := r.write(w); err != nil {
			slog.Error("metrics_write_failed", "error", err)
			return
		}
	})
}

func (r *Registry) write(w io.Writer) error {
	lines := []struct {
		name  string
		value uint64
	}{
		{"http_requests_total", r.httpRequestsTotal.Load()},
		{"http_requests_4xx_total", r.httpRequests4xxTotal.Load()},
		{"http_requests_5xx_total", r.httpRequests5xxTotal.Load()},
		{"http_request_duration_ms_total", r.httpDurationMSTotal.Load()},
		{"agent_runs_total", r.agentRunsTotal.Load()},
		{"agent_runs_succeeded_total", r.agentRunsSucceeded.Load()},
		{"agent_runs_failed_total", r.agentRunsFailed.Load()},
		{"agent_runs_canceled_total", r.agentRunsCanceled.Load()},
		{"agent_runs_timeout_total", r.agentRunsTimeout.Load()},
		{"agent_steps_total", r.agentStepsTotal.Load()},
		{"agent_tool_calls_total", r.agentToolCallsTotal.Load()},
		{"agent_tool_failures_total", r.agentToolFailuresTotal.Load()},
		{"agent_tokens_total", r.agentTokensTotal.Load()},
		{"agent_run_duration_ms_total", r.agentDurationMSTotal.Load()},
		{"worker_tasks_started_total", r.workerTasksStarted.Load()},
		{"worker_tasks_succeeded_total", r.workerTasksSucceeded.Load()},
		{"worker_tasks_failed_total", r.workerTasksFailed.Load()},
		{"worker_tasks_canceled_total", r.workerTasksCanceled.Load()},
		{"worker_task_claim_failures_total", r.workerClaimFailures.Load()},
		{"worker_task_status_update_failures_total", r.workerStatusUpdateFails.Load()},
		{"worker_retries_total", r.workerRetries.Load()},
		{"worker_dead_letters_total", r.workerDeadLetters.Load()},
		{"worker_task_duration_ms_total", r.workerDurationMSTotal.Load()},
		{"a2a_tasks_submitted_total", r.a2aTasksSubmitted.Load()},
		{"a2a_tasks_started_total", r.a2aTasksStarted.Load()},
		{"a2a_tasks_completed_total", r.a2aTasksCompleted.Load()},
		{"a2a_tasks_failed_total", r.a2aTasksFailed.Load()},
		{"a2a_task_duration_ms_total", r.a2aTaskDurationMSTotal.Load()},
	}
	for _, line := range lines {
		if _, err := fmt.Fprintf(w, "%s %d\n", line.name, line.value); err != nil {
			return fmt.Errorf("write metric %s: %w", line.name, err)
		}
	}
	return nil
}

func (r *Registry) Middleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		response := &statusWriter{ResponseWriter: w}
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			r.ObserveHTTP(HTTPObservation{StatusCode: status, Duration: time.Since(started)})
		}()
		next.ServeHTTP(response, request)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
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
	return w.ResponseWriter.Write(data)
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

func durationMilliseconds(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	return uint64(duration.Milliseconds())
}

func nonNegativeInt(value int) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}
