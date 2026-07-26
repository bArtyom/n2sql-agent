package main

import (
	"context"
	"log"

	"github.com/bArtyom/n2sql-agent/internal/config"
	"github.com/bArtyom/n2sql-agent/internal/database/migrations"
)

func main() {
	cfg := config.Load()

	if err := migrations.Apply(context.Background(), cfg.DatabaseURL); err != nil {
		log.Fatal(err)
	}

	log.Print("database migrations applied")
}
