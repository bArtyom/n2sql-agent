package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg := config.Load()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	providerStore := modelprovider.NewPostgresStore(db)
	knowledgeBaseStore := knowledgebase.NewPostgresStore(db)
	documentStore := document.NewPostgresStore(db)
	documentService := document.NewService(documentStore, document.NewLocalFileStore(cfg.UploadDir))
	modelClient := modelclient.NewHTTPClient(&http.Client{Timeout: 10 * time.Second}, cfg.ModelProviderAllowedHosts)
	embeddingService := modelruntime.NewEmbeddingService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)
	chatService := modelruntime.NewChatService(providerStore, modelClient, cfg.ModelProviderAPIKeyEnvVar, os.LookupEnv)

	server := &http.Server{
		Addr: cfg.Address,
		Handler: app.New(app.Dependencies{
			Providers:         providerStore,
			KnowledgeBases:    knowledgeBaseStore,
			Documents:         documentService,
			ConnectionChecker: modelClient,
			Embeddings:        embeddingService,
			Chat:              chatService,
			APIKeyEnvVar:      cfg.ModelProviderAPIKeyEnvVar,
		}),
	}

	log.Printf("server listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
