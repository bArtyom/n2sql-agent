package app

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

func New(providers modelprovider.Store, connectionChecker modelclient.ConnectionChecker, apiKeyEnvVar string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)

	modelProviderHandler := handler.NewModelProvider(providers, apiKeyEnvVar)
	mux.Handle("GET /api/model-provider", modelProviderHandler)
	mux.Handle("PUT /api/model-provider", modelProviderHandler)
	mux.Handle("POST /api/model-provider/connection-test", handler.NewModelProviderConnectionTest(providers, connectionChecker, apiKeyEnvVar))

	return mux
}
