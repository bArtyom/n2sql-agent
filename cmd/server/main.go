package main

import (
	"log"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/config"
)

func main() {
	cfg := config.Load()

	server := &http.Server{
		Addr:    cfg.Address,
		Handler: app.New(),
	}

	log.Printf("server listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
