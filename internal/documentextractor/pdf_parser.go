package documentextractor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const defaultPDFMinTextRunes = 100

type PDFParserDependencies struct {
	ScannedPDF     ScannedPDFProcessor
	Inspector      PDFPageInspector
	Renderer       PDFPageRenderer
	OCR            PDFPageOCR
	EmbeddedImages PDFEmbeddedImageExtractor
	Layout         PDFLayoutAnalyzer
}

type pdfParserEngine struct {
	scannedPDF     ScannedPDFProcessor
	inspector      PDFPageInspector
	renderer       PDFPageRenderer
	ocr            PDFPageOCR
	embeddedImages PDFEmbeddedImageExtractor
	layout         PDFLayoutAnalyzer
}

func newPDFParserEngine(deps PDFParserDependencies) *pdfParserEngine {
	if deps.Inspector == nil {
		if inspector, ok := deps.ScannedPDF.(PDFPageInspector); ok {
			deps.Inspector = inspector
		}
	}
	if deps.Renderer == nil {
		if renderer, ok := deps.ScannedPDF.(PDFPageRenderer); ok {
			deps.Renderer = renderer
		}
	}
	if deps.OCR == nil {
		if ocr, ok := deps.ScannedPDF.(PDFPageOCR); ok {
			deps.OCR = ocr
		}
	}
	return &pdfParserEngine{
		scannedPDF:     deps.ScannedPDF,
		inspector:      deps.Inspector,
		renderer:       deps.Renderer,
		ocr:            deps.OCR,
		embeddedImages: deps.EmbeddedImages,
		layout:         deps.Layout,
	}
}

func (*pdfParserEngine) Name() string { return "pdf" }

func (*pdfParserEngine) Description() string { return "PDF native, scanned-page and layout parser" }

func (*pdfParserEngine) Available() (bool, string) { return true, "" }

func (*pdfParserEngine) Supports(contentType string) bool { return contentType == "application/pdf" }

func (e *pdfParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if forceScanned(request.EngineOptions) {
		return e.parseForcedOCR(ctx, request)
	}
	if e.inspector == nil {
		return e.parseLegacy(ctx, request)
	}
	result, err := e.parsePageAware(ctx, request)
	if err == nil {
		return result, nil
	}
	// Page-aware dependencies are optional. A provider outage should not make
	// a PDF with a usable native text layer unreadable.
	legacy, legacyErr := e.parseLegacy(ctx, request)
	if legacyErr == nil {
		return legacy, nil
	}
	return ParseResult{}, fmt.Errorf("page-aware PDF parse: %v; legacy parse: %w", err, legacyErr)
}

func (e *pdfParserEngine) parseForcedOCR(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if e.scannedPDF == nil {
		return ParseResult{}, ErrEmptyText
	}
	text, err := e.scannedPDF.Extract(ctx, request.Content)
	if err != nil {
		return ParseResult{}, fmt.Errorf("forced OCR scanned PDF: %w", err)
	}
	return ParseResult{Markdown: text, Metadata: map[string]string{
		"parser_mode":       "ocr",
		"text_source":       "ocr",
		"image_source_type": "scanned_pdf",
		"ocr_forced":        "true",
	}}, nil
}

func (e *pdfParserEngine) parseLegacy(ctx context.Context, request ParseRequest) (ParseResult, error) {
	text, err := extractPDFText(ctx, request.Content)
	if err == nil && strings.TrimSpace(text) == "" {
		err = ErrEmptyText
	}
	if errors.Is(err, ErrEmptyText) && e.scannedPDF != nil {
		text, err = e.scannedPDF.Extract(ctx, request.Content)
		if err != nil {
			return ParseResult{}, fmt.Errorf("OCR scanned PDF: %w", err)
		}
		return ParseResult{Markdown: text, Metadata: map[string]string{
			"parser_mode":       "ocr",
			"text_source":       "ocr",
			"image_source_type": "scanned_pdf",
		}}, nil
	}
	if err != nil {
		return ParseResult{}, err
	}
	return ParseResult{Markdown: text, Metadata: map[string]string{
		"parser_mode": "native_pdf",
		"text_source": "embedded_text",
	}}, nil
}

func (e *pdfParserEngine) parsePageAware(ctx context.Context, request ParseRequest) (ParseResult, error) {
	pages, err := e.inspector.InspectPages(ctx, request.Content)
	if err != nil {
		return ParseResult{}, err
	}
	if len(pages) == 0 {
		return ParseResult{}, ErrEmptyText
	}

	parts := make([]string, 0, len(pages))
	ocrPages := make([]int, 0)
	layoutFailedPages := make([]int, 0)
	allImages := make([]ImageAsset, 0)
	candidates := make([]int, 0)
	states := make(map[int]PDFPageState, len(pages))
	for index := range pages {
		page := &pages[index]
		embedded := []ImageAsset(nil)
		if e.embeddedImages != nil {
			embedded, err = e.embeddedImages.ExtractPageImages(ctx, request.Content, page.Number)
			if err != nil {
				embedded = nil
			}
			allImages = append(allImages, embedded...)
		}
		state := ClassifyPDFPage(page.Number, page.Text, len(embedded) > 0, defaultPDFMinTextRunes)
		states[page.Number] = state
		if state.NeedsOCR {
			candidates = append(candidates, page.Number)
			continue
		}
		appendPDFPageText(&parts, page.Number, page.Text)
	}

	if len(candidates) == 0 {
		return buildPDFParseResult(parts, allImages, len(pages), nil, nil, "none", states), nil
	}

	var rendered []PDFPage
	var renderErr error
	if e.renderer == nil {
		renderErr = errors.New("PDF page renderer is not configured")
	} else {
		rendered, renderErr = e.renderer.RenderPDFPages(ctx, request.Content, candidates)
	}
	renderedByPage := make(map[int]PDFPage, len(rendered))
	if renderErr == nil {
		for _, page := range rendered {
			renderedByPage[page.Number] = page
		}
	}
	layoutUsed := false
	ocrUsed := false
	layoutMode := "none"
	for _, pageNumber := range candidates {
		ocrPages = append(ocrPages, pageNumber)
		page := renderedByPage[pageNumber]
		page.Number = pageNumber
		if renderErr != nil || len(page.Image) == 0 {
			layoutFailedPages = append(layoutFailedPages, pageNumber)
			layoutMode = "fallback"
			if text, ocrErr := e.ocrPage(ctx, page, pages, pageNumber); ocrErr == nil {
				appendPDFPageText(&parts, pageNumber, text)
				ocrUsed = true
			}
			continue
		}

		if e.layout != nil {
			blocks, layoutErr := e.layout.AnalyzePage(ctx, page)
			if layoutErr == nil && len(blocks) > 0 {
				if source := firstBlockSource(blocks); source != "" {
					layoutMode = source
				} else {
					layoutMode = "layout"
				}
				appendPDFPageBlocks(&parts, &allImages, pageNumber, blocks)
				layoutUsed = true
				continue
			}
			layoutFailedPages = append(layoutFailedPages, pageNumber)
			layoutMode = "fallback"
		}
		if text, ocrErr := e.ocrPage(ctx, page, pages, pageNumber); ocrErr == nil {
			appendPDFPageText(&parts, pageNumber, text)
			ocrUsed = true
		}
	}

	mode := "native"
	textSource := "embedded_text"
	if layoutUsed || ocrUsed {
		mode = "mixed"
		textSource = "mixed"
		if len(candidates) == len(pages) {
			if layoutUsed && !ocrUsed {
				mode = "layout"
			} else if ocrUsed && !layoutUsed {
				mode = "ocr"
			}
			if layoutUsed && !ocrUsed {
				textSource = "ocr"
			} else if ocrUsed && !layoutUsed {
				textSource = "ocr"
			}
		}
	}
	return buildPDFParseResult(parts, allImages, len(pages), ocrPages, layoutFailedPages, layoutMode, states, mode, textSource), nil
}

func (e *pdfParserEngine) ocrPage(ctx context.Context, page PDFPage, pages []PDFPage, pageNumber int) (string, error) {
	if e.ocr != nil {
		return e.ocr.OCRPage(ctx, page)
	}
	for _, candidate := range pages {
		if candidate.Number == pageNumber && strings.TrimSpace(candidate.Text) != "" {
			return candidate.Text, nil
		}
	}
	return "", ErrEmptyText
}

func appendPDFPageText(parts *[]string, pageNumber int, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*parts = append(*parts, fmt.Sprintf("[Page %d]\n%s", pageNumber, text))
}

func appendPDFPageBlocks(parts *[]string, images *[]ImageAsset, pageNumber int, blocks []PDFPageBlock) {
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].Order < blocks[j].Order })
	textParts := make([]string, 0, len(blocks))
	figureIndex := 0
	for _, block := range blocks {
		switch block.Kind {
		case PDFBlockFigure:
			if len(block.Image) == 0 {
				continue
			}
			figureIndex++
			filename := fmt.Sprintf("page-%d-figure-%d%s", pageNumber, figureIndex, imageExtension(block.MIMEType))
			*images = append(*images, ImageAsset{Filename: filename, MIMEType: block.MIMEType, Data: block.Image, Page: pageNumber, Source: block.Source})
		default:
			if text := strings.TrimSpace(block.Text); text != "" {
				textParts = append(textParts, text)
			}
		}
	}
	if len(textParts) > 0 {
		appendPDFPageText(parts, pageNumber, strings.Join(textParts, "\n\n"))
	}
}

func buildPDFParseResult(parts []string, images []ImageAsset, pageCount int, ocrPages, layoutFailedPages []int, layoutMode string, states map[int]PDFPageState, modeAndSource ...string) ParseResult {
	mode := "native"
	textSource := "embedded_text"
	if len(modeAndSource) >= 2 {
		mode, textSource = modeAndSource[0], modeAndSource[1]
	}
	metadata := map[string]string{
		"parser_mode":  mode,
		"text_source":  textSource,
		"layout_mode":  layoutMode,
		"page_count":   strconv.Itoa(pageCount),
		"ocr_pages":    joinPageNumbers(ocrPages),
		"figure_count": strconv.Itoa(len(images)),
	}
	if len(layoutFailedPages) > 0 {
		metadata["layout_failed_pages"] = joinPageNumbers(layoutFailedPages)
	}
	if len(parts) == 0 {
		for _, state := range states {
			if strings.TrimSpace(state.NativeText) != "" {
				appendPDFPageText(&parts, state.Number, state.NativeText)
			}
		}
	}
	return ParseResult{Markdown: strings.TrimSpace(strings.Join(parts, "\n\n")), Images: images, Metadata: metadata}
}

func joinPageNumbers(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	values := append([]int(nil), numbers...)
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	last := 0
	for _, value := range values {
		if value <= 0 || value == last {
			continue
		}
		parts = append(parts, strconv.Itoa(value))
		last = value
	}
	return strings.Join(parts, ",")
}

func firstBlockSource(blocks []PDFPageBlock) string {
	for _, block := range blocks {
		if source := strings.TrimSpace(block.Source); source != "" {
			return source
		}
	}
	return ""
}

func imageExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
