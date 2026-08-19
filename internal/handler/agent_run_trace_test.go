package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

type agentRunTraceEventStoreStub struct {
	events []agentstream.Event
}

func (s agentRunTraceEventStoreStub) Append(context.Context, agentrun.Run, agentstream.Event) error {
	return nil
}

func (s agentRunTraceEventStoreStub) List(context.Context, string, int64) ([]agentstream.Event, error) {
	return s.events, nil
}

func TestAgentRunTraceReturnsSafeEventSummaries(t *testing.T) {
	endpoint := NewAgentRunTrace(agentRunTraceEventStoreStub{events: []agentstream.Event{
		{ID: "event-1", RunID: "run-1", Type: "run_started", CreatedAt: time.Unix(1, 0).UTC()},
		{ID: "event-2", RunID: "run-1", Type: "tool_called", StepNumber: 1, Data: map[string]any{
			"tool_name": "knowledge_search", "arguments": `{"query":"年假"}`,
			"secret": "must not be returned",
		}},
		{ID: "event-3", RunID: "run-1", Type: "tool_finished", StepNumber: 1, Data: map[string]any{
			"tool_name": "knowledge_search", "result_summary": "返回 2 条资料",
			"content": "原始工具结果不应返回",
		}},
		{ID: "event-4", RunID: "run-1", Type: "run_finished", Data: map[string]any{
			"answer": "最终答案不通过轨迹接口返回",
		}},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/trace", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	for _, want := range []string{"event-1", "knowledge_search", "返回 2 条资料"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body = %q, want %q", body, want)
		}
	}
	for _, forbidden := range []string{"must not be returned", "原始工具结果不应返回", "最终答案不通过轨迹接口返回", `"arguments"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body = %q, must not contain %q", body, forbidden)
		}
	}
}

func TestAgentRunTraceRejectsInvalidInput(t *testing.T) {
	endpoint := NewAgentRunTrace(agentRunTraceEventStoreStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs//trace", nil)
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
