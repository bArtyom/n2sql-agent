package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	handlerpkg "github.com/bArtyom/n2sql-agent/internal/handler"
)

type authStoreStub struct {
	user       auth.User
	registered bool
	revoked    string
}

func (s *authStoreStub) Register(_ context.Context, email, _ string) (auth.User, error) {
	s.registered = true
	s.user = auth.User{ID: 1, Email: email, CreatedAt: time.Now()}
	return s.user, nil
}

func (s *authStoreStub) Authenticate(_ context.Context, email, _ string) (auth.User, string, error) {
	s.user = auth.User{ID: 1, Email: email, CreatedAt: time.Now()}
	return s.user, "session-token", nil
}

func (s *authStoreStub) UserBySession(context.Context, string) (auth.User, error) { return s.user, nil }

func (s *authStoreStub) RevokeSession(_ context.Context, token string) error {
	s.revoked = token
	return nil
}

func TestAuthHandlerLoginSetsHttpOnlyCookie(t *testing.T) {
	store := &authStoreStub{}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"email":"a@example.com","password":"password123"}`))
	recorder := httptest.NewRecorder()
	handlerpkg.NewAuth(store, false).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Result().Cookies()[0].Name != auth.SessionCookieName || !recorder.Result().Cookies()[0].HttpOnly {
		t.Fatalf("status=%d cookies=%#v, want login cookie", recorder.Code, recorder.Result().Cookies())
	}
}

func TestAuthHandlerLogoutRevokesCookie(t *testing.T) {
	store := &authStoreStub{}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "session-token"})
	recorder := httptest.NewRecorder()
	handlerpkg.NewAuth(store, false).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || store.revoked != "session-token" {
		t.Fatalf("status=%d revoked=%q, want logout", recorder.Code, store.revoked)
	}
}
