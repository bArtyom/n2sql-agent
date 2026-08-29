package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

type multitaskSubmissionStoreStub struct {
	agentRunStoreStub
	result   agentrun.AdmissionResult
	err      error
	admitted agentrun.AdmissionInput
}

func (s *multitaskSubmissionStoreStub) Admit(_ context.Context, input agentrun.AdmissionInput) (agentrun.AdmissionResult, error) {
	s.admitted = input
	if s.err != nil {
		return agentrun.AdmissionResult{}, s.err
	}
	if s.result.Run.RunID == "" {
		s.result.Run = agentrun.Run{
			RunID:           input.Create.RunID,
			KnowledgeBaseID: input.Create.KnowledgeBaseID,
			ConversationID:  input.Create.ConversationID,
			Status:          agentrun.StatusPending,
			Request:         input.Create.Request,
		}
	}
	return s.result, nil
}

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

func TestPersistentAgentRunSubmissionRejectsActiveConversation(t *testing.T) {
	store := &multitaskSubmissionStoreStub{err: &agentrun.ActiveRunConflict{
		ActiveRun: agentrun.Run{RunID: "run-101", ConversationID: 42, Status: agentrun.StatusRunning},
		Requested: agentrun.MultitaskReject,
	}}
	endpoint := handler.NewPersistentAgentRunSubmission(64*1024, store, nil, agentstream.NewHub())
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"conversation_id":42,"message":"第二个问题"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusConflict, response.Body.String())
	}
	for _, fragment := range []string{`"code":"conversation_run_active"`, `"active_run_id":"run-101"`, `"active_status":"running"`} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("body=%s, missing %s", response.Body.String(), fragment)
		}
	}
	if store.admitted.Strategy != agentrun.MultitaskReject {
		t.Fatalf("admitted strategy = %q, want reject", store.admitted.Strategy)
	}
}

func TestPersistentAgentRunSubmissionRollbackCancelsReplacedHubRun(t *testing.T) {
	hub := agentstream.NewHub()
	oldCanceled := make(chan struct{})
	if err := hub.Start("run-101", 7); err != nil {
		t.Fatal(err)
	}
	if err := hub.RegisterCancel("run-101", func() { close(oldCanceled) }); err != nil {
		t.Fatal(err)
	}
	old := agentrun.Run{RunID: "run-101", ConversationID: 42, KnowledgeBaseID: 7, Status: agentrun.StatusCanceled}
	store := &multitaskSubmissionStoreStub{result: agentrun.AdmissionResult{ReplacedRun: &old}}
	endpoint := handler.NewPersistentAgentRunSubmission(64*1024, store, nil, hub)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"conversation_id":42,"message":"替换问题","multitask_strategy":"rollback"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusAccepted, response.Body.String())
	}
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("old Hub run was not canceled")
	}
	if store.admitted.Strategy != agentrun.MultitaskRollback {
		t.Fatalf("admitted strategy = %q, want rollback", store.admitted.Strategy)
	}
	if !strings.Contains(response.Body.String(), `"replaced_run_id":"run-101"`) {
		t.Fatalf("body=%s, want replaced run metadata", response.Body.String())
	}
}

func TestPersistentAgentRunSubmissionInterruptReturnsReplacementMetadata(t *testing.T) {
	hub := agentstream.NewHub()
	if err := hub.Start("run-201", 7); err != nil {
		t.Fatal(err)
	}
	old := agentrun.Run{RunID: "run-201", ConversationID: 43, KnowledgeBaseID: 7, Status: agentrun.StatusInterrupted}
	store := &multitaskSubmissionStoreStub{result: agentrun.AdmissionResult{ReplacedRun: &old}}
	endpoint := handler.NewPersistentAgentRunSubmission(64*1024, store, nil, hub)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"conversation_id":43,"message":"打断问题","multitask_strategy":"interrupt"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"replaced_status":"interrupted"`) {
		t.Fatalf("status=%d body=%s, want interrupted replacement metadata", response.Code, response.Body.String())
	}
	snapshot, _, cancel, _, err := hub.Subscribe("run-201", 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if len(snapshot) == 0 || snapshot[len(snapshot)-1].Type != "run_interrupted" {
		t.Fatalf("replaced run events = %#v, want run_interrupted", snapshot)
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
