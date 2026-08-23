package handler_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

func TestParserEnginesListsRegisteredEngines(t *testing.T) {
	endpoint := handler.NewParserEngines(documentextractor.NewDefaultParserRegistry(nil, nil))
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/parser-engines", nil))

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"simple"`) || !strings.Contains(response.Body.String(), `"available":false`) {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
