package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentAnswererStub struct {
	knowledgeBaseID int64
	question        string
	response        agentservice.Response
	err             error
}

func (s *agentAnswererStub) Answer(_ context.Context, knowledgeBaseID int64, question string) (agentservice.Response, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.question = question
	if s.err != nil {
		return agentservice.Response{}, s.err
	}
	return s.response, nil
}

func TestKnowledgeBaseAgentChatReturnsAnswer(t *testing.T) {
	answerer := &agentAnswererStub{response: agentservice.Response{
		Answer: "年假按照公司制度执行。",
		RunID:  "agent-run-1",
		Status: agent.RunSucceeded,
		Steps:  []agent.Step{{Number: 1, Kind: agent.StepFinalAnswer, Status: agent.StepSucceeded}},
	}}
	endpoint := handler.NewKnowledgeBaseAgentChat(answerer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"  年假怎么计算？  "}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != `{"answer":"年假按照公司制度执行。","run_id":"agent-run-1","status":"succeeded","steps":[{"number":1,"kind":"final_answer","status":"succeeded"}]}`+"\n" {
		t.Fatalf("response body = %q", response.Body.String())
	}
	if answerer.knowledgeBaseID != 7 || answerer.question != "年假怎么计算？" {
		t.Fatalf("answer arguments = %#v", answerer)
	}
}

func TestKnowledgeBaseAgentChatRejectsInvalidRequest(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChat(&agentAnswererStub{})
	cases := []struct {
		name string
		id   string
		body string
	}{
		{name: "invalid ID", id: "invalid", body: `{"message":"问题"}`},
		{name: "malformed JSON", id: "7", body: `{"message":"问题"`},
		{name: "unknown field", id: "7", body: `{"message":"问题","topK":5}`},
		{name: "trailing JSON", id: "7", body: `{"message":"问题"}{"message":"另一个"}`},
		{name: "empty message", id: "7", body: `{"message":"  "}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/"+test.id+"/agent-chat", strings.NewReader(test.body))
			request.SetPathValue("id", test.id)
			endpoint.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestKnowledgeBaseAgentChatMapsServiceErrors(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChat(&agentAnswererStub{err: agentservice.ErrInvalidRequest})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request status = %d, want %d", response.Code, http.StatusBadRequest)
	}

	endpoint = handler.NewKnowledgeBaseAgentChat(&agentAnswererStub{err: errors.New("model unavailable")})
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected failure status = %d, want %d", response.Code, http.StatusBadGateway)
	}

	endpoint = handler.NewKnowledgeBaseAgentChat(&agentAnswererStub{err: context.DeadlineExceeded})
	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("timeout status = %d, want %d", response.Code, http.StatusGatewayTimeout)
	}
}
