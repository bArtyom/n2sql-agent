package multiagent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type researcherStub struct {
	report multiagent.ResearchReport
	err    error
	calls  int
}

func (s *researcherStub) Research(_ context.Context, knowledgeBaseID int64, question string, topK int) (multiagent.ResearchReport, error) {
	s.calls++
	if knowledgeBaseID != 7 || question != "如何启动？" || topK != 3 {
		return multiagent.ResearchReport{}, errors.New("unexpected research request")
	}
	return s.report, s.err
}

type finalAnswererStub struct {
	answer string
	err    error
	called bool
	report multiagent.ResearchReport
}

func (s *finalAnswererStub) Synthesize(_ context.Context, question string, report multiagent.ResearchReport) (string, error) {
	s.called = true
	s.report = report
	if question != "如何启动？" {
		return "", errors.New("unexpected answer question")
	}
	return s.answer, s.err
}

func TestSupervisorRunsResearcherThenAnswerer(t *testing.T) {
	researcher := &researcherStub{report: multiagent.ResearchReport{
		Content: "启动命令是 go run ./cmd/server",
		Sources: []retrieval.Result{{DocumentID: 11, Position: 2, Content: "go run ./cmd/server"}},
	}}
	answerer := &finalAnswererStub{answer: "执行 go run ./cmd/server 即可。"}
	supervisor, err := multiagent.NewSupervisor(researcher, answerer, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}

	response, err := supervisor.Answer(context.Background(), 7, "如何启动？", 3)
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != answerer.answer || len(response.Sources) != 1 || !answerer.called {
		t.Fatalf("response = %#v answerer called=%v", response, answerer.called)
	}
	if len(response.Steps) != 2 || response.Steps[0].Role != multiagent.RoleResearcher || response.Steps[0].Status != multiagent.StepSucceeded || response.Steps[1].Role != multiagent.RoleAnswerer || response.Steps[1].Status != multiagent.StepSucceeded {
		t.Fatalf("steps = %#v", response.Steps)
	}
	if answerer.report.Content != researcher.report.Content {
		t.Fatalf("answerer report = %#v, want researcher report", answerer.report)
	}
}

func TestSupervisorReturnsResearchRefusalWithoutCallingAnswerer(t *testing.T) {
	researcher := &researcherStub{report: multiagent.ResearchReport{
		NoRelevantResults: true,
		FallbackAnswer:    "知识库中没有足够资料。",
	}}
	answerer := &finalAnswererStub{answer: "不应调用"}
	supervisor, err := multiagent.NewSupervisor(researcher, answerer, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}

	response, err := supervisor.Answer(context.Background(), 7, "如何启动？", 3)
	if err != nil || response.Answer != researcher.report.FallbackAnswer || answerer.called {
		t.Fatalf("response=%#v err=%v answerer called=%v", response, err, answerer.called)
	}
	if len(response.Steps) != 2 || response.Steps[1].Status != multiagent.StepSkipped {
		t.Fatalf("steps = %#v, want answerer skipped", response.Steps)
	}
}

func TestSupervisorStopsWhenResearchFails(t *testing.T) {
	researcher := &researcherStub{err: errors.New("embedding unavailable")}
	answerer := &finalAnswererStub{answer: "不应调用"}
	supervisor, err := multiagent.NewSupervisor(researcher, answerer, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}

	response, err := supervisor.Answer(context.Background(), 7, "如何启动？", 3)
	if err == nil || !strings.Contains(err.Error(), "researcher") || answerer.called {
		t.Fatalf("response=%#v err=%v answerer called=%v", response, err, answerer.called)
	}
	if len(response.Steps) != 1 || response.Steps[0].Status != multiagent.StepFailed {
		t.Fatalf("steps = %#v, want researcher failed", response.Steps)
	}
}

func TestSupervisorStopsWhenAnswererFails(t *testing.T) {
	researcher := &researcherStub{report: multiagent.ResearchReport{Content: "资料"}}
	answerer := &finalAnswererStub{err: errors.New("chat unavailable")}
	supervisor, err := multiagent.NewSupervisor(researcher, answerer, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}

	response, err := supervisor.Answer(context.Background(), 7, "如何启动？", 3)
	if err == nil || !strings.Contains(err.Error(), "answerer") || !answerer.called {
		t.Fatalf("response=%#v err=%v answerer called=%v", response, err, answerer.called)
	}
	if len(response.Steps) != 2 || response.Steps[1].Status != multiagent.StepFailed {
		t.Fatalf("steps = %#v, want answerer failed", response.Steps)
	}
}

type blockingResearcher struct{}

func (blockingResearcher) Research(ctx context.Context, _ int64, _ string, _ int) (multiagent.ResearchReport, error) {
	<-ctx.Done()
	return multiagent.ResearchReport{}, ctx.Err()
}

func TestSupervisorHonorsTimeout(t *testing.T) {
	supervisor, err := multiagent.NewSupervisor(blockingResearcher{}, &finalAnswererStub{}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}

	response, err := supervisor.Answer(context.Background(), 7, "问题", 3)
	if !errors.Is(err, context.DeadlineExceeded) || len(response.Steps) != 1 || response.Steps[0].Status != multiagent.StepFailed {
		t.Fatalf("response=%#v err=%v, want timeout", response, err)
	}
}

func TestSupervisorRejectsInvalidConfigurationAndRequest(t *testing.T) {
	researcher := &researcherStub{}
	answerer := &finalAnswererStub{}
	if _, err := multiagent.NewSupervisor(nil, answerer, time.Second); !errors.Is(err, multiagent.ErrInvalidSupervisor) {
		t.Fatalf("nil researcher error = %v", err)
	}
	if _, err := multiagent.NewSupervisor(researcher, nil, time.Second); !errors.Is(err, multiagent.ErrInvalidSupervisor) {
		t.Fatalf("nil answerer error = %v", err)
	}
	if _, err := multiagent.NewSupervisor(researcher, answerer, 0); !errors.Is(err, multiagent.ErrInvalidTimeout) {
		t.Fatalf("invalid timeout error = %v", err)
	}

	supervisor, err := multiagent.NewSupervisor(researcher, answerer, time.Second)
	if err != nil {
		t.Fatalf("NewSupervisor() error = %v", err)
	}
	for _, test := range []struct {
		name            string
		knowledgeBaseID int64
		question        string
		topK            int
		want            error
	}{
		{name: "invalid knowledge base", knowledgeBaseID: 0, question: "问题", topK: 3, want: multiagent.ErrInvalidRequest},
		{name: "empty question", knowledgeBaseID: 7, question: " ", topK: 3, want: multiagent.ErrInvalidRequest},
		{name: "invalid topK", knowledgeBaseID: 7, question: "问题", topK: 21, want: multiagent.ErrInvalidRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := supervisor.Answer(context.Background(), test.knowledgeBaseID, test.question, test.topK)
			if !errors.Is(err, test.want) {
				t.Fatalf("Answer() error = %v, want %v", err, test.want)
			}
		})
	}
}

type searcherStub struct{}

func (searcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: 11, Position: 2, Content: "go run ./cmd/server"}}, nil
}

func TestKnowledgeSearchResearcherUsesScopedReadOnlyTool(t *testing.T) {
	researcher, err := multiagent.NewKnowledgeSearchResearcher(searcherStub{}, 2048)
	if err != nil {
		t.Fatalf("NewKnowledgeSearchResearcher() error = %v", err)
	}

	report, err := researcher.Research(context.Background(), 7, "如何启动？", 3)
	if err != nil {
		t.Fatalf("Research() error = %v", err)
	}
	if report.NoRelevantResults || len(report.Sources) != 1 || !strings.Contains(report.Content, "go run ./cmd/server") {
		t.Fatalf("report = %#v", report)
	}
}

type sensitiveSearcherStub struct{}

func (sensitiveSearcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{DocumentID: 11, Position: 2, Content: "password=super-secret"}}, nil
}

func TestKnowledgeSearchResearcherRedactsReportAndSources(t *testing.T) {
	researcher, err := multiagent.NewKnowledgeSearchResearcher(sensitiveSearcherStub{}, 2048)
	if err != nil {
		t.Fatalf("NewKnowledgeSearchResearcher() error = %v", err)
	}

	report, err := researcher.Research(context.Background(), 7, "问题", 3)
	if err != nil {
		t.Fatalf("Research() error = %v", err)
	}
	if strings.Contains(report.Content, "super-secret") || len(report.Sources) != 1 || strings.Contains(report.Sources[0].Content, "super-secret") {
		t.Fatalf("report = %#v, contains sensitive content", report)
	}
}

type chatStub struct {
	messages []modelclient.ChatMessage
}

func (s *chatStub) ChatMessages(_ context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	s.messages = messages
	return modelclient.ChatResponse{Message: "最终回答"}, nil
}

func TestModelAnswererMarksResearchAsUntrusted(t *testing.T) {
	chat := &chatStub{}
	answerer, err := multiagent.NewModelAnswerer(chat)
	if err != nil {
		t.Fatalf("NewModelAnswerer() error = %v", err)
	}
	answer, err := answerer.Synthesize(context.Background(), "问题", multiagent.ResearchReport{Content: "资料中的内容"})
	if err != nil || answer != "最终回答" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if len(chat.messages) != 2 || !strings.Contains(chat.messages[1].Content, "UNTRUSTED_TOOL_RESULT") || !strings.Contains(chat.messages[1].Content, "资料中的内容") {
		t.Fatalf("chat messages = %#v", chat.messages)
	}
}

type toolChatStub struct {
	responses []modelclient.ChatResponse
	calls     int
	messages  [][]modelclient.ChatMessage
}

func (s *toolChatStub) ChatMessagesWithTools(_ context.Context, messages []modelclient.ChatMessage, _ []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	s.calls++
	s.messages = append(s.messages, append([]modelclient.ChatMessage(nil), messages...))
	if len(s.responses) == 0 {
		return modelclient.ChatResponse{Message: "研究摘要"}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

type querySearchStub struct {
	queries map[string][]retrieval.Result
	seen    []string
}

func (s *querySearchStub) Search(_ context.Context, _ int64, query string, _ int) ([]retrieval.Result, error) {
	s.seen = append(s.seen, query)
	return s.queries[query], nil
}

func researchToolCall(id, query string) modelclient.ChatResponse {
	return modelclient.ChatResponse{ToolCalls: []modelclient.ToolCall{{
		ID:   id,
		Type: "function",
		Function: modelclient.ToolCallFunction{
			Name:      "knowledge_search",
			Arguments: fmt.Sprintf(`{"query":%q}`, query),
		},
	}}}
}

func TestAutonomousResearcherLetsModelContinueSearching(t *testing.T) {
	searcher := &querySearchStub{queries: map[string][]retrieval.Result{
		"启动命令": {{DocumentID: 11, Position: 1, Distance: 0.2, Content: "go run ./cmd/server"}},
		"开发环境": {{DocumentID: 12, Position: 2, Distance: 0.3, Content: "docker compose up -d"}},
	}}
	chat := &toolChatStub{responses: []modelclient.ChatResponse{
		researchToolCall("call-1", "启动命令"),
		researchToolCall("call-2", "开发环境"),
		{Message: "研究摘要：项目需要 Go 和 Docker。"},
	}}
	researcher, err := multiagent.NewAutonomousKnowledgeSearchResearcher(chat, searcher, 3, 2048)
	if err != nil {
		t.Fatalf("NewAutonomousKnowledgeSearchResearcher() error = %v", err)
	}

	report, err := researcher.Research(context.Background(), 7, "如何启动项目？", 3)
	if err != nil {
		t.Fatalf("Research() error = %v", err)
	}
	if report.Content != "研究摘要：项目需要 Go 和 Docker。" || len(report.Sources) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if chat.calls != 3 || len(searcher.seen) != 2 {
		t.Fatalf("chat calls=%d search queries=%#v, want 3 and 2", chat.calls, searcher.seen)
	}
}

func TestAutonomousResearcherContinuesAfterNoRelevantResults(t *testing.T) {
	searcher := &querySearchStub{queries: map[string][]retrieval.Result{
		"原始问题":  nil,
		"替代关键词": {{DocumentID: 13, Position: 1, Distance: 0.2, Content: "有用资料"}},
	}}
	chat := &toolChatStub{responses: []modelclient.ChatResponse{
		researchToolCall("call-1", "原始问题"),
		researchToolCall("call-2", "替代关键词"),
		{Message: "找到资料了。"},
	}}
	researcher, err := multiagent.NewAutonomousKnowledgeSearchResearcher(chat, searcher, 3, 2048)
	if err != nil {
		t.Fatalf("NewAutonomousKnowledgeSearchResearcher() error = %v", err)
	}

	report, err := researcher.Research(context.Background(), 7, "问题", 3)
	if err != nil || report.NoRelevantResults || len(report.Sources) != 1 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if len(searcher.seen) != 2 {
		t.Fatalf("search queries=%#v, want retry with alternative query", searcher.seen)
	}
}

func TestAutonomousResearcherDetectsDuplicateQuery(t *testing.T) {
	searcher := &querySearchStub{queries: map[string][]retrieval.Result{
		"相同问题": {{DocumentID: 14, Position: 1, Distance: 0.2, Content: "已有资料"}},
	}}
	chat := &toolChatStub{responses: []modelclient.ChatResponse{
		researchToolCall("call-1", "相同问题"),
		researchToolCall("call-2", "相同问题"),
		{Message: "依据已有资料完成研究。"},
	}}
	researcher, err := multiagent.NewAutonomousKnowledgeSearchResearcher(chat, searcher, 3, 2048)
	if err != nil {
		t.Fatalf("NewAutonomousKnowledgeSearchResearcher() error = %v", err)
	}

	if _, err := researcher.Research(context.Background(), 7, "问题", 3); err != nil {
		t.Fatalf("Research() error = %v", err)
	}
	if len(searcher.seen) != 1 {
		t.Fatalf("search queries=%#v, duplicate query should not hit searcher", searcher.seen)
	}
	if len(chat.messages) < 3 || !strings.Contains(chat.messages[2][len(chat.messages[2])-1].Content, "duplicate_query") {
		t.Fatalf("duplicate tool result was not returned to model: %#v", chat.messages)
	}
}

func TestAutonomousResearcherStopsAtMaxSteps(t *testing.T) {
	searcher := &querySearchStub{queries: map[string][]retrieval.Result{
		"继续": {{DocumentID: 15, Position: 1, Distance: 0.2, Content: "资料"}},
	}}
	chat := &toolChatStub{responses: []modelclient.ChatResponse{
		researchToolCall("call-1", "继续"),
		researchToolCall("call-2", "继续二"),
	}}
	researcherAgent, err := multiagent.NewAutonomousKnowledgeSearchResearcher(chat, searcher, 2, 2048)
	if err != nil {
		t.Fatalf("NewAutonomousKnowledgeSearchResearcher() error = %v", err)
	}

	_, err = researcherAgent.Research(context.Background(), 7, "问题", 3)
	if !errors.Is(err, agentruntime.ErrMaxStepsExceeded) {
		t.Fatalf("Research() error = %v, want ErrMaxStepsExceeded", err)
	}
}
