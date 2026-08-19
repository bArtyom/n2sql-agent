package agentrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrInvalidCheckpointRef = errors.New("invalid checkpoint file reference")

// ToolResultFileStore keeps complete large tool results outside PostgreSQL.
// References are relative to root and are safe to persist in checkpoint JSON.
type ToolResultFileStore struct {
	root string
	ttl  time.Duration
}

func NewToolResultFileStore(root string, ttl time.Duration) (*ToolResultFileStore, error) {
	root = strings.TrimSpace(root)
	if root == "" || ttl <= 0 {
		return nil, ErrInvalidCheckpointRef
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create checkpoint file directory: %w", err)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve checkpoint file directory: %w", err)
	}
	return &ToolResultFileStore{root: absoluteRoot, ttl: ttl}, nil
}

func (s *ToolResultFileStore) Put(ctx context.Context, key, content string) (string, error) {
	if s == nil || s.root == "" || strings.TrimSpace(key) == "" || content == "" {
		return "", ErrInvalidCheckpointRef
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key = filepath.ToSlash(strings.Trim(key, "/"))
	if strings.Contains(key, "..") {
		return "", ErrInvalidCheckpointRef
	}
	// The caller supplies a logical key, while the digest prevents tool call IDs
	// or other untrusted text from becoming a filesystem path.
	digest := sha256.Sum256([]byte(key))
	relative := filepath.ToSlash(filepath.Join("blobs", hex.EncodeToString(digest[:])+".txt"))
	path, err := s.safePath(relative)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create checkpoint blob directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return "", fmt.Errorf("create checkpoint blob temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("protect checkpoint blob: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write checkpoint blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close checkpoint blob: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("commit checkpoint blob: %w", err)
	}
	return relative, nil
}

func (s *ToolResultFileStore) Get(ctx context.Context, ref string) (string, error) {
	if s == nil {
		return "", ErrInvalidCheckpointRef
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := s.safePath(ref)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read checkpoint blob: %w", err)
	}
	return string(content), nil
}

func (s *ToolResultFileStore) Cleanup(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	cutoff := time.Now().Add(-s.ttl)
	removed := 0
	err := filepath.WalkDir(s.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("cleanup checkpoint blobs: %w", err)
	}
	return removed, nil
}

func (s *ToolResultFileStore) safePath(ref string) (string, error) {
	ref = filepath.FromSlash(strings.TrimSpace(ref))
	if ref == "" || filepath.IsAbs(ref) || filepath.Clean(ref) != ref || strings.HasPrefix(ref, ".."+string(filepath.Separator)) {
		return "", ErrInvalidCheckpointRef
	}
	path := filepath.Join(s.root, ref)
	absolute, err := filepath.Abs(path)
	if err != nil || absolute != s.root && !strings.HasPrefix(absolute, s.root+string(filepath.Separator)) {
		return "", ErrInvalidCheckpointRef
	}
	return absolute, nil
}
