package documentocr_test

import (
	"context"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentocr"
)

type pageCounterStub struct {
	pages int
}

func (s pageCounterStub) CountPages(context.Context, []byte) (int, error) {
	return s.pages, nil
}

type countingRecognizer struct {
	calls int
}

func (s *countingRecognizer) Recognize(context.Context, []byte) (string, error) {
	s.calls++
	return "should not run during inspection", nil
}

type selectiveRendererStub struct {
	requested []int
}

func (s *selectiveRendererStub) Render(context.Context, []byte) ([]documentocr.PageImage, error) {
	return nil, nil
}

func (s *selectiveRendererStub) RenderPages(_ context.Context, _ []byte, pages []int) ([]documentocr.PageImage, error) {
	s.requested = append([]int(nil), pages...)
	return []documentocr.PageImage{{Number: pages[0], Data: []byte("selected page")}}, nil
}

func TestInspectPagesDoesNotCallOCR(t *testing.T) {
	recognizer := &countingRecognizer{}
	service := documentocr.NewServiceWithPageTextAndCounter(
		rendererStub{},
		recognizer,
		pageTextStub{},
		pageCounterStub{pages: 3},
		10,
		1,
	)

	pages, err := service.InspectPages(context.Background(), []byte("pdf"))
	if err != nil {
		t.Fatalf("InspectPages() error = %v", err)
	}
	if len(pages) != 3 || pages[0].Number != 1 || pages[0].Text != "text layer page" || pages[1].Number != 2 || pages[1].Text != "" {
		t.Fatalf("pages = %#v", pages)
	}
	if recognizer.calls != 0 {
		t.Fatalf("OCR calls = %d, want 0", recognizer.calls)
	}
}

func TestRenderPagesUsesOnlyRequestedPages(t *testing.T) {
	renderer := &selectiveRendererStub{}
	service := documentocr.NewService(renderer, &countingRecognizer{}, 10, 1)

	pages, err := service.RenderPages(context.Background(), []byte("pdf"), []int{2, 5})
	if err != nil {
		t.Fatalf("RenderPages() error = %v", err)
	}
	if len(pages) != 1 || pages[0].Number != 2 || string(pages[0].Data) != "selected page" {
		t.Fatalf("pages = %#v", pages)
	}
	if len(renderer.requested) != 2 || renderer.requested[0] != 2 || renderer.requested[1] != 5 {
		t.Fatalf("requested pages = %#v", renderer.requested)
	}
}
