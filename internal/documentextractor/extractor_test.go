package documentextractor_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

func TestExtractorReadsTextAndMarkdown(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.md"), []byte("# title"), 0o600); err != nil {
		t.Fatal(err)
	}
	extractor := documentextractor.New(root)
	for _, testCase := range []struct{ path, contentType, want string }{{"documents/notes.txt", "text/plain", "hello"}, {"documents/notes.md", "text/markdown", "# title"}} {
		text, err := extractor.Extract(context.Background(), testCase.path, testCase.contentType)
		if err != nil || text != testCase.want {
			t.Fatalf("Extract(%s) = %q, %v", testCase.path, text, err)
		}
	}
}

func TestExtractorReadsPDFText(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "guide.pdf"), []byte(minimalPDF("Hello PDF")), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/guide.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Extract PDF = %v", err)
	}
	if text != "Hello PDF" {
		t.Fatalf("Extract PDF text = %q, want %q", text, "Hello PDF")
	}
}

func TestExtractorReadsCompressedPDFText(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "compressed.pdf"), compressedPDF("Compressed PDF"), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/compressed.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Extract compressed PDF = %v", err)
	}
	if text != "Compressed PDF" {
		t.Fatalf("Extract compressed PDF text = %q, want %q", text, "Compressed PDF")
	}
}

func TestExtractorReadsPDFTextArray(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "array.pdf"), []byte(minimalPDFContent("[(Hello) 120 (PDF)] TJ")), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/array.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Extract PDF text array = %v", err)
	}
	if text != "Hello\nPDF" {
		t.Fatalf("Extract PDF text array = %q, want %q", text, "Hello\nPDF")
	}
}

func TestExtractorRejectsPDFWithoutText(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "empty.pdf"), []byte(minimalPDF("")), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := documentextractor.New(root).Extract(context.Background(), "documents/empty.pdf", "application/pdf")
	if !errors.Is(err, documentextractor.ErrEmptyText) {
		t.Fatalf("empty PDF error = %v", err)
	}
}

func TestExtractorRejectsEmptyAndEscapingFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "documents"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "empty.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	extractor := documentextractor.New(root)
	_, err := extractor.Extract(context.Background(), "documents/empty.txt", "text/plain")
	if !errors.Is(err, documentextractor.ErrEmptyText) {
		t.Fatalf("empty error = %v", err)
	}
	_, err = extractor.Extract(context.Background(), "../secret.txt", "text/plain")
	if !errors.Is(err, documentextractor.ErrInvalidStoragePath) {
		t.Fatalf("path error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "documents", "broken.pdf"), []byte("%PDF-1.4\n%%EOF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = extractor.Extract(context.Background(), "documents/broken.pdf", "application/pdf")
	if !errors.Is(err, documentextractor.ErrInvalidPDF) {
		t.Fatalf("invalid PDF error = %v", err)
	}
}

func minimalPDF(text string) string {
	return minimalPDFContent("(" + text + ") Tj")
}

func minimalPDFContent(content string) string {
	return "%PDF-1.4\n" +
		"1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n" +
		"2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n" + "3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj\n" +
		"4 0 obj << /Length 46 >> stream\n" +
		"BT /F1 18 Tf 72 720 Td " + content + " ET\n" +
		"endstream endobj\n" +
		"5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n" +
		"trailer << /Root 1 0 R >>\n%%EOF\n"
}

func compressedPDF(text string) []byte {
	var stream bytes.Buffer
	writer := zlib.NewWriter(&stream)
	if _, err := writer.Write([]byte("BT /F1 18 Tf 72 720 Td (" + text + ") Tj ET\n")); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return []byte(fmt.Sprintf("%%PDF-1.4\n1 0 obj << /Length %d /Filter /FlateDecode >> stream\n%s\nendstream\n%%%%EOF\n", stream.Len(), stream.Bytes()))
}
