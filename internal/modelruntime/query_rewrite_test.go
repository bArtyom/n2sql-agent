package modelruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type rewriteChatStub struct {
	message string
}

func (s *rewriteChatStub) ChatMessages(_ context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	s.message = messages[len(messages)-1].Content
	return modelclient.ChatResponse{Message: `["关键词一", "关键词一", "关键词二"]`}, nil
}

func TestQueryRewriteServiceParsesAndDeduplicatesVariants(t *testing.T) {
	chat := &rewriteChatStub{}
	service := modelruntime.NewQueryRewriteService(chat)

	variants, err := service.Rewrite(context.Background(), "如何启动", 2)
	if err != nil {
		t.Fatalf("Rewrite() error = %v", err)
	}
	if len(variants) != 2 || variants[0] != "关键词一" || variants[1] != "关键词二" {
		t.Fatalf("variants = %#v", variants)
	}
	if chat.message == "" {
		t.Fatal("rewrite request message is empty")
	}
}

func TestQueryRewriteServiceRejectsInvalidResponse(t *testing.T) {
	service := modelruntime.NewQueryRewriteService(messageChatStub{message: "not-json"})
	_, err := service.Rewrite(context.Background(), "问题", 2)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Rewrite() error = %v, want decode error", err)
	}
}

type messageChatStub struct{ message string }

func (s messageChatStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{Message: s.message}, nil
}
