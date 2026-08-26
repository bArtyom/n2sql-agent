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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/access"
	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/blobstore"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/diagnostics"
	"github.com/bArtyom/n2sql-agent/internal/docreader"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documentruntime"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/bArtyom/n2sql-agent/internal/documenttag"
	"github.com/bArtyom/n2sql-agent/internal/evaluationprepare"
	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/evaluationworker"
	"github.com/bArtyom/n2sql-agent/internal/followup"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/knowledgegraph"
	"github.com/bArtyom/n2sql-agent/internal/logging"
	"github.com/bArtyom/n2sql-agent/internal/memory"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/postprocess"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
	"github.com/bArtyom/n2sql-agent/internal/usage"
	"github.com/bArtyom/n2sql-agent/internal/worker"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type pendingCountReader interface {
	PendingCount(context.Context) (int64, error)
}

func monitorQueueDepth(ctx context.Context, interval time.Duration, registry *metrics.Registry, warningThreshold int64, queues map[string]pendingCountReader) {
	if registry == nil || len(queues) == 0 {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}
	if warningThreshold <= 0 {
		warningThreshold = 1000
	}
	sample := func() {
		for name, reader := range queues {
			if reader == nil {
				continue
			}
			depth, err := reader.PendingCount(ctx)
			if err != nil {
				slog.WarnContext(ctx, "queue_depth_read_failed", "queue", name, "error", err)
				continue
			}
			registry.ObserveQueueDepth(name, depth)
			if depth >= warningThreshold {
				slog.WarnContext(ctx, "queue_backlog_high", "queue", name, "depth", depth, "warning_threshold", warningThreshold)
			}
		}
	}
	sample()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample()
		}
	}
}

func main() {
	cfg := config.Load()
	logger, closeLogger, err := logging.New(cfg.LogFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configure structured logging: %v\n", err)
		os.Exit(1)
	}
	defer closeLogger()
	slog.SetDefault(logger)
	// Route legacy stdlib log calls through the same JSON sinks while the
	// remaining startup/shutdown paths are gradually migrated to slog.
	log.SetFlags(0)
	log.SetOutput(logging.StdlibWriter(logger))
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	providerStore := modelprovider.NewPostgresStore(db)
	conversationService := conversation.NewService(conversation.NewPostgresStore(db))
	memoryStore := memory.NewPostgresStore(db)
	authStore := auth.NewPostgresStore(db)
	authorizationStore := access.NewPostgresStore(db)
	knowledgeBaseStore := knowledgebase.NewPostgresStore(db)
	var objectStore blobstore.Store
	if cfg.ObjectStorageEndpoint != "" {
		objectStore, err = blobstore.NewS3CompatibleStore(cfg.ObjectStorageEndpoint, cfg.ObjectStorageRegion, cfg.ObjectStorageAccessKey, cfg.ObjectStorageSecretKey, cfg.ObjectStorageBucket, nil)
		if err != nil {
			log.Fatalf("configure S3-compatible object store: %v", err)
		}
		log.Printf("document object storage enabled: endpoint=%s bucket=%s", cfg.ObjectStorageEndpoint, cfg.ObjectStorageBucket)
	} else {
		objectStore, err = blobstore.NewLocalStore(cfg.UploadDir)
	}
	if err != nil {
		log.Fatalf("configure object store: %v", err)
	}
	fileStore, err := document.NewBlobFileStore(objectStore)
	if err != nil {
		log.Fatalf("configure document file store: %v", err)
	}
	documentStore := document.NewPostgresStoreWithFileStore(db, fileStore)
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: cfg.ModelProviderTimeout}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingServiceWithLocalFallback(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.LocalEmbeddingBaseURL, cfg.LocalEmbeddingModel, cfg.LocalEmbeddingAPIKey)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chunkStore := documentchunk.NewPostgresStore(db)
	rerankService := modelruntime.NewRerankService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	queryRewriteService := modelruntime.NewQueryRewriteService(chatService)
	followUpService := followup.NewModelService(chatService, cfg.AgentTimeout)
	var graphStore knowledgegraph.Store
	var graphExtractor *knowledgegraph.Extractor
	if cfg.Neo4jEnabled {
		if strings.TrimSpace(cfg.Neo4jPassword) == "" {
			log.Fatal("NEO4J_PASSWORD is required when NEO4J_ENABLE=true")
		}
		neo4jStore, neo4jErr := knowledgegraph.NewNeo4jHTTPStore(
			cfg.Neo4jURI,
			cfg.Neo4jUsername,
			cfg.Neo4jPassword,
			cfg.Neo4jDatabase,
			&http.Client{Timeout: cfg.ModelProviderTimeout},
		)
		if neo4jErr != nil {
			log.Fatalf("configure Neo4j graph store: %v", neo4jErr)
		}
		graphStore = neo4jStore
		graphExtractor = knowledgegraph.NewExtractor(chatService)
		log.Printf("knowledge graph enabled: endpoint=%s database=%s", cfg.Neo4jURI, cfg.Neo4jDatabase)
	}
	var imageTaskEnricher worker.ImageTaskEnricher
	parserRuntime, err := documentruntime.New(cfg, providerStore, modelClient)
	if err != nil {
		log.Fatalf("configure document parser runtime: %v", err)
	}
	if parserRuntime.OCRService != nil {
		imageTaskEnricher = modelruntime.NewImageEnricherService(parserRuntime.OCRService, cfg.ImageCaptionPrompt)
	}
	var extractor worker.TextExtractor = parserRuntime.Extractor
	if cfg.DocumentReaderGRPCURL != "" {
		docReaderClient, clientErr := docreader.NewClientWithTimeout(context.Background(), cfg.DocumentReaderGRPCURL, docreader.DefaultLimits, cfg.DocumentReaderRetryAttempts, cfg.DocumentReaderTimeout)
		if clientErr != nil {
			log.Fatalf("configure DocReader client: %v", clientErr)
		}
		defer docReaderClient.Close()
		var localFallback *documentextractor.Extractor
		if cfg.DocumentReaderFallbackLocal {
			localFallback = parserRuntime.Extractor
		}
		extractor = documentextractor.NewDocReaderExtractor(cfg.UploadDir, docReaderClient, localFallback, cfg.DocumentParserEngine)
		log.Printf("standalone DocReader enabled: endpoint=%s fallback_local=%t", cfg.DocumentReaderGRPCURL, cfg.DocumentReaderFallbackLocal)
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
	if graphStore != nil {
		searchService.SetGraphSearcher(knowledgegraph.NewRetriever(graphStore, graphExtractor, chunkStore))
	}
	documentService := document.NewServiceWithInvalidator(documentStore, fileStore, searchService)
	if graphStore != nil {
		documentService.SetGraphInvalidator(graphStore)
	}
	documentTagService := documenttag.NewService(documenttag.NewPostgresStore(db))
	knowledgeBaseService := knowledgebase.NewServiceWithInvalidator(knowledgeBaseStore, fileStore, searchService)
	parentSplitter := documentchunk.NewAdaptiveSplitter(3000, 300)
	childSplitter := documentchunk.NewAdaptiveSplitter(1000, 150)
	processor := worker.NewEmbeddingHierarchicalChunkingProcessorWithParseResultStore(extractor, parentSplitter, childSplitter, chunkStore, embeddingService, documentService)
	postprocessStore := postprocess.NewPostgresStoreWithLease(db, cfg.PostprocessLease)
	postprocessRunner := postprocess.NewRunner(postprocessStore, postprocess.RetryPolicy{MaxAttempts: cfg.PostprocessMaxAttempts, InitialDelay: time.Second, MaxDelay: time.Minute})
	parserRegistry := parserRuntime.Registry
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
	documentSummaryAsync.SetTaskEnqueuer(func(ctx context.Context, knowledgeBaseID, documentID int64, indexOnly bool) error {
		kind := postprocess.KindDocumentSummary
		key := fmt.Sprintf("document-summary:%d", documentID)
		if indexOnly {
			kind = postprocess.KindSummaryIndex
			key = fmt.Sprintf("summary-index:%d", documentID)
		}
		return postprocessStore.Enqueue(ctx, postprocess.EnqueueRequest{TaskKey: key, KnowledgeBaseID: knowledgeBaseID, DocumentID: documentID, Kind: kind})
	})
	if err := postprocessRunner.Register(postprocess.KindDocumentSummary, worker.NewDocumentSummaryPostprocessHandler(documentSummaryService, documentService)); err != nil {
		log.Fatal(err)
	}
	if err := postprocessRunner.Register(postprocess.KindSummaryIndex, worker.NewSummaryIndexPostprocessHandler(documentSummaryService)); err != nil {
		log.Fatal(err)
	}
	if err := postprocessRunner.Register(postprocess.KindImageOCR, worker.NewImagePostprocessHandler(postprocessStore, documentService, chunkStore, embeddingService, imageTaskEnricher)); err != nil {
		log.Fatal(err)
	}
	if err := postprocessRunner.Register(postprocess.KindImageCaption, worker.NewImagePostprocessHandler(postprocessStore, documentService, chunkStore, embeddingService, imageTaskEnricher)); err != nil {
		log.Fatal(err)
	}
	if err := postprocessRunner.Register(postprocess.KindRecommendedQuery, worker.NewRecommendedQueryPostprocessHandler(followUpService)); err != nil {
		log.Fatal(err)
	}
	if graphStore != nil {
		if err := postprocessRunner.Register(postprocess.KindGraphExtract, worker.NewGraphPostprocessHandler(chunkStore, graphExtractor, graphStore)); err != nil {
			log.Fatal(err)
		}
	}
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
		if graphStore != nil {
			graphScheduled := 0
			for _, candidate := range candidates {
				if !strings.EqualFold(candidate.ProcessingStatus, "succeeded") {
					continue
				}
				if err := postprocessStore.EnqueueDocument(backfillContext, candidate.DocumentID, postprocess.DocumentOptions{EnableGraph: true}); err != nil {
					slog.WarnContext(backfillContext, "graph_backfill_enqueue_failed", "document_id", candidate.DocumentID, "knowledge_base_id", candidate.KnowledgeBaseID, "error", err)
					continue
				}
				graphScheduled++
			}
			if graphScheduled > 0 {
				slog.InfoContext(backfillContext, "graph_backfill_scheduled", "count", graphScheduled)
			}
		}
	}()
	documentTaskStore := worker.NewPostgresStore(db)
	runner := worker.NewRunnerWithMetricsAndInvalidator(documentTaskStore, processor, metricsRegistry, searchService)
	runner.SetSuccessHook(func(ctx context.Context, task worker.Task) {
		if graphStore != nil {
			if err := graphStore.DeleteDocument(ctx, task.KnowledgeBaseID, task.DocumentID); err != nil {
				slog.WarnContext(ctx, "graph_document_cleanup_before_reindex_failed", "document_id", task.DocumentID, "knowledge_base_id", task.KnowledgeBaseID, "error", err)
			}
		}
		if err := postprocessStore.EnqueueDocument(ctx, task.DocumentID, postprocess.DocumentOptions{
			EnableSummary:         true,
			EnableImageOCR:        imageTaskEnricher != nil,
			EnableImageCaption:    imageTaskEnricher != nil && strings.TrimSpace(cfg.ImageCaptionPrompt) != "",
			EnableRecommendations: true,
			EnableGraph:           graphStore != nil,
		}); err != nil {
			slog.WarnContext(ctx, "document_postprocess_tasks_enqueue_failed", "document_id", task.DocumentID, "knowledge_base_id", task.KnowledgeBaseID, "error", err)
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
				Version:     agentstream.EventSchemaVersion,
				ID:          event.ID,
				RunID:       event.RunID,
				Type:        string(event.Type),
				ToolCallID:  event.ToolCallID,
				ExecutionID: event.ExecutionID,
				TraceID:     event.TraceID,
				StepNumber:  event.StepNumber,
				Data:        event.Data,
				CreatedAt:   event.CreatedAt,
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
			Authorization:           authorizationStore,
			SecureCookies:           cfg.SecureCookies,
			AgentMaxToolResultBytes: cfg.AgentMaxToolResultBytes,
			AgentMaxHistoryBytes:    cfg.AgentMaxHistoryBytes,
			FollowUpSuggestions:     followUpService,
			DocumentSummary:         documentSummaryService,
			PostprocessTasks:        postprocessStore,
			EvaluationRuns:          evaluationStore,
			EvaluationReader:        evaluationStore,
			APIKeyEnvVar:            cfg.ModelProviderAPIKeyEnvVar,
			Metrics:                 metricsRegistry,
		}),
	}
	runContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runContext = usage.WithCallObserver(runContext, metricsRegistry)
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
	postprocessDone := make(chan struct{})
	queueMonitorDone := make(chan struct{})
	go func() {
		defer close(queueMonitorDone)
		monitorQueueDepth(runContext, cfg.WorkerPollInterval, metricsRegistry, int64(cfg.QueueBacklogWarningThreshold), map[string]pendingCountReader{
			"document":    documentTaskStore,
			"postprocess": postprocessStore,
			"evaluation":  evaluationStore,
			"agent":       agentRunStore,
		})
	}()
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
		var workers sync.WaitGroup
		for index := 0; index < cfg.DocumentWorkerConcurrency; index++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				runner.Run(runContext, cfg.WorkerPollInterval, func(err error) {
					slog.ErrorContext(runContext, "document_worker_loop_error", "error", err)
				})
			}()
		}
		workers.Wait()
	}()
	go func() {
		defer close(postprocessDone)
		var workers sync.WaitGroup
		for index := 0; index < cfg.PostprocessConcurrency; index++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				postprocessRunner.Run(runContext, cfg.WorkerPollInterval, func(err error) {
					slog.ErrorContext(runContext, "document_postprocess_worker_loop_error", "error", err)
				})
			}()
		}
		workers.Wait()
	}()
	go func() {
		defer close(evaluationDone)
		evaluationWorker := evaluationworker.Worker{Store: evaluationStore, Cases: evaluationworker.SnapshotCaseProvider{}, Answerer: answerService, Preparer: evaluationprepare.Preparer{DB: db, Embedder: embeddingService}, TopK: retrieval.DefaultResults}
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
	<-postprocessDone
	<-evaluationDone
	<-agentRunDone
	<-checkpointCleanupDone
	<-queueMonitorDone
	if pprofDone != nil {
		<-pprofDone
	}
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
