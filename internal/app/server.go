package app

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

func New(providers modelprovider.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)
	mux.Handle("GET /api/model-provider", handler.NewModelProvider(providers))
	mux.Handle("PUT /api/model-provider", handler.NewModelProvider(providers))

	return mux
}
