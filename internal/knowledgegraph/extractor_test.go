package knowledgegraph

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type chatResponseStub struct {
	prompt string
}

func (s *chatResponseStub) Chat(_ context.Context, prompt string) (modelclient.ChatResponse, error) {
	s.prompt = prompt
	return modelclient.ChatResponse{Message: `{"entities":[{"name":"年假"}],"relations":[]}`}, nil
}

func TestExtractorUsesDedicatedQueryPrompt(t *testing.T) {
	chat := &chatResponseStub{}
	extractor := NewExtractor(chat)
	entities, _, err := extractor.ExtractQueryEntities(context.Background(), "年假怎么计算")
	if err != nil {
		t.Fatalf("ExtractQueryEntities() error = %v", err)
	}
	if len(entities) != 1 || entities[0] != "年假" {
		t.Fatalf("entities = %#v", entities)
	}
	if !strings.Contains(chat.prompt, "用户问题") || strings.Contains(chat.prompt, "原文") {
		t.Fatalf("query prompt = %q", chat.prompt)
	}
}
