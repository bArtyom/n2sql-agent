package app

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/a2a"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/mcp"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/requestid"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type Dependencies struct {
	Providers                  modelprovider.Store
	KnowledgeBases             knowledgebase.Store
	Documents                  document.Uploader
	ChunkReader                documentchunk.Reader
	ConnectionChecker          modelclient.ConnectionChecker
	Embeddings                 modelruntime.EmbeddingRunner
	Chat                       modelruntime.ChatRunner
	Search                     retrieval.Searcher
	Answers                    rag.Answerer
	StreamingAnswers           rag.StreamAnswerer
	AgentAnswers               agentservice.Answerer
	AgentStreamingAnswers      agentservice.EventAnswerer
	AgentStreamHub             *agentstream.Hub
	MultiAgentAnswers          multiagent.Answerer
	MultiAgentStreamingAnswers multiagent.EventAnswerer
	A2AAnswers                 multiagent.Answerer
	A2AStore                   a2a.TaskStore
	A2ATaskTimeout             time.Duration
	MCPKnowledgeSearch         retrieval.Searcher
	MCPDocuments               document.Reader
	MCPKnowledgeBases          knowledgebase.Store
	Conversations              *conversation.Service
	AgentMaxToolResultBytes    int
	AgentMaxHistoryBytes       int
	APIKeyEnvVar               string
	Metrics                    *metrics.Registry
}

func New(dependencies Dependencies) http.Handler {
	registry := dependencies.Metrics
	if registry == nil {
		registry = metrics.New()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)
	mux.Handle("GET /metrics", registry.Handler())

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
	if dependencies.Conversations != nil {
		conversationHandler := handler.NewConversations(dependencies.Conversations)
		mux.Handle("POST /api/knowledge-bases/{id}/conversations", conversationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/conversations", conversationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/conversations/{conversationId}/messages", conversationHandler)
		mux.Handle("PATCH /api/knowledge-bases/{id}/conversations/{conversationId}", conversationHandler)
		mux.Handle("DELETE /api/knowledge-bases/{id}/conversations/{conversationId}", conversationHandler)
	}
	if dependencies.Documents != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/documents", handler.NewDocumentUpload(dependencies.Documents))
		if reader, ok := dependencies.Documents.(document.Reader); ok {
			mux.Handle("GET /api/knowledge-bases/{id}/documents", handler.NewDocumentList(reader))
		}
		if deleter, ok := dependencies.Documents.(document.Deleter); ok {
			mux.Handle("DELETE /api/knowledge-bases/{id}/documents/{documentID}", handler.NewDocumentDelete(deleter))
		}
	}
	if dependencies.ChunkReader != nil {
		mux.Handle("GET /api/knowledge-bases/{id}/documents/{documentID}/chunks/{position}", handler.NewDocumentChunk(dependencies.ChunkReader))
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
	if dependencies.Answers != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/chat", handler.NewKnowledgeBaseChat(dependencies.Answers))
	}
	if dependencies.StreamingAnswers != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/chat/stream", handler.NewKnowledgeBaseChatStream(dependencies.StreamingAnswers))
	}
	if dependencies.AgentAnswers != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/agent-chat", handler.NewKnowledgeBaseAgentChatWithConversationAndMetrics(dependencies.AgentAnswers, dependencies.Conversations, dependencies.AgentMaxHistoryBytes, registry))
	}
	if dependencies.AgentStreamingAnswers != nil {
		hub := dependencies.AgentStreamHub
		if hub == nil {
			hub = agentstream.NewHub()
		}
		mux.Handle("POST /api/knowledge-bases/{id}/agent-chat/stream", handler.NewKnowledgeBaseAgentChatStreamWithHub(dependencies.AgentStreamingAnswers, dependencies.Conversations, dependencies.AgentMaxHistoryBytes, registry, hub))
		mux.Handle("GET /api/knowledge-bases/{id}/agent-runs/{runID}/stream", handler.NewAgentRunStream(hub))
	}
	if dependencies.MultiAgentAnswers != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/multi-agent-chat", handler.NewMultiAgentChat(dependencies.MultiAgentAnswers))
	}
	if dependencies.MultiAgentStreamingAnswers != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/multi-agent-chat/stream", handler.NewMultiAgentChatStream(dependencies.MultiAgentStreamingAnswers))
	}
	if dependencies.A2AAnswers != nil {
		var a2aHandler http.Handler
		if dependencies.A2AStore != nil {
			a2aHandler = a2a.NewHandlerWithStore(dependencies.A2AAnswers, dependencies.A2AStore, dependencies.A2ATaskTimeout, registry)
		} else {
			a2aHandler = a2a.NewHandlerWithTimeoutAndMetrics(dependencies.A2AAnswers, dependencies.A2ATaskTimeout, registry)
		}
		mux.Handle("GET /.well-known/agent.json", a2aHandler)
		mux.Handle("POST /api/a2a/tasks", a2aHandler)
		mux.Handle("GET /api/a2a/tasks/{id}", a2aHandler)
		mux.Handle("GET /api/a2a/tasks/{id}/result", a2aHandler)
	}
	if dependencies.MCPKnowledgeSearch != nil && dependencies.MCPDocuments != nil && dependencies.MCPKnowledgeBases != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/mcp", mcp.NewKnowledgeBaseHandler(dependencies.MCPKnowledgeSearch, dependencies.MCPDocuments, dependencies.MCPKnowledgeBases, dependencies.AgentMaxToolResultBytes))
	}

	return requestid.NewMiddleware(slog.Default(), registry.Middleware(mux))
}
