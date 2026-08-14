package knowledgebase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
)

type storeStub struct {
	paths     []string
	deleteErr error
	deletedID int64
	withFiles bool
}

func (s *storeStub) Create(context.Context, knowledgebase.CreateInput) (knowledgebase.KnowledgeBase, error) {
	return knowledgebase.KnowledgeBase{}, nil
}

func (s *storeStub) List(context.Context) ([]knowledgebase.KnowledgeBase, error) {
	return nil, nil
}

func (s *storeStub) Delete(_ context.Context, id int64) error {
	s.deletedID = id
	return s.deleteErr
}

func (s *storeStub) DeleteWithFiles(_ context.Context, id int64) ([]string, error) {
	s.withFiles = true
	s.deletedID = id
	return s.paths, s.deleteErr
}

type fileStoreStub struct {
	paths []string
	err   error
}

func (s *fileStoreStub) Delete(_ context.Context, path string) error {
	s.paths = append(s.paths, path)
	return s.err
}

type cacheInvalidatorStub struct{ ids []int64 }

func (s *cacheInvalidatorStub) ClearCache(id int64) { s.ids = append(s.ids, id) }

func TestServiceDeletesSourcesAndInvalidatesCache(t *testing.T) {
	store := &storeStub{paths: []string{"documents/a.txt", "documents/b.pdf"}}
	files := &fileStoreStub{}
	cache := &cacheInvalidatorStub{}
	service := knowledgebase.NewServiceWithInvalidator(store, files, cache)

	if err := service.Delete(context.Background(), 7); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !store.withFiles || store.deletedID != 7 {
		t.Fatalf("store delete = withFiles:%v id:%d, want true/7", store.withFiles, store.deletedID)
	}
	if len(files.paths) != 2 || files.paths[1] != "documents/b.pdf" {
		t.Fatalf("deleted source paths = %#v", files.paths)
	}
	if len(cache.ids) != 1 || cache.ids[0] != 7 {
		t.Fatalf("invalidated IDs = %#v, want [7]", cache.ids)
	}
}

func TestServiceKeepsDeletionErrorAndDoesNotCleanSources(t *testing.T) {
	store := &storeStub{paths: []string{"documents/a.txt"}, deleteErr: knowledgebase.ErrProcessing}
	files := &fileStoreStub{}
	service := knowledgebase.NewServiceWithInvalidator(store, files, &cacheInvalidatorStub{})

	if err := service.Delete(context.Background(), 7); !errors.Is(err, knowledgebase.ErrProcessing) {
		t.Fatalf("Delete() error = %v, want ErrProcessing", err)
	}
	if len(files.paths) != 0 {
		t.Fatalf("deleted source paths = %#v, want none", files.paths)
	}
}
