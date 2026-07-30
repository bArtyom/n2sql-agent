package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
)

type knowledgeBaseStoreStub struct {
	items      []knowledgebase.KnowledgeBase
	createErr  error
	listErr    error
	deleteErr  error
	deletedID  int64
	nextItemID int64
}

func (s *knowledgeBaseStoreStub) Create(_ context.Context, input knowledgebase.CreateInput) (knowledgebase.KnowledgeBase, error) {
	if s.createErr != nil {
		return knowledgebase.KnowledgeBase{}, s.createErr
	}
	s.nextItemID++
	item := knowledgebase.KnowledgeBase{ID: s.nextItemID, Name: input.Name, Description: input.Description}
	s.items = append(s.items, item)
	return item, nil
}

func (s *knowledgeBaseStoreStub) List(context.Context) ([]knowledgebase.KnowledgeBase, error) {
	return s.items, s.listErr
}

func (s *knowledgeBaseStoreStub) Delete(_ context.Context, id int64) error {
	s.deletedID = id
	return s.deleteErr
}

func TestKnowledgeBasesCreatesKnowledgeBase(t *testing.T) {
	store := &knowledgeBaseStoreStub{}
	endpoint := handler.NewKnowledgeBases(store)
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":"Go 学习资料","description":"后端笔记"}`)))

	if response.Code != http.StatusCreated {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusCreated)
	}
	if body := response.Body.String(); !strings.Contains(body, `"id":1`) || !strings.Contains(body, `"name":"Go 学习资料"`) {
		t.Fatalf("response body = %q", body)
	}
}

func TestKnowledgeBasesListsKnowledgeBases(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{items: []knowledgebase.KnowledgeBase{{ID: 1, Name: "Go 学习资料"}}})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/knowledge-bases", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, `"id":1`) {
		t.Fatalf("response body = %q", body)
	}
}

func TestKnowledgeBasesRejectsInvalidCreateRequest(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":" ","unexpected":true}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestKnowledgeBasesRejectsTrailingJSON(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":"Go 学习资料"}{"name":"另一个"}`)))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestKnowledgeBasesRejectsOversizedRequest(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{})
	response := httptest.NewRecorder()
	body := `{"name":"` + strings.Repeat("a", 5000) + `"}`

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(body)))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestKnowledgeBasesReportsStoreFailures(t *testing.T) {
	testCases := []struct {
		name    string
		request *http.Request
		store   *knowledgeBaseStoreStub
	}{
		{
			name:    "list",
			request: httptest.NewRequest(http.MethodGet, "/api/knowledge-bases", nil),
			store:   &knowledgeBaseStoreStub{listErr: errors.New("database unavailable")},
		},
		{
			name:    "create",
			request: httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":"Go 学习资料"}`)),
			store:   &knowledgeBaseStoreStub{createErr: errors.New("database unavailable")},
		},
		{
			name: "delete",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/9", nil)
				request.SetPathValue("id", "9")
				return request
			}(),
			store: &knowledgeBaseStoreStub{deleteErr: errors.New("database unavailable")},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.NewKnowledgeBases(testCase.store).ServeHTTP(response, testCase.request)

			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusInternalServerError)
			}
		})
	}
}

func TestKnowledgeBasesRejectsDuplicateName(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{createErr: knowledgebase.ErrConflict})
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/knowledge-bases", strings.NewReader(`{"name":"Go 学习资料"}`)))

	if response.Code != http.StatusConflict {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusConflict)
	}
}

func TestKnowledgeBaseDeleteReturnsNotFound(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{deleteErr: knowledgebase.ErrNotFound})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/9", nil)
	request.SetPathValue("id", "9")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestKnowledgeBaseDeleteRejectsInvalidID(t *testing.T) {
	endpoint := handler.NewKnowledgeBases(&knowledgeBaseStoreStub{deleteErr: errors.New("not used")})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/invalid", nil)
	request.SetPathValue("id", "invalid")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestKnowledgeBaseDeletesKnowledgeBase(t *testing.T) {
	store := &knowledgeBaseStoreStub{}
	endpoint := handler.NewKnowledgeBases(store)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/9", nil)
	request.SetPathValue("id", "9")

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNoContent)
	}
	if store.deletedID != 9 {
		t.Fatalf("deleted ID = %d, want 9", store.deletedID)
	}
}
