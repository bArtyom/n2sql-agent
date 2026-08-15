package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/conversation"
)

const maxConversationRequestBytes = 4096

// NewConversations exposes conversation creation, listing, and message history.
func NewConversations(service *conversation.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		knowledgeBaseID, err := parsePositiveID(r.PathValue("id"))
		if err != nil {
			http.Error(w, `{"error":"invalid knowledge base ID"}`, http.StatusBadRequest)
			return
		}

		if r.PathValue("conversationId") != "" {
			conversationID, err := parsePositiveID(r.PathValue("conversationId"))
			if err != nil {
				http.Error(w, `{"error":"invalid conversation ID"}`, http.StatusBadRequest)
				return
			}
			switch r.Method {
			case http.MethodGet:
				messages, err := service.Messages(r.Context(), conversationID, knowledgeBaseID)
				if err != nil {
					writeConversationError(w, err)
					return
				}
				writeJSON(w, messages)
			case http.MethodPatch:
				updateConversation(w, r, service, conversationID, knowledgeBaseID)
			case http.MethodDelete:
				if err := service.Delete(r.Context(), conversationID, knowledgeBaseID); err != nil {
					writeConversationError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}

		switch r.Method {
		case http.MethodGet:
			conversations, err := service.List(r.Context(), knowledgeBaseID)
			if err != nil {
				writeConversationError(w, err)
				return
			}
			writeJSON(w, conversations)
		case http.MethodPost:
			createConversation(w, r, service, knowledgeBaseID)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// updateConversation handles PATCH with either a new title or a pinned flag.
// A pointer keeps "is_pinned: false" distinct from "field not provided".
func updateConversation(w http.ResponseWriter, r *http.Request, service *conversation.Service, conversationID, knowledgeBaseID int64) {
	var request struct {
		Title    string `json:"title"`
		IsPinned *bool  `json:"is_pinned"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConversationRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Title) != "" {
		updated, err := service.Rename(r.Context(), conversationID, knowledgeBaseID, request.Title)
		if err != nil {
			writeConversationError(w, err)
			return
		}
		writeJSON(w, updated)
		return
	}
	if request.IsPinned != nil {
		updated, err := service.SetPinned(r.Context(), conversationID, knowledgeBaseID, *request.IsPinned)
		if err != nil {
			writeConversationError(w, err)
			return
		}
		writeJSON(w, updated)
		return
	}
	http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
}

func createConversation(w http.ResponseWriter, r *http.Request, service *conversation.Service, knowledgeBaseID int64) {
	var request struct {
		Title string `json:"title"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConversationRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	conversationRecord, err := service.Create(r.Context(), knowledgeBaseID, strings.TrimSpace(request.Title))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, conversationRecord)
}

func parsePositiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid ID")
	}
	return id, nil
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrInvalidConversation), errors.Is(err, conversation.ErrInvalidKnowledgeBase), errors.Is(err, conversation.ErrInvalidTitle):
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
	case errors.Is(err, conversation.ErrNotFound):
		http.Error(w, `{"error":"conversation not found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"error":"conversation operation failed"}`, http.StatusInternalServerError)
	}
}
