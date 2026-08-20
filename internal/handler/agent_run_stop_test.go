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
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type cancelTreeStoreStub struct {
	agentrun.Store
	childIDs []string
	called   bool
}

func (s *cancelTreeStoreStub) CancelTree(context.Context, string, int64) ([]string, error) {
	s.called = true
	return s.childIDs, nil
}

// blockingAgentAnswererStub simulates a real Agent run that keeps working
// until its execution context is canceled: it emits run_started, then blocks
// on ctx.Done() and reports run_canceled, mirroring the Agent Engine behavior
// when the stop endpoint cancels the run context.
type blockingAgentAnswererStub struct {
	started chan struct{}
	runID   string
}

func (s *blockingAgentAnswererStub) AnswerWithEvents(ctx context.Context, _ int64, request agentservice.ChatRequest, emit agentruntime.EventSink) (agentservice.Response, error) {
	s.runID = request.RunID
	if err := emit(agent.Event{ID: "event-1", RunID: request.RunID, Type: agent.EventRunStarted, Data: map[string]any{"status": "running"}, CreatedAt: time.Now().UTC()}); err != nil {
		return agentservice.Response{}, err
	}
	close(s.started)
	<-ctx.Done()
	if err := emit(agent.Event{ID: "event-2", RunID: request.RunID, Type: agent.EventRunCanceled, Data: map[string]any{"error": "请求已取消。"}, CreatedAt: time.Now().UTC()}); err != nil {
		return agentservice.Response{}, err
	}
	return agentservice.Response{RunID: request.RunID, Status: agent.RunCanceled}, ctx.Err()
}

func TestAgentRunStopCancelsRunningStream(t *testing.T) {
	hub := agentstream.NewHub()
	stub := &blockingAgentAnswererStub{started: make(chan struct{})}
	streamEndpoint := handler.NewKnowledgeBaseAgentChatStreamWithHub(stub, nil, 64*1024, nil, hub)
	stopEndpoint := handler.NewAgentRunStop(hub)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		streamEndpoint.ServeHTTP(response, request)
	}()

	select {
	case <-stub.started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent run did not start")
	}

	stopResponse := httptest.NewRecorder()
	stopRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs/"+stub.runID+"/stop", nil)
	stopRequest.SetPathValue("id", "7")
	stopRequest.SetPathValue("runID", stub.runID)
	stopEndpoint.ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", stopResponse.Code)
	}
	if !strings.Contains(stopResponse.Body.String(), `"status":"canceled"`) {
		t.Fatalf("stop body = %q, want canceled status", stopResponse.Body.String())
	}

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not finish after stop")
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: run_canceled\n") {
		t.Fatalf("stream body = %q, want run_canceled event", body)
	}
	if strings.Contains(body, "event: run_finished\n") {
		t.Fatal("stopped stream must not write run_finished event")
	}
	if strings.Contains(body, "event: error\n") {
		t.Fatal("stopped stream must not write generic error event")
	}
}

func TestAgentRunStopScopesByKnowledgeBase(t *testing.T) {
	hub := agentstream.NewHub()
	stub := &blockingAgentAnswererStub{started: make(chan struct{})}
	streamEndpoint := handler.NewKnowledgeBaseAgentChatStreamWithHub(stub, nil, 64*1024, nil, hub)
	stopEndpoint := handler.NewAgentRunStop(hub)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		streamEndpoint.ServeHTTP(response, request)
	}()

	select {
	case <-stub.started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent run did not start")
	}

	// A different knowledge base must not be able to stop this run.
	wrongResponse := httptest.NewRecorder()
	wrongRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/8/agent-runs/"+stub.runID+"/stop", nil)
	wrongRequest.SetPathValue("id", "8")
	wrongRequest.SetPathValue("runID", stub.runID)
	stopEndpoint.ServeHTTP(wrongResponse, wrongRequest)
	if wrongResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong knowledge base stop status = %d, want 404", wrongResponse.Code)
	}

	// The correct knowledge base can still stop it afterwards.
	stopResponse := httptest.NewRecorder()
	stopRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs/"+stub.runID+"/stop", nil)
	stopRequest.SetPathValue("id", "7")
	stopRequest.SetPathValue("runID", stub.runID)
	stopEndpoint.ServeHTTP(stopResponse, stopRequest)
	if stopResponse.Code != http.StatusOK {
		t.Fatalf("stop status = %d, want 200", stopResponse.Code)
	}

	select {
	case <-streamDone:
	case <-time.After(5 * time.Second):
		t.Fatal("agent stream did not finish after stop")
	}
}

func TestAgentRunStopCancelsWaitingParentAndChildren(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("parent-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.Start("child-1", 7); err != nil {
		t.Fatal(err)
	}
	parentCanceled := make(chan struct{})
	childCanceled := make(chan struct{})
	if err := hub.RegisterCancel("parent-1", func() { close(parentCanceled) }); err != nil {
		t.Fatal(err)
	}
	if err := hub.RegisterCancel("child-1", func() { close(childCanceled) }); err != nil {
		t.Fatal(err)
	}
	store := &cancelTreeStoreStub{childIDs: []string{"child-1"}}
	endpoint := handler.NewAgentRunStopWithStore(hub, store)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs/parent-1/stop", nil)
	request.SetPathValue("id", "7")
	request.SetPathValue("runID", "parent-1")
	response := httptest.NewRecorder()
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !store.called {
		t.Fatalf("status=%d store_called=%v body=%q", response.Code, store.called, response.Body.String())
	}
	select {
	case <-parentCanceled:
	case <-time.After(time.Second):
		t.Fatal("parent cancel was not propagated")
	}
	select {
	case <-childCanceled:
	case <-time.After(time.Second):
		t.Fatal("child cancel was not propagated")
	}
}

func TestAgentRunStopRejectsInvalidInput(t *testing.T) {
	hub := agentstream.NewHub()
	stopEndpoint := handler.NewAgentRunStop(hub)

	methodResponse := httptest.NewRecorder()
	methodRequest := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/agent-runs/some-run/stop", nil)
	stopEndpoint.ServeHTTP(methodResponse, methodRequest)
	if methodResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET stop status = %d, want 405", methodResponse.Code)
	}

	missingResponse := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs//stop", nil)
	missingRequest.SetPathValue("id", "7")
	stopEndpoint.ServeHTTP(missingResponse, missingRequest)
	if missingResponse.Code != http.StatusBadRequest {
		t.Fatalf("empty run ID status = %d, want 400", missingResponse.Code)
	}

	unknownResponse := httptest.NewRecorder()
	unknownRequest := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs/unknown-run/stop", nil)
	unknownRequest.SetPathValue("id", "7")
	unknownRequest.SetPathValue("runID", "unknown-run")
	stopEndpoint.ServeHTTP(unknownResponse, unknownRequest)
	if unknownResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown run status = %d, want 404", unknownResponse.Code)
	}
}
