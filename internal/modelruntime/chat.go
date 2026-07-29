package modelruntime

import (
	"context"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

type ChatCallError struct {
	Err error
}

func (e *ChatCallError) Error() string { return e.Err.Error() }

func (e *ChatCallError) Unwrap() error { return e.Err }

type ChatRunner interface {
	Chat(context.Context, string) (modelclient.ChatResponse, error)
}

type ChatService struct {
	providers    modelprovider.Store
	completer    modelclient.ChatCompleter
	apiKeyEnvVar string
	lookupAPIKey APIKeyLookup
}

func NewChatService(providers modelprovider.Store, completer modelclient.ChatCompleter, apiKeyEnvVar string, lookupAPIKey APIKeyLookup) *ChatService {
	return &ChatService{
		providers:    providers,
		completer:    completer,
		apiKeyEnvVar: apiKeyEnvVar,
		lookupAPIKey: lookupAPIKey,
	}
}

func (s *ChatService) Chat(ctx context.Context, message string) (modelclient.ChatResponse, error) {
	provider, err := s.providers.Current(ctx)
	if err != nil {
		return modelclient.ChatResponse{}, err
	}
	if provider.APIKeyEnvVar != s.apiKeyEnvVar {
		return modelclient.ChatResponse{}, ErrAPIKeyEnvironmentMismatch
	}

	apiKey, found := s.lookupAPIKey(s.apiKeyEnvVar)
	if !found || apiKey == "" {
		return modelclient.ChatResponse{}, ErrAPIKeyNotConfigured
	}

	response, err := s.completer.Chat(ctx, provider.BaseURL, apiKey, modelclient.ChatRequest{
		Model: provider.ChatModel,
		Messages: []modelclient.ChatMessage{{
			Role:    "user",
			Content: message,
		}},
		Stream: false,
	})
	if err != nil {
		return modelclient.ChatResponse{}, &ChatCallError{Err: fmt.Errorf("complete chat: %w", err)}
	}
	return response, nil
}
