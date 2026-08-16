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
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
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

func TestKnowledgeBaseAgentChatPassesSelectedChatModel(t *testing.T) {
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "OK"}}
	endpoint := handler.NewKnowledgeBaseAgentChat(answerer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题","chat_model":" chat-fast "}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || answerer.request.ChatModel != "chat-fast" {
		t.Fatalf("status=%d chat_model=%q, want 200 and chat-fast", response.Code, answerer.request.ChatModel)
	}
}

func TestKnowledgeBaseAgentChatRestoresConversationChatModel(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7, ChatModel: "chat-fast"}}}
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "OK"}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversation.NewService(store), 64*1024)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || answerer.request.ChatModel != "chat-fast" {
		t.Fatalf("status=%d chat_model=%q, want 200 and restored chat-fast", response.Code, answerer.request.ChatModel)
	}
}

func TestKnowledgeBaseAgentChatRecordsMetrics(t *testing.T) {
	registry := metrics.New()
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "答案", RunID: "run-metrics", Status: agent.RunSucceeded}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversationAndMetrics(answerer, nil, 64*1024, registry)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	metricsResponse := httptest.NewRecorder()
	registry.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), "agent_runs_total 1\n") || !strings.Contains(metricsResponse.Body.String(), "agent_runs_succeeded_total 1\n") {
		t.Fatalf("metrics body = %q, want successful Agent run counters", metricsResponse.Body.String())
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
	registry := metrics.New()
	answerer := &agentAnswererStub{response: agentservice.Response{Answer: "五天", RunID: "run-1", Status: agent.RunSucceeded}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversationAndMetrics(answerer, conversations, 64*1024, registry)

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
	metricsResponse := httptest.NewRecorder()
	registry.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(), "agent_runs_total 1\n") {
		t.Fatalf("metrics body = %q, want replay excluded from Agent run count", metricsResponse.Body.String())
	}
}

func TestKnowledgeBaseAgentChatPersistsRetrievalMetadata(t *testing.T) {
	store := &conversationStoreStub{records: []conversation.Conversation{{ID: 9, KnowledgeBaseID: 7}}}
	answerer := &agentAnswererStub{response: agentservice.Response{
		Answer:  "答案",
		RunID:   "run-history",
		Status:  agent.RunSucceeded,
		Steps:   []agent.Step{{Number: 1, Kind: agent.StepToolCall, Status: agent.StepSucceeded, ToolName: "knowledge_search"}},
		Trace:   []agentservice.TraceEvent{{Type: "tool_call", Step: 1, ToolCallID: "call-1", ToolName: "knowledge_search", Arguments: `{"query":"年假"}`, ResultSummary: "返回 1 条资料", SourceKeys: []string{"11:2", "99:8"}, Status: "succeeded"}},
		Sources: []retrieval.Result{{DocumentID: 11, OriginalFilename: "guide.md", Position: 2, Content: "原始引用", Distance: 0.2, MatchType: "hybrid"}},
		Stats: &agent.RunStats{
			StepCount: 1, ModelCalls: 2, ToolCalls: 1, SuccessfulToolCalls: 1,
			PromptTokens: 120, CompletionTokens: 45, TotalTokens: 165, DurationMS: 320,
			Retrieval: &usage.RetrievalObservation{VectorCandidates: 8, FinalFiltered: 3},
		},
	}}
	endpoint := handler.NewKnowledgeBaseAgentChatWithConversation(answerer, conversation.NewService(store), 64*1024)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"conversation_id":9,"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if store.exchangeMeta.Retrieval == nil || store.exchangeMeta.Retrieval.VectorCandidates != 8 || store.exchangeMeta.Retrieval.FinalFiltered != 3 {
		t.Fatalf("saved exchange metadata = %#v, want retrieval stats", store.exchangeMeta)
	}
	if len(store.exchangeMeta.Sources) != 1 || store.exchangeMeta.Sources[0].DocumentID != 11 || store.exchangeMeta.Sources[0].Content != "原始引用" {
		t.Fatalf("saved sources = %#v, want bounded citation reference", store.exchangeMeta.Sources)
	}
	if store.exchangeMeta.AgentTrace == nil || store.exchangeMeta.AgentTrace.RunID != "run-history" || len(store.exchangeMeta.AgentTrace.Steps) != 1 {
		t.Fatalf("saved agent trace = %#v, want one step", store.exchangeMeta.AgentTrace)
	}
	if store.exchangeMeta.AgentTrace.Stats == nil || store.exchangeMeta.AgentTrace.Stats.ModelCalls != 2 || store.exchangeMeta.AgentTrace.Stats.TotalTokens != 165 || store.exchangeMeta.AgentTrace.Stats.DurationMS != 320 {
		t.Fatalf("saved agent trace stats = %#v, want run summary", store.exchangeMeta.AgentTrace.Stats)
	}
	if len(store.exchangeMeta.AgentTrace.Events) != 1 || store.exchangeMeta.AgentTrace.Events[0].Arguments != `{"query":"年假"}` || store.exchangeMeta.AgentTrace.Events[0].ResultSummary != "返回 1 条资料" {
		t.Fatalf("saved agent trace events = %#v, want tool details", store.exchangeMeta.AgentTrace.Events)
	}
	if got := store.exchangeMeta.AgentTrace.Events[0].SourceKeys; len(got) != 1 || got[0] != "11:2" {
		t.Fatalf("saved agent trace source keys = %#v, want [11:2]", got)
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
