package agentservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentruntime"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

const maxQuestionBytes = 8000

const systemPrompt = "你是一个通用文档知识库问答助手。需要事实信息时必须调用 knowledge_search 工具；只能依据工具返回的资料回答，不要凭空编造。如果资料不足，请明确说明知识库中没有足够信息。"

var (
	ErrInvalidService  = errors.New("invalid agent service")
	ErrInvalidRequest  = errors.New("invalid agent chat request")
	ErrInvalidMaxSteps = errors.New("agent max steps must be positive")
)

type Answerer interface {
	Answer(context.Context, int64, string) (Response, error)
}

type Response struct {
	Answer string          `json:"answer"`
	RunID  string          `json:"run_id"`
	Status agent.RunStatus `json:"status"`
	Steps  []agent.Step    `json:"steps"`
}

type Service struct {
	chat     modelruntime.ToolChatRunner
	searcher retrieval.Searcher
	maxSteps int
	sequence atomic.Uint64
}

func NewService(chat modelruntime.ToolChatRunner, searcher retrieval.Searcher, maxSteps int) (*Service, error) {
	if chat == nil || searcher == nil {
		return nil, ErrInvalidService
	}
	if maxSteps <= 0 {
		return nil, ErrInvalidMaxSteps
	}
	return &Service{chat: chat, searcher: searcher, maxSteps: maxSteps}, nil
}

func (s *Service) Answer(ctx context.Context, knowledgeBaseID int64, question string) (Response, error) {
	question = strings.TrimSpace(question)
	if knowledgeBaseID <= 0 || question == "" || len(question) > maxQuestionBytes {
		return Response{}, ErrInvalidRequest
	}

	registry, err := agent.NewKnowledgeSearchRegistryForKnowledgeBase(s.searcher, knowledgeBaseID)
	if err != nil {
		return Response{}, fmt.Errorf("create knowledge search registry: %w", err)
	}
	engine, err := agentruntime.NewEngine(s.chat, registry, s.maxSteps)
	if err != nil {
		return Response{}, fmt.Errorf("create agent engine: %w", err)
	}

	result, err := engine.Run(ctx, s.nextRunID(), []modelclient.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: question},
	})
	response := responseFromRun(result)
	if err != nil {
		return response, fmt.Errorf("run agent answer: %w", err)
	}
	return response, nil
}

func (s *Service) nextRunID() string {
	return fmt.Sprintf("agent-run-%d-%d", time.Now().UnixNano(), s.sequence.Add(1))
}

func responseFromRun(result agentruntime.Result) Response {
	if result.Run == nil {
		return Response{}
	}
	return Response{
		Answer: result.Run.FinalAnswer(),
		RunID:  result.Run.ID(),
		Status: result.Run.Status(),
		Steps:  result.Run.Steps(),
	}
}
