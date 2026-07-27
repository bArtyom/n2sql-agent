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
				http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
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
			if err := decoder.Decode(&provider); err != nil || provider.Name == "" || provider.BaseURL == "" || provider.APIKeyEnvVar != apiKeyEnvVar || provider.ChatModel == "" || provider.EmbeddingModel == "" {
				http.Error(w, `{"error":"invalid model provider"}`, http.StatusBadRequest)
				return
			}
			provider, err := store.Save(r.Context(), provider)
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
