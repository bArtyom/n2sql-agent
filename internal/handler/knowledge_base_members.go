package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/bArtyom/n2sql-agent/internal/access"
	"github.com/bArtyom/n2sql-agent/internal/auth"
)

const maxMemberRequestBytes = 1024

// NewKnowledgeBaseMembers manages membership for one knowledge base. The
// access Store repeats the owner check in SQL, so this handler is not the
// security boundary by itself.
func NewKnowledgeBaseMembers(store access.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeAccessHandlerError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		knowledgeBaseID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || knowledgeBaseID <= 0 {
			writeAccessHandlerError(w, http.StatusBadRequest, "invalid knowledge base ID")
			return
		}
		targetUserID := int64(0)
		if raw := r.PathValue("userID"); raw != "" {
			targetUserID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || targetUserID <= 0 {
				writeAccessHandlerError(w, http.StatusBadRequest, "invalid user ID")
				return
			}
		}
		switch r.Method {
		case http.MethodGet:
			members, err := store.ListMembers(r.Context(), user.ID, knowledgeBaseID)
			if err != nil {
				writeAccessStoreError(w, err)
				return
			}
			writeJSON(w, struct {
				Members []access.Member `json:"members"`
			}{Members: members})
		case http.MethodPut:
			if targetUserID <= 0 {
				writeAccessHandlerError(w, http.StatusBadRequest, "invalid user ID")
				return
			}
			var request struct {
				Role access.Role `json:"role"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMemberRequestBytes))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				writeAccessHandlerError(w, http.StatusBadRequest, "invalid member request")
				return
			}
			if err := store.UpsertMember(r.Context(), user.ID, knowledgeBaseID, targetUserID, request.Role); err != nil {
				writeAccessStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			if targetUserID <= 0 {
				writeAccessHandlerError(w, http.StatusBadRequest, "invalid user ID")
				return
			}
			if err := store.RemoveMember(r.Context(), user.ID, knowledgeBaseID, targetUserID); err != nil {
				writeAccessStoreError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func writeAccessStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, access.ErrUnauthorized):
		writeAccessHandlerError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, access.ErrForbidden):
		writeAccessHandlerError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, access.ErrInvalidRole), errors.Is(err, access.ErrUserNotFound):
		writeAccessHandlerError(w, http.StatusBadRequest, "invalid member request")
	default:
		writeAccessHandlerError(w, http.StatusInternalServerError, "member operation failed")
	}
}

func writeAccessHandlerError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
