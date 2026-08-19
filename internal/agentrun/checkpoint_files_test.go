package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToolResultFileStoreExternalizesAndLoadsResult(t *testing.T) {
	store, err := NewToolResultFileStore(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("NewToolResultFileStore() error = %v", err)
	}

	ref, err := store.Put(context.Background(), "run-1/result.json", "完整工具结果")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if filepath.IsAbs(ref) || !strings.HasPrefix(ref, "blobs/") {
		t.Fatalf("Put() ref = %q, want safe relative reference", ref)
	}

	got, err := store.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got != "完整工具结果" {
		t.Fatalf("Get() = %q, want complete result", got)
	}
}

func TestToolResultFileStoreRejectsPathEscape(t *testing.T) {
	store, err := NewToolResultFileStore(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("NewToolResultFileStore() error = %v", err)
	}
	if _, err := store.Get(context.Background(), "../outside"); !errors.Is(err, ErrInvalidCheckpointRef) {
		t.Fatalf("Get() error = %v, want ErrInvalidCheckpointRef", err)
	}
}

func TestToolResultFileStoreCleansExpiredFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewToolResultFileStore(root, time.Hour)
	if err != nil {
		t.Fatalf("NewToolResultFileStore() error = %v", err)
	}
	ref, err := store.Put(context.Background(), "run-1/result.json", "过期结果")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(ref))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("os.Chtimes() error = %v", err)
	}

	removed, err := store.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("Cleanup() removed = %d, want 1", removed)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still exists, stat error = %v", err)
	}
}
