package agentservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type searcherStub struct {
	knowledgeBaseID int64
	query           string
	limit           int
}

func (s *searcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.query = query
	s.limit = limit
	return []retrieval.Result{{Content: "年假制度内容"}}, nil
}

type chatStub struct {
	call func([]modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error)
}

func (s chatStub) ChatMessagesWithTools(_ context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return s.call(messages, definitions)
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
		if err := json.Unmarshal([]byte(messages[3].Content), &results); err != nil || len(results) != 1 || results[0].Content != "年假制度内容" {
			t.Fatalf("tool result = %s", messages[3].Content)
		}
		return modelclient.ChatResponse{Message: "年假按照公司制度执行。"}, nil
	}}

	service, err := agentservice.NewService(chat, searcher, 3)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	response, err := service.Answer(context.Background(), 7, "年假怎么计算？")
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if response.Answer != "年假按照公司制度执行。" || response.Status != agent.RunSucceeded || response.RunID == "" {
		t.Fatalf("response = %#v", response)
	}
	if searcher.knowledgeBaseID != 7 || searcher.query != "年假" {
		t.Fatalf("search request = %#v", searcher)
	}
	if callCount != 2 {
		t.Fatalf("model call count = %d, want 2", callCount)
	}
}

func TestServiceRejectsInvalidQuestion(t *testing.T) {
	service, err := agentservice.NewService(chatStub{call: func([]modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
		t.Fatal("model must not be called")
		return modelclient.ChatResponse{}, nil
	}}, &searcherStub{}, 3)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	_, err = service.Answer(context.Background(), 7, "  ")
	if !errors.Is(err, agentservice.ErrInvalidRequest) {
		t.Fatalf("Answer() error = %v, want ErrInvalidRequest", err)
	}
}

func TestNewServiceRejectsInvalidDependencies(t *testing.T) {
	if _, err := agentservice.NewService(nil, &searcherStub{}, 3); !errors.Is(err, agentservice.ErrInvalidService) {
		t.Fatalf("nil chat error = %v, want ErrInvalidService", err)
	}
	if _, err := agentservice.NewService(chatStub{}, nil, 3); !errors.Is(err, agentservice.ErrInvalidService) {
		t.Fatalf("nil searcher error = %v, want ErrInvalidService", err)
	}
	if _, err := agentservice.NewService(chatStub{}, &searcherStub{}, 0); !errors.Is(err, agentservice.ErrInvalidMaxSteps) {
		t.Fatalf("invalid max steps error = %v, want ErrInvalidMaxSteps", err)
	}
}
