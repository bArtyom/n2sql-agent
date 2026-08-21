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

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/diagnostics"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documentocr"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/bArtyom/n2sql-agent/internal/followup"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/memory"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
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
	memoryStore := memory.NewPostgresStore(db)
	authStore := auth.NewPostgresStore(db)
	knowledgeBaseStore := knowledgebase.NewPostgresStore(db)
	documentStore := document.NewPostgresStore(db)
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: cfg.ModelProviderTimeout}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingServiceWithLocalFallback(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.LocalEmbeddingBaseURL, cfg.LocalEmbeddingModel, cfg.LocalEmbeddingAPIKey)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chunkStore := documentchunk.NewPostgresStore(db)
	rerankService := modelruntime.NewRerankService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	queryRewriteService := modelruntime.NewQueryRewriteService(chatService)
	followUpService := followup.NewModelService(chatService, cfg.AgentTimeout)
	extractor := documentextractor.New(cfg.UploadDir)
	if cfg.OCRModel != "" {
		ocrService := modelruntime.NewOCRService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.OCRModel, cfg.OCRPrompt)
		pageRenderer := documentocr.NewPDFToImageRenderer(cfg.OCRRendererBinary, cfg.OCRRenderDPI, cfg.OCRMaxPages)
		scannedPDF := documentocr.NewService(pageRenderer, ocrService, cfg.OCRMaxPages, cfg.OCRConcurrency)
		extractor = documentextractor.NewWithOCR(cfg.UploadDir, scannedPDF)
		log.Printf("scanned PDF OCR enabled: model=%s renderer=%s max_pages=%d concurrency=%d", cfg.OCRModel, cfg.OCRRendererBinary, cfg.OCRMaxPages, cfg.OCRConcurrency)
	}
	parentSplitter := documentchunk.NewAdaptiveSplitter(3000, 300)
	childSplitter := documentchunk.NewAdaptiveSplitter(1000, 150)
	processor := worker.NewEmbeddingHierarchicalChunkingProcessor(extractor, parentSplitter, childSplitter, chunkStore, embeddingService)
	metricsRegistry := metrics.New()
	agentStreamHub := agentstream.NewHub()
	checkpointFiles, err := agentrun.NewToolResultFileStore(cfg.AgentCheckpointDir, cfg.AgentCheckpointFileTTL)
	if err != nil {
		log.Fatal(err)
	}
	agentRunStore := agentrun.NewPostgresStoreWithCheckpointFiles(db, checkpointFiles, cfg.AgentCheckpointInlineBytes)
	var agentEventStore agentrun.EventStore = agentrun.NewPostgresEventStore(db)
	if cfg.AgentStreamRedisURL != "" {
		redisEventStore, redisErr := agentrun.NewRedisEventStore(
			cfg.AgentStreamRedisURL,
			"",
			cfg.AgentStreamTTL,
			cfg.AgentStreamMaxLen,
		)
		if redisErr != nil {
			log.Printf("agent stream Redis disabled, falling back to PostgreSQL event replay: %v", redisErr)
		} else {
			agentEventStore = redisEventStore
			defer redisEventStore.Close()
			log.Printf("agent stream Redis enabled: ttl=%s max_len=%d", cfg.AgentStreamTTL, cfg.AgentStreamMaxLen)
		}
	}
	searchService := retrieval.NewHybridServiceWithRerankerAndRewriterAndCache(
		embeddingService,
		chunkStore,
		chunkStore,
		rerankService,
		queryRewriteService,
		retrieval.CacheConfig{MaxEntries: cfg.RetrievalCacheEntries, TTL: cfg.RetrievalCacheTTL},
	)
	fileStore := document.NewLocalFileStore(cfg.UploadDir)
	documentService := document.NewServiceWithInvalidator(documentStore, fileStore, searchService)
	knowledgeBaseService := knowledgebase.NewServiceWithInvalidator(knowledgeBaseStore, fileStore, searchService)
	runner := worker.NewRunnerWithMetricsAndInvalidator(worker.NewPostgresStore(db), processor, metricsRegistry, searchService)
	answerService := rag.NewService(searchService, chatService)
	documentSummaryService := documentsummary.NewService(chunkStore, documentStore, chatService, cfg.DocumentSummaryInputChars)
	documentSummaryAsync := documentsummary.NewAsyncService(documentSummaryService, 1)
	documentSummaryAsync.Run(context.Background())
	var historySummarizer agentservice.HistorySummarizer
	if cfg.AgentHistorySummaryEnabled {
		historySummarizer = agentservice.NewModelHistorySummarizerWithTimeout(chatService, cfg.AgentHistorySummaryTimeout)
	}
	agentAnswerService, err := agentservice.NewServiceWithLimitsAndSummarizerAndDocumentsAndChunksAndSummary(
		chatService,
		searchService,
		cfg.AgentMaxSteps,
		cfg.AgentTimeout,
		cfg.AgentMaxToolResultBytes,
		cfg.AgentMaxHistoryMessages,
		cfg.AgentMaxHistoryBytes,
		historySummarizer,
		documentService,
		chunkStore,
		documentSummaryAsync,
	)
	if err != nil {
		log.Fatal(err)
	}
	agentRunExecutor := handler.NewPersistentAgentExecutorWithCheckpoint(agentAnswerService, conversationService, agentStreamHub, metricsRegistry, agentRunStore, agentEventStore, agentRunStore)
	agentRunRunner, err := agentrun.NewRunnerWithEventSink(agentRunStore, agentRunExecutor, func(run agentrun.Run) func(agent.Event) error {
		return func(event agent.Event) error {
			if err := agentEventStore.Append(context.Background(), run, agentstream.Event{
				Version:    agentstream.EventSchemaVersion,
				ID:         event.ID,
				RunID:      event.RunID,
				Type:       string(event.Type),
				StepNumber: event.StepNumber,
				Data:       event.Data,
				CreatedAt:  event.CreatedAt,
			}); err != nil {
				return err
			}
			return agentStreamHub.PublishAgent(event)
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	agentRunRunner.SetChildTimeout(cfg.AgentChildTimeout)
	agentAnswerService.SetMemoryStore(memoryStore)
	agentAnswerService.SetDelegateResearchEnabled(true)
	agentAnswerService.SetChildRunLifecycle(agentservice.NewPersistentChildRunLifecycle(agentRunStore))

	server := &http.Server{
		Addr: cfg.Address,
		Handler: app.New(app.Dependencies{
			Providers:               providerStore,
			KnowledgeBases:          knowledgeBaseService,
			Documents:               documentService,
			ChunkReader:             chunkStore,
			ConnectionChecker:       modelClient,
			Embeddings:              embeddingService,
			Chat:                    chatService,
			Search:                  searchService,
			Answers:                 answerService,
			StreamingAnswers:        answerService,
			AgentAnswers:            agentAnswerService,
			AgentStreamingAnswers:   agentAnswerService,
			AgentStreamHub:          agentStreamHub,
			AgentRuns:               agentRunStore,
			AgentRunReader:          agentRunStore,
			AgentEventStore:         agentEventStore,
			AgentRunExecutor:        agentRunExecutor,
			MCPKnowledgeSearch:      searchService,
			MCPDocuments:            documentService,
			MCPKnowledgeBases:       knowledgeBaseService,
			Conversations:           conversationService,
			Memories:                memoryStore,
			MemoryProfile:           memoryStore,
			Auth:                    authStore,
			SecureCookies:           cfg.SecureCookies,
			AgentMaxToolResultBytes: cfg.AgentMaxToolResultBytes,
			AgentMaxHistoryBytes:    cfg.AgentMaxHistoryBytes,
			FollowUpSuggestions:     followUpService,
			DocumentSummary:         documentSummaryService,
			APIKeyEnvVar:            cfg.ModelProviderAPIKeyEnvVar,
			Metrics:                 metricsRegistry,
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
	agentRunDone := make(chan struct{})
	checkpointCleanupDone := make(chan struct{})
	go func() {
		defer close(checkpointCleanupDone)
		ticker := time.NewTicker(cfg.AgentCheckpointCleanup)
		defer ticker.Stop()
		cleanup := func() {
			if removed, err := checkpointFiles.Cleanup(runContext); err != nil {
				slog.WarnContext(runContext, "agent_checkpoint_cleanup_failed", "error", err)
			} else if removed > 0 {
				slog.InfoContext(runContext, "agent_checkpoint_files_cleaned", "removed", removed)
			}
			if removed, err := agentRunStore.CleanupTerminalToolCheckpoints(runContext, cfg.AgentCheckpointFileTTL); err != nil {
				slog.WarnContext(runContext, "agent_checkpoint_metadata_cleanup_failed", "error", err)
			} else if removed > 0 {
				slog.InfoContext(runContext, "agent_checkpoint_metadata_cleaned", "removed", removed)
			}
		}
		cleanup()
		for {
			select {
			case <-runContext.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
	go func() {
		defer close(agentRunDone)
		agentRunRunner.Run(runContext, cfg.WorkerPollInterval, func(err error) {
			slog.ErrorContext(runContext, "agent_worker_loop_error", "error", err)
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
	<-agentRunDone
	<-checkpointCleanupDone
	if pprofDone != nil {
		<-pprofDone
	}
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
