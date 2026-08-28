package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type durableApprovalStoreStub struct {
	called   bool
	runID    string
	kbID     int64
	approved bool
}

func (s *durableApprovalStoreStub) MarkWaitingApproval(context.Context, int64, string) error {
	return nil
}

func (s *durableApprovalStoreStub) ResolveApproval(_ context.Context, runID string, knowledgeBaseID int64, approved bool) error {
	s.called, s.runID, s.kbID, s.approved = true, runID, knowledgeBaseID, approved
	return nil
}

func TestAgentRunApprovalResolvesDurableInterrupt(t *testing.T) {
	store := &durableApprovalStoreStub{}
	req := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-runs/run-1/approval", strings.NewReader(`{"approved":true}`))
	req.SetPathValue("id", "7")
	req.SetPathValue("runID", "run-1")
	response := httptest.NewRecorder()
	handler.NewAgentRunApprovalWithStore(nil, store).ServeHTTP(response, req)
	if response.Code != http.StatusOK || !store.called || store.runID != "run-1" || store.kbID != 7 || !store.approved {
		t.Fatalf("status=%d body=%q store=%#v", response.Code, response.Body.String(), store)
	}
}

var _ agentrun.ApprovalInterruptStore = (*durableApprovalStoreStub)(nil)
