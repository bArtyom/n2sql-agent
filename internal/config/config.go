package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddress                   = ":8080"
	defaultModelProviderAPIKeyEnvVar = "OPENAI_API_KEY"
	defaultModelProviderAllowedHost  = "api.openai.com"
	defaultUploadDir                 = "./.data/uploads"
	defaultWorkerPollInterval        = 2 * time.Second
	defaultOCRPrompt                 = "请只转写图片中清晰可见的文字，保留原有段落和表格结构，不要解释或补充内容。"
	defaultOCRRendererBinary         = "pdftoppm"
	defaultOCRRenderDPI              = 150
	defaultOCRMaxPages               = 20
	defaultOCRConcurrency            = 1
	defaultAgentMaxSteps             = 4
	defaultAgentTimeout              = time.Minute
)

type Config struct {
	Address                   string
	DatabaseURL               string
	ModelProviderAPIKeyEnvVar string
	ModelProviderAllowedHosts []string
	UploadDir                 string
	WorkerPollInterval        time.Duration
	OCRModel                  string
	OCRPrompt                 string
	OCRRendererBinary         string
	OCRRenderDPI              int
	OCRMaxPages               int
	OCRConcurrency            int
	AgentMaxSteps             int
	AgentTimeout              time.Duration
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
		Address:                   address,
		DatabaseURL:               os.Getenv("DATABASE_URL"),
		ModelProviderAPIKeyEnvVar: apiKeyEnvVar,
		ModelProviderAllowedHosts: allowedHosts,
		UploadDir:                 uploadDir(),
		WorkerPollInterval:        workerPollInterval(),
		OCRModel:                  strings.TrimSpace(os.Getenv("OCR_MODEL")),
		OCRPrompt:                 valueOrDefault("OCR_PROMPT", defaultOCRPrompt),
		OCRRendererBinary:         valueOrDefault("OCR_RENDERER_BIN", defaultOCRRendererBinary),
		OCRRenderDPI:              positiveIntEnv("OCR_RENDER_DPI", defaultOCRRenderDPI),
		OCRMaxPages:               positiveIntEnv("OCR_MAX_PAGES", defaultOCRMaxPages),
		OCRConcurrency:            positiveIntEnv("OCR_CONCURRENCY", defaultOCRConcurrency),
		AgentMaxSteps:             positiveIntEnv("AGENT_MAX_STEPS", defaultAgentMaxSteps),
		AgentTimeout:              time.Duration(positiveIntEnv("AGENT_TIMEOUT_MS", int(defaultAgentTimeout/time.Millisecond))) * time.Millisecond,
	}
}

func workerPollInterval() time.Duration {
	value, err := time.ParseDuration(os.Getenv("WORKER_POLL_INTERVAL"))
	if err != nil || value <= 0 {
		return defaultWorkerPollInterval
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
