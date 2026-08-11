package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
)

type multiAgentAnswererStub struct {
	response multiagent.Response
	err      error
	calls    int
}

func (s *multiAgentAnswererStub) Answer(_ context.Context, knowledgeBaseID int64, question string, topK int) (multiagent.Response, error) {
	s.calls++
	if knowledgeBaseID != 7 || question != "如何启动？" || topK != 3 {
		return multiagent.Response{}, errors.New("unexpected multi-agent request")
	}
	return s.response, s.err
}

func TestMultiAgentChatReturnsStructuredWorkflow(t *testing.T) {
	answerer := &multiAgentAnswererStub{response: multiagent.Response{
		Answer: "执行启动命令即可。",
		Steps:  []multiagent.Step{{Number: 1, Role: multiagent.RoleResearcher, Status: multiagent.StepSucceeded}, {Number: 2, Role: multiagent.RoleAnswerer, Status: multiagent.StepSucceeded}},
	}}
	handler := handler.NewMultiAgentChat(answerer)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat", strings.NewReader(`{"message":"如何启动？","topK":3}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"answer":"执行启动命令即可。"`) || !strings.Contains(response.Body.String(), `"role":"researcher"`) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	if answerer.calls != 1 {
		t.Fatalf("answerer calls=%d, want 1", answerer.calls)
	}
}

func TestMultiAgentChatRejectsInvalidRequestBeforeCallingService(t *testing.T) {
	answerer := &multiAgentAnswererStub{}
	handler := handler.NewMultiAgentChat(answerer)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat", strings.NewReader(`{"message":"问题","topK":21}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || answerer.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%q", response.Code, answerer.calls, response.Body.String())
	}
}

func TestMultiAgentChatMapsWorkflowFailure(t *testing.T) {
	answerer := &multiAgentAnswererStub{err: errors.New("researcher failed")}
	handler := handler.NewMultiAgentChat(answerer)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat", strings.NewReader(`{"message":"如何启动？","topK":3}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "multi-agent chat failed") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestMultiAgentChatHandlesMissingService(t *testing.T) {
	handler := handler.NewMultiAgentChat(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
