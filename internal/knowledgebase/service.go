package knowledgebase

import (
	"context"
	"log/slog"
)

// FileStore is the small file-system capability needed by deletion. It is
// intentionally defined here instead of importing the document package.
type FileStore interface {
	Delete(context.Context, string) error
}

// CacheInvalidator clears retrieval results associated with one knowledge base.
type CacheInvalidator interface {
	ClearCache(int64)
}

// Service keeps knowledge-base lifecycle work together while preserving the
// existing Store interface for callers that only need CRUD operations.
type Service struct {
	store       Store
	files       FileStore
	invalidator CacheInvalidator
}

func NewServiceWithInvalidator(store Store, files FileStore, invalidator CacheInvalidator) *Service {
	return &Service{store: store, files: files, invalidator: invalidator}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (KnowledgeBase, error) {
	return s.store.Create(ctx, input)
}

func (s *Service) List(ctx context.Context) ([]KnowledgeBase, error) {
	return s.store.List(ctx)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	fileStore, ok := s.store.(FileDeleteStore)
	if !ok {
		if err := s.store.Delete(ctx, id); err != nil {
			return err
		}
		if s.invalidator != nil {
			s.invalidator.ClearCache(id)
		}
		return nil
	}
	paths, err := fileStore.DeleteWithFiles(ctx, id)
	if err != nil {
		return err
	}
	if s.invalidator != nil {
		s.invalidator.ClearCache(id)
	}
	for _, path := range paths {
		if err := s.files.Delete(context.WithoutCancel(ctx), path); err != nil {
			slog.ErrorContext(ctx, "knowledge_base_file_cleanup_failed", "knowledge_base_id", id, "error", err)
		}
	}
	return nil
}
