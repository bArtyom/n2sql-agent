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

func TestLoadReadsStructuredLogFilePath(t *testing.T) {
	t.Setenv("LOG_FILE_PATH", "./.data/logs/test.jsonl")

	cfg := config.Load()

	if cfg.LogFilePath != "./.data/logs/test.jsonl" {
		t.Fatalf("log file path = %q", cfg.LogFilePath)
	}
}

func TestLoadReadsObjectStorageSettings(t *testing.T) {
	t.Setenv("OBJECT_STORAGE_ENDPOINT", "http://minio:9000")
	t.Setenv("OBJECT_STORAGE_REGION", "cn-east-1")
	t.Setenv("OBJECT_STORAGE_ACCESS_KEY", "access")
	t.Setenv("OBJECT_STORAGE_SECRET_KEY", "secret")
	t.Setenv("OBJECT_STORAGE_BUCKET", "knowledge")
	cfg := config.Load()
	if cfg.ObjectStorageEndpoint != "http://minio:9000" || cfg.ObjectStorageRegion != "cn-east-1" || cfg.ObjectStorageBucket != "knowledge" {
		t.Fatalf("object storage settings = %#v", cfg)
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
	t.Setenv("PDF_IMAGE_BIN", "/usr/local/bin/pdfimages")
	t.Setenv("OCR_RENDER_DPI", "180")
	t.Setenv("OCR_MAX_PAGES", "7")
	t.Setenv("OCR_CONCURRENCY", "3")

	cfg := config.Load()
	if cfg.OCRModel != "vision-model" || cfg.OCRPrompt != "read the page" {
		t.Fatalf("OCR model/prompt = %q/%q", cfg.OCRModel, cfg.OCRPrompt)
	}
	if cfg.OCRRendererBinary != "/usr/local/bin/pdftoppm" || cfg.PDFImageBinary != "/usr/local/bin/pdfimages" || cfg.OCRRenderDPI != 180 || cfg.OCRMaxPages != 7 || cfg.OCRConcurrency != 3 {
		t.Fatalf("OCR settings = %#v", cfg)
	}
}

func TestLoadReadsPostprocessTaskSettings(t *testing.T) {
	t.Setenv("POSTPROCESS_TASK_CONCURRENCY", "4")
	t.Setenv("POSTPROCESS_TASK_MAX_ATTEMPTS", "5")
	t.Setenv("POSTPROCESS_TASK_LEASE", "7m")

	cfg := config.Load()
	if cfg.PostprocessConcurrency != 4 || cfg.PostprocessMaxAttempts != 5 || cfg.PostprocessLease != 7*time.Minute {
		t.Fatalf("postprocess task settings = %d/%d/%s", cfg.PostprocessConcurrency, cfg.PostprocessMaxAttempts, cfg.PostprocessLease)
	}
}

func TestLoadReadsAgentWorkerConcurrency(t *testing.T) {
	t.Setenv("AGENT_WORKER_CONCURRENCY", "4")

	cfg := config.Load()

	if cfg.AgentWorkerConcurrency != 4 {
		t.Fatalf("agent worker concurrency = %d, want 4", cfg.AgentWorkerConcurrency)
	}
}

func TestLoadReadsModelCircuitBreakerSettings(t *testing.T) {
	t.Setenv("MODEL_CIRCUIT_BREAKER_FAILURE_THRESHOLD", "7")
	t.Setenv("MODEL_CIRCUIT_BREAKER_RECOVERY_TIMEOUT", "45s")

	cfg := config.Load()

	if cfg.ModelCircuitBreakerFailureThreshold != 7 {
		t.Fatalf("model circuit failure threshold = %d, want 7", cfg.ModelCircuitBreakerFailureThreshold)
	}
	if cfg.ModelCircuitBreakerRecoveryTimeout != 45*time.Second {
		t.Fatalf("model circuit recovery timeout = %s, want 45s", cfg.ModelCircuitBreakerRecoveryTimeout)
	}
}

func TestLoadReadsAgentSettings(t *testing.T) {
	t.Setenv("AGENT_MAX_STEPS", "7")
	t.Setenv("AGENT_TIMEOUT_MS", "1250")
	t.Setenv("AGENT_CHILD_TIMEOUT", "45s")
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
	if cfg.AgentChildTimeout != 45*time.Second {
		t.Fatalf("agent child timeout = %s, want 45s", cfg.AgentChildTimeout)
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

func TestLoadReadsRetrievalCacheSettings(t *testing.T) {
	t.Setenv("RETRIEVAL_CACHE_ENTRIES", "32")
	t.Setenv("RETRIEVAL_CACHE_TTL", "45s")

	cfg := config.Load()
	if cfg.RetrievalCacheEntries != 32 || cfg.RetrievalCacheTTL != 45*time.Second {
		t.Fatalf("retrieval cache settings = %d/%s, want 32/45s", cfg.RetrievalCacheEntries, cfg.RetrievalCacheTTL)
	}
}

func TestLoadReadsMultimodalEmbeddingSettings(t *testing.T) {
	t.Setenv("MULTIMODAL_EMBEDDING_BASE_URL", "http://127.0.0.1:8000/v1")
	t.Setenv("MULTIMODAL_EMBEDDING_MODEL", "Qwen3-VL-Embedding-2B")
	t.Setenv("MULTIMODAL_EMBEDDING_API_KEY", "local-secret")

	cfg := config.Load()
	if cfg.MultimodalEmbeddingBaseURL != "http://127.0.0.1:8000/v1" ||
		cfg.MultimodalEmbeddingModel != "Qwen3-VL-Embedding-2B" ||
		cfg.MultimodalEmbeddingAPIKey != "local-secret" {
		t.Fatalf("multimodal embedding settings = %#v", cfg)
	}
}

func TestLoadReadsAgentStreamRedisSettings(t *testing.T) {
	t.Setenv("AGENT_STREAM_REDIS_URL", "redis://localhost:6379/2")
	t.Setenv("AGENT_STREAM_TTL", "45m")
	t.Setenv("AGENT_STREAM_MAX_LEN", "2048")

	cfg := config.Load()
	if cfg.AgentStreamRedisURL != "redis://localhost:6379/2" {
		t.Fatalf("agent stream redis URL = %q", cfg.AgentStreamRedisURL)
	}
	if cfg.AgentStreamTTL != 45*time.Minute || cfg.AgentStreamMaxLen != 2048 {
		t.Fatalf("agent stream settings = %s/%d", cfg.AgentStreamTTL, cfg.AgentStreamMaxLen)
	}
}

func TestLoadReadsAgentCheckpointSettings(t *testing.T) {
	t.Setenv("AGENT_CHECKPOINT_DIR", "./tmp/checkpoints")
	t.Setenv("AGENT_CHECKPOINT_INLINE_BYTES", "4096")
	t.Setenv("AGENT_CHECKPOINT_FILE_TTL", "30m")
	t.Setenv("AGENT_CHECKPOINT_CLEANUP_INTERVAL", "5m")

	cfg := config.Load()
	if cfg.AgentCheckpointDir != "./tmp/checkpoints" || cfg.AgentCheckpointInlineBytes != 4096 {
		t.Fatalf("checkpoint directory/inline bytes = %q/%d", cfg.AgentCheckpointDir, cfg.AgentCheckpointInlineBytes)
	}
	if cfg.AgentCheckpointFileTTL != 30*time.Minute || cfg.AgentCheckpointCleanup != 5*time.Minute {
		t.Fatalf("checkpoint cleanup settings = %s/%s", cfg.AgentCheckpointFileTTL, cfg.AgentCheckpointCleanup)
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

func TestLoadReadsSandboxSettings(t *testing.T) {
	t.Setenv("SANDBOX_ENABLE", "true")
	t.Setenv("SANDBOX_DOCKER_BIN", "docker.exe")
	t.Setenv("SANDBOX_IMAGE", "python:3.12-alpine")
	t.Setenv("SANDBOX_TIMEOUT", "45s")
	t.Setenv("SANDBOX_MEMORY_BYTES", "268435456")
	t.Setenv("SANDBOX_CPUS", "0.5")
	t.Setenv("SANDBOX_PIDS_LIMIT", "32")
	t.Setenv("SANDBOX_DISK_BYTES", "52428800")
	t.Setenv("SANDBOX_MAX_OUTPUT_BYTES", "65536")
	t.Setenv("SANDBOX_MAX_CODE_BYTES", "32768")
	t.Setenv("SANDBOX_MAX_CONCURRENT", "3")

	cfg := config.Load()
	if !cfg.SandboxEnabled || cfg.SandboxDockerBinary != "docker.exe" || cfg.SandboxImage != "python:3.12-alpine" {
		t.Fatalf("sandbox basic settings = %#v", cfg)
	}
	if cfg.SandboxTimeout != 45*time.Second || cfg.SandboxMemoryBytes != 268435456 || cfg.SandboxCPUs != 0.5 || cfg.SandboxPIDs != 32 || cfg.SandboxDiskBytes != 52428800 || cfg.SandboxMaxOutputBytes != 65536 || cfg.SandboxMaxCodeBytes != 32768 || cfg.SandboxMaxConcurrent != 3 {
		t.Fatalf("sandbox resource settings = %#v", cfg)
	}
}
