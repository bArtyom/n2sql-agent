package handler

import (
	"errors"
	"net/http"
	"os"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

func NewModelProviderConnectionTest(store modelprovider.Store, checker modelclient.ConnectionChecker, apiKeyEnvVar string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		provider, err := store.Current(r.Context())
		if errors.Is(err, modelprovider.ErrNotFound) {
			http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, `{"error":"unable to load model provider"}`, http.StatusInternalServerError)
			return
		}

		if provider.APIKeyEnvVar != apiKeyEnvVar {
			http.Error(w, `{"error":"invalid model provider API key environment variable"}`, http.StatusBadRequest)
			return
		}

		apiKey, found := os.LookupEnv(apiKeyEnvVar)
		if !found || apiKey == "" {
			http.Error(w, `{"error":"model provider API key environment variable is not set"}`, http.StatusBadRequest)
			return
		}

		if err := checker.Check(r.Context(), provider.BaseURL, apiKey); err != nil {
			http.Error(w, `{"error":"model provider connection failed"}`, http.StatusBadGateway)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"})
	})
}
