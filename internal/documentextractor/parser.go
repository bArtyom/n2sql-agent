package documentextractor

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ParseRequest is the parser-independent input passed to one document engine.
type ParseRequest struct {
	Content     []byte
	ContentType string
	Filename    string
}

// ImageAsset describes an image discovered during parsing. Data is kept in
// memory only for the current processing task; persistence is owned by the
// document processing layer.
type ImageAsset struct {
	Filename string
	MIMEType string
	Data     []byte
	Page     int
	Source   string
	Original bool
}

// ParseResult is the unified boundary between document parsing and indexing.
// Markdown is sent to the existing chunking pipeline, while Images and
// Metadata are available to later asset/reference stages.
type ParseResult struct {
	Markdown string
	Images   []ImageAsset
	Metadata map[string]string
}

type ParserEngine interface {
	Name() string
	Supports(string) bool
	Parse(context.Context, ParseRequest) (ParseResult, error)
}

type ParserRegistry struct {
	engines []ParserEngine
}

func NewParserRegistry(engines ...ParserEngine) *ParserRegistry {
	return &ParserRegistry{engines: append([]ParserEngine(nil), engines...)}
}

func (r *ParserRegistry) Select(contentType string) (ParserEngine, error) {
	if r == nil {
		return nil, ErrUnsupportedType
	}
	for _, engine := range r.engines {
		if engine != nil && engine.Supports(contentType) {
			return engine, nil
		}
	}
	return nil, ErrUnsupportedType
}

func (r *ParserRegistry) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	engine, err := r.Select(request.ContentType)
	if err != nil {
		return ParseResult{}, err
	}
	result, err := engine.Parse(ctx, request)
	if err != nil {
		return ParseResult{}, fmt.Errorf("%s parser: %w", engine.Name(), err)
	}
	if strings.TrimSpace(result.Markdown) == "" {
		return ParseResult{}, ErrEmptyText
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["parser"] = engine.Name()
	return result, nil
}

func NewDefaultParserRegistry(scannedPDF ScannedPDFProcessor, image ImageProcessor) *ParserRegistry {
	return NewParserRegistry(
		&simpleParserEngine{},
		&officeParserEngine{},
		&pdfParserEngine{scannedPDF: scannedPDF},
		&imageParserEngine{processor: image},
	)
}

type simpleParserEngine struct{}

func (*simpleParserEngine) Name() string { return "simple" }

func (*simpleParserEngine) Supports(contentType string) bool {
	return contentType == "text/plain" || contentType == "text/markdown" || contentType == "text/html"
}

func (*simpleParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	text := string(request.Content)
	var err error
	if request.ContentType == "text/html" {
		text, err = extractHTMLText(ctx, request.Content)
		if err != nil {
			return ParseResult{}, err
		}
	}
	return ParseResult{Markdown: text}, nil
}

type officeParserEngine struct{}

func (*officeParserEngine) Name() string { return "office" }

func (*officeParserEngine) Supports(contentType string) bool {
	return contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		contentType == "application/vnd.openxmlformats-officedocument.presentationml.presentation" ||
		contentType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
}

func (*officeParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	var (
		text string
		err  error
	)
	switch request.ContentType {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		text, err = extractDOCXText(ctx, request.Content)
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		text, err = extractPPTXText(ctx, request.Content)
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		text, err = extractXLSXText(ctx, request.Content)
	default:
		return ParseResult{}, ErrUnsupportedType
	}
	return ParseResult{Markdown: text}, err
}

type pdfParserEngine struct{ scannedPDF ScannedPDFProcessor }

func (*pdfParserEngine) Name() string { return "pdf" }

func (*pdfParserEngine) Supports(contentType string) bool { return contentType == "application/pdf" }

func (e *pdfParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
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
			"image_source_type": "scanned_pdf",
		}}, nil
	}
	if err != nil {
		return ParseResult{}, err
	}
	return ParseResult{Markdown: text, Metadata: map[string]string{"parser_mode": "text"}}, nil
}

type imageParserEngine struct{ processor ImageProcessor }

func (*imageParserEngine) Name() string { return "image_ocr" }

func (*imageParserEngine) Supports(contentType string) bool {
	return strings.HasPrefix(contentType, "image/")
}

func (e *imageParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if e.processor == nil {
		return ParseResult{}, ErrEmptyText
	}
	text, err := e.processor.ExtractImage(ctx, request.ContentType, request.Content)
	if err != nil {
		return ParseResult{}, err
	}
	return ParseResult{
		Markdown: text,
		Images: []ImageAsset{{
			Filename: request.Filename,
			MIMEType: request.ContentType,
			Data:     append([]byte(nil), request.Content...),
			Source:   "original",
			Original: true,
		}},
		Metadata: map[string]string{"parser_mode": "ocr"},
	}, nil
}
