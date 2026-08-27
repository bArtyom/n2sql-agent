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

type enabledProviderStoreStub struct {
	current    modelprovider.Provider
	enabled    []modelprovider.Provider
	currentErr error
}

func (s enabledProviderStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.current, s.currentErr
}
func (enabledProviderStoreStub) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	return modelprovider.Provider{}, nil
}
func (s enabledProviderStoreStub) Enabled(context.Context) ([]modelprovider.Provider, error) {
	return s.enabled, nil
}

type sequencedChatCompleter struct {
	errors []error
	calls  []string
}

func (s *sequencedChatCompleter) Chat(_ context.Context, baseURL, _ string, _ modelclient.ChatRequest) (modelclient.ChatResponse, error) {
	s.calls = append(s.calls, baseURL)
	index := len(s.calls) - 1
	if index < len(s.errors) && s.errors[index] != nil {
		return modelclient.ChatResponse{}, s.errors[index]
	}
	return modelclient.ChatResponse{Message: "fallback response"}, nil
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

type sequencedChatStreamer struct {
	errors []error
	calls  []string
}

func (s *sequencedChatStreamer) Chat(_ context.Context, _ string, _ string, _ modelclient.ChatRequest) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{}, nil
}

func (s *sequencedChatStreamer) ChatStream(_ context.Context, baseURL, _ string, _ modelclient.ChatRequest, onDelta func(string) error) error {
	s.calls = append(s.calls, baseURL)
	index := len(s.calls) - 1
	if index < len(s.errors) && s.errors[index] != nil {
		return s.errors[index]
	}
	return onDelta("fallback stream")
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

func TestChatServiceFailsOverToNextEnabledProviderAfterTransientFailure(t *testing.T) {
	first := modelprovider.Provider{
		Name:         "primary",
		BaseURL:      "https://primary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "primary-chat",
	}
	second := modelprovider.Provider{
		Name:         "secondary",
		BaseURL:      "https://secondary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "secondary-chat",
	}
	completer := &sequencedChatCompleter{errors: []error{
		&modelclient.HTTPStatusError{Operation: "chat", StatusCode: 503},
		nil,
	}}
	service := modelruntime.NewChatService(enabledProviderStoreStub{current: first, enabled: []modelprovider.Provider{first, second}}, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})

	response, err := service.Chat(context.Background(), "retry once")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if response.Message != "fallback response" {
		t.Fatalf("message = %q", response.Message)
	}
	if !reflect.DeepEqual(completer.calls, []string{first.BaseURL, second.BaseURL}) {
		t.Fatalf("provider call order = %#v", completer.calls)
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

func TestChatServiceForwardsReasoningEffortFromContext(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "reasoning-model",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})
	ctx := modelruntime.WithReasoningEffort(context.Background(), "high")
	if _, err := service.ChatMessagesWithToolsForModel(ctx, "reasoning-model", []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, nil); err != nil {
		t.Fatalf("ChatMessagesWithToolsForModel() error = %v", err)
	}
	if completer.request.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want high", completer.request.ReasoningEffort)
	}
}

func TestChatServiceForwardsCompletionTokenLimitFromContext(t *testing.T) {
	store := providerStoreStub{provider: modelprovider.Provider{
		BaseURL:      "https://api.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "chat-default",
	}}
	completer := &chatCompleterStub{}
	service := modelruntime.NewChatService(store, completer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})
	ctx := modelruntime.WithMaxCompletionTokens(context.Background(), 321)
	if _, err := service.ChatMessagesWithTools(ctx, []modelclient.ChatMessage{{Role: "user", Content: "问题"}}, nil); err != nil {
		t.Fatalf("ChatMessagesWithTools() error = %v", err)
	}
	if completer.request.MaxCompletionTokens != 321 {
		t.Fatalf("max completion tokens = %d, want 321", completer.request.MaxCompletionTokens)
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

func TestChatServiceStreamsFailOverBeforeAnyDelta(t *testing.T) {
	first := modelprovider.Provider{
		Name:         "primary",
		BaseURL:      "https://primary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "primary-chat",
	}
	second := modelprovider.Provider{
		Name:         "secondary",
		BaseURL:      "https://secondary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "secondary-chat",
	}
	streamer := &sequencedChatStreamer{errors: []error{&modelclient.HTTPStatusError{Operation: "chat stream", StatusCode: 503}, nil}}
	service := modelruntime.NewChatService(enabledProviderStoreStub{current: first, enabled: []modelprovider.Provider{first, second}}, streamer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
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
	if !reflect.DeepEqual(streamer.calls, []string{first.BaseURL, second.BaseURL}) {
		t.Fatalf("provider call order = %#v", streamer.calls)
	}
	if !reflect.DeepEqual(deltas, []string{"fallback stream"}) {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestChatServiceDoesNotFailOverAfterStreamingStarted(t *testing.T) {
	first := modelprovider.Provider{
		Name:         "primary",
		BaseURL:      "https://primary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "primary-chat",
	}
	second := modelprovider.Provider{
		Name:         "secondary",
		BaseURL:      "https://secondary.example.com/v1",
		APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		ChatModel:    "secondary-chat",
	}
	streamer := &partialFailureStreamer{}
	service := modelruntime.NewChatService(enabledProviderStoreStub{current: first, enabled: []modelprovider.Provider{first, second}}, streamer, "TEST_MODEL_PROVIDER_API_KEY", func(string) (string, bool) {
		return "test-secret", true
	})

	var deltas []string
	err := service.StreamMessages(context.Background(), []modelclient.ChatMessage{{Role: "user", Content: "question"}}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err == nil {
		t.Fatal("StreamMessages() error = nil, want partial stream error")
	}
	if !reflect.DeepEqual(streamer.calls, []string{first.BaseURL}) {
		t.Fatalf("provider call order = %#v, want no duplicate stream", streamer.calls)
	}
	if !reflect.DeepEqual(deltas, []string{"partial"}) {
		t.Fatalf("deltas = %#v", deltas)
	}
}

type partialFailureStreamer struct {
	calls []string
}

func (*partialFailureStreamer) Chat(_ context.Context, _ string, _ string, _ modelclient.ChatRequest) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{}, nil
}

func (s *partialFailureStreamer) ChatStream(_ context.Context, baseURL, _ string, _ modelclient.ChatRequest, onDelta func(string) error) error {
	s.calls = append(s.calls, baseURL)
	if err := onDelta("partial"); err != nil {
		return err
	}
	return &modelclient.HTTPStatusError{Operation: "chat stream", StatusCode: 503}
}
