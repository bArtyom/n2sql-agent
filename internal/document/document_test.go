package document_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
)

type documentStoreStub struct {
	ensureErr error
	createErr error
	deleteErr error
	input     document.CreateInput
	documents []document.Document
	deletedKB int64
	deletedID int64
}

func (s *documentStoreStub) EnsureKnowledgeBase(context.Context, int64) error { return s.ensureErr }
func (s *documentStoreStub) List(context.Context, int64) ([]document.Document, error) {
	return s.documents, nil
}
func (s *documentStoreStub) Create(_ context.Context, input document.CreateInput) (document.Document, error) {
	s.input = input
	if s.createErr != nil {
		return document.Document{}, s.createErr
	}
	return document.Document{ID: 8, KnowledgeBaseID: input.KnowledgeBaseID, OriginalFilename: input.OriginalFilename, ContentType: input.ContentType, SizeBytes: input.SizeBytes, ProcessingStatus: "pending"}, nil
}
func (s *documentStoreStub) Delete(_ context.Context, knowledgeBaseID, documentID int64) (string, error) {
	s.deletedKB, s.deletedID = knowledgeBaseID, documentID
	if s.deleteErr != nil {
		return "", s.deleteErr
	}
	return "documents/8.txt", nil
}

type cacheInvalidatorStub struct{ knowledgeBaseIDs []int64 }

func (s *cacheInvalidatorStub) ClearCache(knowledgeBaseID int64) {
	s.knowledgeBaseIDs = append(s.knowledgeBaseIDs, knowledgeBaseID)
}

func TestServiceListsDocumentsForKnowledgeBase(t *testing.T) {
	want := []document.Document{{ID: 8, KnowledgeBaseID: 4, OriginalFilename: "notes.txt", ProcessingStatus: "succeeded"}}
	service := document.NewService(&documentStoreStub{documents: want}, document.NewLocalFileStore(t.TempDir()))

	got, err := service.List(context.Background(), 4)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0].ProcessingStatus != "succeeded" {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
}

func TestServiceUploadsDocumentAndCreatesPendingTask(t *testing.T) {
	root := t.TempDir()
	store := &documentStoreStub{}
	service := document.NewService(store, document.NewLocalFileStore(root))

	uploaded, err := service.Upload(context.Background(), document.UploadInput{
		KnowledgeBaseID:  4,
		OriginalFilename: "notes.txt",
		ContentType:      "text/plain",
		Content:          strings.NewReader("hello knowledge base"),
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if uploaded.ProcessingStatus != "pending" {
		t.Fatalf("processing status = %q, want pending", uploaded.ProcessingStatus)
	}
	if store.input.StoragePath == "" || store.input.SizeBytes != int64(len("hello knowledge base")) {
		t.Fatalf("create input = %#v", store.input)
	}
	if store.input.ContentSHA256 != "2b7199f223d19ebf4ef20d51b3ad6cab5482f54709f96460db1337ed2471ef15" {
		t.Fatalf("content sha256 = %q", store.input.ContentSHA256)
	}
	content, err := os.ReadFile(filepath.Join(root, store.input.StoragePath))
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(content) != "hello knowledge base" {
		t.Fatalf("stored content = %q", content)
	}
}

func TestServiceCleansUpFileWhenDatabaseCreateFails(t *testing.T) {
	root := t.TempDir()
	service := document.NewService(&documentStoreStub{createErr: errors.New("database unavailable")}, document.NewLocalFileStore(root))

	_, err := service.Upload(context.Background(), document.UploadInput{
		KnowledgeBaseID:  4,
		OriginalFilename: "notes.txt",
		ContentType:      "text/plain",
		Content:          strings.NewReader("hello"),
	})
	if err == nil {
		t.Fatal("Upload() error = nil, want database error")
	}
	entries, err := os.ReadDir(filepath.Join(root, "documents"))
	if err != nil {
		t.Fatalf("read document directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("stored files = %#v, want none", entries)
	}
}

func TestServiceRejectsOversizedFileAndCleansUp(t *testing.T) {
	root := t.TempDir()
	service := document.NewService(&documentStoreStub{}, document.NewLocalFileStore(root))

	_, err := service.Upload(context.Background(), document.UploadInput{
		KnowledgeBaseID:  4,
		OriginalFilename: "notes.txt",
		ContentType:      "text/plain",
		Content:          bytes.NewReader(make([]byte, document.MaxFileBytes+1)),
	})
	if !errors.Is(err, document.ErrFileTooLarge) {
		t.Fatalf("Upload() error = %v, want ErrFileTooLarge", err)
	}
}

func TestServiceDeletesDatabaseRecordFileAndCache(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o750); err != nil {
		t.Fatalf("create document directory: %v", err)
	}
	storagePath := filepath.Join(root, "documents", "8.txt")
	if err := os.WriteFile(storagePath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("write document: %v", err)
	}
	store := &documentStoreStub{}
	invalidator := &cacheInvalidatorStub{}
	service := document.NewServiceWithInvalidator(store, document.NewLocalFileStore(root), invalidator)

	if err := service.Delete(context.Background(), 4, 8); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.deletedKB != 4 || store.deletedID != 8 {
		t.Fatalf("delete arguments = %d/%d, want 4/8", store.deletedKB, store.deletedID)
	}
	if _, err := os.Stat(storagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted file stat error = %v, want not exist", err)
	}
	if len(invalidator.knowledgeBaseIDs) != 1 || invalidator.knowledgeBaseIDs[0] != 4 {
		t.Fatalf("invalidated knowledge bases = %#v, want [4]", invalidator.knowledgeBaseIDs)
	}
}

func TestServiceDoesNotDeleteFileWhenDatabaseDeleteFails(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "documents"), 0o750); err != nil {
		t.Fatalf("create document directory: %v", err)
	}
	storagePath := filepath.Join(root, "documents", "8.txt")
	if err := os.WriteFile(storagePath, []byte("hello"), 0o640); err != nil {
		t.Fatalf("write document: %v", err)
	}
	service := document.NewService(&documentStoreStub{deleteErr: document.ErrDocumentProcessing}, document.NewLocalFileStore(root))

	if err := service.Delete(context.Background(), 4, 8); !errors.Is(err, document.ErrDocumentProcessing) {
		t.Fatalf("Delete() error = %v, want ErrDocumentProcessing", err)
	}
	if _, err := os.Stat(storagePath); err != nil {
		t.Fatalf("source file stat error = %v, want file preserved", err)
	}
}
