package documentocr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
)

const (
	defaultMaxPages    = 20
	defaultConcurrency = 1
)

var (
	ErrNotConfigured = errors.New("OCR service is not configured")
	ErrNoText        = errors.New("no OCR text was produced")
)

// PageImage is one rendered PDF page. Number is one-based and is used to keep
// OCR output in the original document order.
type PageImage struct {
	Number int
	Data   []byte
}

// PageRenderer turns a PDF into page images. Implementations may use a local
// renderer or a dedicated document parsing service.
type PageRenderer interface {
	Render(context.Context, []byte) ([]PageImage, error)
}

type PageTextExtractor interface {
	ExtractPageText(context.Context, []byte, int) (string, error)
}

// Provider recognizes one page image as text.
type Provider interface {
	Recognize(context.Context, []byte) (string, error)
}

// Service coordinates page rendering and OCR while preserving page order.
type Service struct {
	renderer    PageRenderer
	provider    Provider
	pageText    PageTextExtractor
	maxPages    int
	concurrency int
}

func NewService(renderer PageRenderer, provider Provider, maxPages, concurrency int) *Service {
	return newService(renderer, provider, nil, maxPages, concurrency)
}

func NewServiceWithPageText(renderer PageRenderer, provider Provider, pageText PageTextExtractor, maxPages, concurrency int) *Service {
	return newService(renderer, provider, pageText, maxPages, concurrency)
}

func newService(renderer PageRenderer, provider Provider, pageText PageTextExtractor, maxPages, concurrency int) *Service {
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &Service{
		renderer:    renderer,
		provider:    provider,
		pageText:    pageText,
		maxPages:    maxPages,
		concurrency: concurrency,
	}
}

func (s *Service) Extract(ctx context.Context, pdf []byte) (string, error) {
	pages, err := s.ExtractPages(ctx, pdf)
	if err != nil {
		return "", err
	}
	blocks := make([]string, 0, len(pages))
	for _, page := range pages {
		if strings.TrimSpace(page.Text) != "" {
			blocks = append(blocks, fmt.Sprintf("[Page %d]\n%s", page.Number, strings.TrimSpace(page.Text)))
		}
	}
	if len(blocks) == 0 {
		return "", ErrNoText
	}
	return strings.Join(blocks, "\n\n"), nil
}

func (s *Service) ExtractPages(ctx context.Context, pdf []byte) ([]documentextractor.PDFPage, error) {
	if s == nil || s.renderer == nil || s.provider == nil {
		return nil, ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pages, err := s.renderer.Render(ctx, pdf)
	if err != nil {
		return nil, fmt.Errorf("render PDF pages: %w", err)
	}
	if len(pages) == 0 {
		return nil, ErrNoText
	}
	pages = append([]PageImage(nil), pages...)
	for index := range pages {
		if pages[index].Number <= 0 {
			pages[index].Number = index + 1
		}
	}
	sort.SliceStable(pages, func(i, j int) bool {
		return pages[i].Number < pages[j].Number
	})
	if len(pages) > s.maxPages {
		pages = pages[:s.maxPages]
	}

	results := make([]documentextractor.PDFPage, len(pages))
	semaphore := make(chan struct{}, s.concurrency)
	var wait sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for index, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		wait.Add(1)
		go func(index int, page PageImage) {
			defer wait.Done()
			defer func() { <-semaphore }()
			text := ""
			usedOCR := false
			if s.pageText != nil {
				text, _ = s.pageText.ExtractPageText(ctx, pdf, page.Number)
			}
			if strings.TrimSpace(text) == "" {
				var err error
				text, err = s.provider.Recognize(ctx, page.Data)
				if err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("OCR page %d: %w", page.Number, err)
					}
					errMu.Unlock()
					return
				}
				usedOCR = true
			}
			results[index] = documentextractor.PDFPage{Number: page.Number, Text: strings.TrimSpace(text), Image: page.Data, OCR: usedOCR}
		}(index, page)
	}
	wait.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

func (s *Service) ExtractImage(ctx context.Context, mimeType string, image []byte) (string, error) {
	if s == nil || s.provider == nil {
		return "", ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if provider, ok := s.provider.(interface {
		RecognizeWithMIME(context.Context, string, []byte) (string, error)
	}); ok {
		return provider.RecognizeWithMIME(ctx, mimeType, image)
	}
	return s.provider.Recognize(ctx, image)
}
