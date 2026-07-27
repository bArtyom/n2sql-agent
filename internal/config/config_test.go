package config_test

import (
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/config"
)

func TestLoadReadsDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://knowledgebase:knowledgebase@localhost:5432/knowledgebase?sslmode=disable")

	cfg := config.Load()

	if cfg.DatabaseURL != "postgres://knowledgebase:knowledgebase@localhost:5432/knowledgebase?sslmode=disable" {
		t.Fatalf("database URL = %q", cfg.DatabaseURL)
	}
}

func TestLoadUsesSafeModelProviderDefaults(t *testing.T) {
	t.Setenv("MODEL_PROVIDER_API_KEY_ENV_VAR", "")
	t.Setenv("MODEL_PROVIDER_ALLOWED_HOSTS", "")

	cfg := config.Load()

	if cfg.ModelProviderAPIKeyEnvVar != "OPENAI_API_KEY" {
		t.Fatalf("API key environment variable = %q", cfg.ModelProviderAPIKeyEnvVar)
	}
	if len(cfg.ModelProviderAllowedHosts) != 1 || cfg.ModelProviderAllowedHosts[0] != "api.openai.com" {
		t.Fatalf("allowed hosts = %q", cfg.ModelProviderAllowedHosts)
	}
}
