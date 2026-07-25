package main

import (
	"log"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

func main() {
	cfg := config.Load()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)

	server := &http.Server{
		Addr:    cfg.Address,
		Handler: mux,
	}

	log.Printf("server listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
