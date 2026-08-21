package agentservice_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

func TestServiceSendsAttachmentPartsAndThinkingEffortContext(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		user := messages[len(messages)-1]
		if len(user.ContentParts) != 2 || user.ContentParts[0].Type != "text" || user.ContentParts[1].Type != "image_url" {
			t.Fatalf("user content parts = %#v, want text/image", user.ContentParts)
		}
		validPNG := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
		if user.ContentParts[1].ImageURL == nil || user.ContentParts[1].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(validPNG) {
			t.Fatalf("image part = %#v", user.ContentParts[1].ImageURL)
		}
		return modelclient.ChatResponse{Message: "已分析图片"}, nil
	}}
	service, err := agentservice.NewService(chat, &searcherStub{}, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message:      "请分析图片",
		ThinkingMode: "deep",
		Attachments: []agentservice.ChatAttachment{{
			Filename:    "chart.png",
			ContentType: "image/png",
			DataBase64:  base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}),
		}},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "已分析图片" {
		t.Fatalf("answer = %q, want attachment answer", response.Answer)
	}
}

type searcherStub struct {
	knowledgeBaseID int64
	query           string
	limit           int
	content         string
}

type historySummarizerStub struct {
	called  int
	summary string
	err     error
}

func (s *historySummarizerStub) Summarize(_ context.Context, history []agentservice.HistoryMessage) (agentservice.HistorySummaryResult, error) {
	s.called++
	if s.err != nil {
		return agentservice.HistorySummaryResult{}, s.err
	}
	if len(history) == 0 {
		return agentservice.HistorySummaryResult{}, errors.New("history must not be empty")
	}
	return agentservice.HistorySummaryResult{Content: s.summary}, nil
}

func (s *searcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.query = query
	s.limit = limit
	content := s.content
	if content == "" {
		content = "年假制度内容"
	}
	return []retrieval.Result{{Content: content}}, nil
}

type chatStub struct {
	call func([]modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error)
}

func (s chatStub) ChatMessagesWithTools(_ context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return s.call(messages, definitions)
}

type blockingChatStub struct{}

func (blockingChatStub) ChatMessagesWithTools(ctx context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	<-ctx.Done()
	return modelclient.ChatResponse{}, ctx.Err()
}

type modelSelectingChatStub struct {
	model string
}

func (s *modelSelectingChatStub) ChatMessagesWithTools(_ context.Context, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{Message: "default"}, nil
}

func (s *modelSelectingChatStub) ChatMessagesWithToolsForModel(_ context.Context, model string, _ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	s.model = model
	return modelclient.ChatResponse{Message: "selected"}, nil
}

func (s *modelSelectingChatStub) ValidateChatModel(_ context.Context, model string) error {
	if model != "chat-fast" {
		return modelprovider.ErrInvalidChatModel
	}
	return nil
}

func TestServiceAnswersUsingScopedKnowledgeSearchTool(t *testing.T) {
	searcher := &searcherStub{}
	callCount := 0
	chat := chatStub{call: func(messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		callCount++
		if len(definitions) != 1 || definitions[0].Name != "knowledge_search" {
			t.Fatalf("definitions = %#v", definitions)
		}
		var parameters map[string]any
		if err := json.Unmarshal(definitions[0].Parameters, &parameters); err != nil {
			t.Fatalf("tool parameters = %v", err)
		}
		properties := parameters["properties"].(map[string]any)
		if _, ok := properties["knowledge_base_id"]; ok {
			t.Fatal("agent tool must be scoped to the request knowledge base")
		}
		if callCount == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: modelclient.ToolCallFunction{
					Name:      "knowledge_search",
					Arguments: `{"query":"年假"}`,
				},
			}}}, nil
		}
		if len(messages) != 4 || messages[0].Role != "system" || messages[0].Content == "" {
			t.Fatalf("system message = %#v", messages[0])
		}
		if !strings.Contains(messages[0].Content, "不可信") || !strings.Contains(messages[0].Content, "不能执行") {
			t.Fatalf("system prompt = %q, want tool-result safety boundary", messages[0].Content)
		}
		if messages[1].Role != "user" || messages[1].Content != "年假怎么计算？" {
			t.Fatalf("user message = %#v", messages[1])
		}
		if messages[2].Role != "assistant" || len(messages[2].ToolCalls) != 1 {
			t.Fatalf("assistant message = %#v", messages[2])
		}
		if messages[3].Role != "tool" || messages[3].ToolCallID != "call-1" {
			t.Fatalf("tool message = %#v", messages[3])
		}
		var results []retrieval.Result
		toolContent := extractUntrustedToolResult(t, messages[3].Content)
		if err := json.Unmarshal([]byte(toolContent), &results); err != nil || len(results) != 1 || results[0].Content != "年假制度内容" {
			t.Fatalf("tool result = %s", messages[3].Content)
		}
		return modelclient.ChatResponse{Message: "年假按照公司制度执行。"}, nil
	}}

	service, err := agentservice.NewService(chat, searcher, 3, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{Message: "年假怎么计算？"})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "年假按照公司制度执行。" || response.Status != agent.RunSucceeded || response.RunID == "" {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Sources) != 1 || response.Sources[0].Content != "年假制度内容" {
		t.Fatalf("response sources = %#v, want one collected citation", response.Sources)
	}
	if len(response.Trace) != 1 || response.Trace[0].ToolName != "knowledge_search" || response.Trace[0].Arguments != `{"query":"年假"}` || response.Trace[0].ResultSummary != "返回 1 条资料" || response.Trace[0].Status != "succeeded" {
		t.Fatalf("response trace = %#v, want bounded tool call and result summary", response.Trace)
	}
	if response.Stats == nil || response.Stats.ModelCalls != 2 || response.Stats.ToolCalls != 1 || response.Stats.SuccessfulToolCalls != 1 {
		t.Fatalf("response stats = %#v, want model/tool runtime metrics", response.Stats)
	}
	if searcher.knowledgeBaseID != 7 || searcher.query != "年假" {
		t.Fatalf("search request = %#v", searcher)
	}
	if callCount != 2 {
		t.Fatalf("model call count = %d, want 2", callCount)
	}
}

func TestServiceKnowledgeBaseOnlyRefusesWithoutEvidence(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if !strings.Contains(messages[0].Content, "严格知识库问答模式") {
			t.Fatalf("system prompt = %q, want closed-book policy", messages[0].Content)
		}
		if len(definitions) != 1 || definitions[0].Name != "knowledge_search" {
			t.Fatalf("definitions = %#v, want only knowledge search", definitions)
		}
		return modelclient.ChatResponse{Message: "根据常识，这个问题的答案是……"}, nil
	}}
	service, err := agentservice.NewService(chat, &searcherStub{}, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message:         "知识库没有覆盖的问题",
		KnowledgePolicy: agentservice.KnowledgeBaseOnly,
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "知识库中没有找到足够资料，暂时无法回答这个问题。" {
		t.Fatalf("answer = %q, want refusal", response.Answer)
	}
	if response.Status != agent.RunSucceeded {
		t.Fatalf("status = %v, want succeeded with refusal answer", response.Status)
	}
}

func TestServiceUsesSelectedChatModelRunner(t *testing.T) {
	chat := &modelSelectingChatStub{}
	service, err := agentservice.NewService(chat, &searcherStub{}, 2, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{Message: "问题", ChatModel: " chat-fast "})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "selected" || chat.model != "chat-fast" {
		t.Fatalf("answer/model = %q/%q, want selected/chat-fast", response.Answer, chat.model)
	}
}

func TestServiceIncludesBoundedConversationHistory(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 4 {
			t.Fatalf("messages = %#v, want system, one complete recent turn, and current question", messages)
		}
		if messages[0].Role != "system" {
			t.Fatalf("messages[0] = %#v, want system prompt", messages[0])
		}
		if messages[1].Role != "user" || messages[1].Content != "第二轮问题" {
			t.Fatalf("messages[1] = %#v, want most recent complete turn", messages[1])
		}
		if messages[2].Role != "assistant" || messages[2].Content != "第二轮回答" {
			t.Fatalf("messages[2] = %#v, want most recent complete turn", messages[2])
		}
		if messages[3].Role != "user" || messages[3].Content != "第三轮问题" {
			t.Fatalf("messages[3] = %#v, want current question", messages[3])
		}
		return modelclient.ChatResponse{Message: "基于上下文的回答"}, nil
	}}
	service, err := agentservice.NewServiceWithLimits(
		chat,
		&searcherStub{},
		3,
		time.Minute,
		agent.DefaultMaxToolResultBytes,
		2,
		128,
	)
	if err != nil {
		t.Fatalf("NewServiceWithLimits() error = %v", err)
	}

	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "第三轮问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "第一轮问题"},
			{Role: "assistant", Content: "第一轮回答"},
			{Role: "user", Content: "第二轮问题"},
			{Role: "assistant", Content: "第二轮回答"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "基于上下文的回答" {
		t.Fatalf("answer = %q", response.Answer)
	}
}

func TestServiceDropsOrphanAssistantHistory(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 4 || messages[2].Role != "user" || messages[2].Content != "最新问题" {
			t.Fatalf("messages = %#v, want summary and the incomplete latest user turn", messages)
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	service, err := agentservice.NewServiceWithLimits(chat, &searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 2, 128)
	if err != nil {
		t.Fatalf("NewServiceWithLimits() error = %v", err)
	}
	_, err = service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "旧问题"},
			{Role: "assistant", Content: "旧回答"},
			{Role: "assistant", Content: "孤立回答"},
			{Role: "user", Content: "最新问题"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceAddsExtractiveHistorySummaryWhenOlderTurnsAreDropped(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 5 {
			t.Fatalf("messages = %#v, want system, summary, two recent turns, and current question", messages)
		}
		if messages[1].Role != "system" || !strings.Contains(messages[1].Content, "更早对话的压缩记录") || !strings.Contains(messages[1].Content, "旧问题") {
			t.Fatalf("summary = %#v", messages[1])
		}
		if messages[2].Content != "新问题" || messages[3].Content != "新回答" {
			t.Fatalf("recent history = %#v", messages[2:4])
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	service, err := agentservice.NewServiceWithLimits(chat, &searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 3, 1024)
	if err != nil {
		t.Fatalf("NewServiceWithLimits() error = %v", err)
	}
	_, err = service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "旧问题"},
			{Role: "assistant", Content: "旧回答"},
			{Role: "user", Content: "新问题"},
			{Role: "assistant", Content: "新回答"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceUsesModelHistorySummaryWhenOlderTurnsAreDropped(t *testing.T) {
	summarizer := &historySummarizerStub{summary: "用户之前讨论过年假制度。"}
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 5 || messages[1].Role != "system" || !strings.Contains(messages[1].Content, "用户之前讨论过年假制度。") || !strings.Contains(messages[1].Content, "仅作背景") {
			t.Fatalf("messages = %#v, want model summary in context", messages)
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	service, err := agentservice.NewServiceWithLimitsAndSummarizer(chat, &searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 3, 1024, summarizer)
	if err != nil {
		t.Fatalf("NewServiceWithLimitsAndSummarizer() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "旧问题"},
			{Role: "assistant", Content: "旧回答"},
			{Role: "user", Content: "新问题"},
			{Role: "assistant", Content: "新回答"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.HistorySummary == nil || !response.HistorySummary.Attempted || !response.HistorySummary.Used || response.HistorySummary.DroppedMessages != 2 {
		t.Fatalf("history summary stats = %#v", response.HistorySummary)
	}
	if summarizer.called != 1 {
		t.Fatalf("summarizer calls = %d, want 1", summarizer.called)
	}
}

func TestServiceFallsBackWhenModelHistorySummaryFails(t *testing.T) {
	summarizer := &historySummarizerStub{err: errors.New("summary model unavailable")}
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 5 || !strings.Contains(messages[1].Content, "更早对话的压缩记录") || !strings.Contains(messages[1].Content, "旧问题") {
			t.Fatalf("messages = %#v, want extractive fallback", messages)
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	service, err := agentservice.NewServiceWithLimitsAndSummarizer(chat, &searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 3, 1024, summarizer)
	if err != nil {
		t.Fatalf("NewServiceWithLimitsAndSummarizer() error = %v", err)
	}
	_, err = service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "旧问题"},
			{Role: "assistant", Content: "旧回答"},
			{Role: "user", Content: "新问题"},
			{Role: "assistant", Content: "新回答"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceAddsHistorySummaryStatsToFinishedEvent(t *testing.T) {
	summarizer := &historySummarizerStub{summary: "更早对话摘要"}
	service, err := agentservice.NewServiceWithLimitsAndSummarizer(
		chatStub{call: func(_ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
			return modelclient.ChatResponse{Message: "OK"}, nil
		}},
		&searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 3, 1024, summarizer,
	)
	if err != nil {
		t.Fatalf("NewServiceWithLimitsAndSummarizer() error = %v", err)
	}
	var events []agent.Event
	_, err = service.AnswerWithEvents(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{
			{Role: "user", Content: "旧问题"},
			{Role: "assistant", Content: "旧回答"},
			{Role: "user", Content: "新问题"},
			{Role: "assistant", Content: "新回答"},
		},
	}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("AnswerWithEvents() error = %v", err)
	}
	finished := events[len(events)-1]
	data, ok := finished.Data.(map[string]any)
	if !ok {
		t.Fatalf("finished data = %#v", finished.Data)
	}
	stats, ok := data["history_summary"].(agentservice.HistorySummaryStats)
	if !ok || !stats.Used {
		t.Fatalf("history summary event stats = %#v", data["history_summary"])
	}
}

func TestServiceUsesCachedHistorySummaryAndSkipsCoveredMessages(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 5 || !strings.Contains(messages[1].Content, "之前摘要") {
			t.Fatalf("messages = %#v, want cached summary and only new history", messages)
		}
		if messages[2].Content != "新问题" || messages[3].Content != "新回答" {
			t.Fatalf("history after cache = %#v", messages[2:4])
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	summarizer := &historySummarizerStub{summary: "不应调用"}
	service, err := agentservice.NewServiceWithLimitsAndSummarizer(chat, &searcherStub{}, 3, time.Minute, agent.DefaultMaxToolResultBytes, 4, 1024, summarizer)
	if err != nil {
		t.Fatalf("NewServiceWithLimitsAndSummarizer() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message:       "当前问题",
		CachedSummary: &agentservice.CachedHistorySummary{ThroughMessageID: 2, Content: "之前摘要"},
		History: []agentservice.HistoryMessage{
			{ID: 1, Role: "user", Content: "旧问题"},
			{ID: 2, Role: "assistant", Content: "旧回答"},
			{ID: 3, Role: "user", Content: "新问题"},
			{ID: 4, Role: "assistant", Content: "新回答"},
		},
	})
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.HistorySummary == nil || !response.HistorySummary.CacheHit || response.HistorySummary.CacheMiss {
		t.Fatalf("cache stats = %#v", response.HistorySummary)
	}
	if summarizer.called != 0 {
		t.Fatalf("summarizer calls = %d, want 0", summarizer.called)
	}
}

func TestServiceTruncatesHistoryOnUTF8Boundary(t *testing.T) {
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		if len(messages) != 3 || messages[1].Content != "中" {
			t.Fatalf("messages = %#v, want one UTF-8-safe truncated history message", messages)
		}
		return modelclient.ChatResponse{Message: "OK"}, nil
	}}
	service, err := agentservice.NewServiceWithLimits(
		chat,
		&searcherStub{},
		3,
		time.Minute,
		agent.DefaultMaxToolResultBytes,
		10,
		5,
	)
	if err != nil {
		t.Fatalf("NewServiceWithLimits() error = %v", err)
	}
	if _, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "当前问题",
		History: []agentservice.HistoryMessage{{Role: "user", Content: "中文"}},
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceAnswersWithEvents(t *testing.T) {
	service, err := agentservice.NewService(chatStub{call: func(_ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		return modelclient.ChatResponse{Message: "事件版答案"}, nil
	}}, &searcherStub{}, 3, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	var events []agent.Event
	response, err := service.AnswerWithEvents(context.Background(), 7, agentservice.ChatRequest{Message: "问题"}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("AnswerWithEvents() error = %v", err)
	}
	if response.Answer != "事件版答案" || response.Status != agent.RunSucceeded || response.RunID == "" {
		t.Fatalf("response = %#v", response)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventMessageDelta, agent.EventRunFinished)
}

func TestServicePassesToolResultLimitToKnowledgeSearch(t *testing.T) {
	callCount := 0
	chat := chatStub{call: func(messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		callCount++
		if callCount == 1 {
			return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: modelclient.ToolCallFunction{
					Name:      "knowledge_search",
					Arguments: `{"query":"年假"}`,
				},
			}}}, nil
		}
		toolContent := extractUntrustedToolResult(t, messages[3].Content)
		if len(messages) != 4 || len([]byte(toolContent)) > 180 {
			t.Fatalf("tool message = %#v, want raw result at most 180 bytes", messages[3])
		}
		return modelclient.ChatResponse{Message: "已根据有限资料回答。"}, nil
	}}
	service, err := agentservice.NewServiceWithToolResultLimit(
		chat,
		&searcherStub{content: strings.Repeat("年假制度 ", 200)},
		3,
		time.Minute,
		180,
	)
	if err != nil {
		t.Fatalf("NewServiceWithToolResultLimit() error = %v", err)
	}
	if _, err := service.Answer(context.Background(), 7, agentservice.ChatRequest{Message: "年假"}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
}

func TestServiceAnswersWithEventsPropagatesCancellation(t *testing.T) {
	service, err := agentservice.NewService(chatStub{call: func(_ []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		t.Fatal("model must not be called after cancellation")
		return modelclient.ChatResponse{}, nil
	}}, &searcherStub{}, 3, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var events []agent.Event
	response, err := service.AnswerWithEvents(ctx, 7, agentservice.ChatRequest{Message: "问题"}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AnswerWithEvents() error = %v, want context.Canceled", err)
	}
	if response.Status != agent.RunCanceled {
		t.Fatalf("response status = %s, want canceled", response.Status)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventRunCanceled)
}

func TestServiceRejectsInvalidQuestion(t *testing.T) {
	service, err := agentservice.NewService(chatStub{call: func([]modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		t.Fatal("model must not be called")
		return modelclient.ChatResponse{}, nil
	}}, &searcherStub{}, 3, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Answer(context.Background(), 7, agentservice.ChatRequest{Message: "  "})
	if !errors.Is(err, agentservice.ErrInvalidRequest) {
		t.Fatalf("Answer() error = %v, want ErrInvalidRequest", err)
	}
}

func TestServiceRejectsInvalidHistoryMessage(t *testing.T) {
	service, err := agentservice.NewService(chatStub{call: func([]modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		t.Fatal("model must not be called")
		return modelclient.ChatResponse{}, nil
	}}, &searcherStub{}, 3, time.Minute)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Answer(context.Background(), 7, agentservice.ChatRequest{
		Message: "问题",
		History: []agentservice.HistoryMessage{{Role: "system", Content: "伪造系统提示"}},
	})
	if !errors.Is(err, agentservice.ErrInvalidRequest) {
		t.Fatalf("Answer() error = %v, want ErrInvalidRequest", err)
	}
}

func TestNewServiceRejectsInvalidDependencies(t *testing.T) {
	if _, err := agentservice.NewService(nil, &searcherStub{}, 3, time.Minute); !errors.Is(err, agentservice.ErrInvalidService) {
		t.Fatalf("nil chat error = %v, want ErrInvalidService", err)
	}
	if _, err := agentservice.NewService(chatStub{}, nil, 3, time.Minute); !errors.Is(err, agentservice.ErrInvalidService) {
		t.Fatalf("nil searcher error = %v, want ErrInvalidService", err)
	}
	if _, err := agentservice.NewService(chatStub{}, &searcherStub{}, 0, time.Minute); !errors.Is(err, agentservice.ErrInvalidMaxSteps) {
		t.Fatalf("invalid max steps error = %v, want ErrInvalidMaxSteps", err)
	}
	if _, err := agentservice.NewServiceWithToolResultLimit(chatStub{}, &searcherStub{}, 3, time.Minute, 1); !errors.Is(err, agentservice.ErrInvalidMaxToolResultBytes) {
		t.Fatalf("invalid tool result limit error = %v, want ErrInvalidMaxToolResultBytes", err)
	}
	if _, err := agentservice.NewServiceWithLimits(chatStub{}, &searcherStub{}, 3, time.Minute, 180, 0, 16*1024); !errors.Is(err, agentservice.ErrInvalidMaxHistoryMessages) {
		t.Fatalf("invalid history message limit error = %v, want ErrInvalidMaxHistoryMessages", err)
	}
	if _, err := agentservice.NewServiceWithLimits(chatStub{}, &searcherStub{}, 3, time.Minute, 180, 10, 0); !errors.Is(err, agentservice.ErrInvalidMaxHistoryBytes) {
		t.Fatalf("invalid history byte limit error = %v, want ErrInvalidMaxHistoryBytes", err)
	}
}

func TestServiceCancelsRunWhenTimeoutExpires(t *testing.T) {
	service, err := agentservice.NewService(blockingChatStub{}, &searcherStub{}, 3, time.Hour)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	var events []agent.Event
	requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	response, err := service.AnswerWithEvents(requestContext, 7, agentservice.ChatRequest{Message: "问题"}, func(event agent.Event) error {
		events = append(events, event)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AnswerWithEvents() error = %v, want deadline exceeded", err)
	}
	if response.Status != agent.RunCanceled {
		t.Fatalf("response status = %s, want canceled", response.Status)
	}
	assertEventTypes(t, events, agent.EventRunStarted, agent.EventRunCanceled)
}

func assertEventTypes(t *testing.T, events []agent.Event, want ...agent.EventType) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d (%#v)", len(events), len(want), events)
	}
	for index, eventType := range want {
		if events[index].Type != eventType {
			t.Fatalf("event[%d] type = %s, want %s", index, events[index].Type, eventType)
		}
	}
}

func extractUntrustedToolResult(t *testing.T, content string) string {
	t.Helper()
	const prefix = "UNTRUSTED_TOOL_RESULT\n"
	if !strings.HasPrefix(content, prefix) {
		t.Fatalf("tool content = %q, want structured untrusted result prefix", content)
	}
	var envelope struct {
		Trusted bool   `json:"trusted"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(content, prefix)), &envelope); err != nil {
		t.Fatalf("tool content = %q, want valid JSON envelope: %v", content, err)
	}
	if envelope.Trusted {
		t.Fatalf("tool content = %q, want trusted=false", content)
	}
	return envelope.Content
}
