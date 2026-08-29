package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentRunStoreStub struct {
	created agentrun.CreateInput
}

func (s *agentRunStoreStub) Create(_ context.Context, input agentrun.CreateInput) (agentrun.Run, error) {
	s.created = input
	return agentrun.Run{RunID: input.RunID, KnowledgeBaseID: input.KnowledgeBaseID, Request: input.Request, Status: agentrun.StatusPending}, nil
}
func (*agentRunStoreStub) ClaimNext(context.Context) (agentrun.Run, error) {
	return agentrun.Run{}, agentrun.ErrNoRun
}
func (*agentRunStoreStub) RequeueExpired(context.Context) error                    { return nil }
func (*agentRunStoreStub) RenewLease(context.Context, int64, string) error         { return nil }
func (*agentRunStoreStub) MarkSucceeded(context.Context, int64, string) error      { return nil }
func (*agentRunStoreStub) MarkFailed(context.Context, int64, string, string) error { return nil }
func (*agentRunStoreStub) MarkCanceled(context.Context, int64, string) error       { return nil }

func TestPersistentAgentRunSubmissionReturnsRunIDWithoutExecuting(t *testing.T) {
	store := &agentRunStoreStub{}
	hub := agentstream.NewHub()
	endpoint := handler.NewPersistentAgentRunSubmission(64*1024, store, nil, hub)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
	if store.created.RunID == "" || store.created.KnowledgeBaseID != 7 {
		t.Fatalf("created run = %#v", store.created)
	}
	var payload struct {
		RunID  string          `json:"run_id"`
		Status agentrun.Status `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.RunID != store.created.RunID || payload.Status != agentrun.StatusPending {
		t.Fatalf("payload = %#v, created run = %#v", payload, store.created)
	}
	if !strings.Contains(response.Header().Get("X-Agent-Run-ID"), store.created.RunID) {
		t.Fatalf("missing run header: %q", response.Header().Get("X-Agent-Run-ID"))
	}
}

func TestPersistentAgentRunSubmissionPersistsNormalizedMultitaskStrategy(t *testing.T) {
	store := &agentRunStoreStub{}
	hub := agentstream.NewHub()
	endpoint := handler.NewPersistentAgentRunSubmission(64*1024, store, nil, hub)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题","multitask_strategy":" INTERRUPT "}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	var snapshot struct {
		Request struct {
			MultitaskStrategy string `json:"multitask_strategy"`
		} `json:"request"`
	}
	if err := json.Unmarshal(store.created.Request, &snapshot); err != nil {
		t.Fatalf("decode persisted request: %v", err)
	}
	if snapshot.Request.MultitaskStrategy != "interrupt" {
		t.Fatalf("persisted multitask strategy = %q, want interrupt", snapshot.Request.MultitaskStrategy)
	}
}

type waitingChildrenAnswerer struct{}

func (waitingChildrenAnswerer) AnswerWithEvents(context.Context, int64, agentservice.ChatRequest, agentruntime.EventSink) (agentservice.Response, error) {
	return agentservice.Response{}, agentruntime.ErrAgentWaitingChildren
}

func TestPersistentAgentExecutorKeepsHubOpenWhileWaitingForChildren(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("parent-waiting", 7); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(agentservice.ChatRequest{Message: "研究问题", ChildMode: true})
	if err != nil {
		t.Fatal(err)
	}
	executor := handler.NewPersistentAgentExecutorWithCheckpoint(waitingChildrenAnswerer{}, nil, hub, nil, nil, nil, nil)
	err = executor.Execute(context.Background(), agentrun.Run{
		ID:              1,
		RunID:           "parent-waiting",
		KnowledgeBaseID: 7,
		LeaseToken:      "lease-1",
		Request:         request,
	}, nil)
	if !errors.Is(err, agentruntime.ErrAgentWaitingChildren) {
		t.Fatalf("Execute() error = %v, want waiting-children error", err)
	}

	snapshot, _, cancel, done, err := hub.Subscribe("parent-waiting", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if done {
		t.Fatal("parent stream closed while waiting for children")
	}
	if len(snapshot) == 0 || snapshot[len(snapshot)-1].Type != "waiting_children" {
		t.Fatalf("snapshot = %#v, want waiting_children event", snapshot)
	}
}
