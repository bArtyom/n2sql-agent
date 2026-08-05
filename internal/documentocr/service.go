package documentocr

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// Provider recognizes one page image as text.
type Provider interface {
	Recognize(context.Context, []byte) (string, error)
}

// Service coordinates page rendering and OCR while preserving page order.
type Service struct {
	renderer    PageRenderer
	provider    Provider
	maxPages    int
	concurrency int
}

func NewService(renderer PageRenderer, provider Provider, maxPages, concurrency int) *Service {
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &Service{
		renderer:    renderer,
		provider:    provider,
		maxPages:    maxPages,
		concurrency: concurrency,
	}
}

func (s *Service) Extract(ctx context.Context, pdf []byte) (string, error) {
	if s == nil || s.renderer == nil || s.provider == nil {
		return "", ErrNotConfigured
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	pages, err := s.renderer.Render(ctx, pdf)
	if err != nil {
		return "", fmt.Errorf("render PDF pages: %w", err)
	}
	if len(pages) == 0 {
		return "", ErrNoText
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

	texts := make([]string, len(pages))
	semaphore := make(chan struct{}, s.concurrency)
	var wait sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for index, page := range pages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		wait.Add(1)
		go func(index int, page PageImage) {
			defer wait.Done()
			defer func() { <-semaphore }()
			text, err := s.provider.Recognize(ctx, page.Data)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("OCR page %d: %w", page.Number, err)
				}
				errMu.Unlock()
				return
			}
			texts[index] = strings.TrimSpace(text)
		}(index, page)
	}
	wait.Wait()
	if firstErr != nil {
		return "", firstErr
	}

	blocks := make([]string, 0, len(texts))
	for index, text := range texts {
		if text == "" {
			continue
		}
		blocks = append(blocks, fmt.Sprintf("[Page %d]\n%s", pages[index].Number, text))
	}
	if len(blocks) == 0 {
		return "", ErrNoText
	}
	return strings.Join(blocks, "\n\n"), nil
}
