package main

import (
	"database/sql"
	"log"
	"net/http"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg := config.Load()
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	server := &http.Server{
		Addr: cfg.Address,
		Handler: app.New(
			modelprovider.NewPostgresStore(db),
			modelclient.NewHTTPConnectionChecker(&http.Client{Timeout: 10 * time.Second}, cfg.ModelProviderAllowedHosts),
			cfg.ModelProviderAPIKeyEnvVar,
		),
	}

	log.Printf("server listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
