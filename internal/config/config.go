package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

const (
	defaultAddress                    = ":8080"
	defaultModelProviderAPIKeyEnvVar  = "OPENAI_API_KEY"
	defaultModelProviderAllowedHost   = "api.openai.com"
	defaultUploadDir                  = "./.data/uploads"
	defaultWorkerPollInterval         = 2 * time.Second
	defaultA2ATaskRetention           = 7 * 24 * time.Hour
	defaultA2ACleanupInterval         = time.Hour
	defaultOCRPrompt                  = "请只转写图片中清晰可见的文字，保留原有段落和表格结构，不要解释或补充内容。"
	defaultOCRRendererBinary          = "pdftoppm"
	defaultOCRRenderDPI               = 150
	defaultOCRMaxPages                = 20
	defaultOCRConcurrency             = 1
	defaultAgentMaxSteps              = 4
	defaultAgentTimeout               = time.Minute
	defaultAgentMaxToolResultBytes    = 32 * 1024
	defaultAgentHistorySummary        = true
	defaultAgentHistorySummaryTimeout = 10 * time.Second
)

type Config struct {
	Address                    string
	DatabaseURL                string
	ModelProviderAPIKeyEnvVar  string
	ModelProviderAllowedHosts  []string
	UploadDir                  string
	WorkerPollInterval         time.Duration
	A2ATaskRetention           time.Duration
	A2ACleanupInterval         time.Duration
	OCRModel                   string
	OCRPrompt                  string
	OCRRendererBinary          string
	OCRRenderDPI               int
	OCRMaxPages                int
	OCRConcurrency             int
	AgentMaxSteps              int
	AgentTimeout               time.Duration
	AgentMaxToolResultBytes    int
	AgentMaxHistoryMessages    int
	AgentMaxHistoryBytes       int
	AgentHistorySummaryEnabled bool
	AgentHistorySummaryTimeout time.Duration
}

func Load() Config {
	address := os.Getenv("SERVER_ADDRESS")
	if address == "" {
		address = defaultAddress
	}
	apiKeyEnvVar := os.Getenv("MODEL_PROVIDER_API_KEY_ENV_VAR")
	if apiKeyEnvVar == "" {
		apiKeyEnvVar = defaultModelProviderAPIKeyEnvVar
	}
	allowedHosts := splitHosts(os.Getenv("MODEL_PROVIDER_ALLOWED_HOSTS"))
	if len(allowedHosts) == 0 {
		allowedHosts = []string{defaultModelProviderAllowedHost}
	}

	return Config{
		Address:                    address,
		DatabaseURL:                os.Getenv("DATABASE_URL"),
		ModelProviderAPIKeyEnvVar:  apiKeyEnvVar,
		ModelProviderAllowedHosts:  allowedHosts,
		UploadDir:                  uploadDir(),
		WorkerPollInterval:         workerPollInterval(),
		A2ATaskRetention:           durationEnv("A2A_TASK_RETENTION", defaultA2ATaskRetention),
		A2ACleanupInterval:         durationEnv("A2A_CLEANUP_INTERVAL", defaultA2ACleanupInterval),
		OCRModel:                   strings.TrimSpace(os.Getenv("OCR_MODEL")),
		OCRPrompt:                  valueOrDefault("OCR_PROMPT", defaultOCRPrompt),
		OCRRendererBinary:          valueOrDefault("OCR_RENDERER_BIN", defaultOCRRendererBinary),
		OCRRenderDPI:               positiveIntEnv("OCR_RENDER_DPI", defaultOCRRenderDPI),
		OCRMaxPages:                positiveIntEnv("OCR_MAX_PAGES", defaultOCRMaxPages),
		OCRConcurrency:             positiveIntEnv("OCR_CONCURRENCY", defaultOCRConcurrency),
		AgentMaxSteps:              positiveIntEnv("AGENT_MAX_STEPS", defaultAgentMaxSteps),
		AgentTimeout:               time.Duration(positiveIntEnv("AGENT_TIMEOUT_MS", int(defaultAgentTimeout/time.Millisecond))) * time.Millisecond,
		AgentMaxToolResultBytes:    agentMaxToolResultBytes(),
		AgentMaxHistoryMessages:    positiveIntEnv("AGENT_MAX_HISTORY_MESSAGES", agent.DefaultMaxHistoryMessages),
		AgentMaxHistoryBytes:       positiveIntEnv("AGENT_MAX_HISTORY_BYTES", agent.DefaultMaxHistoryBytes),
		AgentHistorySummaryEnabled: boolEnv("AGENT_HISTORY_SUMMARY_ENABLED", defaultAgentHistorySummary),
		AgentHistorySummaryTimeout: time.Duration(positiveIntEnv("AGENT_HISTORY_SUMMARY_TIMEOUT_MS", int(defaultAgentHistorySummaryTimeout/time.Millisecond))) * time.Millisecond,
	}
}

func boolEnv(name string, fallback bool) bool {
	value, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	if err != nil {
		return fallback
	}
	return value
}

func agentMaxToolResultBytes() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AGENT_MAX_TOOL_RESULT_BYTES")))
	if err != nil || value < 2 {
		return defaultAgentMaxToolResultBytes
	}
	return value
}

func workerPollInterval() time.Duration {
	return durationEnv("WORKER_POLL_INTERVAL", defaultWorkerPollInterval)
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func uploadDir() string {
	if value := os.Getenv("UPLOAD_DIR"); value != "" {
		return value
	}
	return defaultUploadDir
}

func splitHosts(value string) []string {
	var hosts []string
	for _, host := range strings.Split(value, ",") {
		host = strings.TrimSpace(host)
		if host != "" {
			hosts = append(hosts, strings.ToLower(host))
		}
	}
	return hosts
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func positiveIntEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
