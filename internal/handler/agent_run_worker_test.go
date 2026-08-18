package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
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
func (*agentRunStoreStub) RequeueExpired(context.Context) error            { return nil }
func (*agentRunStoreStub) RenewLease(context.Context, int64) error         { return nil }
func (*agentRunStoreStub) MarkSucceeded(context.Context, int64) error      { return nil }
func (*agentRunStoreStub) MarkFailed(context.Context, int64, string) error { return nil }
func (*agentRunStoreStub) MarkCanceled(context.Context, int64) error       { return nil }

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
