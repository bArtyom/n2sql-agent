package documentocr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultRendererBinary = "pdftoppm"
	defaultRenderDPI      = 150
	maxRenderedImageBytes = 8 << 20
	maxCommandErrorBytes  = 2000
)

// PDFToImageRenderer uses the Poppler pdftoppm command to render PDF pages.
// The command is optional: it is only constructed when OCR is enabled.
type PDFToImageRenderer struct {
	binary   string
	dpi      int
	maxPages int
}

func NewPDFToImageRenderer(binary string, dpi, maxPages int) *PDFToImageRenderer {
	if strings.TrimSpace(binary) == "" {
		binary = defaultRendererBinary
	}
	if dpi <= 0 {
		dpi = defaultRenderDPI
	}
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	return &PDFToImageRenderer{binary: binary, dpi: dpi, maxPages: maxPages}
}

func (r *PDFToImageRenderer) Render(ctx context.Context, pdf []byte) ([]PageImage, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	return r.renderRange(ctx, pdf, 1, r.maxPages)
}

func (r *PDFToImageRenderer) RenderPages(ctx context.Context, pdf []byte, pageNumbers []int) ([]PageImage, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unique := make(map[int]struct{}, len(pageNumbers))
	for _, pageNumber := range pageNumbers {
		if pageNumber > 0 && pageNumber <= r.maxPages {
			unique[pageNumber] = struct{}{}
		}
	}
	selected := make([]int, 0, len(unique))
	for pageNumber := range unique {
		selected = append(selected, pageNumber)
	}
	sort.Ints(selected)
	pages := make([]PageImage, 0, len(selected))
	for _, pageNumber := range selected {
		rendered, err := r.renderRange(ctx, pdf, pageNumber, pageNumber)
		if err != nil {
			return nil, err
		}
		pages = append(pages, rendered...)
	}
	return pages, nil
}

func (r *PDFToImageRenderer) renderRange(ctx context.Context, pdf []byte, firstPage, lastPage int) ([]PageImage, error) {
	if r == nil {
		return nil, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstPage <= 0 || lastPage < firstPage {
		return nil, fmt.Errorf("invalid PDF render page range %d-%d", firstPage, lastPage)
	}
	workDir, err := os.MkdirTemp("", "n2sql-ocr-render-")
	if err != nil {
		return nil, fmt.Errorf("create PDF render directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	pdfPath := filepath.Join(workDir, "source.pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return nil, fmt.Errorf("write PDF for rendering: %w", err)
	}
	prefix := filepath.Join(workDir, "page")
	args := []string{"-jpeg", "-r", strconv.Itoa(r.dpi), "-f", strconv.Itoa(firstPage), "-l", strconv.Itoa(lastPage), pdfPath, prefix}
	output, err := exec.CommandContext(ctx, r.binary, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if len(message) > maxCommandErrorBytes {
			message = message[:maxCommandErrorBytes]
		}
		if message == "" {
			return nil, fmt.Errorf("render PDF pages with %s: %w", r.binary, err)
		}
		return nil, fmt.Errorf("render PDF pages with %s: %w: %s", r.binary, err, message)
	}

	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil, fmt.Errorf("list rendered PDF pages: %w", err)
	}
	type renderedPage struct {
		number int
		path   string
	}
	files := make([]renderedPage, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "page-") || filepath.Ext(entry.Name()) != ".jpg" {
			continue
		}
		numberText := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "page-"), ".jpg")
		number, parseErr := strconv.Atoi(numberText)
		if parseErr != nil || number <= 0 {
			continue
		}
		files = append(files, renderedPage{number: number, path: filepath.Join(workDir, entry.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].number < files[j].number })
	if len(files) == 0 {
		return nil, fmt.Errorf("render PDF pages with %s produced no JPEG pages", r.binary)
	}

	pages := make([]PageImage, 0, len(files))
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil {
			return nil, fmt.Errorf("read rendered PDF page %d: %w", file.number, err)
		}
		if len(data) > maxRenderedImageBytes {
			return nil, fmt.Errorf("rendered PDF page %d is too large", file.number)
		}
		pages = append(pages, PageImage{Number: file.number, Data: data})
	}
	return pages, nil
}
