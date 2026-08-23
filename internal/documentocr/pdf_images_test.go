package documentocr_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentocr"
)

func TestPDFImageExtractorReadsImagesForOnePage(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "fake-pdfimages")
	script := "#!/bin/sh\nfor last do :; done\nprintf png > \"$last-000.png\"\n"
	if runtime.GOOS == "windows" {
		command += ".cmd"
		script = "@echo off\r\nset \"prefix=%~7\"\r\necho|set /p \"=png\">\"%prefix%-000.png\"\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	extractor := documentocr.NewPDFImageExtractor(command)
	assets, err := extractor.ExtractPageImages(context.Background(), []byte("pdf"), 3)
	if err != nil {
		t.Fatalf("ExtractPageImages() error = %v", err)
	}
	if len(assets) != 1 || assets[0].Page != 3 || assets[0].MIMEType != "image/png" || string(assets[0].Data) != "png" {
		t.Fatalf("assets = %#v", assets)
	}
}
