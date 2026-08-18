package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/auth"
)

const maxAuthRequestBytes = 4096

func NewAuth(store auth.Store, secureCookies bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/register":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			registerUser(w, r, store)
		case "/api/auth/login":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			loginUser(w, r, store, secureCookies)
		case "/api/auth/logout":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			logoutUser(w, r, store)
		case "/api/auth/me":
			if r.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			currentUser(w, r, store)
		default:
			http.NotFound(w, r)
		}
	})
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentialsRequest, bool) {
	var request credentialsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, `{"error":"invalid auth request"}`, http.StatusBadRequest)
		return credentialsRequest{}, false
	}
	request.Email = strings.TrimSpace(request.Email)
	return request, true
}

func registerUser(w http.ResponseWriter, r *http.Request, store auth.Store) {
	request, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	user, err := store.Register(r.Context(), request.Email, request.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, user)
}

func loginUser(w http.ResponseWriter, r *http.Request, store auth.Store, secureCookies bool) {
	request, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	user, token, err := store.Authenticate(r.Context(), request.Email, request.Password)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	http.SetCookie(w, auth.Cookie(token, secureCookies))
	writeJSON(w, user)
}

func logoutUser(w http.ResponseWriter, r *http.Request, store auth.Store) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := store.RevokeSession(r.Context(), cookie.Value); err != nil {
			writeAuthError(w, err)
			return
		}
	}
	http.SetCookie(w, auth.ClearCookie())
	w.WriteHeader(http.StatusNoContent)
}

func currentUser(w http.ResponseWriter, r *http.Request, store auth.Store) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		writeAuthError(w, auth.ErrUnauthorized)
		return
	}
	user, err := store.UserBySession(r.Context(), cookie.Value)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, user)
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrInvalidPassword):
		http.Error(w, `{"error":"invalid auth request"}`, http.StatusBadRequest)
	case errors.Is(err, auth.ErrEmailExists):
		http.Error(w, `{"error":"email already exists"}`, http.StatusConflict)
	case errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrInvalidSession):
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	default:
		http.Error(w, `{"error":"auth operation failed"}`, http.StatusInternalServerError)
	}
}
