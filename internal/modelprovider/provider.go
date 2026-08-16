package modelprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

var ErrNotFound = errors.New("model provider not found")
var ErrInvalidChatModel = errors.New("invalid chat model")

const (
	MaxChatModels     = 16
	MaxChatModelBytes = 200
)

type Provider struct {
	Name           string   `json:"name"`
	BaseURL        string   `json:"baseUrl"`
	APIKeyEnvVar   string   `json:"apiKeyEnvVar"`
	ChatModel      string   `json:"chatModel"`
	ChatModels     []string `json:"chatModels,omitempty"`
	EmbeddingModel string   `json:"embeddingModel"`
	RerankBaseURL  string   `json:"rerankBaseUrl,omitempty"`
	RerankModel    string   `json:"rerankModel,omitempty"`
	Enabled        bool     `json:"enabled"`
}

// NormalizeChatModels returns a provider with a bounded, de-duplicated model
// allowlist. The default chat model is always the first option so old
// configurations remain usable without any extra setting.
func NormalizeChatModels(provider Provider) (Provider, error) {
	provider.ChatModel = strings.TrimSpace(provider.ChatModel)
	if provider.ChatModel == "" || len(provider.ChatModel) > MaxChatModelBytes {
		return Provider{}, ErrInvalidChatModel
	}
	models := make([]string, 0, len(provider.ChatModels)+1)
	seen := make(map[string]struct{}, len(provider.ChatModels)+1)
	appendModel := func(raw string) error {
		model := strings.TrimSpace(raw)
		if model == "" {
			return nil
		}
		if len(model) > MaxChatModelBytes {
			return ErrInvalidChatModel
		}
		if _, exists := seen[model]; exists {
			return nil
		}
		if len(models) >= MaxChatModels {
			return ErrInvalidChatModel
		}
		seen[model] = struct{}{}
		models = append(models, model)
		return nil
	}
	if err := appendModel(provider.ChatModel); err != nil {
		return Provider{}, err
	}
	for _, model := range provider.ChatModels {
		if err := appendModel(model); err != nil {
			return Provider{}, err
		}
	}
	provider.ChatModels = models
	return provider, nil
}

// ResolveChatModel validates a client-selected model against the provider's
// server-side allowlist. An empty selection intentionally means the default.
func (provider Provider) ResolveChatModel(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return strings.TrimSpace(provider.ChatModel), nil
	}
	for _, model := range provider.ChatModels {
		if model == requested {
			return model, nil
		}
	}
	if requested == strings.TrimSpace(provider.ChatModel) {
		return requested, nil
	}
	return "", fmt.Errorf("%w: %s", ErrInvalidChatModel, requested)
}

type Store interface {
	Current(context.Context) (Provider, error)
	Save(context.Context, Provider) (Provider, error)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Current(ctx context.Context) (Provider, error) {
	var provider Provider
	err := s.db.QueryRowContext(ctx, `SELECT name, base_url, api_key_env_var, chat_model, chat_models, embedding_model, rerank_base_url, rerank_model, enabled FROM model_providers WHERE enabled = TRUE ORDER BY id LIMIT 1`).Scan(&provider.Name, &provider.BaseURL, &provider.APIKeyEnvVar, &provider.ChatModel, pq.Array(&provider.ChatModels), &provider.EmbeddingModel, &provider.RerankBaseURL, &provider.RerankModel, &provider.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get model provider: %w", err)
	}
	return provider, nil
}

func (s *PostgresStore) Save(ctx context.Context, provider Provider) (Provider, error) {
	normalized, err := NormalizeChatModels(provider)
	if err != nil {
		return Provider{}, fmt.Errorf("normalize model provider: %w", err)
	}
	provider = normalized
	err = s.db.QueryRowContext(ctx, `INSERT INTO model_providers (name, base_url, api_key_env_var, chat_model, chat_models, embedding_model, rerank_base_url, rerank_model, enabled) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (name) DO UPDATE SET base_url = EXCLUDED.base_url, api_key_env_var = EXCLUDED.api_key_env_var, chat_model = EXCLUDED.chat_model, chat_models = EXCLUDED.chat_models, embedding_model = EXCLUDED.embedding_model, rerank_base_url = EXCLUDED.rerank_base_url, rerank_model = EXCLUDED.rerank_model, enabled = EXCLUDED.enabled RETURNING name, base_url, api_key_env_var, chat_model, chat_models, embedding_model, rerank_base_url, rerank_model, enabled`, provider.Name, provider.BaseURL, provider.APIKeyEnvVar, provider.ChatModel, pq.Array(provider.ChatModels), provider.EmbeddingModel, provider.RerankBaseURL, provider.RerankModel, provider.Enabled).Scan(&provider.Name, &provider.BaseURL, &provider.APIKeyEnvVar, &provider.ChatModel, pq.Array(&provider.ChatModels), &provider.EmbeddingModel, &provider.RerankBaseURL, &provider.RerankModel, &provider.Enabled)
	if err != nil {
		return Provider{}, fmt.Errorf("save model provider: %w", err)
	}
	return provider, nil
}
