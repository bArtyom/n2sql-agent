package documentocr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultPDFInfoBinary = "pdfinfo"
	maxPDFPageCount      = 10000
)

type PageCounter interface {
	CountPages(context.Context, []byte) (int, error)
}

type PDFInfoPageCounter struct {
	binary string
}

func NewPDFInfoPageCounter(binary string) *PDFInfoPageCounter {
	if strings.TrimSpace(binary) == "" {
		binary = defaultPDFInfoBinary
	}
	return &PDFInfoPageCounter{binary: binary}
}

func (c *PDFInfoPageCounter) CountPages(ctx context.Context, pdf []byte) (int, error) {
	if c == nil || strings.TrimSpace(c.binary) == "" {
		return 0, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	workDir, err := os.MkdirTemp("", "n2sql-pdf-info-")
	if err != nil {
		return 0, fmt.Errorf("create PDF info directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	pdfPath := filepath.Join(workDir, "source.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return 0, fmt.Errorf("write PDF for page count: %w", err)
	}
	output, err := exec.CommandContext(ctx, c.binary, pdfPath).Output()
	if err != nil {
		return 0, fmt.Errorf("read PDF page count with %s: %w", c.binary, err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(strings.TrimSuffix(fields[0], ":"), "pages") {
			continue
		}
		count, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil || count <= 0 || count > maxPDFPageCount {
			return 0, fmt.Errorf("invalid PDF page count %q", fields[1])
		}
		return count, nil
	}
	return 0, fmt.Errorf("PDF page count is missing")
}
