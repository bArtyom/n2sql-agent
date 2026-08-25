package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

const (
	defaultAddress                      = ":8080"
	defaultModelProviderAPIKeyEnvVar    = "OPENAI_API_KEY"
	defaultModelProviderAllowedHost     = "api.openai.com"
	defaultUploadDir                    = "./.data/uploads"
	defaultWorkerPollInterval           = 2 * time.Second
	defaultDocumentWorkerConcurrency    = 2
	defaultQueueBacklogWarningThreshold = 1000
	defaultOCRPrompt                    = "请只转写图片中清晰可见的文字，保留原有段落和表格结构，不要解释或补充内容。"
	defaultOCRRendererBinary            = "pdftoppm"
	defaultPDFImageBinary               = "pdfimages"
	defaultOCRRenderDPI                 = 150
	defaultOCRMaxPages                  = 20
	defaultOCRConcurrency               = 1
	defaultPostprocessConcurrency       = 2
	defaultPostprocessMaxAttempts       = 3
	defaultPostprocessLease             = 5 * time.Minute
	defaultDocumentParserRemoteEngine   = "remote_http"
	defaultDocumentParserRemoteTimeout  = 10 * time.Minute
	defaultDocumentReaderAddress        = "127.0.0.1:50051"
	defaultDocumentReaderTimeout        = 15 * time.Minute
	defaultDocumentReaderRetryAttempts  = 2
	defaultAgentMaxSteps                = 4
	defaultAgentTimeout                 = time.Minute
	defaultAgentChildTimeout            = 30 * time.Minute
	defaultModelProviderTimeout         = 2 * time.Minute
	defaultDocumentSummaryInputChars    = 30000
	defaultAgentMaxToolResultBytes      = 32 * 1024
	defaultAgentHistorySummary          = true
	defaultAgentHistorySummaryTimeout   = 10 * time.Second
	defaultRetrievalCacheEntries        = 128
	defaultRetrievalCacheTTL            = 2 * time.Minute
	defaultAgentStreamTTL               = time.Hour
	defaultAgentStreamMaxLen            = 4096
	defaultAgentCheckpointDir           = "./.data/agent-checkpoints"
	defaultAgentCheckpointInlineBytes   = 8 * 1024
	defaultAgentCheckpointFileTTL       = time.Hour
	defaultAgentCheckpointCleanup       = 10 * time.Minute
	defaultLogFilePath                  = "./.data/logs/server.jsonl"
	defaultNeo4jURI                     = "bolt://localhost:7687"
	defaultNeo4jDatabase                = "neo4j"
)

type Config struct {
	Address                      string
	LogFilePath                  string
	DatabaseURL                  string
	ModelProviderAPIKeyEnvVar    string
	ModelProviderAllowedHosts    []string
	LocalEmbeddingBaseURL        string
	LocalEmbeddingModel          string
	LocalEmbeddingAPIKey         string
	UploadDir                    string
	ObjectStorageEndpoint        string
	ObjectStorageRegion          string
	ObjectStorageAccessKey       string
	ObjectStorageSecretKey       string
	ObjectStorageBucket          string
	WorkerPollInterval           time.Duration
	DocumentWorkerConcurrency    int
	QueueBacklogWarningThreshold int
	PprofAddress                 string
	OCRModel                     string
	OCRPrompt                    string
	ImageCaptionPrompt           string
	OCRRendererBinary            string
	PDFImageBinary               string
	OCRTextRendererBinary        string
	OCRRenderDPI                 int
	OCRMaxPages                  int
	OCRConcurrency               int
	PostprocessConcurrency       int
	PostprocessMaxAttempts       int
	PostprocessLease             time.Duration
	DocumentParserEngine         string
	DocumentParserRemoteURL      string
	DocumentParserRemoteEngine   string
	DocumentParserAllowedHosts   []string
	DocumentParserRemoteTimeout  time.Duration
	DocumentReaderGRPCURL        string
	DocumentReaderAddress        string
	DocumentReaderTimeout        time.Duration
	DocumentReaderRetryAttempts  int
	DocumentReaderFallbackLocal  bool
	DocumentParserMinerUURL      string
	DocumentParserPaddleURL      string
	WeKnoraCloudParserURL        string
	WeKnoraCloudAppID            string
	WeKnoraCloudAPIKey           string
	AgentMaxSteps                int
	AgentTimeout                 time.Duration
	AgentChildTimeout            time.Duration
	ModelProviderTimeout         time.Duration
	DocumentSummaryInputChars    int
	AgentMaxToolResultBytes      int
	AgentMaxHistoryMessages      int
	AgentMaxHistoryBytes         int
	AgentHistorySummaryEnabled   bool
	AgentHistorySummaryTimeout   time.Duration
	RetrievalCacheEntries        int
	RetrievalCacheTTL            time.Duration
	Neo4jEnabled                 bool
	Neo4jURI                     string
	Neo4jUsername                string
	Neo4jPassword                string
	Neo4jDatabase                string
	AgentStreamRedisURL          string
	AgentStreamTTL               time.Duration
	AgentStreamMaxLen            int
	AgentCheckpointDir           string
	AgentCheckpointInlineBytes   int
	AgentCheckpointFileTTL       time.Duration
	AgentCheckpointCleanup       time.Duration
	SecureCookies                bool
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
		Address:                      address,
		LogFilePath:                  valueOrDefault("LOG_FILE_PATH", defaultLogFilePath),
		DatabaseURL:                  os.Getenv("DATABASE_URL"),
		ModelProviderAPIKeyEnvVar:    apiKeyEnvVar,
		ModelProviderAllowedHosts:    allowedHosts,
		LocalEmbeddingBaseURL:        strings.TrimSpace(os.Getenv("LOCAL_EMBEDDING_BASE_URL")),
		LocalEmbeddingModel:          strings.TrimSpace(os.Getenv("LOCAL_EMBEDDING_MODEL")),
		LocalEmbeddingAPIKey:         strings.TrimSpace(os.Getenv("LOCAL_EMBEDDING_API_KEY")),
		UploadDir:                    uploadDir(),
		ObjectStorageEndpoint:        strings.TrimSpace(os.Getenv("OBJECT_STORAGE_ENDPOINT")),
		ObjectStorageRegion:          valueOrDefault("OBJECT_STORAGE_REGION", "us-east-1"),
		ObjectStorageAccessKey:       strings.TrimSpace(os.Getenv("OBJECT_STORAGE_ACCESS_KEY")),
		ObjectStorageSecretKey:       strings.TrimSpace(os.Getenv("OBJECT_STORAGE_SECRET_KEY")),
		ObjectStorageBucket:          strings.TrimSpace(os.Getenv("OBJECT_STORAGE_BUCKET")),
		WorkerPollInterval:           workerPollInterval(),
		DocumentWorkerConcurrency:    positiveIntEnv("DOCUMENT_WORKER_CONCURRENCY", defaultDocumentWorkerConcurrency),
		QueueBacklogWarningThreshold: positiveIntEnv("QUEUE_BACKLOG_WARNING_THRESHOLD", defaultQueueBacklogWarningThreshold),
		PprofAddress:                 strings.TrimSpace(os.Getenv("PPROF_ADDRESS")),
		OCRModel:                     strings.TrimSpace(os.Getenv("OCR_MODEL")),
		OCRPrompt:                    valueOrDefault("OCR_PROMPT", defaultOCRPrompt),
		ImageCaptionPrompt:           strings.TrimSpace(os.Getenv("IMAGE_CAPTION_PROMPT")),
		OCRRendererBinary:            valueOrDefault("OCR_RENDERER_BIN", defaultOCRRendererBinary),
		PDFImageBinary:               valueOrDefault("PDF_IMAGE_BIN", defaultPDFImageBinary),
		OCRTextRendererBinary:        valueOrDefault("OCR_TEXT_BIN", "pdftotext"),
		OCRRenderDPI:                 positiveIntEnv("OCR_RENDER_DPI", defaultOCRRenderDPI),
		OCRMaxPages:                  positiveIntEnv("OCR_MAX_PAGES", defaultOCRMaxPages),
		OCRConcurrency:               positiveIntEnv("OCR_CONCURRENCY", defaultOCRConcurrency),
		PostprocessConcurrency:       positiveIntEnv("POSTPROCESS_TASK_CONCURRENCY", defaultPostprocessConcurrency),
		PostprocessMaxAttempts:       positiveIntEnv("POSTPROCESS_TASK_MAX_ATTEMPTS", defaultPostprocessMaxAttempts),
		PostprocessLease:             durationEnv("POSTPROCESS_TASK_LEASE", defaultPostprocessLease),
		DocumentParserEngine:         strings.TrimSpace(os.Getenv("DOCUMENT_PARSER_ENGINE")),
		DocumentParserRemoteURL:      strings.TrimSpace(os.Getenv("DOCUMENT_PARSER_REMOTE_URL")),
		DocumentParserRemoteEngine:   valueOrDefault("DOCUMENT_PARSER_REMOTE_ENGINE", defaultDocumentParserRemoteEngine),
		DocumentParserAllowedHosts:   documentParserAllowedHosts(),
		DocumentParserRemoteTimeout:  durationEnv("DOCUMENT_PARSER_REMOTE_TIMEOUT", defaultDocumentParserRemoteTimeout),
		DocumentReaderGRPCURL:        strings.TrimSpace(os.Getenv("DOCUMENT_READER_GRPC_URL")),
		DocumentReaderAddress:        valueOrDefault("DOCREADER_ADDRESS", defaultDocumentReaderAddress),
		DocumentReaderTimeout:        durationEnv("DOCUMENT_READER_GRPC_TIMEOUT", defaultDocumentReaderTimeout),
		DocumentReaderRetryAttempts:  positiveIntEnv("DOCUMENT_READER_GRPC_RETRY_ATTEMPTS", defaultDocumentReaderRetryAttempts),
		DocumentReaderFallbackLocal:  boolEnv("DOCUMENT_READER_FALLBACK_LOCAL", true),
		DocumentParserMinerUURL:      strings.TrimSpace(os.Getenv("DOCUMENT_PARSER_MINERU_URL")),
		DocumentParserPaddleURL:      strings.TrimSpace(os.Getenv("DOCUMENT_PARSER_PADDLEOCR_VL_URL")),
		WeKnoraCloudParserURL:        strings.TrimSpace(os.Getenv("WEKNORA_CLOUD_PARSER_URL")),
		WeKnoraCloudAppID:            strings.TrimSpace(os.Getenv("WEKNORA_CLOUD_APP_ID")),
		WeKnoraCloudAPIKey:           strings.TrimSpace(os.Getenv("WEKNORA_CLOUD_API_KEY")),
		AgentMaxSteps:                positiveIntEnv("AGENT_MAX_STEPS", defaultAgentMaxSteps),
		AgentTimeout:                 time.Duration(positiveIntEnv("AGENT_TIMEOUT_MS", int(defaultAgentTimeout/time.Millisecond))) * time.Millisecond,
		AgentChildTimeout:            durationEnv("AGENT_CHILD_TIMEOUT", defaultAgentChildTimeout),
		ModelProviderTimeout:         time.Duration(positiveIntEnv("MODEL_PROVIDER_TIMEOUT_MS", int(defaultModelProviderTimeout/time.Millisecond))) * time.Millisecond,
		DocumentSummaryInputChars:    positiveIntEnv("DOCUMENT_SUMMARY_MAX_INPUT_CHARS", defaultDocumentSummaryInputChars),
		AgentMaxToolResultBytes:      agentMaxToolResultBytes(),
		AgentMaxHistoryMessages:      positiveIntEnv("AGENT_MAX_HISTORY_MESSAGES", agent.DefaultMaxHistoryMessages),
		AgentMaxHistoryBytes:         positiveIntEnv("AGENT_MAX_HISTORY_BYTES", agent.DefaultMaxHistoryBytes),
		AgentHistorySummaryEnabled:   boolEnv("AGENT_HISTORY_SUMMARY_ENABLED", defaultAgentHistorySummary),
		AgentHistorySummaryTimeout:   time.Duration(positiveIntEnv("AGENT_HISTORY_SUMMARY_TIMEOUT_MS", int(defaultAgentHistorySummaryTimeout/time.Millisecond))) * time.Millisecond,
		RetrievalCacheEntries:        positiveIntEnv("RETRIEVAL_CACHE_ENTRIES", defaultRetrievalCacheEntries),
		RetrievalCacheTTL:            durationEnv("RETRIEVAL_CACHE_TTL", defaultRetrievalCacheTTL),
		Neo4jEnabled:                 boolEnv("NEO4J_ENABLE", false),
		Neo4jURI:                     valueOrDefault("NEO4J_URI", defaultNeo4jURI),
		Neo4jUsername:                valueOrDefault("NEO4J_USERNAME", "neo4j"),
		Neo4jPassword:                strings.TrimSpace(os.Getenv("NEO4J_PASSWORD")),
		Neo4jDatabase:                valueOrDefault("NEO4J_DATABASE", defaultNeo4jDatabase),
		AgentStreamRedisURL:          strings.TrimSpace(os.Getenv("AGENT_STREAM_REDIS_URL")),
		AgentStreamTTL:               durationEnv("AGENT_STREAM_TTL", defaultAgentStreamTTL),
		AgentStreamMaxLen:            positiveIntEnv("AGENT_STREAM_MAX_LEN", defaultAgentStreamMaxLen),
		AgentCheckpointDir:           agentCheckpointDir(),
		AgentCheckpointInlineBytes:   positiveIntEnv("AGENT_CHECKPOINT_INLINE_BYTES", defaultAgentCheckpointInlineBytes),
		AgentCheckpointFileTTL:       durationEnv("AGENT_CHECKPOINT_FILE_TTL", defaultAgentCheckpointFileTTL),
		AgentCheckpointCleanup:       durationEnv("AGENT_CHECKPOINT_CLEANUP_INTERVAL", defaultAgentCheckpointCleanup),
		SecureCookies:                boolEnv("SECURE_COOKIES", false),
	}
}

func documentParserAllowedHosts() []string {
	hosts := splitHosts(os.Getenv("DOCUMENT_PARSER_ALLOWED_HOSTS"))
	if len(hosts) == 0 {
		return []string{"localhost", "127.0.0.1", "::1"}
	}
	return hosts
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

func agentCheckpointDir() string {
	if value := strings.TrimSpace(os.Getenv("AGENT_CHECKPOINT_DIR")); value != "" {
		return value
	}
	return defaultAgentCheckpointDir
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
