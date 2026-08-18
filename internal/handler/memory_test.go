package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	handlerpkg "github.com/bArtyom/n2sql-agent/internal/handler"
	"github.com/bArtyom/n2sql-agent/internal/memory"
)

type memoryStoreStub struct {
	items   []memory.Memory
	input   memory.CreateInput
	delete  [2]int64
	profile memory.Profile
	cleared int64
}

func (s *memoryStoreStub) Create(_ context.Context, _ int64, input memory.CreateInput) (memory.Memory, error) {
	s.input = input
	return memory.Memory{ID: 1, KnowledgeBaseID: input.KnowledgeBaseID, Content: input.Content, Source: "explicit"}, nil
}

func (s *memoryStoreStub) List(context.Context, int64, int64) ([]memory.Memory, error) {
	return s.items, nil
}

func (s *memoryStoreStub) Delete(_ context.Context, _, knowledgeBaseID, memoryID int64) error {
	s.delete = [2]int64{knowledgeBaseID, memoryID}
	return nil
}

func (s *memoryStoreStub) GetProfile(_ context.Context, userID int64) (memory.Profile, error) {
	s.profile.UserID = userID
	return s.profile, nil
}

func (s *memoryStoreStub) SaveProfile(_ context.Context, userID int64, content string) (memory.Profile, error) {
	s.profile = memory.Profile{UserID: userID, Content: content}
	return s.profile, nil
}

func (s *memoryStoreStub) DeleteProfile(_ context.Context, userID int64) error {
	s.cleared = userID
	return nil
}

func TestMemoriesHandlerCreatesAndListsKnowledgeBaseMemories(t *testing.T) {
	store := &memoryStoreStub{items: []memory.Memory{{ID: 2, KnowledgeBaseID: 7, Content: "回答简洁"}}}
	handler := http.NewServeMux()
	handler.Handle("/api/knowledge-bases/{id}/memories", handlerpkg.NewMemories(store))
	handler.Handle("/api/knowledge-bases/{id}/memories/{memoryID}", handlerpkg.NewMemories(store))

	create := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/memories", strings.NewReader(`{"content":"  回答简洁  "}`))
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))
	handler.ServeHTTP(create, request)
	if create.Code != http.StatusCreated || store.input.Content != "回答简洁" {
		t.Fatalf("create status=%d input=%#v, want 201 and trimmed content", create.Code, store.input)
	}

	list := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/7/memories", nil)
	listRequest = listRequest.WithContext(auth.WithUser(listRequest.Context(), auth.User{ID: 11}))
	handler.ServeHTTP(list, listRequest)
	var response struct {
		Memories []memory.Memory `json:"memories"`
	}
	if list.Code != http.StatusOK || json.NewDecoder(list.Body).Decode(&response) != nil || len(response.Memories) != 1 {
		t.Fatalf("list status=%d body=%s response=%#v", list.Code, list.Body.String(), response)
	}
}

func TestMemoriesHandlerDeletesMemory(t *testing.T) {
	store := &memoryStoreStub{}
	h := http.NewServeMux()
	h.Handle("/api/knowledge-bases/{id}/memories/{memoryID}", handlerpkg.NewMemories(store))
	request := httptest.NewRequest(http.MethodDelete, "/api/knowledge-bases/7/memories/3", nil)
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))
	h.ServeHTTP(httptest.NewRecorder(), request)
	if store.delete != [2]int64{7, 3} {
		t.Fatalf("delete = %#v, want knowledge base 7 memory 3", store.delete)
	}
}

func TestMemoriesHandlerRejectsInvalidContent(t *testing.T) {
	store := &memoryStoreStub{}
	server := http.NewServeMux()
	server.Handle("/api/knowledge-bases/{id}/memories", handlerpkg.NewMemories(store))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/7/memories", strings.NewReader(`{"content":""}`))
	request = request.WithContext(auth.WithUser(request.Context(), auth.User{ID: 11}))
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want bad request", recorder.Code)
	}
}

func TestMemoryProfileHandlerReadsAndClearsAuthenticatedProfile(t *testing.T) {
	store := &memoryStoreStub{profile: memory.Profile{Content: "偏好简洁回答", Version: 2}}
	server := http.NewServeMux()
	server.Handle("/api/memory-profile", handlerpkg.NewMemoryProfile(store))

	get := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/api/memory-profile", nil)
	getRequest = getRequest.WithContext(auth.WithUser(getRequest.Context(), auth.User{ID: 11}))
	server.ServeHTTP(get, getRequest)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "偏好简洁回答") {
		t.Fatalf("get status=%d body=%s, want profile", get.Code, get.Body.String())
	}

	clear := httptest.NewRecorder()
	clearRequest := httptest.NewRequest(http.MethodDelete, "/api/memory-profile", nil)
	clearRequest = clearRequest.WithContext(auth.WithUser(clearRequest.Context(), auth.User{ID: 11}))
	server.ServeHTTP(clear, clearRequest)
	if clear.Code != http.StatusNoContent || store.cleared != 11 {
		t.Fatalf("clear status=%d user=%d, want 204 for user 11", clear.Code, store.cleared)
	}
}
