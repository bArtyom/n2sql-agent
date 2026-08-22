package documentextractor_test

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

type scannedPDFProcessorStub struct{}

func (scannedPDFProcessorStub) Extract(context.Context, []byte) (string, error) {
	return "OCR scanned page", nil
}

type imageProcessorStub struct {
	mime string
}

func (s *imageProcessorStub) ExtractImage(_ context.Context, mime string, image []byte) (string, error) {
	s.mime = mime
	if len(image) == 0 {
		return "", errors.New("image was empty")
	}
	return "OCR image text", nil
}

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

func TestExtractorReadsDOCXHeadingsAndParagraphs(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "guide.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = entry.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Install</w:t></w:r></w:p><w:p><w:r><w:t>Run the service.</w:t></w:r></w:p></w:body></w:document>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/guide.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if err != nil {
		t.Fatalf("Extract DOCX = %v", err)
	}
	if text != "# Install\nRun the service." {
		t.Fatalf("Extract DOCX = %q", text)
	}
}

func TestExtractorReadsPPTXSlidesAsStructuredText(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeZipFile(t, filepath.Join(directory, "deck.pptx"), map[string]string{
		"ppt/slides/slide2.xml": `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><a:p><a:r><a:t>Second slide</a:t></a:r></a:p></p:cSld></p:sld>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><a:p><a:r><a:t>Title</a:t></a:r></a:p><a:p><a:r><a:t>Body</a:t></a:r></a:p></p:cSld></p:sld>`,
	})

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/deck.pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
	if err != nil {
		t.Fatalf("Extract PPTX = %v", err)
	}
	want := "# Slide 1\nTitle\nBody\n# Slide 2\nSecond slide"
	if text != want {
		t.Fatalf("Extract PPTX = %q, want %q", text, want)
	}
}

func TestExtractorReadsXLSXRowsAndSharedStrings(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	writeZipFile(t, filepath.Join(directory, "table.xlsx"), map[string]string{
		"xl/sharedStrings.xml":     `<sst xmlns="s"><si><t>Name</t></si><si><t>Value</t></si><si><t>Deep</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="s"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>42</v></c></row></sheetData></worksheet>`,
	})

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/table.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	if err != nil {
		t.Fatalf("Extract XLSX = %v", err)
	}
	want := "# Sheet 1\nName\tValue\nDeep\t42"
	if text != want {
		t.Fatalf("Extract XLSX = %q, want %q", text, want)
	}
}

func writeZipFile(t *testing.T, path string, files map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractorPreservesDOCXTableAsMarkdown(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "table.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = entry.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:tbl><w:tr><w:tc><w:p><w:r><w:t>Name</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Value</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>Mode</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Deep</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`))
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	text, err := documentextractor.New(root).Extract(context.Background(), "documents/table.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if err != nil {
		t.Fatalf("Extract DOCX table = %v", err)
	}
	if text != "| Name | Value |\n| --- | --- |\n| Mode | Deep |" {
		t.Fatalf("Extract DOCX table = %q", text)
	}
}

func TestExtractorReadsHTMLStructureWithoutScriptOrStyle(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "guide.html"), []byte(`<html><head><style>.x{}</style><script>alert(1)</script></head><body><h1>Install</h1><p>Run <b>the service</b>.</p><ul><li>Check logs</li></ul></body></html>`), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := documentextractor.New(root).Extract(context.Background(), "documents/guide.html", "text/html")
	if err != nil {
		t.Fatalf("Extract HTML = %v", err)
	}
	if text != "# Install\nRun the service.\nCheck logs" {
		t.Fatalf("Extract HTML = %q", text)
	}
}

func TestExtractorPreservesHTMLTableAsMarkdown(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "table.html"), []byte(`<table><tr><th>Name</th><th>Value</th></tr><tr><td>Mode</td><td>Deep</td></tr></table>`), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := documentextractor.New(root).Extract(context.Background(), "documents/table.html", "text/html")
	if err != nil {
		t.Fatalf("Extract HTML table = %v", err)
	}
	if text != "| Name | Value |\n| --- | --- |\n| Mode | Deep |" {
		t.Fatalf("Extract HTML table = %q", text)
	}
}

func TestExtractorRejectsDOCXWithoutDocumentXML(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "broken.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	if _, err := archive.Create("word/styles.xml"); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = documentextractor.New(root).Extract(context.Background(), "documents/broken.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	if err == nil || !strings.Contains(err.Error(), "document.xml is missing") {
		t.Fatalf("broken DOCX error = %v", err)
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

func TestExtractorReadsPDFHexText(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "hex.pdf"), []byte(minimalPDFContent("<FEFF00480069> Tj")), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.New(root).Extract(context.Background(), "documents/hex.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Extract PDF hex text = %v", err)
	}
	if text != "Hi" {
		t.Fatalf("Extract PDF hex text = %q, want %q", text, "Hi")
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

func TestExtractorUsesOCRForPDFWithoutTextLayer(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "scan.pdf"), []byte(minimalPDF("")), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := documentextractor.NewWithOCR(root, scannedPDFProcessorStub{}).Extract(context.Background(), "documents/scan.pdf", "application/pdf")
	if err != nil {
		t.Fatalf("Extract scanned PDF = %v", err)
	}
	if text != "OCR scanned page" {
		t.Fatalf("Extract scanned PDF text = %q, want OCR output", text)
	}
}

func TestExtractorUsesOCRForUploadedImage(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "documents"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "documents", "scan.png"), []byte("PNG image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	processor := &imageProcessorStub{}
	text, err := documentextractor.NewWithOCRAndImages(root, nil, processor).Extract(context.Background(), "documents/scan.png", "image/png")
	if err != nil {
		t.Fatalf("Extract image = %v", err)
	}
	if text != "OCR image text" || processor.mime != "image/png" {
		t.Fatalf("Extract image = %q, mime=%q", text, processor.mime)
	}
}

func TestExtractorLimitsInflatedPDFStream(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "documents")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "large.pdf"), compressedPDF(strings.Repeat("A", 10<<20+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := documentextractor.New(root).Extract(context.Background(), "documents/large.pdf", "application/pdf")
	if err == nil || !strings.Contains(err.Error(), "decoded PDF stream is too large") {
		t.Fatalf("large PDF error = %v", err)
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
	stream := "BT /F1 18 Tf 72 720 Td " + content + " ET\n"
	return "%PDF-1.4\n" +
		"1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n" +
		"2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n" + "3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj\n" +
		fmt.Sprintf("4 0 obj << /Length %d >> stream\n%s", len(stream), stream) +
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
