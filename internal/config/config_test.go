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
