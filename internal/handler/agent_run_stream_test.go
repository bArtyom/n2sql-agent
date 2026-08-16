package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

func TestAgentRunStreamReplaysFinishedRun(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "event-1", RunID: "run-1", Type: agent.EventRunStarted}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "event-reasoning", RunID: "run-1", Type: agent.EventReasoningDelta, Data: map[string]any{"content": "先检查资料"}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "event-2", RunID: "run-1", Type: agent.EventRunFinished, Data: map[string]any{"answer": "完成"}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Finish("run-1"); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/stream", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: run_started\n") || !strings.Contains(body, "event: reasoning_delta\n") || !strings.Contains(body, `"content":"先检查资料"`) || !strings.Contains(body, `"answer":"完成"`) {
		t.Fatalf("body=%q, want replayed events", body)
	}
}

func TestAgentRunStreamRejectsWrongKnowledgeBase(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/8/agent-runs/run-1/stream", nil)
	request.SetPathValue("id", "8")
	request.SetPathValue("runID", "run-1")
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusNotFound)
	}
}
