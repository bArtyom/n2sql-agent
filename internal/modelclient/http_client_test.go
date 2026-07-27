package modelclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

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

	checker := modelclient.NewHTTPConnectionChecker(server.Client(), []string{serverHost(t, server.URL)})
	if err := checker.Check(context.Background(), server.URL+"/v1", "test-secret"); err != nil {
		t.Fatalf("Check() error = %v, want nil", err)
	}
}

func TestHTTPConnectionCheckerRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	checker := modelclient.NewHTTPConnectionChecker(server.Client(), []string{serverHost(t, server.URL)})
	if err := checker.Check(context.Background(), server.URL, "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want HTTP status error")
	}
}

func TestHTTPConnectionCheckerRejectsUntrustedHostBeforeSendingKey(t *testing.T) {
	checker := modelclient.NewHTTPConnectionChecker(http.DefaultClient, []string{"api.openai.com"})

	if err := checker.Check(context.Background(), "https://untrusted.example/v1", "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want untrusted host error")
	}
}

func TestHTTPConnectionCheckerRejectsHTTPURL(t *testing.T) {
	checker := modelclient.NewHTTPConnectionChecker(http.DefaultClient, []string{"api.openai.com"})

	if err := checker.Check(context.Background(), "http://api.openai.com/v1", "test-secret"); err == nil {
		t.Fatal("Check() error = nil, want HTTPS validation error")
	}
}

func TestHTTPConnectionCheckerReportsTransportError(t *testing.T) {
	checker := modelclient.NewHTTPConnectionChecker(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
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

	checker := modelclient.NewHTTPConnectionChecker(source.Client(), []string{serverHost(t, source.URL)})
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
