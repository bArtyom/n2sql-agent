package modelruntime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type chatCompleterStub struct {
	baseURL string
	apiKey  string
	request modelclient.ChatRequest
}

func TestChatServiceRejectsMissingAPIKey(t *testing.T) {
	service := modelruntime.NewChatService(
		providerStoreStub{provider: modelprovider.Provider{APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY"}},
		&chatCompleterStub{},
		"TEST_MODEL_PROVIDER_API_KEY",
		func(string) (string, bool) { return "", false },
	)

	_, err := service.Chat(context.Background(), "hello")
	if !errors.Is(err, modelruntime.ErrAPIKeyNotConfigured) {
		t.Fatalf("Chat() error = %v, want ErrAPIKeyNotConfigured", err)
	}
}

func (s *chatCompleterStub) Chat(_ context.Context, baseURL, apiKey string, request modelclient.ChatRequest) (modelclient.ChatResponse, error) {
	s.baseURL = baseURL
	s.apiKey = apiKey
	s.request = request
	return modelclient.ChatResponse{Message: "OK"}, nil
}

func (s *chatCompleterStub) ChatStream(_ context.Context, baseURL, apiKey string, request modelclient.ChatRequest, onDelta func(string) error) error {
	s.baseURL = baseURL
	s.apiKey = apiKey
	s.request = request
	return onDelta("OK")
}

func TestChatServiceUsesConfiguredChatModelAndEnvironmentKey(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "test-chat-model",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(name string) (string, bool) {
		if name != "TEST_MODEL_PROVIDER_API_KEY" {
			t.Fatalf("environment lookup name = %q", name)
		}
		return "test-secret", true
	})

	response, err := service.Chat(context.Background(), "reply with OK")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if response.Message != "OK" {
		t.Fatalf("message = %q, want %q", response.Message, "OK")
	}
	if completer.baseURL != store.provider.BaseURL {
		t.Fatalf("base URL = %q, want %q", completer.baseURL, store.provider.BaseURL)
	}
	if completer.apiKey != "test-secret" {
		t.Fatalf("API key = %q, want environment value", completer.apiKey)
	}
	if completer.request.Model != store.provider.ChatModel {
		t.Fatalf("model = %q, want %q", completer.request.Model, store.provider.ChatModel)
	}
	if len(completer.request.Messages) != 1 || completer.request.Messages[0].Content != "reply with OK" {
		t.Fatalf("messages = %#v", completer.request.Messages)
	}
	if completer.request.Stream {
		t.Fatal("stream = true, want false")
	}
}

func TestChatServiceForwardsSystemAndUserMessages(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "test-chat-model",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})
	messages := []modelclient.ChatMessage{
		{Role: "system", Content: "use sources only"},
		{Role: "user", Content: "question"},
	}

	if _, err := service.ChatMessages(context.Background(), messages); err != nil {
		t.Fatalf("ChatMessages() error = %v", err)
	}
	if len(completer.request.Messages) != 2 || completer.request.Messages[0] != messages[0] || completer.request.Messages[1] != messages[1] {
		t.Fatalf("messages = %#v, want %#v", completer.request.Messages, messages)
	}
}

func TestChatServiceStreamsProvidedMessages(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "test-chat-model",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})
	var deltas []string
	err := service.StreamMessages(context.Background(), []modelclient.ChatMessage{{Role: "user", Content: "question"}}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamMessages() error = %v", err)
	}
	if len(deltas) != 1 || deltas[0] != "OK" || !completer.request.Stream {
		t.Fatalf("deltas = %#v, request = %#v", deltas, completer.request)
	}
}
