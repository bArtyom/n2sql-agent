package modelprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("model provider not found")

type Provider struct {
	Name           string `json:"name"`
	BaseURL        string `json:"baseUrl"`
	APIKeyEnvVar   string `json:"apiKeyEnvVar"`
	ChatModel      string `json:"chatModel"`
	EmbeddingModel string `json:"embeddingModel"`
	Enabled        bool   `json:"enabled"`
}

type Store interface {
	Current(context.Context) (Provider, error)
	Save(context.Context, Provider) (Provider, error)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Current(ctx context.Context) (Provider, error) {
	var provider Provider
	err := s.db.QueryRowContext(ctx, `SELECT name, base_url, api_key_env_var, chat_model, embedding_model, enabled FROM model_providers WHERE enabled = TRUE ORDER BY id LIMIT 1`).Scan(&provider.Name, &provider.BaseURL, &provider.APIKeyEnvVar, &provider.ChatModel, &provider.EmbeddingModel, &provider.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get model provider: %w", err)
	}
	return provider, nil
}

func (s *PostgresStore) Save(ctx context.Context, provider Provider) (Provider, error) {
	err := s.db.QueryRowContext(ctx, `INSERT INTO model_providers (name, base_url, api_key_env_var, chat_model, embedding_model, enabled) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (name) DO UPDATE SET base_url = EXCLUDED.base_url, api_key_env_var = EXCLUDED.api_key_env_var, chat_model = EXCLUDED.chat_model, embedding_model = EXCLUDED.embedding_model, enabled = EXCLUDED.enabled RETURNING name, base_url, api_key_env_var, chat_model, embedding_model, enabled`, provider.Name, provider.BaseURL, provider.APIKeyEnvVar, provider.ChatModel, provider.EmbeddingModel, provider.Enabled).Scan(&provider.Name, &provider.BaseURL, &provider.APIKeyEnvVar, &provider.ChatModel, &provider.EmbeddingModel, &provider.Enabled)
	if err != nil {
		return Provider{}, fmt.Errorf("save model provider: %w", err)
	}
	return provider, nil
}
