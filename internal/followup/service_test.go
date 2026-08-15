package followup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/followup"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

type chatStub struct {
	response modelclient.ChatResponse
	err      error
}

func (s chatStub) ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	return s.response, s.err
}

func TestModelServiceParsesAndBoundsSuggestions(t *testing.T) {
	service := followup.NewModelService(chatStub{response: modelclient.ChatResponse{Message: "```json\n[{\"text\":\"请展开部署步骤\",\"category\":\"deepen\"},{\"text\":\"请展开部署步骤\",\"category\":\"action\"},{\"text\":\"这个结论有哪些例外？\",\"category\":\"other\"}]\n```"}}, 0)
	suggestions, err := service.Suggest(context.Background(), 7, "如何部署？", "部署需要先准备配置文件。")
	if err != nil {
		t.Fatalf("Suggest() error = %v", err)
	}
	if len(suggestions) != 2 || suggestions[0].ID != "follow-up-1" || suggestions[1].Category != "deepen" {
		t.Fatalf("suggestions = %#v", suggestions)
	}
}

func TestModelServiceRejectsInvalidInputAndResponse(t *testing.T) {
	service := followup.NewModelService(chatStub{response: modelclient.ChatResponse{Message: "[]"}}, 0)
	if _, err := service.Suggest(context.Background(), 7, "", "已有回答"); !errors.Is(err, followup.ErrInvalidRequest) {
		t.Fatalf("invalid input error = %v", err)
	}
	if _, err := service.Suggest(context.Background(), 7, "问题", "回答"); !errors.Is(err, followup.ErrInvalidResponse) {
		t.Fatalf("invalid response error = %v", err)
	}
}

func TestModelServiceWrapsChatFailure(t *testing.T) {
	expected := errors.New("model unavailable")
	service := followup.NewModelService(chatStub{err: expected}, 0)
	_, err := service.Suggest(context.Background(), 7, "问题", "回答")
	if !errors.Is(err, expected) {
		t.Fatalf("Suggest() error = %v, want wrapped model error", err)
	}
}
