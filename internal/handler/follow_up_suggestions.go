package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/followup"
)

const maxFollowUpBodyBytes = 40 << 10

type followUpSuggestionRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

func NewFollowUpSuggestions(suggester followup.Suggester) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		if suggester == nil {
			writeFollowUpError(w, http.StatusServiceUnavailable, "follow-up suggestions are unavailable")
			return
		}
		var request followUpSuggestionRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFollowUpBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeFollowUpError(w, http.StatusBadRequest, "invalid follow-up suggestion request")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeFollowUpError(w, http.StatusBadRequest, "invalid follow-up suggestion request")
			return
		}
		request.Question = strings.TrimSpace(request.Question)
		request.Answer = strings.TrimSpace(request.Answer)
		if request.Question == "" || request.Answer == "" || len(request.Question) > 8<<10 || len(request.Answer) > 24<<10 {
			writeFollowUpError(w, http.StatusBadRequest, "invalid follow-up suggestion request")
			return
		}
		questions, err := suggester.Suggest(r.Context(), knowledgeBaseID, request.Question, request.Answer)
		if err != nil {
			if errors.Is(err, followup.ErrInvalidRequest) {
				writeFollowUpError(w, http.StatusBadRequest, "invalid follow-up suggestion request")
				return
			}
			writeFollowUpError(w, http.StatusBadGateway, "follow-up suggestions are unavailable")
			return
		}
		writeJSON(w, map[string]any{"questions": questions})
	})
}

func writeFollowUpError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		slog.Debug("follow-up error response write failed", "error_kind", "response_write_failed")
	}
}
