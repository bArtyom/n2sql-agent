package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/auth"
)

type sessionStoreStub struct{}

func (sessionStoreStub) Register(context.Context, string, string) (auth.User, error) {
	return auth.User{}, nil
}
func (sessionStoreStub) Authenticate(context.Context, string, string) (auth.User, string, error) {
	return auth.User{}, "", nil
}
func (sessionStoreStub) UserBySession(context.Context, string) (auth.User, error) {
	return auth.User{ID: 9, Email: "user@example.com", CreatedAt: time.Now()}, nil
}
func (sessionStoreStub) RevokeSession(context.Context, string) error { return nil }

func TestMiddlewareAddsUserToRequestContext(t *testing.T) {
	var got auth.User
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = auth.UserFromContext(r.Context())
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "token"})
	auth.Middleware(sessionStoreStub{})(next).ServeHTTP(httptest.NewRecorder(), request)
	if got.ID != 9 || got.Email != "user@example.com" {
		t.Fatalf("user = %#v, want authenticated user", got)
	}
}
