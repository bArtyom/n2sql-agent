package documentextractor

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const maxEmbeddedImageBytes int64 = 5 << 20

// ParseRequest is the parser-independent input passed to one document engine.
type ParseRequest struct {
	Content     []byte
	ContentType string
	Filename    string
	EngineName  string
}

// ParserEngineRule maps one or more file types to a parser engine. File types
// may be extensions ("pdf", ".docx") or MIME types. Rules are resolved in
// order, so the first matching rule wins, matching WeKnora's per-file-type
// parser configuration.
type ParserEngineRule struct {
	FileTypes []string `json:"file_types"`
	Engine    string   `json:"engine"`
}

func ResolveParserEngine(rules []ParserEngineRule, contentType, filename string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	for _, rule := range rules {
		for _, fileType := range rule.FileTypes {
			fileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(fileType), "."))
			if fileType == "" {
				continue
			}
			if fileType == contentType || fileType == extension {
				return strings.TrimSpace(rule.Engine)
			}
		}
	}
	return ""
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
	return r.SelectNamed("", contentType)
}

// SelectNamed selects a parser by name when requested. An empty name keeps
// the default MIME-based selection behavior.
func (r *ParserRegistry) SelectNamed(name, contentType string) (ParserEngine, error) {
	if r == nil {
		return nil, ErrUnsupportedType
	}
	for _, engine := range r.engines {
		if engine == nil {
			continue
		}
		if strings.TrimSpace(name) != "" && !strings.EqualFold(engine.Name(), name) {
			continue
		}
		if engine.Supports(contentType) {
			return engine, nil
		}
	}
	return nil, ErrUnsupportedType
}

func (r *ParserRegistry) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	engine, err := r.SelectNamed(request.EngineName, request.ContentType)
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
	return NewDefaultParserRegistryWithExtras(scannedPDF, image)
}

func NewDefaultParserRegistryWithExtras(scannedPDF ScannedPDFProcessor, image ImageProcessor, extras ...ParserEngine) *ParserRegistry {
	engines := []ParserEngine{
		&simpleParserEngine{},
		&officeParserEngine{},
		&pdfParserEngine{scannedPDF: scannedPDF},
		&imageParserEngine{processor: image},
	}
	engines = append(engines, extras...)
	return NewParserRegistry(
		engines...,
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
	if err != nil {
		return ParseResult{}, err
	}
	prefix := "word/media/"
	if request.ContentType == "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		prefix = "ppt/media/"
	}
	images, imageErr := extractArchiveImages(ctx, request.Content, prefix)
	if imageErr != nil {
		return ParseResult{}, imageErr
	}
	return ParseResult{Markdown: text, Images: images}, nil
}

func extractArchiveImages(ctx context.Context, content []byte, prefix string) ([]ImageAsset, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, fmt.Errorf("open office archive for images: %w", err)
	}
	files := make([]*zip.File, 0)
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, prefix) && !strings.HasSuffix(file.Name, "/") {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	assets := make([]ImageAsset, 0, len(files))
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.UncompressedSize64 > uint64(maxEmbeddedImageBytes) {
			return nil, fmt.Errorf("embedded image %s is too large", file.Name)
		}
		reader, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open embedded image %s: %w", file.Name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, maxEmbeddedImageBytes+1))
		_ = reader.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read embedded image %s: %w", file.Name, readErr)
		}
		if int64(len(data)) > maxEmbeddedImageBytes {
			return nil, fmt.Errorf("embedded image %s is too large", file.Name)
		}
		filename := filepath.Base(file.Name)
		mimeType := imageMIMEType(filename)
		if mimeType == "" {
			continue
		}
		assets = append(assets, ImageAsset{
			Filename: filename,
			MIMEType: mimeType,
			Data:     data,
			Source:   "embedded",
			Page:     index + 1,
		})
	}
	return assets, nil
}

func imageMIMEType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

type pdfParserEngine struct{ scannedPDF ScannedPDFProcessor }

func (*pdfParserEngine) Name() string { return "pdf" }

func (*pdfParserEngine) Supports(contentType string) bool { return contentType == "application/pdf" }

func (e *pdfParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if pageProcessor, ok := e.scannedPDF.(PageAwareScannedPDFProcessor); ok {
		pages, pageErr := pageProcessor.ExtractPages(ctx, request.Content)
		if pageErr != nil {
			return ParseResult{}, pageErr
		}
		blocks := make([]string, 0, len(pages))
		images := make([]ImageAsset, 0)
		for _, page := range pages {
			if strings.TrimSpace(page.Text) != "" {
				blocks = append(blocks, fmt.Sprintf("[Page %d]\n%s", page.Number, strings.TrimSpace(page.Text)))
			}
			if page.OCR && len(page.Image) > 0 {
				images = append(images, ImageAsset{Filename: fmt.Sprintf("page-%d.jpg", page.Number), MIMEType: "image/jpeg", Data: append([]byte(nil), page.Image...), Page: page.Number, Source: "scanned_pdf"})
			}
		}
		if len(blocks) > 0 {
			return ParseResult{Markdown: strings.Join(blocks, "\n\n"), Images: images, Metadata: map[string]string{"parser_mode": "mixed_pdf", "image_source_type": "scanned_pdf"}}, nil
		}
	}
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
