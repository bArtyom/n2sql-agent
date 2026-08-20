package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentRunReaderStub struct {
	run      agentrun.Run
	err      error
	children []agentrun.Run
}

func (s agentRunReaderStub) Get(context.Context, string, int64) (agentrun.Run, error) {
	return s.run, s.err
}

func (s agentRunReaderStub) ListChildren(context.Context, int64, int64) ([]agentrun.Run, error) {
	return s.children, nil
}

func TestAgentRunStatusDoesNotExposeRequestSnapshot(t *testing.T) {
	created := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	endpoint := handler.NewAgentRunStatus(agentRunReaderStub{run: agentrun.Run{
		RunID:           "run-1",
		Status:          agentrun.StatusRunning,
		AttemptCount:    2,
		FailureCategory: agent.FailureModel,
		Request:         []byte(`{"message":"secret question"}`),
		Response:        []byte(`{"answer":"恢复后的答案","status":"succeeded"}`),
		CreatedAt:       created,
		UpdatedAt:       created,
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, `"status":"running"`) || !strings.Contains(body, `"attempt_count":2`) {
		t.Fatalf("body = %q, want run metadata", body)
	}
	if !strings.Contains(body, `"answer":"恢复后的答案"`) {
		t.Fatalf("body = %q, want persisted response", body)
	}
	if !strings.Contains(body, `"failure_category":"model_failed"`) {
		t.Fatalf("body = %q, want failure category", body)
	}
	if strings.Contains(body, "secret question") || strings.Contains(body, "request") {
		t.Fatalf("body = %q, must not expose request snapshot", body)
	}
}

func TestAgentRunStatusScopesNotFound(t *testing.T) {
	endpoint := handler.NewAgentRunStatus(agentRunReaderStub{err: agentrun.ErrRunNotFound})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/8/agent-runs/run-1", nil)
	request.SetPathValue("id", "8")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestAgentRunStatusIncludesSafeChildSummaries(t *testing.T) {
	created := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	endpoint := handler.NewAgentRunStatus(agentRunReaderStub{
		run:      agentrun.Run{ID: 10, RunID: "parent-1", KnowledgeBaseID: 7, Status: agentrun.StatusWaitingChildren, UpdatedAt: created},
		children: []agentrun.Run{{RunID: "child-1", Status: agentrun.StatusRunning, AttemptCount: 1, ErrorMessage: "secret provider detail", UpdatedAt: created}},
	})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/parent-1", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "parent-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)
	body := response.Body.String()
	if !strings.Contains(body, `"status":"waiting_children"`) || !strings.Contains(body, `"run_id":"child-1"`) {
		t.Fatalf("body = %q, want parent and child status", body)
	}
	if strings.Contains(body, "secret provider detail") {
		t.Fatalf("body = %q, must not expose child error details", body)
	}
}
