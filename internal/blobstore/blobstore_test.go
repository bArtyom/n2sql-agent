package blobstore_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/blobstore"
)

type legacyStub struct{ data string }

func (s *legacyStub) Save(context.Context, string, io.Reader) (string, int64, string, error) {
	return "documents/file.md", int64(len(s.data)), blobstore.Checksum([]byte(s.data)), nil
}
func (s *legacyStub) Delete(context.Context, string) error { return nil }
func (s *legacyStub) Open(context.Context, string) (io.ReadSeeker, func() error, error) {
	return strings.NewReader(s.data), func() error { return nil }, nil
}

func TestAdapterPreservesStreamingOpenBoundary(t *testing.T) {
	adapter, err := blobstore.NewAdapter(&legacyStub{data: "stream me"})
	if err != nil {
		t.Fatal(err)
	}
	object, err := adapter.Put(context.Background(), ".md", strings.NewReader("ignored"))
	if err != nil || object.Key == "" {
		t.Fatalf("put = %#v, %v", object, err)
	}
	reader, err := adapter.Open(context.Background(), object.Key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, _ := io.ReadAll(reader)
	if string(data) != "stream me" {
		t.Fatalf("data = %q", data)
	}
}
