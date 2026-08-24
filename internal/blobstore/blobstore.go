// Package blobstore defines the small object boundary used by document files
// and derived image assets. The current adapter is local-disk backed, but the
// document pipeline depends only on Put/Open/Delete semantics, so MinIO, S3,
// COS, or OSS can be added without changing parsers or workers.
package blobstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var ErrUnsupported = errors.New("blob store capability is unavailable")

type Object struct {
	Key      string
	Size     int64
	SHA256   string
	Metadata map[string]string
}

type Store interface {
	Put(context.Context, string, io.Reader) (Object, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// LegacyFileStore is the existing document file contract. Keeping the adapter
// here makes migration incremental and prevents storage details from leaking
// into parser/embedding code.
type LegacyFileStore interface {
	Save(context.Context, string, io.Reader) (string, int64, string, error)
	Delete(context.Context, string) error
}

type LegacyOpener interface {
	Open(context.Context, string) (io.ReadSeeker, func() error, error)
}

type Adapter struct {
	legacy LegacyFileStore
}

func NewAdapter(legacy LegacyFileStore) (*Adapter, error) {
	if legacy == nil {
		return nil, ErrUnsupported
	}
	return &Adapter{legacy: legacy}, nil
}

func (a *Adapter) Put(ctx context.Context, extension string, content io.Reader) (Object, error) {
	if a == nil || a.legacy == nil || content == nil {
		return Object{}, ErrUnsupported
	}
	key, size, checksum, err := a.legacy.Save(ctx, extension, content)
	if err != nil {
		return Object{}, fmt.Errorf("put blob: %w", err)
	}
	return Object{Key: key, Size: size, SHA256: checksum}, nil
}

func (a *Adapter) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if a == nil || a.legacy == nil {
		return nil, ErrUnsupported
	}
	opener, ok := a.legacy.(LegacyOpener)
	if !ok {
		return nil, ErrUnsupported
	}
	content, closeContent, err := opener.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return &readSeekCloser{ReadSeeker: content, close: closeContent}, nil
}

func (a *Adapter) Delete(ctx context.Context, key string) error {
	if a == nil || a.legacy == nil {
		return ErrUnsupported
	}
	return a.legacy.Delete(ctx, key)
}

// ReadLimited copies a bounded object into memory for consumers such as OCR.
// Parsers should prefer Open and stream directly; this helper is only for
// genuinely in-memory model inputs and fails instead of silently truncating.
func ReadLimited(ctx context.Context, store Store, key string, maxBytes int64) ([]byte, error) {
	if store == nil || maxBytes <= 0 {
		return nil, ErrUnsupported
	}
	reader, err := store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	limited := io.LimitReader(reader, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("blob exceeds %d bytes", maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func Checksum(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type readSeekCloser struct {
	io.ReadSeeker
	close func() error
}

func (r *readSeekCloser) Close() error {
	if r == nil || r.close == nil {
		return nil
	}
	return r.close()
}
