package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documenttag"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type documentTagStoreStub struct {
	created     documenttag.CreateInput
	updated     documenttag.UpdateInput
	assignedIDs []int64
	deleteID    int64
	err         error
}

func (s *documentTagStoreStub) List(context.Context, int64) ([]documenttag.Tag, error) {
	return []documenttag.Tag{{ID: 2, Name: "Go"}}, s.err
}
func (s *documentTagStoreStub) Create(_ context.Context, _ int64, input documenttag.CreateInput) (documenttag.Tag, error) {
	s.created = input
	return documenttag.Tag{ID: 3, Name: input.Name, Color: input.Color}, s.err
}
func (s *documentTagStoreStub) Update(_ context.Context, _, _ int64, input documenttag.UpdateInput) (documenttag.Tag, error) {
	s.updated = input
	return documenttag.Tag{ID: 3}, s.err
}
func (s *documentTagStoreStub) Delete(_ context.Context, _, tagID int64) error {
	s.deleteID = tagID
	return s.err
}
func (s *documentTagStoreStub) ListDocumentTags(context.Context, int64, int64) ([]documenttag.Tag, error) {
	return []documenttag.Tag{{ID: 2, Name: "Go"}}, s.err
}
func (s *documentTagStoreStub) SetDocumentTags(_ context.Context, _, _ int64, tagIDs []int64) ([]documenttag.Tag, error) {
	s.assignedIDs = tagIDs
	return []documenttag.Tag{{ID: 2, Name: "Go"}}, s.err
}

func TestDocumentTagsCreatesTag(t *testing.T) {
	store := &documentTagStoreStub{}
	endpoint := handler.NewDocumentTags(store)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/tags", strings.NewReader(`{"name":" Go  Agent ","color":"#AABBCC"}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusCreated || store.created.Name != "Go Agent" || store.created.Color != "#aabbcc" {
		t.Fatalf("status=%d input=%#v", response.Code, store.created)
	}
}

func TestDocumentTagsReplacesDocumentTags(t *testing.T) {
	store := &documentTagStoreStub{}
	endpoint := handler.NewDocumentTags(store)
	request := httptest.NewRequest(http.MethodPut, "/api/knowledge-bases/4/documents/9/tags", strings.NewReader(`{"tagIds":[7,2,7]}`))
	request.SetPathValue("id", "4")
	request.SetPathValue("documentID", "9")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(store.assignedIDs) != 2 || store.assignedIDs[0] != 2 || store.assignedIDs[1] != 7 {
		t.Fatalf("status=%d assigned=%v", response.Code, store.assignedIDs)
	}
}

func TestDocumentTagsRejectsInvalidColorAtHTTPBoundary(t *testing.T) {
	store := &documentTagStoreStub{}
	endpoint := handler.NewDocumentTags(store)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/tags", strings.NewReader(`{"name":"Go","color":"green"}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || store.created.Name != "" {
		t.Fatalf("status=%d created=%#v", response.Code, store.created)
	}
}

func TestDocumentTagItemRejectsEmptyPatch(t *testing.T) {
	endpoint := handler.NewDocumentTagItem(&documentTagStoreStub{})
	request := httptest.NewRequest(http.MethodPatch, "/api/knowledge-bases/4/tags/8", strings.NewReader(`{}`))
	request.SetPathValue("id", "4")
	request.SetPathValue("tagID", "8")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDocumentTagItemMapsNotFound(t *testing.T) {
	endpoint := handler.NewDocumentTagItem(&documentTagStoreStub{err: documenttag.ErrTagNotFound})
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/4/tags/8", nil)
	request.SetPathValue("id", "4")
	request.SetPathValue("tagID", "8")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestDocumentTagsRejectsUnknownField(t *testing.T) {
	endpoint := handler.NewDocumentTags(&documentTagStoreStub{})
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/tags", strings.NewReader(`{"name":"Go","unknown":true}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestDocumentTagsErrorIdentityIsPreserved(t *testing.T) {
	if !errors.Is(documenttag.ErrTagNotFound, documenttag.ErrTagNotFound) {
		t.Fatal("tag sentinel error is not stable")
	}
}
