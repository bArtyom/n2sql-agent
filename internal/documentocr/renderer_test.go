package documentocr_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentocr"
)

func TestPDFToImageRendererReadsPagesFromCommand(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "fake-pdftoppm")
	script := "#!/bin/sh\nfor last do :; done\nprefix=\"$last\"\nprintf one > \"$prefix-1.jpg\"\nprintf two > \"$prefix-2.jpg\"\n"
	if runtime.GOOS == "windows" {
		command += ".cmd"
		script = "@echo off\r\nset \"prefix=%~9\"\r\necho|set /p \"=one\">\"%prefix%-1.jpg\"\r\necho|set /p \"=two\">\"%prefix%-2.jpg\"\r\nexit /b 0\r\n"
	}
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	renderer := documentocr.NewPDFToImageRenderer(command, 150, 2)
	pages, err := renderer.Render(context.Background(), []byte("%PDF-1.7"))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(pages) != 2 || pages[0].Number != 1 || string(pages[0].Data) != "one" || pages[1].Number != 2 || string(pages[1].Data) != "two" {
		t.Fatalf("pages = %#v, want two ordered pages", pages)
	}
}
