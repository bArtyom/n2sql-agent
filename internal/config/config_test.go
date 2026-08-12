package config_test

import (
	"testing"
	"time"

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

func TestLoadReadsOCRSettings(t *testing.T) {
	t.Setenv("OCR_MODEL", "vision-model")
	t.Setenv("OCR_PROMPT", "read the page")
	t.Setenv("OCR_RENDERER_BIN", "/usr/local/bin/pdftoppm")
	t.Setenv("OCR_RENDER_DPI", "180")
	t.Setenv("OCR_MAX_PAGES", "7")
	t.Setenv("OCR_CONCURRENCY", "3")

	cfg := config.Load()
	if cfg.OCRModel != "vision-model" || cfg.OCRPrompt != "read the page" {
		t.Fatalf("OCR model/prompt = %q/%q", cfg.OCRModel, cfg.OCRPrompt)
	}
	if cfg.OCRRendererBinary != "/usr/local/bin/pdftoppm" || cfg.OCRRenderDPI != 180 || cfg.OCRMaxPages != 7 || cfg.OCRConcurrency != 3 {
		t.Fatalf("OCR settings = %#v", cfg)
	}
}

func TestLoadReadsAgentSettings(t *testing.T) {
	t.Setenv("AGENT_MAX_STEPS", "7")
	t.Setenv("AGENT_TIMEOUT_MS", "1250")
	t.Setenv("AGENT_MAX_TOOL_RESULT_BYTES", "4096")
	t.Setenv("AGENT_MAX_HISTORY_MESSAGES", "6")
	t.Setenv("AGENT_MAX_HISTORY_BYTES", "8192")
	t.Setenv("AGENT_HISTORY_SUMMARY_ENABLED", "false")
	t.Setenv("AGENT_HISTORY_SUMMARY_TIMEOUT_MS", "2500")

	cfg := config.Load()
	if cfg.AgentMaxSteps != 7 {
		t.Fatalf("agent max steps = %d, want 7", cfg.AgentMaxSteps)
	}
	if cfg.AgentTimeout != 1250*time.Millisecond {
		t.Fatalf("agent timeout = %s, want 1.25s", cfg.AgentTimeout)
	}
	if cfg.AgentMaxToolResultBytes != 4096 {
		t.Fatalf("agent max tool result bytes = %d, want 4096", cfg.AgentMaxToolResultBytes)
	}
	if cfg.AgentMaxHistoryMessages != 6 || cfg.AgentMaxHistoryBytes != 8192 {
		t.Fatalf("agent history settings = %d/%d, want 6/8192", cfg.AgentMaxHistoryMessages, cfg.AgentMaxHistoryBytes)
	}
	if cfg.AgentHistorySummaryEnabled {
		t.Fatal("agent history summary enabled = true, want false")
	}
	if cfg.AgentHistorySummaryTimeout != 2500*time.Millisecond {
		t.Fatalf("agent history summary timeout = %s, want 2.5s", cfg.AgentHistorySummaryTimeout)
	}
}

func TestLoadReadsA2ACleanupSettings(t *testing.T) {
	t.Setenv("A2A_TASK_RETENTION", "48h")
	t.Setenv("A2A_CLEANUP_INTERVAL", "15m")

	cfg := config.Load()
	if cfg.A2ATaskRetention != 48*time.Hour || cfg.A2ACleanupInterval != 15*time.Minute {
		t.Fatalf("A2A cleanup settings = %s/%s, want 48h/15m", cfg.A2ATaskRetention, cfg.A2ACleanupInterval)
	}
}

func TestLoadReadsPprofAddress(t *testing.T) {
	t.Setenv("PPROF_ADDRESS", "127.0.0.1:6060")

	cfg := config.Load()
	if cfg.PprofAddress != "127.0.0.1:6060" {
		t.Fatalf("pprof address = %q, want 127.0.0.1:6060", cfg.PprofAddress)
	}
}

func TestLoadDisablesPprofByDefault(t *testing.T) {
	t.Setenv("PPROF_ADDRESS", "")

	cfg := config.Load()
	if cfg.PprofAddress != "" {
		t.Fatalf("pprof address = %q, want disabled", cfg.PprofAddress)
	}
}

func TestLoadUsesDefaultA2ACleanupSettingsForInvalidValues(t *testing.T) {
	t.Setenv("A2A_TASK_RETENTION", "not-a-duration")
	t.Setenv("A2A_CLEANUP_INTERVAL", "0s")

	cfg := config.Load()
	if cfg.A2ATaskRetention != 7*24*time.Hour || cfg.A2ACleanupInterval != time.Hour {
		t.Fatalf("A2A cleanup settings = %s/%s, want 168h/1h defaults", cfg.A2ATaskRetention, cfg.A2ACleanupInterval)
	}
}

func TestLoadUsesDefaultAgentTimeoutForInvalidValue(t *testing.T) {
	t.Setenv("AGENT_TIMEOUT_MS", "not-a-duration")

	cfg := config.Load()
	if cfg.AgentTimeout != time.Minute {
		t.Fatalf("agent timeout = %s, want 1m default", cfg.AgentTimeout)
	}
}

func TestLoadUsesDefaultAgentToolResultBytesForInvalidValue(t *testing.T) {
	t.Setenv("AGENT_MAX_TOOL_RESULT_BYTES", "1")

	cfg := config.Load()
	if cfg.AgentMaxToolResultBytes != 32*1024 {
		t.Fatalf("agent max tool result bytes = %d, want 32768 default", cfg.AgentMaxToolResultBytes)
	}
}

func TestLoadUsesDefaultAgentHistoryLimitsForInvalidValues(t *testing.T) {
	t.Setenv("AGENT_MAX_HISTORY_MESSAGES", "0")
	t.Setenv("AGENT_MAX_HISTORY_BYTES", "not-a-number")

	cfg := config.Load()
	if cfg.AgentMaxHistoryMessages != 10 || cfg.AgentMaxHistoryBytes != 16*1024 {
		t.Fatalf("agent history settings = %d/%d, want 10/16384 defaults", cfg.AgentMaxHistoryMessages, cfg.AgentMaxHistoryBytes)
	}
}
