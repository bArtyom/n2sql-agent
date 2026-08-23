package documentextractor

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyPDFPage(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		hasImage   bool
		wantOCR    bool
		wantLayout bool
	}{
		{name: "rich text", text: strings.Repeat("正文 ", 80)},
		{name: "empty page", wantOCR: true, wantLayout: true},
		{name: "sparse page", text: "标题", wantOCR: true, wantLayout: true},
		{name: "sparse page with image", text: "标题", hasImage: true, wantOCR: true, wantLayout: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := ClassifyPDFPage(1, tt.text, tt.hasImage, 100)
			if state.NeedsOCR != tt.wantOCR || state.NeedsLayout != tt.wantLayout {
				t.Fatalf("state=%#v, want OCR=%t layout=%t", state, tt.wantOCR, tt.wantLayout)
			}
		})
	}
}

func TestPDFPageBlockKindsAreStable(t *testing.T) {
	block := PDFPageBlock{Page: 2, Kind: PDFBlockFigure, Order: 3, Bounds: [4]int{1, 2, 3, 4}}
	if block.Kind != PDFBlockFigure || block.Page != 2 || block.Order != 3 {
		t.Fatalf("block=%#v", block)
	}
}

func TestPDFLayoutInterfacesRemainComposable(t *testing.T) {
	var _ PDFPageInspector = pdfPageInspectorStub{}
	var _ PDFEmbeddedImageExtractor = pdfEmbeddedImageExtractorStub{}
	var _ PDFLayoutAnalyzer = pdfLayoutAnalyzerStub{}
}

type pdfPageInspectorStub struct{}

func (pdfPageInspectorStub) InspectPages(context.Context, []byte) ([]PDFPage, error) {
	return nil, nil
}

type pdfEmbeddedImageExtractorStub struct{}

func (pdfEmbeddedImageExtractorStub) ExtractPageImages(context.Context, []byte, int) ([]ImageAsset, error) {
	return nil, nil
}

type pdfLayoutAnalyzerStub struct{}

func (pdfLayoutAnalyzerStub) AnalyzePage(context.Context, PDFPage) ([]PDFPageBlock, error) {
	return nil, nil
}
