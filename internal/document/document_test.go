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
	input     document.CreateInput
}

func (s *documentStoreStub) EnsureKnowledgeBase(context.Context, int64) error { return s.ensureErr }
func (s *documentStoreStub) Create(_ context.Context, input document.CreateInput) (document.Document, error) {
	s.input = input
	if s.createErr != nil {
		return document.Document{}, s.createErr
	}
	return document.Document{ID: 8, KnowledgeBaseID: input.KnowledgeBaseID, OriginalFilename: input.OriginalFilename, ContentType: input.ContentType, SizeBytes: input.SizeBytes, ProcessingStatus: "pending"}, nil
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
