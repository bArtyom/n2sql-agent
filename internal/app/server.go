package app

import (
	"log/slog"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/agentrun"
	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/bArtyom/n2sql-agent/internal/evaluationrun"
	"github.com/bArtyom/n2sql-agent/internal/followup"
	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/mcp"
	"github.com/bArtyom/n2sql-agent/internal/memory"
	"github.com/bArtyom/n2sql-agent/internal/metrics"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/requestid"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type Dependencies struct {
	Providers               modelprovider.Store
	KnowledgeBases          knowledgebase.Store
	Documents               document.Uploader
	ChunkReader             documentchunk.Reader
	ConnectionChecker       modelclient.ConnectionChecker
	Embeddings              modelruntime.EmbeddingRunner
	Chat                    modelruntime.ChatRunner
	Search                  retrieval.Searcher
	Answers                 rag.Answerer
	StreamingAnswers        rag.StreamAnswerer
	AgentAnswers            agentservice.Answerer
	AgentStreamingAnswers   agentservice.EventAnswerer
	AgentStreamHub          *agentstream.Hub
	AgentRuns               agentrun.Store
	AgentRunReader          agentrun.Reader
	AgentEventStore         agentrun.EventStore
	AgentRunExecutor        agentrun.Executor
	MCPKnowledgeSearch      retrieval.Searcher
	MCPDocuments            document.Reader
	MCPKnowledgeBases       knowledgebase.Store
	Conversations           *conversation.Service
	AgentMaxToolResultBytes int
	AgentMaxHistoryBytes    int
	FollowUpSuggestions     followup.Suggester
	DocumentSummary         *documentsummary.Service
	EvaluationRuns          evaluationrun.Store
	EvaluationReader        evaluationrun.Reader
	Memories                memory.Store
	MemoryProfile           memory.ProfileStore
	APIKeyEnvVar            string
	Auth                    auth.Store
	SecureCookies           bool
	Metrics                 *metrics.Registry
}

func New(dependencies Dependencies) http.Handler {
	registry := dependencies.Metrics
	if registry == nil {
		registry = metrics.New()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.Health)
	mux.Handle("GET /metrics", registry.Handler())
	if dependencies.Auth != nil {
		mux.Handle("/api/auth/", handler.NewAuth(dependencies.Auth, dependencies.SecureCookies))
	}

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
		conversationHandler := handler.NewConversationsWithModelProvider(dependencies.Conversations, dependencies.Providers)
		mux.Handle("POST /api/knowledge-bases/{id}/conversations", conversationHandler)
		mux.Handle("POST /api/knowledge-bases/{id}/conversations/batch-delete", conversationHandler)
		mux.Handle("POST /api/knowledge-bases/{id}/conversations/batch-pin", conversationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/conversations", conversationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/conversations/{conversationId}/messages", conversationHandler)
		mux.Handle("POST /api/knowledge-bases/{id}/conversations/{conversationId}/messages/{messageId}/feedback", conversationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/feedback/stats", handler.NewConversationFeedbackStats(dependencies.Conversations))
		mux.Handle("PATCH /api/knowledge-bases/{id}/conversations/{conversationId}", conversationHandler)
		mux.Handle("DELETE /api/knowledge-bases/{id}/conversations/{conversationId}", conversationHandler)
		mux.Handle("DELETE /api/knowledge-bases/{id}/conversations/{conversationId}/messages", conversationHandler)
	}
	if dependencies.Memories != nil {
		memoriesHandler := handler.NewMemories(dependencies.Memories)
		mux.Handle("GET /api/knowledge-bases/{id}/memories", memoriesHandler)
		mux.Handle("POST /api/knowledge-bases/{id}/memories", memoriesHandler)
		mux.Handle("DELETE /api/knowledge-bases/{id}/memories/{memoryID}", memoriesHandler)
	}
	if dependencies.MemoryProfile != nil {
		mux.Handle("GET /api/memory-profile", handler.NewMemoryProfile(dependencies.MemoryProfile))
		mux.Handle("DELETE /api/memory-profile", handler.NewMemoryProfile(dependencies.MemoryProfile))
	}
	if dependencies.Documents != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/documents", handler.NewDocumentUpload(dependencies.Documents))
		if reader, ok := dependencies.Documents.(document.Reader); ok {
			mux.Handle("GET /api/knowledge-bases/{id}/documents", handler.NewDocumentList(reader))
		}
		if deleter, ok := dependencies.Documents.(document.Deleter); ok {
			mux.Handle("DELETE /api/knowledge-bases/{id}/documents/{documentID}", handler.NewDocumentDelete(deleter))
		}
		if reprocessor, ok := dependencies.Documents.(document.Reprocessor); ok {
			mux.Handle("POST /api/knowledge-bases/{id}/documents/{documentID}/reprocess", handler.NewDocumentReprocess(reprocessor))
		}
		if assetReader, ok := dependencies.Documents.(document.AssetReader); ok {
			mux.Handle("GET /api/knowledge-bases/{id}/documents/{documentID}/asset", handler.NewDocumentAsset(assetReader))
		}
	}
	if dependencies.ChunkReader != nil {
		mux.Handle("GET /api/knowledge-bases/{id}/documents/{documentID}/chunks/{position}", handler.NewDocumentChunk(dependencies.ChunkReader))
		mux.Handle("GET /api/knowledge-bases/{id}/documents/{documentID}/preview", handler.NewDocumentPreview(dependencies.ChunkReader))
	}
	if dependencies.DocumentSummary != nil {
		summaryHandler := handler.NewDocumentSummary(dependencies.DocumentSummary)
		mux.Handle("POST /api/knowledge-bases/{id}/documents/{documentID}/summary", summaryHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/documents/{documentID}/summary", summaryHandler)
	}
	if dependencies.EvaluationRuns != nil || dependencies.EvaluationReader != nil {
		evaluationHandler := handler.NewEvaluation(dependencies.EvaluationRuns, dependencies.EvaluationReader)
		mux.Handle("POST /api/knowledge-bases/{id}/evaluations", evaluationHandler)
		mux.Handle("GET /api/knowledge-bases/{id}/evaluations/{runID}", evaluationHandler)
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
		if dependencies.AgentRuns != nil && dependencies.AgentRunExecutor != nil {
			mux.Handle("POST /api/knowledge-bases/{id}/agent-chat/stream", handler.NewPersistentAgentRunSubmission(dependencies.AgentMaxHistoryBytes, dependencies.AgentRuns, dependencies.Conversations, hub))
		} else {
			mux.Handle("POST /api/knowledge-bases/{id}/agent-chat/stream", handler.NewKnowledgeBaseAgentChatStreamWithHub(dependencies.AgentStreamingAnswers, dependencies.Conversations, dependencies.AgentMaxHistoryBytes, registry, hub))
		}
		mux.Handle("GET /api/knowledge-bases/{id}/agent-runs/{runID}/stream", handler.NewAgentRunStreamWithStore(hub, dependencies.AgentEventStore))
		mux.Handle("GET /api/knowledge-bases/{id}/agent-runs/{runID}", handler.NewAgentRunStatus(dependencies.AgentRunReader))
		mux.Handle("GET /api/knowledge-bases/{id}/agent-runs/{runID}/children", handler.NewAgentRunChildren(dependencies.AgentRunReader))
		mux.Handle("GET /api/knowledge-bases/{id}/agent-runs/{runID}/trace", handler.NewAgentRunTrace(dependencies.AgentEventStore))
		mux.Handle("POST /api/knowledge-bases/{id}/agent-runs/{runID}/stop", handler.NewAgentRunStopWithStore(hub, dependencies.AgentRuns))
		mux.Handle("POST /api/knowledge-bases/{id}/agent-runs/{runID}/approval", handler.NewAgentRunApproval(hub))
	}
	if dependencies.FollowUpSuggestions != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/follow-up-suggestions", handler.NewFollowUpSuggestions(dependencies.FollowUpSuggestions))
	}
	if dependencies.MCPKnowledgeSearch != nil && dependencies.MCPDocuments != nil && dependencies.MCPKnowledgeBases != nil {
		mux.Handle("POST /api/knowledge-bases/{id}/mcp", mcp.NewKnowledgeBaseHandler(dependencies.MCPKnowledgeSearch, dependencies.MCPDocuments, dependencies.MCPKnowledgeBases, dependencies.AgentMaxToolResultBytes))
	}

	var root http.Handler = mux
	if dependencies.Auth != nil {
		root = auth.Middleware(dependencies.Auth)(root)
	}
	return requestid.NewMiddleware(slog.Default(), registry.Middleware(root))
}
