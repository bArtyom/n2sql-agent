package documentocr_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/documentocr"
)

type rendererStub struct {
	pages []documentocr.PageImage
}

func (r rendererStub) Render(context.Context, []byte) ([]documentocr.PageImage, error) {
	return r.pages, nil
}

type recognizerStub struct{}

func (recognizerStub) Recognize(_ context.Context, image []byte) (string, error) {
	return string(image), nil
}

func TestServiceRecognizesPagesInDocumentOrder(t *testing.T) {
	service := documentocr.NewService(rendererStub{pages: []documentocr.PageImage{
		{Number: 2, Data: []byte("page two")},
		{Number: 1, Data: []byte("page one")},
	}}, recognizerStub{}, 10, 2)

	text, err := service.Extract(context.Background(), []byte("pdf"))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if want := "[Page 1]\npage one\n\n[Page 2]\npage two"; text != want {
		t.Fatalf("Extract() = %q, want %q", text, want)
	}
}

func TestServiceRejectsMissingOCROutput(t *testing.T) {
	service := documentocr.NewService(rendererStub{pages: []documentocr.PageImage{{Number: 1, Data: nil}}}, recognizerStub{}, 10, 1)

	_, err := service.Extract(context.Background(), []byte("pdf"))
	if err == nil || !strings.Contains(err.Error(), "no OCR text") {
		t.Fatalf("Extract() error = %v, want no OCR text error", err)
	}
}

func TestServiceLimitsRenderedPages(t *testing.T) {
	service := documentocr.NewService(rendererStub{pages: []documentocr.PageImage{
		{Number: 1, Data: []byte("one")},
		{Number: 2, Data: []byte("two")},
		{Number: 3, Data: []byte("three")},
	}}, recognizerStub{}, 2, 1)

	text, err := service.Extract(context.Background(), []byte("pdf"))
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if strings.Contains(text, "three") {
		t.Fatalf("Extract() included page beyond max: %q", text)
	}
}
