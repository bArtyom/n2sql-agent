package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type agentEventAnswererStub struct {
	err         error
	emitStarted bool
	answer      string
	sourceData  map[string]any
	request     *agentservice.ChatRequest
}

func (s agentEventAnswererStub) AnswerWithEvents(_ context.Context, _ int64, request agentservice.ChatRequest, emit agentruntime.EventSink) (agentservice.Response, error) {
	if s.request != nil {
		*s.request = request
	}
	if s.emitStarted {
		if err := emit(agent.Event{
			ID:        "event-1",
			RunID:     "run-1",
			Type:      agent.EventRunStarted,
			Data:      map[string]any{"status": "running"},
			CreatedAt: time.Date(2026, time.August, 7, 0, 0, 0, 0, time.UTC),
		}); err != nil {
			return agentservice.Response{}, err
		}
	}
	if s.err != nil {
		return agentservice.Response{Status: agent.RunFailed}, s.err
	}
	if s.sourceData != nil {
		if err := emit(agent.Event{
			ID:        "event-sources",
			RunID:     "run-1",
			Type:      agent.EventToolFinished,
			Data:      s.sourceData,
			CreatedAt: time.Date(2026, time.August, 7, 0, 0, 0, 500000000, time.UTC),
		}); err != nil {
			return agentservice.Response{}, err
		}
	}
	for _, event := range []agent.Event{
		{
			ID:        "event-2",
			RunID:     "run-1",
			Type:      agent.EventMessageDelta,
			Data:      map[string]any{"content": s.answer},
			CreatedAt: time.Date(2026, time.August, 7, 0, 0, 1, 0, time.UTC),
		},
		{
			ID:        "event-3",
			RunID:     "run-1",
			Type:      agent.EventRunFinished,
			Data:      map[string]any{"answer": s.answer},
			CreatedAt: time.Date(2026, time.August, 7, 0, 0, 2, 0, time.UTC),
		},
	} {
		if err := emit(event); err != nil {
			return agentservice.Response{}, err
		}
	}
	return agentservice.Response{Answer: s.answer, RunID: "run-1", Status: agent.RunSucceeded}, nil
}

func TestKnowledgeBaseAgentChatStreamWritesToolSources(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{
		sourceData: map[string]any{
			"tool_name": "knowledge_search",
			"sources": []map[string]any{{
				"documentId":       11,
				"originalFilename": "employee-handbook.md",
				"position":         2,
				"content":          "工作满一年可享受五天年假。",
				"distance":         0.12,
			}},
		},
		answer: "年假答案",
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"年假"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: tool_finished\n") || !strings.Contains(body, `"sources":[{"content":"工作满一年可享受五天年假。"`) {
		t.Fatalf("body = %q, want tool sources", body)
	}
}

func TestKnowledgeBaseAgentChatStreamWritesAgentEvents(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{
		emitStarted: true,
		answer:      "最终答案",
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q, want text/event-stream", contentType)
	}
	body := response.Body.String()
	for _, eventType := range []string{"run_started", "message_delta", "run_finished"} {
		if !strings.Contains(body, "event: "+eventType+"\n") {
			t.Fatalf("body = %q, want %s event", body, eventType)
		}
	}
	if !strings.Contains(body, `"answer":"最终答案"`) {
		t.Fatalf("body = %q, want final answer event data", body)
	}
	if strings.Contains(body, "event: done\n") {
		t.Fatal("agent stream must use run_finished instead of done")
	}
}

func TestKnowledgeBaseAgentChatStreamPassesConversationHistory(t *testing.T) {
	var captured agentservice.ChatRequest
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{
		request: &captured,
		answer:  "上下文答案",
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"当前问题","history":[{"role":"user","content":"上一个问题"}]}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if captured.Message != "当前问题" || len(captured.History) != 1 || captured.History[0].Content != "上一个问题" {
		t.Fatalf("stream request = %#v", captured)
	}
}

func TestKnowledgeBaseAgentChatStreamRejectsInvalidRequestBeforeStreaming(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/invalid/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "invalid")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
	if strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("invalid request must not start SSE")
	}
}

func TestKnowledgeBaseAgentChatStreamWritesErrorAfterStreamingStarts(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{
		err:         errors.New("model unavailable"),
		emitStarted: true,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "event: error\n") {
		t.Fatalf("body = %q, want error event", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "event: run_finished\n") {
		t.Fatal("failed stream must not write run_finished event")
	}
}

func TestKnowledgeBaseAgentChatStreamReportsTimeout(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{
		err:         context.DeadlineExceeded,
		emitStarted: true,
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`))
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: error\n") || !strings.Contains(body, `"error":"agent chat timed out"`) {
		t.Fatalf("body = %q, want timeout error event", body)
	}
}

func TestKnowledgeBaseAgentChatStreamIgnoresRequestCancellation(t *testing.T) {
	endpoint := handler.NewKnowledgeBaseAgentChatStream(agentEventAnswererStub{err: context.Canceled})
	response := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat/stream", strings.NewReader(`{"message":"问题"}`)).WithContext(ctx)
	request.SetPathValue("id", "7")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if strings.Contains(response.Body.String(), "event: error\n") {
		t.Fatalf("canceled stream must not write error event: %q", response.Body.String())
	}
}
