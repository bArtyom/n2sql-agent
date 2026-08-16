package modelruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type reasoningEffortContextKey struct{}

// WithReasoningEffort carries a provider-neutral reasoning effort to the
// model boundary without expanding every Agent runner interface. The value is
// emitted only when non-empty, so existing providers receive the old payload.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

func reasoningEffort(ctx context.Context) string {
	value, _ := ctx.Value(reasoningEffortContextKey{}).(string)
	return value
}

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

// ToolChatRunnerWithModel is the optional extension used by a session that
// selected a server-configured chat model. Implementations must still enforce
// their own allowlist; the model name is never treated as a URL or credential.
type ToolChatRunnerWithModel interface {
	ToolChatRunner
	ChatMessagesWithToolsForModel(context.Context, string, []modelclient.ChatMessage, []agent.FunctionDefinition) (modelclient.ChatResponse, error)
}

type ChatModelValidator interface {
	ValidateChatModel(context.Context, string) error
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
	return s.ChatMessagesWithToolsForModel(ctx, "", messages, definitions)
}

func (s *ChatService) ChatMessagesWithToolsForModel(ctx context.Context, requestedModel string, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	provider, apiKey, err := s.credentials(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	model, err := provider.ResolveChatModel(requestedModel)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	response, err := s.completer.Chat(ctx, provider.BaseURL, apiKey, modelclient.ChatRequest{
		Model:           model,
		Messages:        messages,
		Tools:           modelToolDefinitions(definitions),
		ReasoningEffort: reasoningEffort(ctx),
		Stream:          false,
	})
	if err != nil {
		return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat with tools: %w", err)}
	}
	return response, nil
}

func (s *ChatService) ValidateChatModel(ctx context.Context, requestedModel string) error {
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return err
	}
	_, err = provider.ResolveChatModel(requestedModel)
	return err
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
