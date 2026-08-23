package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/bArtyom/n2sql-agent/internal/documenttag"
	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/evaluationworker"
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
	fileStore := document.NewLocalFileStore(cfg.UploadDir)
	documentStore := document.NewPostgresStoreWithFileStore(db, fileStore)
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: cfg.ModelProviderTimeout}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingServiceWithLocalFallback(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.LocalEmbeddingBaseURL, cfg.LocalEmbeddingModel, cfg.LocalEmbeddingAPIKey)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chunkStore := documentchunk.NewPostgresStore(db)
	rerankService := modelruntime.NewRerankService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	queryRewriteService := modelruntime.NewQueryRewriteService(chatService)
	followUpService := followup.NewModelService(chatService, cfg.AgentTimeout)
	var parserExtras []documentextractor.ParserEngine
	if cfg.DocumentParserRemoteURL != "" {
		remoteParser, parserErr := documentextractor.NewHTTPParserEngine(
			cfg.DocumentParserRemoteEngine,
			cfg.DocumentParserRemoteURL,
			[]string{"text/plain", "text/markdown", "text/html", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "image/png", "image/jpeg", "image/webp"},
			cfg.DocumentParserAllowedHosts,
			&http.Client{Timeout: cfg.DocumentParserRemoteTimeout},
		)
		if parserErr != nil {
			log.Fatalf("configure remote document parser: %v", parserErr)
		}
		parserExtras = append(parserExtras, remoteParser)
		log.Printf("remote document parser enabled: engine=%s endpoint=%s", cfg.DocumentParserRemoteEngine, cfg.DocumentParserRemoteURL)
	}
	if cfg.DocumentParserMinerUURL != "" {
		mineruParser, parserErr := documentextractor.NewMinerUParserEngine(cfg.DocumentParserMinerUURL, cfg.DocumentParserAllowedHosts, &http.Client{Timeout: cfg.DocumentParserRemoteTimeout})
		if parserErr != nil {
			log.Fatalf("configure MinerU parser: %v", parserErr)
		}
		parserExtras = append(parserExtras, mineruParser)
		log.Printf("MinerU parser enabled: endpoint=%s", cfg.DocumentParserMinerUURL)
	}
	if cfg.DocumentParserPaddleURL != "" {
		paddleParser, parserErr := documentextractor.NewPaddleOCRVLParserEngine(cfg.DocumentParserPaddleURL, cfg.DocumentParserAllowedHosts, &http.Client{Timeout: cfg.DocumentParserRemoteTimeout})
		if parserErr != nil {
			log.Fatalf("configure PaddleOCR-VL parser: %v", parserErr)
		}
		parserExtras = append(parserExtras, paddleParser)
		log.Printf("PaddleOCR-VL parser enabled: endpoint=%s", cfg.DocumentParserPaddleURL)
	}
	if cfg.WeKnoraCloudAppID != "" || cfg.WeKnoraCloudAPIKey != "" {
		cloudParser, parserErr := documentextractor.NewWeKnoraCloudParserEngine(cfg.WeKnoraCloudParserURL, cfg.WeKnoraCloudAppID, cfg.WeKnoraCloudAPIKey, append(cfg.DocumentParserAllowedHosts, "weknora.weixin.qq.com"), &http.Client{Timeout: cfg.DocumentParserRemoteTimeout})
		if parserErr != nil {
			log.Fatalf("configure WeKnora Cloud parser: %v", parserErr)
		}
		parserExtras = append(parserExtras, cloudParser)
		log.Printf("WeKnora Cloud parser enabled: endpoint=%s", cfg.WeKnoraCloudParserURL)
	}
	extractor := documentextractor.NewWithOCRAndImagesAndParser(cfg.UploadDir, nil, nil, cfg.DocumentParserEngine, parserExtras...)
	var imageEnricher worker.ImageEnricher
	if cfg.OCRModel != "" {
		ocrService := modelruntime.NewOCRService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.OCRModel, cfg.OCRPrompt)
		imageEnricher = modelruntime.NewImageEnricherService(ocrService, cfg.ImageCaptionPrompt)
		pageRenderer := documentocr.NewPDFToImageRenderer(cfg.OCRRendererBinary, cfg.OCRRenderDPI, cfg.OCRMaxPages)
		pageText := documentocr.NewPDFToTextPageExtractor(cfg.OCRTextRendererBinary)
		scannedPDF := documentocr.NewServiceWithPageText(pageRenderer, ocrService, pageText, cfg.OCRMaxPages, cfg.OCRConcurrency)
		extractor = documentextractor.NewWithOCRAndImagesAndParser(cfg.UploadDir, scannedPDF, scannedPDF, cfg.DocumentParserEngine, parserExtras...)
		log.Printf("scanned PDF OCR enabled: model=%s renderer=%s max_pages=%d concurrency=%d", cfg.OCRModel, cfg.OCRRendererBinary, cfg.OCRMaxPages, cfg.OCRConcurrency)
	}
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
	documentService := document.NewServiceWithInvalidator(documentStore, fileStore, searchService)
	documentTagService := documenttag.NewService(documenttag.NewPostgresStore(db))
	knowledgeBaseService := knowledgebase.NewServiceWithInvalidator(knowledgeBaseStore, fileStore, searchService)
	parentSplitter := documentchunk.NewAdaptiveSplitter(3000, 300)
	childSplitter := documentchunk.NewAdaptiveSplitter(1000, 150)
	processor := worker.NewEmbeddingHierarchicalChunkingProcessorWithImageEnricher(extractor, parentSplitter, childSplitter, chunkStore, embeddingService, documentService, imageEnricher)
	parserRegistry := extractor.ParserRegistry()
	answerService := rag.NewService(searchService, chatService)
	evaluationStore, err := evaluationrun.NewPostgresStore(db)
	if err != nil {
		log.Fatal(err)
	}
	documentSummaryService := documentsummary.NewService(chunkStore, documentStore, chatService, cfg.DocumentSummaryInputChars)
	documentSummaryService.SetSummaryIndexer(func(ctx context.Context, knowledgeBaseID, documentID int64, content string) error {
		embeddings, err := embeddingService.Embed(ctx, []string{content})
		if err != nil {
			return fmt.Errorf("embed document summary: %w", err)
		}
		if len(embeddings.Data) != 1 || len(embeddings.Data[0].Vector) == 0 {
			return errors.New("embedding provider returned no summary vector")
		}
		return chunkStore.ReplaceSummary(ctx, documentID, content, embeddings.Data[0].Vector)
	})
	documentSummaryAsync := documentsummary.NewAsyncService(documentSummaryService, 1)
	documentSummaryAsync.Run(context.Background())
	go func() {
		backfillContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		knowledgeBases, err := knowledgeBaseService.List(backfillContext)
		if err != nil {
			slog.WarnContext(backfillContext, "document_summary_backfill_list_knowledge_bases_failed", "error", err)
			return
		}
		candidates := make([]documentsummary.BackfillCandidate, 0)
		for _, knowledgeBase := range knowledgeBases {
			documents, listErr := documentService.List(backfillContext, knowledgeBase.ID)
			if listErr != nil {
				slog.WarnContext(backfillContext, "document_summary_backfill_list_documents_failed", "knowledge_base_id", knowledgeBase.ID, "error", listErr)
				continue
			}
			for _, document := range documents {
				candidates = append(candidates, documentsummary.BackfillCandidate{
					KnowledgeBaseID:    document.KnowledgeBaseID,
					DocumentID:         document.ID,
					ProcessingStatus:   document.ProcessingStatus,
					SummaryStatus:      document.SummaryStatus,
					SummaryIndexStatus: document.SummaryIndexStatus,
				})
			}
		}
		if scheduled := documentSummaryAsync.Backfill(backfillContext, candidates); scheduled > 0 {
			slog.InfoContext(backfillContext, "document_summary_backfill_scheduled", "count", scheduled)
		}
	}()
	runner := worker.NewRunnerWithMetricsAndInvalidator(worker.NewPostgresStore(db), processor, metricsRegistry, searchService)
	runner.SetSuccessHook(func(ctx context.Context, task worker.Task) {
		if err := documentSummaryAsync.PreGenerate(ctx, task.KnowledgeBaseID, task.DocumentID); err != nil {
			slog.WarnContext(ctx, "document_summary_pregeneration_failed", "document_id", task.DocumentID, "knowledge_base_id", task.KnowledgeBaseID, "error", err)
		}
	})
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
			Tags:                    documentTagService,
			ParserEngines:           parserRegistry,
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
			EvaluationRuns:          evaluationStore,
			EvaluationReader:        evaluationStore,
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
	evaluationDone := make(chan struct{})
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
	go func() {
		defer close(evaluationDone)
		evaluationWorker := evaluationworker.Worker{Store: evaluationStore, Cases: evaluationworker.SnapshotCaseProvider{}, Answerer: answerService, TopK: retrieval.DefaultResults}
		ticker := time.NewTicker(cfg.WorkerPollInterval)
		defer ticker.Stop()
		for {
			if err := evaluationWorker.RunOnce(runContext); err != nil {
				slog.ErrorContext(runContext, "evaluation_worker_run_error", "error", err)
			}
			select {
			case <-runContext.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	log.Printf("server listening on %s", cfg.Address)
	serveErr := app.RunServer(runContext, server, 0)
	stop()
	<-workerDone
	<-evaluationDone
	<-agentRunDone
	<-checkpointCleanupDone
	if pprofDone != nil {
		<-pprofDone
	}
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
