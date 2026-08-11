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

type multiAgentEventAnswererStub struct {
	response multiagent.Response
	err      error
}

func (s *multiAgentEventAnswererStub) Answer(_ context.Context, _ int64, _ string, _ int) (multiagent.Response, error) {
	return s.response, s.err
}

func (s *multiAgentEventAnswererStub) AnswerWithEvents(_ context.Context, _ int64, _ string, _ int, sink multiagent.EventSink) (multiagent.Response, error) {
	if err := sink(multiagent.Event{Type: multiagent.EventRunStarted}); err != nil {
		return multiagent.Response{}, err
	}
	if s.err != nil {
		return multiagent.Response{}, s.err
	}
	if err := sink(multiagent.Event{Type: multiagent.EventRunFinished, Data: s.response}); err != nil {
		return multiagent.Response{}, err
	}
	return s.response, nil
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

func TestMultiAgentChatStreamWritesWorkflowEvents(t *testing.T) {
	streamHandler := handler.NewMultiAgentChatStream(&multiAgentEventAnswererStub{response: multiagent.Response{Answer: "完成"}})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat/stream", strings.NewReader(`{"message":"问题","topK":3}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	streamHandler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body.String(), "event: run_started\n") || !strings.Contains(response.Body.String(), "event: run_finished\n") {
		t.Fatalf("status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}

func TestMultiAgentChatStreamWritesErrorEvent(t *testing.T) {
	streamHandler := handler.NewMultiAgentChatStream(&multiAgentEventAnswererStub{err: errors.New("research failed")})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/multi-agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	response := httptest.NewRecorder()

	streamHandler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error\n") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
