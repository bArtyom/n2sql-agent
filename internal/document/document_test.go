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
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

type documentStoreStub struct {
	ensureErr         error
	createErr         error
	deleteErr         error
	input             document.CreateInput
	documents         []document.Document
	deletedKB         int64
	deletedID         int64
	reprocessedConfig *documentextractor.ProcessConfig
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
func (s *documentStoreStub) Reprocess(_ context.Context, _, _ int64, config *documentextractor.ProcessConfig) error {
	s.reprocessedConfig = config
	return nil
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
		FolderPath:       `docs\\Go//agent`,
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
	if store.input.FolderPath != "docs/Go/agent" {
		t.Fatalf("folder path = %q, want %q", store.input.FolderPath, "docs/Go/agent")
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

func TestServiceValidatesAndCarriesUploadProcessConfig(t *testing.T) {
	store := &documentStoreStub{}
	service := document.NewService(store, document.NewLocalFileStore(t.TempDir()))
	config := &documentextractor.ProcessConfig{ParserEngineRules: []documentextractor.ParserEngineRule{
		{FileTypes: []string{"pdf"}, Engine: "mineru"},
	}}

	if _, err := service.Upload(context.Background(), document.UploadInput{
		KnowledgeBaseID:  4,
		OriginalFilename: "guide.pdf",
		ContentType:      "application/pdf",
		Content:          strings.NewReader("%PDF-1.7"),
		ProcessConfig:    config,
	}); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if store.input.ProcessConfig == nil || store.input.ProcessConfig.ParserEngineRules[0].Engine != "mineru" {
		t.Fatalf("create input process config = %#v", store.input.ProcessConfig)
	}
}

func TestServiceRejectsInvalidUploadProcessConfig(t *testing.T) {
	store := &documentStoreStub{}
	service := document.NewService(store, document.NewLocalFileStore(t.TempDir()))
	_, err := service.Upload(context.Background(), document.UploadInput{
		KnowledgeBaseID:  4,
		OriginalFilename: "notes.txt",
		ContentType:      "text/plain",
		Content:          strings.NewReader("hello"),
		ProcessConfig: &documentextractor.ProcessConfig{ParserEngineRules: []documentextractor.ParserEngineRule{
			{FileTypes: []string{"txt"}},
		}},
	})
	if !errors.Is(err, document.ErrInvalidProcessConfig) {
		t.Fatalf("Upload() error = %v, want ErrInvalidProcessConfig", err)
	}
}

func TestServiceReprocessPassesProcessConfig(t *testing.T) {
	store := &documentStoreStub{}
	service := document.NewService(store, document.NewLocalFileStore(t.TempDir()))
	config := &documentextractor.ProcessConfig{ParserEngineOverrides: map[string]string{"pdf_force_scanned": "true"}}
	if err := service.Reprocess(context.Background(), 4, 8, config); err != nil {
		t.Fatalf("Reprocess() error = %v", err)
	}
	if store.reprocessedConfig == nil || store.reprocessedConfig.ParserEngineOverrides["pdf_force_scanned"] != "true" {
		t.Fatalf("reprocess config = %#v", store.reprocessedConfig)
	}
}

func TestNormalizeFolderPathCanonicalizesRelativeDirectory(t *testing.T) {
	got, err := document.NormalizeFolderPath(`  docs\\Go// agent/  `)
	if err != nil {
		t.Fatalf("NormalizeFolderPath() error = %v", err)
	}
	if got != "docs/Go/agent" {
		t.Fatalf("NormalizeFolderPath() = %q, want %q", got, "docs/Go/agent")
	}
}

func TestNormalizeFolderPathRejectsTraversalAndOversizedSegment(t *testing.T) {
	for _, input := range []string{"docs/../secrets", "docs/\x00/private", strings.Repeat("x", document.MaxFolderSegmentBytes+1)} {
		if _, err := document.NormalizeFolderPath(input); !errors.Is(err, document.ErrInvalidFolderPath) {
			t.Fatalf("NormalizeFolderPath(%q) error = %v, want ErrInvalidFolderPath", input, err)
		}
	}
}

func TestBuildFolderTreeMaterializesParentsAndRollsUpCounts(t *testing.T) {
	tree := document.BuildFolderTree(map[string]int64{"": 2, "docs/api": 3, "docs/web": 4})
	if tree.RootDocumentCount != 2 || tree.TotalDocumentCount != 9 {
		t.Fatalf("tree totals = %#v, want root=2 total=9", tree)
	}
	if len(tree.Folders) != 1 || tree.Folders[0].Path != "docs" || tree.Folders[0].TotalCount != 7 {
		t.Fatalf("tree folders = %#v, want docs total=7", tree.Folders)
	}
	if len(tree.Folders[0].Children) != 2 {
		t.Fatalf("tree children = %#v, want 2", tree.Folders[0].Children)
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
