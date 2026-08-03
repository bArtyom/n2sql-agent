package app

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type Dependencies struct {
	Providers         modelprovider.Store
	KnowledgeBases    knowledgebase.Store
	Documents         document.Uploader
	ConnectionChecker modelclient.ConnectionChecker
	Embeddings        modelruntime.EmbeddingRunner
	Chat              modelruntime.ChatRunner
	Search            retrieval.Searcher
	APIKeyEnvVar      string
}

func New(dependencies Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)

	modelProviderHandler := handler.NewModelProvider(dependencies.Providers, dependencies.APIKeyEnvVar)
	mux.Handle("GET /api/model-provider", modelProviderHandler)
	mux.Handle("PUT /api/model-provider", modelProviderHandler)
	mux.Handle("POST /api/model-provider/connection-test", handler.NewModelProviderConnectionTest(dependencies.Providers, dependencies.ConnectionChecker, dependencies.APIKeyEnvVar))
	if dependencies.KnowledgeBases != nil {
		knowledgeBaseHandler := handler.NewKnowledgeBases(dependencies.KnowledgeBases)
		mux.Handle("GET /api/knowledge-bases", knowledgeBaseHandler)
		mux.Handle("POST /api/knowledge-bases", knowledgeBaseHandler)
		mux.Handle("DELETE /api/knowledge-bases/{id}", knowledgeBaseHandler)
	}
	if dependencies.Documents != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/documents", handler.NewDocumentUpload(dependencies.Documents))
	}
	if dependencies.Embeddings != nil {
		mux.Handle("POST /api/model-provider/embedding-test", handler.NewModelProviderEmbeddingTest(dependencies.Embeddings))
	}
	if dependencies.Chat != nil {
		mux.Handle("POST /api/model-provider/chat-test", handler.NewModelProviderChatTest(dependencies.Chat))
	}
	if dependencies.Search != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/search", handler.NewKnowledgeBaseSearch(dependencies.Search))
	}

	return mux
}
