package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/ops"
	"github.com/bArtyom/n2sql-agent/internal/usage"
)

type reasoningEffortContextKey struct{}
type maxCompletionTokensContextKey struct{}

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

// WithMaxCompletionTokens carries an optional provider-neutral output budget
// to the model boundary. A zero value means the provider default is used.
func WithMaxCompletionTokens(ctx context.Context, tokens int) context.Context {
	return context.WithValue(ctx, maxCompletionTokensContextKey{}, tokens)
}

func maxCompletionTokens(ctx context.Context) int {
	value, _ := ctx.Value(maxCompletionTokensContextKey{}).(int)
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
	breaker      *ops.CircuitBreaker
}

func NewChatService(providers modelprovider.Store, completer modelclient.ChatCompleter, apiKeyEnvVar string, lookupAPIKey APIKeyLookup, breakerConfig ...CircuitBreakerConfig) *ChatService {
	return &ChatService{
		providers:    providers,
		completer:    completer,
		streamer:     chatStreamer(completer),
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
		breaker:      newCircuitBreaker(breakerConfig),
	}
}

func (s *ChatService) Chat(ctx context.Context, message string) (modelclient.ChatResponse, error) {
	return s.ChatMessages(ctx, []modelclient.ChatMessage{{
		Role:    "user",
		Content: message,
	}})
}

func (s *ChatService) ChatMessages(ctx context.Context, messages []modelclient.ChatMessage) (modelclient.ChatResponse, error) {
	credentials, err := s.credentialsCandidates(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	var lastErr error
	for index, credential := range credentials {
		if !s.breaker.Allow(credential.provider.Name, capabilityChat) {
			observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", credential.provider.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		response, callErr := s.completer.Chat(ctx, credential.provider.BaseURL, credential.apiKey, modelclient.ChatRequest{
			Model:               credential.provider.ChatModel,
			Messages:            messages,
			MaxCompletionTokens: maxCompletionTokens(ctx),
			Stream:              false,
		})
		observeModelCall(ctx, usage.ModelKindChat, credential.provider.Name, credential.provider.ChatModel, started, response.Usage, callErr)
		if callErr == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(credential.provider.Name, capabilityChat)
			return response, nil
		}
		lastErr = callErr
		if !ops.IsRetryableFailure(callErr) {
			break
		}
		s.breaker.RecordFailure(credential.provider.Name, capabilityChat)
	}
	if lastErr == nil {
		lastErr = modelprovider.ErrNotFound
	}
	return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat: %w", lastErr)}
}

func (s *ChatService) ChatMessagesWithTools(ctx context.Context, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	return s.ChatMessagesWithToolsForModel(ctx, "", messages, definitions)
}

func (s *ChatService) ChatMessagesWithToolsForModel(ctx context.Context, requestedModel string, messages []modelclient.ChatMessage, definitions []agent.FunctionDefinition) (modelclient.ChatResponse, error) {
	credentials, err := s.credentialsCandidates(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	var lastErr error
	for index, credential := range credentials {
		model, resolveErr := credential.provider.ResolveChatModel(requestedModel)
		if resolveErr != nil {
			lastErr = resolveErr
			continue
		}
		if !s.breaker.Allow(credential.provider.Name, capabilityChat) {
			observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", credential.provider.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		response, callErr := s.completer.Chat(ctx, credential.provider.BaseURL, credential.apiKey, modelclient.ChatRequest{
			Model:               model,
			Messages:            messages,
			Tools:               modelToolDefinitions(definitions),
			ReasoningEffort:     reasoningEffort(ctx),
			MaxCompletionTokens: maxCompletionTokens(ctx),
			Stream:              false,
		})
		observeModelCall(ctx, usage.ModelKindChat, credential.provider.Name, model, started, response.Usage, callErr)
		if callErr == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(credential.provider.Name, capabilityChat)
			return response, nil
		}
		lastErr = callErr
		if !ops.IsRetryableFailure(callErr) {
			break
		}
		s.breaker.RecordFailure(credential.provider.Name, capabilityChat)
	}
	if lastErr == nil {
		lastErr = modelprovider.ErrNotFound
	}
	return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat with tools: %w", lastErr)}
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
	credentials, err := s.credentialsCandidates(ctx)
	if err != nil {
		return err
	}
	var lastErr error
	for index, credential := range credentials {
		if !s.breaker.Allow(credential.provider.Name, capabilityChat) {
			observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventOpened)
			lastErr = fmt.Errorf("provider %s: %w", credential.provider.Name, ops.ErrCircuitOpen)
			continue
		}
		started := time.Now()
		emitted := false
		streamErr := s.streamer.ChatStream(ctx, credential.provider.BaseURL, credential.apiKey, modelclient.ChatRequest{
			Model:               credential.provider.ChatModel,
			Messages:            messages,
			MaxCompletionTokens: maxCompletionTokens(ctx),
			Stream:              true,
		}, func(delta string) error {
			emitted = true
			return onDelta(delta)
		})
		observeModelCall(ctx, usage.ModelKindChat, credential.provider.Name, credential.provider.ChatModel, started, nil, streamErr)
		if streamErr == nil {
			if index > 0 {
				observeCircuitBreaker(ctx, credential.provider.Name, capabilityChat, usage.CircuitEventFallback)
			}
			s.breaker.RecordSuccess(credential.provider.Name, capabilityChat)
			return nil
		}
		lastErr = streamErr
		// Once a delta reached the client, retrying another provider would
		// duplicate or reorder the answer. Only fail over before first output.
		if emitted || errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) || !ops.IsRetryableFailure(streamErr) {
			return &ChatCallError{Err: fmt.Errorf("stream chat: %w", streamErr)}
		}
		s.breaker.RecordFailure(credential.provider.Name, capabilityChat)
	}
	if lastErr == nil {
		lastErr = modelprovider.ErrNotFound
	}
	return &ChatCallError{Err: fmt.Errorf("stream chat: %w", lastErr)}
}

func chatStreamer(completer modelclient.ChatCompleter) modelclient.ChatStreamer {
	streamer, _ := completer.(modelclient.ChatStreamer)
	return streamer
}

type providerCredential struct {
	provider modelprovider.Provider
	apiKey   string
}

func (s *ChatService) credentialsCandidates(ctx context.Context) ([]providerCredential, error) {
	current, err := s.providers.Current(ctx)
	if err != nil {
		return nil, err
	}
	providers := []modelprovider.Provider{current}
	if enabledStore, ok := s.providers.(modelprovider.EnabledStore); ok {
		if enabled, listErr := enabledStore.Enabled(ctx); listErr == nil && len(enabled) > 0 {
			providers = enabled
		}
	}
	credentials := make([]providerCredential, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if _, exists := seen[provider.Name]; exists {
			continue
		}
		seen[provider.Name] = struct{}{}
		if provider.APIKeyEnvVar != s.apiKeyEnvVar {
			continue
		}

		apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
		if !found || apiKey == "" {
			return nil, ErrAPIKeyNotConfigured
		}
		credentials = append(credentials, providerCredential{provider: provider, apiKey: apiKey})
	}
	if len(credentials) == 0 {
		return nil, ErrAPIKeyEnvironmentMismatch
	}
	return credentials, nil
}
