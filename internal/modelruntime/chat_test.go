package modelruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type chatCompleterStub struct {
	baseURL string
	apiKey  string
	request modelclient.ChatRequest
	err     error
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
	if s.err != nil {
		return modelclient.ChatResponse{}, s.err
	}
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
	if !reflect.DeepEqual(completer.request.Messages, messages) {
		t.Fatalf("messages = %#v, want %#v", completer.request.Messages, messages)
	}
}

func TestChatServiceForwardsToolDefinitions(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "test-chat-model",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})
	definitions := []agent.FunctionDefinition{{
		Name:        "knowledge_search",
		Description: "search the knowledge base",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}

	response, err := service.ChatMessagesWithTools(context.Background(), []modelclient.ChatMessage{{Role: "user", Content: "查年假"}}, definitions)
	if err != nil {
		t.Fatalf("ChatMessagesWithTools() error = %v", err)
	}
	if response.Message != "OK" {
		t.Fatalf("message = %q, want OK", response.Message)
	}
	if len(completer.request.Tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", completer.request.Tools)
	}
	tool := completer.request.Tools[0]
	if tool.Type != "function" || tool.Function.Name != definitions[0].Name || tool.Function.Description != definitions[0].Description {
		t.Fatalf("tool = %#v", tool)
	}
	if string(tool.Function.Parameters) != string(definitions[0].Parameters) {
		t.Fatalf("parameters = %s, want %s", tool.Function.Parameters, definitions[0].Parameters)
	}
}

func TestChatServiceUsesRequestedConfiguredChatModel(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "chat-default",
		ChatModels:   []string{"chat-default", "chat-fast"},
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})

	if _, err := service.ChatMessagesWithToolsForModel(context.Background(), "chat-fast", []modelclient.ChatMessage{{Role: "user", Content: "查年假"}}, nil); err != nil {
		t.Fatalf("ChatMessagesWithToolsForModel() error = %v", err)
	}
	if completer.request.Model != "chat-fast" {
		t.Fatalf("model = %q, want chat-fast", completer.request.Model)
	}
}

func TestChatServiceRejectsUnconfiguredChatModel(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "chat-default",
		ChatModels:   []string{"chat-default"},
	}}
	service := modelruntime.NewChatService(store, &chatCompleterStub{}, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})

	err := service.ValidateChatModel(context.Background(), "unconfigured")
	if !errors.Is(err, modelprovider.ErrInvalidChatModel) {
		t.Fatalf("ValidateChatModel() error = %v, want ErrInvalidChatModel", err)
	}
}

func TestChatServiceWrapsToolCompletionFailure(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "test-chat-model",
	}}
	wantErr := errors.New("upstream unavailable")
	service := modelruntime.NewChatService(store, &chatCompleterStub{err: wantErr}, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})

	_, err := service.ChatMessagesWithTools(context.Background(), []modelclient.ChatMessage{{Role: "user", Content: "查年假"}}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ChatMessagesWithTools() error = %v, want wrapped upstream error", err)
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
