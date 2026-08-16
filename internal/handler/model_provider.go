package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

func NewModelProvider(store modelprovider.Store, apiKeyEnvVar string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			provider, err := store.Current(r.Context())
			if errors.Is(err, modelprovider.ErrNotFound) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error":        "model provider not configured",
					"apiKeyEnvVar": apiKeyEnvVar,
				})
				return
			}
			if err != nil {
				http.Error(w, `{"error":"unable to load model provider"}`, http.StatusInternalServerError)
				return
			}
			writeJSON(w, provider)
		case http.MethodPut:
			var provider modelprovider.Provider
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&provider); err != nil || provider.Name == "" || provider.BaseURL == "" || provider.APIKeyEnvVar != apiKeyEnvVar || provider.ChatModel == "" || provider.EmbeddingModel == "" || ((provider.RerankBaseURL == "") != (provider.RerankModel == "")) {
				http.Error(w, `{"error":"invalid model provider"}`, http.StatusBadRequest)
				return
			}
			normalized, err := modelprovider.NormalizeChatModels(provider)
			if err != nil {
				http.Error(w, `{"error":"invalid model provider chat models"}`, http.StatusBadRequest)
				return
			}
			provider = normalized
			provider, err = store.Save(r.Context(), provider)
			if err != nil {
				http.Error(w, `{"error":"unable to save model provider"}`, http.StatusInternalServerError)
				return
			}
			writeJSON(w, provider)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
