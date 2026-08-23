package documentextractor

import (
	"context"
	"strings"
)

const (
	PDFBlockText   = "text"
	PDFBlockTable  = "table"
	PDFBlockFigure = "figure"
)

// PDFPageState is the cheap, page-level decision made before rendering or
// invoking OCR. Native text remains the source of truth for rich pages.
type PDFPageState struct {
	Number      int
	NativeText  string
	TextRunes   int
	HasImage    bool
	NeedsOCR    bool
	NeedsLayout bool
}

// PDFPageBlock is a layout provider's normalized region result. Image bytes
// belong to ImageAsset/resource storage and are never appended to Markdown.
type PDFPageBlock struct {
	Page     int
	Kind     string
	Order    int
	Text     string
	Image    []byte
	MIMEType string
	Bounds   [4]int
	Source   string
}

type PDFPageInspector interface {
	InspectPages(context.Context, []byte) ([]PDFPage, error)
}

type PDFPageRenderer interface {
	RenderPDFPages(context.Context, []byte, []int) ([]PDFPage, error)
}

type PDFPageOCR interface {
	OCRPage(context.Context, PDFPage) (string, error)
}

type PDFEmbeddedImageExtractor interface {
	ExtractPageImages(context.Context, []byte, int) ([]ImageAsset, error)
}

type PDFLayoutAnalyzer interface {
	AnalyzePage(context.Context, PDFPage) ([]PDFPageBlock, error)
}

// ClassifyPDFPage decides whether a page needs the expensive scan path. A
// rich native page with an embedded image still does not need whole-page OCR;
// the image extractor can handle that object separately.
func ClassifyPDFPage(number int, text string, hasImage bool, minTextRunes int) PDFPageState {
	if minTextRunes <= 0 {
		minTextRunes = 100
	}
	nativeText := strings.TrimSpace(text)
	textRunes := len([]rune(nativeText))
	sparse := textRunes < minTextRunes
	return PDFPageState{
		Number:      number,
		NativeText:  nativeText,
		TextRunes:   textRunes,
		HasImage:    hasImage,
		NeedsOCR:    sparse,
		NeedsLayout: sparse,
	}
}
