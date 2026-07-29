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
	maxChatMessageBytes = 8000
	maxChatRequestBytes = maxChatMessageBytes + 4096
)

func NewModelProviderChatTest(runner modelruntime.ChatRunner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var request struct {
			Message string `json:"message"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChatRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, `{"error":"chat message is too large"}`, http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, `{"error":"invalid chat message"}`, http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF || strings.TrimSpace(request.Message) == "" || len(request.Message) > maxChatMessageBytes {
			http.Error(w, `{"error":"invalid chat message"}`, http.StatusBadRequest)
			return
		}

		response, err := runner.Chat(r.Context(), request.Message)
		if err != nil {
			writeChatError(w, err)
			return
		}
		writeJSON(w, response)
	})
}

func writeChatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, modelprovider.ErrNotFound):
		http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
	case errors.Is(err, modelruntime.ErrAPIKeyEnvironmentMismatch):
		http.Error(w, `{"error":"invalid model provider API key environment variable"}`, http.StatusBadRequest)
	case errors.Is(err, modelruntime.ErrAPIKeyNotConfigured):
		http.Error(w, `{"error":"model provider API key environment variable is not set"}`, http.StatusBadRequest)
	default:
		var callError *modelruntime.ChatCallError
		if errors.As(err, &callError) {
			http.Error(w, `{"error":"chat request failed"}`, http.StatusBadGateway)
			return
		}
		http.Error(w, `{"error":"unable to load model provider"}`, http.StatusInternalServerError)
	}
}
