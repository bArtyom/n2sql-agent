package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentEventStoreStub struct {
	events []agentstream.Event
}

func (s agentEventStoreStub) Append(context.Context, agentrun.Run, agentstream.Event) error {
	return nil
}
func (s agentEventStoreStub) List(context.Context, string, int64) ([]agentstream.Event, error) {
	return s.events, nil
}

func TestAgentRunStreamReplaysPersistedEventsWhenHubIsEmpty(t *testing.T) {
	endpoint := handler.NewAgentRunStreamWithStore(agentstream.NewHub(), agentEventStoreStub{events: []agentstream.Event{
		{ID: "event-1", RunID: "run-1", Type: "message_delta", Data: map[string]string{"content": "持久化答案"}},
		{ID: "event-2", RunID: "run-1", Type: "run_finished", Data: map[string]string{"answer": "持久化答案"}},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/stream", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "持久化答案") || !strings.Contains(response.Body.String(), "event: run_finished") {
		t.Fatalf("body = %q, want persisted SSE events", response.Body.String())
	}
}
