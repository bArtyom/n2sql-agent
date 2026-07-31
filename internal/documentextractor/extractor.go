package documentextractor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxExtractedTextBytes int64 = 10 << 20

var (
	ErrInvalidStoragePath = errors.New("invalid document storage path")
	ErrUnsupportedType    = errors.New("unsupported document content type")
	ErrEmptyText          = errors.New("document contains no text")
)

type Extractor struct{ root string }

func New(root string) *Extractor { return &Extractor{root: root} }

func (e *Extractor) Extract(ctx context.Context, storagePath, contentType string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if contentType != "text/plain" && contentType != "text/markdown" {
		return "", ErrUnsupportedType
	}
	if filepath.IsAbs(storagePath) || filepath.Clean(storagePath) != storagePath || filepath.Dir(storagePath) != "documents" {
		return "", ErrInvalidStoragePath
	}
	file, err := os.Open(filepath.Join(e.root, storagePath))
	if err != nil {
		return "", fmt.Errorf("open document: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxExtractedTextBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > maxExtractedTextBytes {
		return "", fmt.Errorf("extracted text is too large")
	}
	text := string(content)
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	return text, nil
}
