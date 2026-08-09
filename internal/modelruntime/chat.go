package modelruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

var ErrStreamingUnavailable = errors.New("streaming chat is unavailable")

type ChatCallError struct {
	Err error
}

func (e *ChatCallError) Error() string { return e.Err.Error() }

func (e *ChatCallError) Unwrap() error { return e.Err }

type ChatRunner interface {
	Chat(context.Context, string) (modelclient.ChatResponse, error)
}

type ToolChatRunner interface {
	ChatMessagesWithTools(context.Context, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error)
}

// MessageChatRunner is the no-tools chat boundary used by infrastructure tasks
// such as history summarization.
type MessageChatRunner interface {
	ChatMessages(context.Context, []modelclient.ChatMessage) (modelclient.ChatResponse, error)
}

type ChatService struct {
	providers    modelprovider.Store
	completer    modelclient.ChatCompleter
	streamer     modelclient.ChatStreamer
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
}

func NewChatService(providers modelprovider.Store, completer modelclient.ChatCompleter, apiKeyEnvVar string, lookupAPIKey APIKeyLookup) *ChatService {
	return &ChatService{
		providers:    providers,
		completer:    completer,
		streamer:     chatStreamer(completer),
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
	}
}

func (s *ChatService) Chat(ctx context.Context, message string) (modelclient.ChatResponse, error) {
	return s.ChatMessages(ctx, []modelclient.ChatMessage{{
		Role:    "user",
		Content: message,
	}})
}

func (s *ChatService) ChatMessages(ctx context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	provider, apiKey, err := s.credentials(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	response, err := s.completer.Chat(ctx, provider.BaseURL, apiKey, modelclient.ChatRequest{
		Model:    provider.ChatModel,
		Messages: messages,
		Stream:   false,
	})
	if err != nil {
		return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat: %w", err)}
	}
	return response, nil
}

func (s *ChatService) ChatMessagesWithTools(ctx context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	provider, apiKey, err := s.credentials(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	response, err := s.completer.Chat(ctx, provider.BaseURL, apiKey, modelclient.ChatRequest{
		Model:    provider.ChatModel,
		Messages: messages,
		Tools:    modelToolDefinitions(definitions),
		Stream:   false,
	})
	if err != nil {
		return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat with tools: %w", err)}
	}
	return response, nil
}

func modelToolDefinitions(definitions []agent.FunctionDefinition) []modelclient.ToolDefinition {
	if len(definitions) == 0 {
		return nil
	}
	tools := make([]modelclient.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, modelclient.ToolDefinition{
			Type: "function",
			Function: modelclient.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
			},
		})
	}
	return tools
}

func (s *ChatService) StreamMessages(ctx context.Context, messages []modelclient.ChatMessage, onDelta func(string) error) error {
	if s.streamer == nil {
		return ErrStreamingUnavailable
	}
	provider, apiKey, err := s.credentials(ctx)
	if err != nil {
		return err
	}
	if err := s.streamer.ChatStream(ctx, provider.BaseURL, apiKey, modelclient.ChatRequest{
		Model:    provider.ChatModel,
		Messages: messages,
		Stream:   true,
	}, onDelta); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return &ChatCallError{Err: fmt.Errorf("stream chat: %w", err)}
	}
	return nil
}

func chatStreamer(completer modelclient.ChatCompleter) modelclient.ChatStreamer {
	streamer, _ := completer.(modelclient.ChatStreamer)
	return streamer
}

func (s *ChatService) credentials(ctx context.Context) (modelprovider.Provider, string, error) {
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return modelprovider.Provider{}, "", err
	}
	if provider.APIKeyEnvVar != s.apiKeyEnvVar {
		return modelprovider.Provider{}, "", ErrAPIKeyEnvironmentMismatch
	}

	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return modelprovider.Provider{}, "", ErrAPIKeyNotConfigured
	}
	return provider, apiKey, nil
}
