package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/conversation"
	"github.com/bArtyom/n2sql-agent/internal/modelprovider"
)

const maxConversationRequestBytes = 4096

const (
	defaultConversationSearchLimit = 50
	maxConversationSearchLimit     = 100
)

// NewConversations exposes conversation creation, listing, and message history.
func NewConversations(service *conversation.Service) http.Handler {
	return NewConversationsWithModelProvider(service, nil)
}

// NewConversationsWithModelProvider additionally validates chat model changes
// against the server-side Provider allowlist. The nil form preserves the
// lightweight constructor used by isolated conversation tests.
func NewConversationsWithModelProvider(service *conversation.Service, providers modelprovider.Store) http.Handler {
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
			if r.PathValue("feedback") == "feedback" {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				saveMessageFeedback(w, r, service, conversationID, knowledgeBaseID)
				return
			}
			if r.PathValue("messages") == "messages" {
				if r.Method != http.MethodDelete {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if err := service.ClearMessages(r.Context(), conversationID, knowledgeBaseID); err != nil {
					writeConversationError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			switch r.Method {
			case http.MethodGet:
				beforeID, limit, err := decodeMessagesPage(r)
				if err != nil {
					http.Error(w, `{"error":"invalid message page"}`, http.StatusBadRequest)
					return
				}
				messages, hasMore, err := service.MessagesPage(r.Context(), conversationID, knowledgeBaseID, beforeID, limit)
				if err != nil {
					writeConversationError(w, err)
					return
				}
				writeJSON(w, struct {
					Messages []conversation.Message `json:"messages"`
					HasMore  bool                   `json:"has_more"`
				}{Messages: messages, HasMore: hasMore})
			case http.MethodPatch:
				updateConversation(w, r, service, providers, conversationID, knowledgeBaseID)
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
			query := strings.TrimSpace(r.URL.Query().Get("q"))
			limit, offset, err := decodeConversationPage(r)
			if err != nil {
				http.Error(w, `{"error":"invalid conversation search"}`, http.StatusBadRequest)
				return
			}
			var page conversation.Page
			if query == "" {
				page, err = service.ListPage(r.Context(), knowledgeBaseID, limit, offset)
			} else {
				page, err = service.SearchPage(r.Context(), knowledgeBaseID, query, limit, offset)
			}
			if err != nil {
				writeConversationError(w, err)
				return
			}
			writeJSON(w, page)
		case http.MethodPost:
			if strings.HasSuffix(r.URL.Path, "/batch-delete") {
				deleteConversations(w, r, service, knowledgeBaseID)
			} else if strings.HasSuffix(r.URL.Path, "/batch-pin") {
				pinConversations(w, r, service, knowledgeBaseID)
			} else {
				createConversation(w, r, service, knowledgeBaseID)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func saveMessageFeedback(w http.ResponseWriter, r *http.Request, service *conversation.Service, conversationID, knowledgeBaseID int64) {
	messageID, err := parsePositiveID(r.PathValue("messageId"))
	if err != nil {
		http.Error(w, `{"error":"invalid message ID"}`, http.StatusBadRequest)
		return
	}
	var request struct {
		Rating int `json:"rating"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConversationRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || (request.Rating != -1 && request.Rating != 1) {
		http.Error(w, `{"error":"rating must be -1 or 1"}`, http.StatusBadRequest)
		return
	}
	feedback, err := service.SaveFeedback(r.Context(), conversationID, knowledgeBaseID, messageID, request.Rating)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, feedback)
}

func deleteConversations(w http.ResponseWriter, r *http.Request, service *conversation.Service, knowledgeBaseID int64) {
	var request struct {
		IDs []int64 `json:"ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConversationRequestBytes))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	if err := service.DeleteMany(r.Context(), knowledgeBaseID, request.IDs); err != nil {
		writeConversationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pinConversations(w http.ResponseWriter, r *http.Request, service *conversation.Service, knowledgeBaseID int64) {
	var request struct {
		IDs    []int64 `json:"ids"`
		Pinned bool    `json:"pinned"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConversationRequestBytes)).Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
		return
	}
	if err := service.SetPinnedMany(r.Context(), knowledgeBaseID, request.IDs, request.Pinned); err != nil {
		writeConversationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeConversationPage(r *http.Request) (int, int, error) {
	limit := defaultConversationSearchLimit
	raw := r.URL.Query().Get("limit")
	if raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > maxConversationSearchLimit {
			return 0, 0, errors.New("invalid conversation page")
		}
		limit = parsed
	}
	offset := 0
	if raw = r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, errors.New("invalid conversation page")
		}
		offset = parsed
	}
	return limit, offset, nil
}

// updateConversation handles PATCH with either a new title or a pinned flag.
// A pointer keeps "is_pinned: false" distinct from "field not provided".
func updateConversation(w http.ResponseWriter, r *http.Request, service *conversation.Service, providers modelprovider.Store, conversationID, knowledgeBaseID int64) {
	var request struct {
		Title     string  `json:"title"`
		IsPinned  *bool   `json:"is_pinned"`
		ChatModel *string `json:"chat_model"`
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
	if request.ChatModel != nil {
		if providers != nil && strings.TrimSpace(*request.ChatModel) != "" {
			provider, err := providers.Current(r.Context())
			if err != nil {
				if errors.Is(err, modelprovider.ErrNotFound) {
					http.Error(w, `{"error":"model provider not configured"}`, http.StatusNotFound)
				} else {
					http.Error(w, `{"error":"unable to load model provider"}`, http.StatusInternalServerError)
				}
				return
			}
			if _, err := provider.ResolveChatModel(*request.ChatModel); err != nil {
				http.Error(w, `{"error":"invalid chat model"}`, http.StatusBadRequest)
				return
			}
		}
		updated, err := service.SetChatModel(r.Context(), conversationID, knowledgeBaseID, *request.ChatModel)
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

const (
	defaultMessagePageSize = 50
	maxMessagePageSize     = 200
)

// decodeMessagesPage reads optional before_id and limit query parameters.
// before_id is a message ID cursor; a missing value loads the newest page.
func decodeMessagesPage(r *http.Request) (beforeID int64, limit int, err error) {
	limit = defaultMessagePageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 || parsed > maxMessagePageSize {
			return 0, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed <= 0 {
			return 0, 0, errors.New("invalid before_id")
		}
		beforeID = parsed
	}
	return beforeID, limit, nil
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrInvalidConversation), errors.Is(err, conversation.ErrInvalidKnowledgeBase), errors.Is(err, conversation.ErrInvalidTitle), errors.Is(err, conversation.ErrInvalidChatModel), errors.Is(err, conversation.ErrInvalidSearchQuery), errors.Is(err, conversation.ErrInvalidSearchLimit), errors.Is(err, conversation.ErrInvalidFeedback):
		http.Error(w, `{"error":"invalid conversation request"}`, http.StatusBadRequest)
	case errors.Is(err, conversation.ErrNotFound):
		http.Error(w, `{"error":"conversation not found"}`, http.StatusNotFound)
	default:
		http.Error(w, `{"error":"conversation operation failed"}`, http.StatusInternalServerError)
	}
}
