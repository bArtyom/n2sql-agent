package modelclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

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
