package documentextractor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type pdfPagePipelineStub struct {
	pages []PDFPage
}

func (s pdfPagePipelineStub) InspectPages(context.Context, []byte) ([]PDFPage, error) {
	return append([]PDFPage(nil), s.pages...), nil
}

func (s pdfPagePipelineStub) RenderPDFPages(_ context.Context, _ []byte, pageNumbers []int) ([]PDFPage, error) {
	pages := make([]PDFPage, 0, len(pageNumbers))
	for _, pageNumber := range pageNumbers {
		pages = append(pages, PDFPage{Number: pageNumber, Image: []byte("rendered")})
	}
	return pages, nil
}

type pdfPageOCRStub struct {
	calls []int
}

func (s *pdfPageOCRStub) OCRPage(_ context.Context, page PDFPage) (string, error) {
	s.calls = append(s.calls, page.Number)
	return "OCR page " + string(rune('0'+page.Number)), nil
}

type pdfLayoutStub struct {
	blocks map[int][]PDFPageBlock
	err    error
}

func (s pdfLayoutStub) AnalyzePage(_ context.Context, page PDFPage) ([]PDFPageBlock, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.blocks[page.Number], nil
}

func TestPDFParserOCRsOnlySparsePages(t *testing.T) {
	inspector := pdfPagePipelineStub{pages: []PDFPage{
		{Number: 1, Text: strings.Repeat("native page one ", 20)},
		{Number: 2},
		{Number: 3, Text: "short"},
	}}
	ocr := &pdfPageOCRStub{}
	registry := NewDefaultParserRegistryWithPDFDependencies(PDFParserDependencies{
		Inspector: inspector,
		Renderer:  inspector,
		OCR:       ocr,
	}, nil)

	result, err := registry.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(ocr.calls) != 2 || ocr.calls[0] != 2 || ocr.calls[1] != 3 {
		t.Fatalf("OCR calls = %#v, want pages 2 and 3", ocr.calls)
	}
	if strings.Index(result.Markdown, "native page one") > strings.Index(result.Markdown, "OCR page 2") {
		t.Fatalf("page order is incorrect: %q", result.Markdown)
	}
	if result.Metadata["ocr_pages"] != "2,3" || result.Metadata["parser_mode"] != "mixed" || result.Metadata["text_source"] != "mixed" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestPDFParserKeepsNativePagesInOrder(t *testing.T) {
	registry := NewDefaultParserRegistryWithPDFDependencies(PDFParserDependencies{
		Inspector: pdfPagePipelineStub{pages: []PDFPage{
			{Number: 1, Text: strings.Repeat("first page ", 20)},
			{Number: 2, Text: strings.Repeat("second page ", 20)},
		}},
	}, nil)

	result, err := registry.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if strings.Index(result.Markdown, "first page") > strings.Index(result.Markdown, "second page") {
		t.Fatalf("native page order is incorrect: %q", result.Markdown)
	}
	if result.Metadata["parser_mode"] != "native" || result.Metadata["text_source"] != "embedded_text" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestPDFParserMergesLayoutTextTableAndFigure(t *testing.T) {
	registry := NewDefaultParserRegistryWithPDFDependencies(PDFParserDependencies{
		Inspector: pdfPagePipelineStub{pages: []PDFPage{{Number: 5}}},
		Renderer:  pdfPagePipelineStub{},
		Layout: pdfLayoutStub{blocks: map[int][]PDFPageBlock{5: {
			{Page: 5, Kind: PDFBlockText, Order: 0, Text: "页面标题", Source: "paddleocr_vl"},
			{Page: 5, Kind: PDFBlockTable, Order: 1, Text: "| Name | Value |", Source: "paddleocr_vl"},
			{Page: 5, Kind: PDFBlockFigure, Order: 2, Image: []byte("figure"), MIMEType: "image/png", Source: "paddleocr_vl"},
		}}},
	}, nil)

	result, err := registry.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "页面标题") || !strings.Contains(result.Markdown, "| Name | Value |") || strings.Contains(result.Markdown, "figure") {
		t.Fatalf("merged Markdown = %q", result.Markdown)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "figure" || result.Images[0].Page != 5 {
		t.Fatalf("images = %#v", result.Images)
	}
	if result.Metadata["parser_mode"] != "layout" || result.Metadata["layout_mode"] != "paddleocr_vl" || result.Metadata["figure_count"] != "1" {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestPDFParserFallsBackToWholePageOCRWhenLayoutFails(t *testing.T) {
	ocr := &pdfPageOCRStub{}
	registry := NewDefaultParserRegistryWithPDFDependencies(PDFParserDependencies{
		Inspector: pdfPagePipelineStub{pages: []PDFPage{{Number: 1}}},
		Renderer:  pdfPagePipelineStub{},
		Layout:    pdfLayoutStub{err: errors.New("layout unavailable")},
		OCR:       ocr,
	}, nil)

	result, err := registry.Parse(context.Background(), ParseRequest{Content: []byte("pdf"), ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "OCR page 1") || result.Metadata["layout_mode"] != "fallback" || result.Metadata["layout_failed_pages"] != "1" {
		t.Fatalf("result=%#v metadata=%#v", result, result.Metadata)
	}
}
