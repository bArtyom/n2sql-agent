package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/handler"
)

type folderDocumentReaderStub struct {
	documents []document.Document
	path      string
	recursive bool
	tagIDs    []int64
}

func (s *folderDocumentReaderStub) List(context.Context, int64) ([]document.Document, error) {
	return s.documents, nil
}

func (s *folderDocumentReaderStub) ListInFolder(_ context.Context, _ int64, path string, recursive bool) ([]document.Document, error) {
	s.path, s.recursive = path, recursive
	return s.documents, nil
}

func (s *folderDocumentReaderStub) ListInFolderWithTags(_ context.Context, _ int64, path string, recursive bool, tagIDs []int64) ([]document.Document, error) {
	s.path, s.recursive, s.tagIDs = path, recursive, tagIDs
	return s.documents, nil
}

func TestDocumentListPassesFolderScopeToStore(t *testing.T) {
	reader := &folderDocumentReaderStub{documents: []document.Document{{ID: 1, FolderPath: "docs/api"}}}
	endpoint := handler.NewDocumentList(reader)
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/documents?folder_path=docs%2Fapi&folder_recursive=true", nil)
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || reader.path != "docs/api" || !reader.recursive {
		t.Fatalf("status=%d path=%q recursive=%v", response.Code, reader.path, reader.recursive)
	}
}

func TestDocumentListPassesFolderAndTagScopeToStore(t *testing.T) {
	reader := &folderDocumentReaderStub{documents: []document.Document{{ID: 1, FolderPath: "docs/api"}}}
	endpoint := handler.NewDocumentList(reader)
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/documents?folder_path=docs%2Fapi&folder_recursive=true&tag_ids=6,3", nil)
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || reader.path != "docs/api" || !reader.recursive || len(reader.tagIDs) != 2 || reader.tagIDs[0] != 3 || reader.tagIDs[1] != 6 {
		t.Fatalf("status=%d path=%q recursive=%v tags=%v", response.Code, reader.path, reader.recursive, reader.tagIDs)
	}
}

type folderTreeReaderStub struct{}

func (folderTreeReaderStub) ListFolderTree(context.Context, int64) (document.FolderTree, error) {
	return document.FolderTree{TotalDocumentCount: 2, Folders: []*document.FolderNode{{Path: "docs", Name: "docs", TotalCount: 2}}}, nil
}

func TestDocumentFolderTreeReturnsAggregatedTree(t *testing.T) {
	endpoint := handler.NewDocumentFolderTree(folderTreeReaderStub{})
	request := httptest.NewRequest(http.MethodGet, "/api/knowledge-bases/4/document-folders", nil)
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
	}
	var tree document.FolderTree
	if err := json.Unmarshal(response.Body.Bytes(), &tree); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if tree.TotalDocumentCount != 2 || len(tree.Folders) != 1 {
		t.Fatalf("tree = %#v", tree)
	}
}

type folderMoverStub struct {
	ids  []int64
	path string
}

func (s *folderMoverStub) MoveToFolder(_ context.Context, _ int64, ids []int64, path string) (int64, error) {
	s.ids, s.path = ids, path
	return int64(len(ids)), nil
}

func TestDocumentMoveToFolderAcceptsBatch(t *testing.T) {
	mover := &folderMoverStub{}
	endpoint := handler.NewDocumentMoveToFolder(mover)
	request := httptest.NewRequest(http.MethodPost, "/api/knowledge-bases/4/documents/folder", strings.NewReader(`{"documentIds":[1,2],"folderPath":"docs/api"}`))
	request.SetPathValue("id", "4")
	response := httptest.NewRecorder()

	endpoint.ServeHTTP(response, request)

	if response.Code != http.StatusOK || mover.path != "docs/api" || len(mover.ids) != 2 {
		t.Fatalf("status=%d ids=%v path=%q", response.Code, mover.ids, mover.path)
	}
}
