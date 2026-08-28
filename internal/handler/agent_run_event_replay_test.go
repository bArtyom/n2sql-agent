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

type selectedStreamBridgeStub struct {
	events []agentstream.Event
}

func (selectedStreamBridgeStub) Publish(context.Context, agentrun.Run, agentstream.Event) error {
	return nil
}

func (s selectedStreamBridgeStub) Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return s.events, closedAgentEvents(), func() {}, true, nil
}

func (s selectedStreamBridgeStub) SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return s.events, closedAgentEvents(), func() {}, true, nil
}

func closedAgentEvents() <-chan agentstream.Event {
	channel := make(chan agentstream.Event)
	close(channel)
	return channel
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

func TestAgentRunStreamUsesSelectedStreamBridge(t *testing.T) {
	endpoint := handler.NewAgentRunStreamWithBridge(selectedStreamBridgeStub{events: []agentstream.Event{
		{ID: "bridge-event-1", RunID: "run-bridge", Type: "run_finished", Data: map[string]string{"answer": "bridge"}},
	}}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-bridge/stream", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-bridge")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "bridge-event-1") {
		t.Fatalf("status=%d body=%q, want selected bridge event", response.Code, response.Body.String())
	}
}

type durableGapEventStoreStub struct{}

func (durableGapEventStoreStub) Append(context.Context, agentrun.Run, agentstream.Event) error {
	return nil
}

func (durableGapEventStoreStub) List(context.Context, string, int64) ([]agentstream.Event, error) {
	return nil, nil
}

func (durableGapEventStoreStub) Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, func() {}, false, agentstream.ErrEventGap
}

func (durableGapEventStoreStub) SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, func() {}, false, agentstream.ErrEventGap
}

func (durableGapEventStoreStub) SequenceByEventID(context.Context, string, int64, string) (int64, error) {
	return 3, nil
}

func (durableGapEventStoreStub) ListAfter(context.Context, string, int64, int64, int) ([]agentstream.Event, error) {
	return []agentstream.Event{
		{ID: "event-4", Seq: 4, RunID: "run-1", Type: "message_delta", Data: map[string]string{"content": "恢复答案"}},
		{ID: "event-5", Seq: 5, RunID: "run-1", Type: "run_finished", Data: map[string]string{"answer": "恢复答案"}},
	}, nil
}

type gapStreamBridgeStub struct{}

func (gapStreamBridgeStub) Publish(context.Context, agentrun.Run, agentstream.Event) error {
	return nil
}

func (gapStreamBridgeStub) Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, func() {}, false, agentstream.ErrEventGap
}

func (gapStreamBridgeStub) SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return nil, nil, func() {}, false, agentstream.ErrEventGap
}

func TestAgentRunStreamRecoversRedisGapFromDurableSequence(t *testing.T) {
	endpoint := handler.NewAgentRunStreamWithBridge(gapStreamBridgeStub{}, durableGapEventStoreStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/run-1/stream", nil)
	request.Header.Set("Last-Event-ID", "event-3")
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: message_delta") || !strings.Contains(body, "恢复答案") || !strings.Contains(body, "event: run_finished") {
		t.Fatalf("body = %q, want durable events after cursor", body)
	}
	if strings.Contains(body, "event: gap") {
		t.Fatalf("body = %q, did not expect replay gap", body)
	}
}
