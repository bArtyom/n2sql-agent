package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentRunAttemptsReaderStub struct {
	run      agentrun.Run
	attempts []agentrun.Attempt
}

func (s agentRunAttemptsReaderStub) Get(context.Context, string, int64) (agentrun.Run, error) {
	return s.run, nil
}

func (s agentRunAttemptsReaderStub) ListAttempts(context.Context, int64) ([]agentrun.Attempt, error) {
	return s.attempts, nil
}

func TestAgentRunAttemptsReturnsRetryHistoryWithoutRequest(t *testing.T) {
	created := time.Date(2026, time.August, 23, 10, 0, 0, 0, time.UTC)
	endpoint := handler.NewAgentRunAttempts(agentRunAttemptsReaderStub{
		run:      agentrun.Run{ID: 9, RunID: "run-1", KnowledgeBaseID: 7, Request: []byte(`{"message":"secret"}`)},
		attempts: []agentrun.Attempt{{AttemptCount: 1, Status: agentrun.StatusRequeued, StopReason: agentrun.StopReasonOrphanRecovered, StartedAt: created}, {AttemptCount: 2, Status: agentrun.StatusRunning, StartedAt: created}},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/attempts", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"requeued"`) || !strings.Contains(response.Body.String(), `"stop_reason":"orphan_recovered"`) || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
