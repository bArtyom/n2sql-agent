package handler

import (
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

func NewParserEngines(registry *documentextractor.ParserRegistry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if registry == nil {
			http.Error(w, `{"error":"parser engines are unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, registry.ListEngineInfos())
	})
}
