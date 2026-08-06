package modelclient_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

func TestHTTPClientRecognizesImage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s, want POST /v1/chat/completions", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "vision-model" || len(request.Messages) != 1 || len(request.Messages[0].Content) != 2 {
			t.Fatalf("request = %#v", request)
		}
		if request.Messages[0].Content[0].Type != "text" || request.Messages[0].Content[0].Text != "extract text" {
			t.Fatalf("prompt content = %#v", request.Messages[0].Content[0])
		}
		imageURL := request.Messages[0].Content[1].ImageURL.URL
		wantURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff})
		if request.Messages[0].Content[1].Type != "image_url" || imageURL != wantURL {
			t.Fatalf("image content = %#v, want URL %q", request.Messages[0].Content[1], wantURL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"识别结果"}}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	response, err := client.OCR(context.Background(), server.URL+"/v1", "test-secret", modelclient.OCRRequest{
		Model:  "vision-model",
		Prompt: "extract text",
		Image:  []byte{0xff, 0xd8, 0xff},
	})
	if err != nil {
		t.Fatalf("OCR() error = %v", err)
	}
	if response.Text != "识别结果" {
		t.Fatalf("OCR() text = %q, want %q", response.Text, "识别结果")
	}
}

func TestHTTPClientEmbedsTexts(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/embeddings")
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-secret" {
			t.Fatalf("authorization = %q, want bearer token", authorization)
		}
		if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
			t.Fatalf("content type = %q, want application/json", contentType)
		}

		var request struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "test-embedding-model" {
			t.Fatalf("model = %q", request.Model)
		}
		if len(request.Input) != 2 || request.Input[0] != "first" || request.Input[1] != "second" {
			t.Fatalf("input = %#v", request.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1,0.2]},{"index":1,"embedding":[0.3,0.4]}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	response, err := client.Embed(context.Background(), server.URL+"/v1", "test-secret", modelclient.EmbeddingRequest{
		Model: "test-embedding-model",
		Input: []string{"first", "second"},
	})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("embedding count = %d, want 2", len(response.Data))
	}
	if response.Data[0].Index != 0 || len(response.Data[0].Vector) != 2 {
		t.Fatalf("first embedding = %#v", response.Data[0])
	}
}

func TestHTTPClientCompletesChat(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-secret" {
			t.Fatalf("authorization = %q, want bearer token", authorization)
		}

		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Model != "test-chat-model" {
			t.Fatalf("model = %q", request.Model)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" || request.Messages[0].Content != "reply with OK" {
			t.Fatalf("messages = %#v", request.Messages)
		}
		if request.Stream {
			t.Fatal("stream = true, want false")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	response, err := client.Chat(context.Background(), server.URL+"/v1", "test-secret", modelclient.ChatRequest{
		Model: "test-chat-model",
		Messages: []modelclient.ChatMessage{{
			Role:    "user",
			Content: "reply with OK",
		}},
		Stream: false,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if response.Message != "OK" {
		t.Fatalf("message = %q, want %q", response.Message, "OK")
	}
}

func TestHTTPClientCarriesToolDefinitionsAndParsesToolCalls(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Tools) != 1 {
			t.Fatalf("tools = %#v, want one tool", request.Tools)
		}
		tool := request.Tools[0]
		if tool.Type != "function" || tool.Function.Name != "knowledge_search" || tool.Function.Description != "search the knowledge base" {
			t.Fatalf("tool = %#v", tool)
		}
		if string(tool.Function.Parameters) != `{"type":"object","properties":{"query":{"type":"string"}}}` {
			t.Fatalf("tool parameters = %s", tool.Function.Parameters)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"knowledge_search","arguments":"{\"knowledge_base_id\":7,\"query\":\"年假\"}"}}]}}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	response, err := client.Chat(context.Background(), server.URL+"/v1", "test-secret", modelclient.ChatRequest{
		Model: "test-chat-model",
		Messages: []modelclient.ChatMessage{{
			Role:    "user",
			Content: "查年假",
		}},
		Tools: []modelclient.ToolDefinition{{
			Type: "function",
			Function: modelclient.FunctionDefinition{
				Name:        "knowledge_search",
				Description: "search the knowledge base",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if response.Message != "" {
		t.Fatalf("message = %q, want empty tool-call message", response.Message)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want one call", response.ToolCalls)
	}
	call := response.ToolCalls[0]
	if call.ID != "call-1" || call.Type != "function" || call.Function.Name != "knowledge_search" || call.Function.Arguments != `{"knowledge_base_id":7,"query":"年假"}` {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestHTTPClientRejectsInvalidToolDefinitionParameters(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("invalid tool definition reached the HTTP endpoint")
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Chat(context.Background(), server.URL+"/v1", "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
		Tools: []modelclient.ToolDefinition{{
			Type: "function",
			Function: modelclient.FunctionDefinition{
				Name:       "knowledge_search",
				Parameters: json.RawMessage(`[]`),
			},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "parameters") {
		t.Fatal("Chat() error = nil, want invalid tool parameters error")
	}
}

func TestHTTPClientRejectsMalformedToolCall(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"","type":"function","function":{"name":"knowledge_search","arguments":"{}"}}]}}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Chat(context.Background(), server.URL+"/v1", "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Chat() error = nil, want malformed tool call error")
	}
}

func TestHTTPClientRejectsStreamingToolCallsUntilSupported(t *testing.T) {
	client := modelclient.NewHTTPClient(http.DefaultClient, []string{"api.openai.com"})
	err := client.ChatStream(context.Background(), "https://api.openai.com/v1", "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
		Tools: []modelclient.ToolDefinition{{
			Type: "function",
			Function: modelclient.FunctionDefinition{
				Name:       "knowledge_search",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
	}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("ChatStream() error = %v, want unsupported tool-call error", err)
	}
}

func TestHTTPClientStreamsChatDeltas(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !request.Stream {
			t.Fatal("stream = false, want true")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"，世界\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	var deltas []string
	err := client.ChatStream(context.Background(), server.URL+"/v1", "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if strings.Join(deltas, "") != "你好，世界" {
		t.Fatalf("deltas = %#v", deltas)
	}
}

func TestHTTPClientCombinesMultilineSSEData(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[\n"))
		_, _ = w.Write([]byte("data: {\"delta\":{\"content\":\"分片\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	var delta string
	err := client.ChatStream(context.Background(), server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	}, func(value string) error {
		delta += value
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if delta != "分片" {
		t.Fatalf("delta = %q, want %q", delta, "分片")
	}
}

func TestHTTPClientRejectsChatStreamWithoutDoneEvent(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	err := client.ChatStream(context.Background(), server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	}, func(string) error { return nil })
	if err == nil {
		t.Fatal("ChatStream() error = nil, want missing done error")
	}
}

func TestHTTPClientPropagatesChatStreamCallbackFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	err := client.ChatStream(context.Background(), server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	}, func(string) error { return errors.New("client disconnected") })
	if err == nil || !strings.Contains(err.Error(), "client disconnected") {
		t.Fatalf("ChatStream() error = %v, want callback error", err)
	}
}

func TestHTTPClientStopsChatStreamWhenContextIsCanceled(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	err := client.ChatStream(ctx, server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	}, func(string) error { return nil })
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("ChatStream() error = %v, want context.Canceled", err)
	}
}

func TestHTTPClientRejectsChatEndpointFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Chat(context.Background(), server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Chat() error = nil, want HTTP status error")
	}
}

func TestHTTPClientRejectsInvalidChatResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Chat(context.Background(), server.URL, "test-secret", modelclient.ChatRequest{
		Model:    "test-chat-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err == nil {
		t.Fatal("Chat() error = nil, want invalid response error")
	}
}

func TestHTTPClientRejectsEmptyEmbeddingInput(t *testing.T) {
	client := modelclient.NewHTTPClient(http.DefaultClient, []string{"api.openai.com"})

	_, err := client.Embed(context.Background(), "https://api.openai.com/v1", "test-secret", modelclient.EmbeddingRequest{Model: "text-embedding"})
	if err == nil {
		t.Fatal("Embed() error = nil, want validation error")
	}
}

func TestHTTPClientReportsEmbeddingEndpointFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Embed(context.Background(), server.URL, "test-secret", modelclient.EmbeddingRequest{
		Model: "test-embedding-model",
		Input: []string{"text"},
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want HTTP status error")
	}
}

func TestHTTPClientRejectsInvalidEmbeddingResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Embed(context.Background(), server.URL, "test-secret", modelclient.EmbeddingRequest{
		Model: "test-embedding-model",
		Input: []string{"text"},
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want decode error")
	}
}

func TestHTTPClientRejectsIncompleteEmbeddingResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[0.1]}]}`))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Embed(context.Background(), server.URL, "test-secret", modelclient.EmbeddingRequest{
		Model: "test-embedding-model",
		Input: []string{"first", "second"},
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want incomplete response error")
	}
}

func TestHTTPClientRejectsOversizedEmbeddingResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20+1)))
	}))
	defer server.Close()

	client := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	_, err := client.Embed(context.Background(), server.URL, "test-secret", modelclient.EmbeddingRequest{
		Model: "test-embedding-model",
		Input: []string{"text"},
	})
	if err == nil {
		t.Fatal("Embed() error = nil, want response size error")
	}
}

func TestHTTPConnectionCheckerChecksModelsEndpoint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/v1/models")
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-secret" {
			t.Fatalf("authorization = %q, want bearer token", authorization)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	if err := checker.Check(context.Background(), server.URL+"/v1", "test-secret"); err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
}

func TestHTTPConnectionCheckerRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	checker := modelclient.NewHTTPClient(server.Client(), []string{serverHost(t, server.URL)})
	if err := checker.Check(context.Background(), server.URL, "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want HTTP status error")
	}
}

func TestHTTPConnectionCheckerRejectsUntrustedHostBeforeSendingKey(t *testing.T) {
	checker := modelclient.NewHTTPClient(http.DefaultClient, []string{"api.openai.com"})

	if err := checker.Check(context.Background(), "https://untrusted.example/v1", "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want untrusted host error")
	}
}

func TestHTTPConnectionCheckerRejectsHTTPURL(t *testing.T) {
	checker := modelclient.NewHTTPClient(http.DefaultClient, []string{"api.openai.com"})

	if err := checker.Check(context.Background(), "http://api.openai.com/v1", "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want HTTPS validation error")
	}
}

func TestHTTPConnectionCheckerReportsTransportError(t *testing.T) {
	checker := modelclient.NewHTTPClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}, []string{"api.openai.com"})

	if err := checker.Check(context.Background(), "https://api.openai.com/v1", "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want transport error")
	}
}

func TestHTTPConnectionCheckerDoesNotFollowRedirects(t *testing.T) {
	redirected := false
	destination := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected = true
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	checker := modelclient.NewHTTPClient(source.Client(), []string{serverHost(t, source.URL)})
	if err := checker.Check(context.Background(), source.URL, "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want redirect status error")
	}
	if redirected {
		t.Fatal("connection checker must not follow redirects")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func serverHost(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	return parsed.Hostname()
}
