package documentocr

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultPageTextBinary = "pdftotext"
	maxPageTextBytes      = 10 << 20
)

type PDFToTextPageExtractor struct {
	binary string
}

func NewPDFToTextPageExtractor(binary string) *PDFToTextPageExtractor {
	if strings.TrimSpace(binary) == "" {
		binary = defaultPageTextBinary
	}
	return &PDFToTextPageExtractor{binary: binary}
}

func (e *PDFToTextPageExtractor) ExtractPageText(ctx context.Context, pdf []byte, page int) (string, error) {
	if e == nil || page <= 0 {
		return "", ErrNotConfigured
	}
	workDir, err := os.MkdirTemp("", "n2sql-ocr-text-")
	if err != nil {
		return "", fmt.Errorf("create PDF text directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	pdfPath := filepath.Join(workDir, "source.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return "", fmt.Errorf("write PDF for text extraction: %w", err)
	}
	command := exec.CommandContext(ctx, e.binary, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-layout", "-enc", "UTF-8", pdfPath, "-")
	output, err := command.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("create PDF text output: %w", err)
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start PDF text extractor: %w", err)
	}
	text, readErr := io.ReadAll(io.LimitReader(output, maxPageTextBytes+1))
	waitErr := command.Wait()
	if readErr != nil {
		return "", fmt.Errorf("read PDF page text: %w", readErr)
	}
	if len(text) > maxPageTextBytes {
		return "", fmt.Errorf("PDF page text is too large")
	}
	if waitErr != nil {
		return "", fmt.Errorf("extract PDF page %d text with %s: %w", page, e.binary, waitErr)
	}
	return strings.TrimSpace(string(text)), nil
}
