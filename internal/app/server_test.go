package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agentservice"
	"github.com/bArtyom/n2sql-agent/internal/app"
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/rag"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type modelProviderStoreStub struct {
	provider modelprovider.Provider
}

func (s modelProviderStoreStub) Current(context.Context) (modelprovider.Provider, error) {
	return s.provider, nil
}

func (modelProviderStoreStub) Save(context.Context, modelprovider.Provider) (modelprovider.Provider, error) {
	return modelprovider.Provider{}, nil
}

type connectionCheckerStub struct{}

func (connectionCheckerStub) Check(context.Context, string, string) error { return nil }

type embeddingRunnerStub struct{}

func (embeddingRunnerStub) Embed(context.Context, []string) (modelclient.EmbeddingResponse, error) {
	return modelclient.EmbeddingResponse{Data: []modelclient.Embedding{{Index: 0, Vector: []float32{0.1}}}}, nil
}

type chatRunnerStub struct{}

func (chatRunnerStub) Chat(context.Context, string) (modelclient.ChatResponse, error) {
	return modelclient.ChatResponse{Message: "OK"}, nil
}

type searcherStub struct{}

func (searcherStub) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return []retrieval.Result{{Content: "Go 后端"}}, nil
}

type answererStub struct{}

func (answererStub) Answer(context.Context, int64, string, int) (rag.Response, error) {
	return rag.Response{Answer: "OK"}, nil
}

func (answererStub) Stream(_ context.Context, _ int64, _ string, _ int, emit func(rag.StreamEvent) error) error {
	return emit(rag.StreamEvent{Type: "delta", Delta: "OK"})
}

type agentAnswererStub struct{}

func (agentAnswererStub) Answer(context.Context, int64, string) (agentservice.Response, error) {
	return agentservice.Response{Answer: "OK"}, nil
}

type knowledgeBaseStoreStub struct{}

func (knowledgeBaseStoreStub) Create(context.Context, knowledgebase.CreateInput) (knowledgebase.KnowledgeBase, error) {
	return knowledgebase.KnowledgeBase{ID: 1, Name: "Go 学习资料"}, nil
}

func (knowledgeBaseStoreStub) List(context.Context) ([]knowledgebase.KnowledgeBase, error) {
	return []knowledgebase.KnowledgeBase{}, nil
}

func (knowledgeBaseStoreStub) Delete(context.Context, int64) error { return nil }

type documentUploaderStub struct{}

func (documentUploaderStub) Upload(context.Context, document.UploadInput) (document.Document, error) {
	return document.Document{ID: 1, ProcessingStatus: "pending"}, nil
}

func TestServerServesHealthCheck(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(app.Dependencies{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}

	if body := response.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("response body = %q, want %q", body, `{"status":"ok"}`)
	}
}

func TestServerRoutesConnectionTest(t *testing.T) {
	t.Setenv("TEST_MODEL_PROVIDER_API_KEY", "test-secret")
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{
		Providers: modelProviderStoreStub{provider: modelprovider.Provider{
			BaseURL:      "https://example.com/v1",
			APIKeyEnvVar: "TEST_MODEL_PROVIDER_API_KEY",
		}},
		ConnectionChecker: connectionCheckerStub{},
		APIKeyEnvVar:      "TEST_MODEL_PROVIDER_API_KEY",
	})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/connection-test", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesEmbeddingTest(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Embeddings: embeddingRunnerStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerDoesNotRegisterEmbeddingRouteWithoutRunner(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(app.Dependencies{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/embedding-test", strings.NewReader(`{"input":["document chunk"]}`)))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestServerRoutesChatTest(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Chat: chatRunnerStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"reply with OK"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerDoesNotRegisterChatRouteWithoutRunner(t *testing.T) {
	response := httptest.NewRecorder()

	app.New(app.Dependencies{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/model-provider/chat-test", strings.NewReader(`{"message":"hello"}`)))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestServerRoutesKnowledgeBaseSearch(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Search: searcherStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/search", strings.NewReader(`{"query":"后端"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesKnowledgeBaseChat(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Answers: answererStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat", strings.NewReader(`{"message":"问题"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesKnowledgeBaseChatStream(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{StreamingAnswers: answererStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/chat/stream", strings.NewReader(`{"message":"问题"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesKnowledgeBaseAgentChat(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{AgentAnswers: agentAnswererStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/agent-chat", strings.NewReader(`{"message":"问题"}`)))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestServerRoutesKnowledgeBases(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{KnowledgeBases: knowledgeBaseStoreStub{}})

	server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":"Go 学习资料"}`)))

	if response.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusCreated)
	}
}

func TestServerRoutesDocumentUpload(t *testing.T) {
	response := httptest.NewRecorder()
	server := app.New(app.Dependencies{Documents: documentUploaderStub{}})
	body := "--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"notes.txt\"\r\nContent-Type: text/plain\r\n\r\nhello\r\n--boundary--\r\n"
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")

	server.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusCreated)
	}
}
