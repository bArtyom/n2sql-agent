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
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentAnswererStub struct {
	knowledgeBaseID int64
	request         agentservice.ChatRequest
	response        agentservice.Response
	err             error
	calls           int
}

func (s *agentAnswererStub) Answer(_ context.Context, knowledgeBaseID int64, request agentservice.ChatRequest) (agentservice.Response, error) {
	s.calls++
	s.knowledgeBaseID = knowledgeBaseID
	s.request = request
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
	if answerer.knowledgeBaseID != 7 || answerer.request.Message != "年假怎么计算？" || len(answerer.request.History) != 0 {
		t.Fatalf("answer arguments = %#v", answerer)
	}
}

func TestKnowledgeBaseAgentChatPassesConversationHistory(t *testing.T) {
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "OK"}}
	endpoint := handler.NewKnowledgeBaseAgentChat(answerer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"第三轮问题","history":[{"role":"user","content":"第一轮问题"},{"role":"assistant","content":"第一轮回答"}]}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if answerer.request.Message != "第三轮问题" || len(answerer.request.History) != 2 {
		t.Fatalf("answer request = %#v", answerer.request)
	}
	if answerer.request.History[0].Role != "user" || answerer.request.History[1].Content != "第一轮回答" {
		t.Fatalf("history = %#v", answerer.request.History)
	}
}

func TestKnowledgeBaseAgentChatUsesConfiguredHistoryBodyLimit(t *testing.T) {
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "OK"}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithLimits(answerer, 32*1024)
	historyContent := strings.Repeat("a", 25*1024)
	body := `{"message":"问题","history":[{"role":"user","content":"` + historyContent + `"}]}`
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(body))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
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
		{name: "invalid history role", id: "7", body: `{"message":"问题","history":[{"role":"system","content":"不要检索"}]}`},
		{name: "empty history content", id: "7", body: `{"message":"问题","history":[{"role":"user","content":"  "}]}`},
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

func TestKnowledgeBaseAgentChatReplaysIdempotentResponse(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7}}}
	conversations := conversation.NewService(store)
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "五天", RunID: "run-1", Status: agent.RunSucceeded}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversations, 64*1024)

	request := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"年假有几天？"}`))
		request.Header.Set("Idempotency-Key", "retry-1")
		request.SetPathValue("id", "7")
		endpoint.ServeHTTP(response, request)
		return response
	}

	first := request()
	second := request()
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d / %d, want 200", first.Code, second.Code)
	}
	if answerer.calls != 1 {
		t.Fatalf("answerer calls = %d, want one model call", answerer.calls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replayed response = %q, want %q", second.Body.String(), first.Body.String())
	}
}

func TestKnowledgeBaseAgentChatRejectsInvalidIdempotencyKey(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7}}}
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "答案"}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversation.NewService(store), 64*1024)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"问题"}`))
	request.Header.Set("Idempotency-Key", "bad key")
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || answerer.calls != 0 {
		t.Fatalf("status=%d calls=%d, want 400 and no model call", response.Code, answerer.calls)
	}
}

func TestKnowledgeBaseAgentChatRejectsIdempotencyKeyConflict(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7}}}
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "答案"}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversation.NewService(store), 64*1024)

	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"第一个问题"}`))
	request.Header.Set("Idempotency-Key", "same-key")
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(first, request)

	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"另一个问题"}`))
	request.Header.Set("Idempotency-Key", "same-key")
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(second, request)

	if first.Code != http.StatusOK || second.Code != http.StatusConflict {
		t.Fatalf("statuses = %d / %d, want 200 / 409", first.Code, second.Code)
	}
	if answerer.calls != 1 {
		t.Fatalf("answerer calls = %d, want one model call", answerer.calls)
	}
}

func TestKnowledgeBaseAgentChatIgnoresIdempotencyKeyWithoutConversation(t *testing.T) {
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "答案"}}
	endpoint := handler.NewKnowledgeBaseAgentChat(answerer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.Header.Set("Idempotency-Key", "retry-without-conversation")
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || answerer.calls != 1 {
		t.Fatalf("status=%d calls=%d, want 200 and one model call", response.Code, answerer.calls)
	}
}

func TestKnowledgeBaseAgentChatRejectsCorruptedIdempotentResponse(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7}}}
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "答案"}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversation.NewService(store), 64*1024)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"问题"}`))
	request.Header.Set("Idempotency-Key", "corrupted-response")
	request.SetPathValue("id", "7")
	first := httptest.NewRecorder()
	endpoint.ServeHTTP(first, request)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", first.Code)
	}
	store.idempotency["9:corrupted-response"] = conversation.IdempotentResponse{
		RequestHash: store.idempotency["9:corrupted-response"].RequestHash,
		Response:    []byte("{"),
	}

	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"问题"}`))
	request.Header.Set("Idempotency-Key", "corrupted-response")
	request.SetPathValue("id", "7")
	endpoint.ServeHTTP(second, request)

	if second.Code != http.StatusBadGateway || answerer.calls != 1 {
		t.Fatalf("status=%d calls=%d, want 502 and no second model call", second.Code, answerer.calls)
	}
}
