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

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documentocr"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
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
	knowledgeBaseStore := knowledgebase.NewPostgresStore(db)
	documentStore := document.NewPostgresStore(db)
	documentService := document.NewService(documentStore, document.NewLocalFileStore(cfg.UploadDir))
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: 10 * time.Second}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chunkStore := documentchunk.NewPostgresStore(db)
	extractor := documentextractor.New(cfg.UploadDir)
	if cfg.OCRModel != "" {
		ocrService := modelruntime.NewOCRService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv, cfg.OCRModel, cfg.OCRPrompt)
		pageRenderer := documentocr.NewPDFToImageRenderer(cfg.OCRRendererBinary, cfg.OCRRenderDPI, cfg.OCRMaxPages)
		scannedPDF := documentocr.NewService(pageRenderer, ocrService, cfg.OCRMaxPages, cfg.OCRConcurrency)
		extractor = documentextractor.NewWithOCR(cfg.UploadDir, scannedPDF)
		log.Printf("scanned PDF OCR enabled: model=%s renderer=%s max_pages=%d concurrency=%d", cfg.OCRModel, cfg.OCRRendererBinary, cfg.OCRMaxPages, cfg.OCRConcurrency)
	}
	processor := worker.NewEmbeddingChunkingProcessor(extractor, documentchunk.NewSplitter(1000, 150), chunkStore, embeddingService)
	runner := worker.NewRunner(worker.NewPostgresStore(db), processor)
	searchService := retrieval.NewService(embeddingService, chunkStore)
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

	server := &http.Server{
		Addr: cfg.Address,
		Handler: app.New(app.Dependencies{
			Providers:             providerStore,
			KnowledgeBases:        knowledgeBaseStore,
			Documents:             documentService,
			ConnectionChecker:     modelClient,
			Embeddings:            embeddingService,
			Chat:                  chatService,
			Search:                searchService,
			Answers:               answerService,
			StreamingAnswers:      answerService,
			AgentAnswers:          agentAnswerService,
			AgentStreamingAnswers: agentAnswerService,
			Conversations:         conversationService,
			AgentMaxHistoryBytes:  cfg.AgentMaxHistoryBytes,
			APIKeyEnvVar:          cfg.ModelProviderAPIKeyEnvVar,
		}),
	}
	runContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	workerDone := make(chan struct{})
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
	if serveErr != nil {
		log.Fatal(serveErr)
	}
}
