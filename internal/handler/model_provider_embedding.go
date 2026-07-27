package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
	"github.com/bArtyom/n2sql-agent/internal/modelruntime"
)

const (
	maxEmbeddingInputs       = 32
	maxEmbeddingInputBytes   = 8000
	maxEmbeddingRequestBytes = maxEmbeddingInputs*maxEmbeddingInputBytes + 4096
)

func NewModelProviderEmbeddingTest(runner modelruntime.EmbeddingRunner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var request struct {
			Input []string `json:"input"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEmbeddingRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, `{"error":"embedding input is too large"}`, http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, `{"error":"invalid embedding input"}`, http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || !validEmbeddingInput(request.Input) {
			http.Error(w, `{"error":"invalid embedding input"}`, http.StatusBadRequest)
			return
		}

		response, err := runner.Embed(r.Context(), request.Input)
		if err != nil {
			writeEmbeddingError(w, err)
			return
		}
		writeJSON(w, response)
	})
}

func validEmbeddingInput(input []string) bool {
	if len(input) == 0 || len(input) > maxEmbeddingInputs {
		return false
	}
	for _, text := range input {
		if strings.TrimSpace(text) == "" || len(text) > maxEmbeddingInputBytes {
			return false
		}
	}
	return true
}

func writeEmbeddingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modelprovider.ErrNotFound):
		http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch):
		http.Error(w, `{"error":"invalid model provider API key environment variable"}`, http.StatusBadRequest)
	case errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		http.Error(w, `{"error":"model provider API key environment variable is not set"}`, http.StatusBadRequest)
	default:
		var callError *modelruntime.EmbeddingCallError
		if errors.As(err, &callError) {
			http.Error(w, `{"error":"embedding request failed"}`, http.StatusBadGateway)
			return
		}
		http.Error(w, `{"error":"unable to load model provider"}`, http.StatusInternalServerError)
	}
}
