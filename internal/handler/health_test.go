package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
)

func TestHealthReturnsOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()

	handler.Health(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}

	if body := response.Body.String(); body != `{"status":"ok"}` {
		t.Fatalf("response body = %q, want %q", body, `{"status":"ok"}`)
	}
}
