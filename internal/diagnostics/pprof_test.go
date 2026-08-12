package diagnostics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/diagnostics"
)

func TestPprofHandlerServesIndex(t *testing.T) {
	server := diagnostics.NewPprofHandler()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "profile") {
		t.Fatalf("pprof index = %q, want profile link", response.Body.String())
	}
}

func TestPprofHandlerDoesNotExposeUnknownPath(t *testing.T) {
	server := diagnostics.NewPprofHandler()
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func BenchmarkPprofIndex(b *testing.B) {
	server := diagnostics.NewPprofHandler()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			b.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
		}
	}
}
