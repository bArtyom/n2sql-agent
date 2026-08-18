package handler

import (
	"errors"
	"net/http"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/bArtyom/n2sql-agent/internal/memory"
)

// NewMemoryProfile exposes the authenticated user's free-form long-term profile.
// GET reads it and DELETE clears it. Profile updates happen through explicit
// "请记住" messages so the Agent remains the single write path.
func NewMemoryProfile(store memory.ProfileStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, authenticated := auth.UserFromContext(r.Context())
		if !authenticated {
			writeMemoryError(w, memory.ErrUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			profile, err := store.GetProfile(r.Context(), user.ID)
			if err != nil {
				writeMemoryProfileError(w, err)
				return
			}
			writeJSON(w, profile)
		case http.MethodDelete:
			if err := store.DeleteProfile(r.Context(), user.ID); err != nil {
				writeMemoryProfileError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func writeMemoryProfileError(w http.ResponseWriter, err error) {
	if errors.Is(err, memory.ErrUnauthorized) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	http.Error(w, `{"error":"memory profile operation failed"}`, http.StatusInternalServerError)
}
