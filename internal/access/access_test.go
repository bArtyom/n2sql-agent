package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/auth"
)

type storeStub struct {
	role Role
	err  error
}

func (s storeStub) Authorize(_ context.Context, _, _ int64, permission Permission) error {
	if s.role == RoleViewer && permission != PermissionRead {
		return ErrForbidden
	}
	return s.err
}

func (storeStub) ListMembers(context.Context, int64, int64) ([]Member, error) {
	return nil, nil
}

func (storeStub) UpsertMember(context.Context, int64, int64, int64, Role) error {
	return nil
}

func (storeStub) RemoveMember(context.Context, int64, int64, int64) error {
	return nil
}

func TestMiddlewareRejectsUnauthenticatedKnowledgeBaseRequest(t *testing.T) {
	handler := Middleware(storeStub{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler should not run")
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/documents", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestMiddlewareRejectsViewerWrite(t *testing.T) {
	called := false
	handler := Middleware(storeStub{role: RoleViewer})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/documents", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))

	handler.ServeHTTP(recorder, request)

	if called {
		t.Fatal("viewer write request reached protected handler")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestMiddlewareAllowsViewerRead(t *testing.T) {
	called := false
	handler := Middleware(storeStub{role: RoleViewer})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/search", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))

	handler.ServeHTTP(recorder, request)

	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("handler called=%v status=%d, want called=true status=%d", called, recorder.Code, http.StatusNoContent)
	}
}

func TestMiddlewareProtectsKnowledgeBaseListWithoutMembershipLookup(t *testing.T) {
	called := false
	handler := Middleware(storeStub{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))

	handler.ServeHTTP(recorder, request)

	if !called || recorder.Code != http.StatusNoContent {
		t.Fatalf("handler called=%v status=%d, want called=true status=%d", called, recorder.Code, http.StatusNoContent)
	}
}
