package modelprovider

import (
	"errors"
	"testing"
)

func TestNormalizeChatModelsIncludesDefaultAndDeduplicates(t *testing.T) {
	provider, err := NormalizeChatModels(Provider{
		ChatModel:  " chat-default ",
		ChatModels: []string{"chat-fast", "chat-default", " chat-fast "},
	})
	if err != nil {
		t.Fatalf("NormalizeChatModels() error = %v", err)
	}
	want := []string{"chat-default", "chat-fast"}
	if len(provider.ChatModels) != len(want) {
		t.Fatalf("chat models = %#v, want %#v", provider.ChatModels, want)
	}
	for index := range want {
		if provider.ChatModels[index] != want[index] {
			t.Fatalf("chat models = %#v, want %#v", provider.ChatModels, want)
		}
	}
}

func TestResolveChatModelUsesDefaultWhenRequestIsEmpty(t *testing.T) {
	provider := Provider{ChatModel: "chat-default", ChatModels: []string{"chat-default", "chat-fast"}}
	model, err := provider.ResolveChatModel("")
	if err != nil || model != "chat-default" {
		t.Fatalf("ResolveChatModel(\"\") = %q, %v; want chat-default, nil", model, err)
	}
}

func TestResolveChatModelRejectsUnknownModel(t *testing.T) {
	provider := Provider{ChatModel: "chat-default", ChatModels: []string{"chat-default", "chat-fast"}}
	_, err := provider.ResolveChatModel("unconfigured")
	if !errors.Is(err, ErrInvalidChatModel) {
		t.Fatalf("ResolveChatModel() error = %v, want ErrInvalidChatModel", err)
	}
}

func TestNormalizeChatModelsRejectsTooManyModels(t *testing.T) {
	models := make([]string, MaxChatModels+1)
	for index := range models {
		models[index] = "model-" + string(rune('a'+index))
	}
	_, err := NormalizeChatModels(Provider{ChatModel: "default", ChatModels: models})
	if !errors.Is(err, ErrInvalidChatModel) {
		t.Fatalf("NormalizeChatModels() error = %v, want ErrInvalidChatModel", err)
	}
}
