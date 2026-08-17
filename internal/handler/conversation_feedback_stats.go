package handler

import (
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/conversation"
)

func NewConversationFeedbackStats(service *conversation.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		knowledgeBaseID, ok := decodeKnowledgeBaseID(w, r)
		if !ok {
			return
		}
		stats, err := service.FeedbackStats(r.Context(), knowledgeBaseID)
		if err != nil {
			if errors.Is(err, conversation.ErrInvalidKnowledgeBase) {
				http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
				return
			}
			http.Error(w, `{"error":"unable to load feedback stats"}`, http.StatusInternalServerError)
			return
		}
		writeJSON(w, stats)
	})
}
