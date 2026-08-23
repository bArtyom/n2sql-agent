package documentocr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

const (
	defaultPDFImagesBinary = "pdfimages"
	maxEmbeddedPDFImage    = 8 << 20
)

type PDFImageExtractor struct {
	binary string
}

func NewPDFImageExtractor(binary string) *PDFImageExtractor {
	if strings.TrimSpace(binary) == "" {
		binary = defaultPDFImagesBinary
	}
	return &PDFImageExtractor{binary: binary}
}

func (e *PDFImageExtractor) Available() bool {
	if e == nil || strings.TrimSpace(e.binary) == "" {
		return false
	}
	_, err := exec.LookPath(e.binary)
	return err == nil
}

func (e *PDFImageExtractor) ExtractPageImages(ctx context.Context, pdf []byte, page int) ([]documentextractor.ImageAsset, error) {
	if e == nil || page <= 0 {
		return nil, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workDir, err := os.MkdirTemp("", "n2sql-pdf-images-")
	if err != nil {
		return nil, fmt.Errorf("create PDF image directory: %w", err)
	}
	defer os.RemoveAll(workDir)
	pdfPath := filepath.Join(workDir, "source.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return nil, fmt.Errorf("write PDF for image extraction: %w", err)
	}
	prefix := filepath.Join(workDir, "image")
	command := exec.CommandContext(ctx, e.binary, "-png", "-f", fmt.Sprint(page), "-l", fmt.Sprint(page), pdfPath, prefix)
	if output, err := command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("extract PDF images with %s: %w: %s", e.binary, err, strings.TrimSpace(string(output)))
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("list PDF images: %w", err)
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "image-") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	assets := make([]documentextractor.ImageAsset, 0, len(files))
	for _, filename := range files {
		data, readErr := os.ReadFile(filepath.Join(workDir, filename))
		if readErr != nil {
			return nil, fmt.Errorf("read PDF image %s: %w", filename, readErr)
		}
		if len(data) > maxEmbeddedPDFImage {
			return nil, fmt.Errorf("PDF image %s is too large", filename)
		}
		assets = append(assets, documentextractor.ImageAsset{
			Filename: filename,
			MIMEType: pdfImageMIME(filename),
			Data:     data,
			Page:     page,
			Source:   "embedded_pdf",
		})
	}
	return assets, nil
}

func pdfImageMIME(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
