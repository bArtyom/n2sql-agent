package app

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

type Dependencies struct {
	Providers         modelprovider.Store
	ConnectionChecker modelclient.ConnectionChecker
	Embeddings        modelruntime.EmbeddingRunner
	APIKeyEnvVar      string
}

func New(dependencies Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)

	modelProviderHandler := handler.NewModelProvider(dependencies.Providers, dependencies.APIKeyEnvVar)
	mux.Handle("GET /api/model-provider", modelProviderHandler)
	mux.Handle("PUT /api/model-provider", modelProviderHandler)
	mux.Handle("POST /api/model-provider/connection-test", handler.NewModelProviderConnectionTest(dependencies.Providers, dependencies.ConnectionChecker, dependencies.APIKeyEnvVar))
	if dependencies.Embeddings != nil {
		mux.Handle("POST /api/model-provider/embedding-test", handler.NewModelProviderEmbeddingTest(dependencies.Embeddings))
	}

	return mux
}
