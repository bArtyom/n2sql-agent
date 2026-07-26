package app

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/handler"
)

func New() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)

	return mux
}
