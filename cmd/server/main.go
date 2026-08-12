package main

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/a2a"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/diagnostics"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documentocr"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg := config.Load()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	providerStore := modelprovider.NewPostgresStore(db)
	conversationService := conversation.NewService(conversation.NewPostgresStore(db))
	knowledgeBaseStore := knowledgebase.NewPostgresStore(db)
	documentStore := document.NewPostgresStore(db)
	documentService := document.NewService(documentStore, document.NewLocalFileStore(cfg.UploadDir))
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: 10 * time.Second}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chunkStore := documentchunk.NewPostgresStore(db)
	rerankService := modelruntime.NewRerankService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	extractor := documentextractor.New(cfg.UploadDir)
	if cfg.OCRModel != "" {
		ocrService := modelruntime.NewOCRService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.OCRModel, cfg.OCRPrompt)
		pageRenderer := documentocr.NewPDFToImageRenderer(cfg.OCRRendererBinary, cfg.OCRRenderDPI, cfg.OCRMaxPages)
		scannedPDF := documentocr.NewService(pageRenderer, ocrService, cfg.OCRMaxPages, cfg.OCRConcurrency)
		extractor = documentextractor.NewWithOCR(cfg.UploadDir, scannedPDF)
		log.Printf("scanned PDF OCR enabled: model=%s renderer=%s max_pages=%d concurrency=%d", cfg.OCRModel, cfg.OCRRendererBinary, cfg.OCRMaxPages, cfg.OCRConcurrency)
	}
	processor := worker.NewEmbeddingChunkingProcessor(extractor, documentchunk.NewSplitter(1000, 150), chunkStore, embeddingService)
	metricsRegistry := metrics.New()
	a2aStore := a2a.NewPostgresStore(db)
	runner := worker.NewRunnerWithMetrics(worker.NewPostgresStore(db), processor, metricsRegistry)
	searchService := retrieval.NewHybridServiceWithReranker(embeddingService, chunkStore, chunkStore, rerankService)
	answerService := rag.NewService(searchService, chatService)
	var historySummarizer agentservice.HistorySummarizer
	if cfg.AgentHistorySummaryEnabled {
		historySummarizer = agentservice.NewModelHistorySummarizerWithTimeout(chatService, cfg.AgentHistorySummaryTimeout)
	}
	agentAnswerService, err := agentservice.NewServiceWithLimitsAndSummarizer(
		chatService,
		searchService,
		cfg.AgentMaxSteps,
		cfg.AgentTimeout,
		cfg.AgentMaxToolResultBytes,
		cfg.AgentMaxHistoryMessages,
		cfg.AgentMaxHistoryBytes,
		historySummarizer,
	)
	if err != nil {
		log.Fatal(err)
	}
	knowledgeResearcher, err := multiagent.NewAutonomousKnowledgeSearchResearcher(chatService, searchService, cfg.AgentMaxSteps, cfg.AgentMaxToolResultBytes)
	if err != nil {
		log.Fatal(err)
	}
	modelAnswerer, err := multiagent.NewModelAnswerer(chatService)
	if err != nil {
		log.Fatal(err)
	}
	multiAgentAnswers, err := multiagent.NewSupervisor(knowledgeResearcher, modelAnswerer, cfg.AgentTimeout)
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr: cfg.Address,
		Handler: app.New(app.Dependencies{
			Providers:                  providerStore,
			KnowledgeBases:             knowledgeBaseStore,
			Documents:                  documentService,
			ConnectionChecker:          modelClient,
			Embeddings:                 embeddingService,
			Chat:                       chatService,
			Search:                     searchService,
			Answers:                    answerService,
			StreamingAnswers:           answerService,
			AgentAnswers:               agentAnswerService,
			AgentStreamingAnswers:      agentAnswerService,
			MultiAgentAnswers:          multiAgentAnswers,
			MultiAgentStreamingAnswers: multiAgentAnswers,
			A2AAnswers:                 multiAgentAnswers,
			A2AStore:                   a2aStore,
			A2ATaskTimeout:             cfg.AgentTimeout,
			MCPKnowledgeSearch:         searchService,
			MCPDocuments:               documentService,
			MCPKnowledgeBases:          knowledgeBaseStore,
			Conversations:              conversationService,
			AgentMaxToolResultBytes:    cfg.AgentMaxToolResultBytes,
			AgentMaxHistoryBytes:       cfg.AgentMaxHistoryBytes,
			APIKeyEnvVar:               cfg.ModelProviderAPIKeyEnvVar,
			Metrics:                    metricsRegistry,
		}),
	}
	runContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var pprofDone chan struct{}
	if cfg.PprofAddress != "" {
		pprofServer := &http.Server{Addr: cfg.PprofAddress, Handler: diagnostics.NewPprofHandler()}
		pprofDone = make(chan struct{})
		go func() {
			defer close(pprofDone)
			if err := app.RunServer(runContext, pprofServer, 0); err != nil {
				slog.ErrorContext(runContext, "pprof_server_error", "error", err)
			}
		}()
	}
	workerDone := make(chan struct{})
	a2aRunner := a2a.NewRunnerWithCleanup(a2aStore, multiAgentAnswers, cfg.AgentTimeout, cfg.A2ATaskRetention, cfg.A2ACleanupInterval, metricsRegistry)
	a2aDone := make(chan struct{})
	go func() {
		defer close(a2aDone)
		a2aRunner.Run(runContext, cfg.WorkerPollInterval, func(err error) {
			slog.ErrorContext(runContext, "a2a_worker_loop_error", "error", err)
		})
	}()
	go func() {
		defer close(workerDone)
		runner.Run(runContext, cfg.WorkerPollInterval, func(err error) {
			slog.ErrorContext(runContext, "document_worker_loop_error", "error", err)
		})
	}()

	log.Printf("server listening on %s", cfg.Address)
	serveErr := app.RunServer(runContext, server, 0)
	stop()
	<-workerDone
	<-a2aDone
	if pprofDone != nil {
		<-pprofDone
	}
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
