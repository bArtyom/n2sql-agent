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
	if !strings.Contains(body, "id: event-1\n") || !strings.Contains(body, `"version":1`) {
		t.Fatalf("body=%q, want SSE event ID and version", body)
	}
}

func TestAgentRunStreamReplaysChildEvent(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("parent-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{
		ID: "parent-1-child-event-1", RunID: "parent-1", Type: agent.EventChildEvent,
		Data: map[string]any{"child_run_id": "child-1", "child_event_type": "run_started"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "parent-finished", RunID: "parent-1", Type: agent.EventRunFinished}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Finish("parent-1"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/parent-1/stream", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "parent-1")
	response := httptest.NewRecorder()
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)

	if !strings.Contains(response.Body.String(), "event: child_event\n") || !strings.Contains(response.Body.String(), `"child_run_id":"child-1"`) {
		t.Fatalf("body=%q, want replayed child event", response.Body.String())
	}
}

func TestAgentRunStreamReplaysWaitingChildrenEvent(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("parent-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.Publish(agentstream.Event{ID: "parent-1-transport-1", RunID: "parent-1", Type: "waiting_children"}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Publish(agentstream.Event{ID: "parent-1-finished", RunID: "parent-1", Type: "run_finished"}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Finish("parent-1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/parent-1/stream", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "parent-1")
	response := httptest.NewRecorder()
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), "event: waiting_children\n") {
		t.Fatalf("body=%q, want waiting_children event", response.Body.String())
	}
}

func TestAgentRunStreamResumesAfterLastEventID(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	for _, event := range []agent.Event{
		{ID: "event-1", RunID: "run-1", Type: agent.EventRunStarted},
		{ID: "event-2", RunID: "run-1", Type: agent.EventRunFinished},
	} {
		if err := hub.PublishAgent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := hub.Finish("run-1"); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/stream", nil)
	request.Header.Set("Last-Event-ID", "event-1")
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)

	body := response.Body.String()
	if strings.Contains(body, `"id":"event-1"`) || !strings.Contains(body, `"id":"event-2"`) {
		t.Fatalf("body=%q, want only events after event-1", body)
	}
}

func TestAgentRunStreamReturnsGapForExpiredCursor(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishAgent(agent.Event{ID: "event-2", RunID: "run-1", Type: agent.EventRunFinished}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/stream", nil)
	request.Header.Set("Last-Event-ID", "event-1")
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()
	handler.NewAgentRunStream(hub).ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: gap\n") || !strings.Contains(response.Body.String(), "stream_replay_gap") {
		t.Fatalf("status=%d body=%q, want gap event", response.Code, response.Body.String())
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
