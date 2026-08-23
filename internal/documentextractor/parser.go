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
	"strconv"
	"strings"
)

const maxEmbeddedImageBytes int64 = 5 << 20

// ParseRequest is the parser-independent input passed to one document engine.
type ParseRequest struct {
	Content       []byte
	ContentType   string
	Filename      string
	EngineName    string
	EngineOptions map[string]string
}

// ParserEngineRule maps one or more file types to a parser engine. File types
// may be extensions ("pdf", ".docx") or MIME types. Rules are resolved in
// order, so the first matching rule wins, matching WeKnora's per-file-type
// parser configuration.
type ParserEngineRule struct {
	FileTypes            []string `json:"file_types"`
	Engine               string   `json:"engine"`
	XLSXFirstRowAsHeader *bool    `json:"xlsx_first_row_as_header,omitempty"`
}

// ProcessConfig contains per-upload parser overrides. It is intentionally
// separate from the knowledge-base defaults so a queued task can keep the
// exact configuration selected when the upload was accepted.
type ProcessConfig struct {
	ParserEngineRules     []ParserEngineRule `json:"parser_engine_rules,omitempty"`
	ChunkingConfig        *ChunkingConfig    `json:"chunking_config,omitempty"`
	ParserEngineOverrides map[string]string  `json:"parser_engine_overrides,omitempty"`
}

// ChunkingConfig mirrors the batch-level fields used by WeKnora while keeping
// the current project’s two-level parent/child index as the execution model.
// ChunkSize and ChunkOverlap apply to the indexed child chunks; explicit
// parent/child sizes override the defaults independently.
type ChunkingConfig struct {
	ChunkSize       int    `json:"chunk_size,omitempty"`
	ChunkOverlap    int    `json:"chunk_overlap,omitempty"`
	ParentChunkSize int    `json:"parent_chunk_size,omitempty"`
	ChildChunkSize  int    `json:"child_chunk_size,omitempty"`
	Strategy        string `json:"strategy,omitempty"`
}

const (
	maxProcessConfigRules     = 32
	maxProcessConfigFileTypes = 32
	maxProcessConfigValueSize = 100
	maxProcessConfigOverrides = 32
	minChunkTarget            = 32
	maxChunkTarget            = 100000
	defaultParentChunkSize    = 3000
	defaultChildChunkSize     = 1000
)

// ValidateProcessConfig validates the small, parser-owned portion of the
// upload process configuration. A nil config means "use knowledge-base
// defaults" and is valid.
func ValidateProcessConfig(config *ProcessConfig) error {
	if config == nil {
		return nil
	}
	if len(config.ParserEngineRules) > maxProcessConfigRules {
		return fmt.Errorf("too many parser engine rules")
	}
	if len(config.ParserEngineOverrides) > maxProcessConfigOverrides {
		return fmt.Errorf("too many parser engine overrides")
	}
	for key, value := range config.ParserEngineOverrides {
		if strings.TrimSpace(key) == "" || len(key) > maxProcessConfigValueSize || len(value) > 512 {
			return fmt.Errorf("invalid parser engine override")
		}
		if key == "pdf_force_scanned" {
			if _, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil {
				return fmt.Errorf("pdf_force_scanned must be boolean")
			}
		}
	}
	for _, rule := range config.ParserEngineRules {
		if strings.TrimSpace(rule.Engine) == "" {
			return fmt.Errorf("parser engine is required")
		}
		if len(rule.FileTypes) == 0 || len(rule.FileTypes) > maxProcessConfigFileTypes {
			return fmt.Errorf("invalid parser engine file types")
		}
		for _, fileType := range rule.FileTypes {
			if value := strings.TrimSpace(fileType); value == "" || len(value) > maxProcessConfigValueSize {
				return fmt.Errorf("invalid parser engine file type")
			}
		}
	}
	if err := validateChunkingConfig(config.ChunkingConfig); err != nil {
		return err
	}
	return nil
}

func validateChunkingConfig(config *ChunkingConfig) error {
	if config == nil {
		return nil
	}
	if config.ChunkSize != 0 && !validChunkTarget(config.ChunkSize) {
		return fmt.Errorf("invalid chunk size")
	}
	if config.ParentChunkSize != 0 && !validChunkTarget(config.ParentChunkSize) {
		return fmt.Errorf("invalid parent chunk size")
	}
	if config.ChildChunkSize != 0 && !validChunkTarget(config.ChildChunkSize) {
		return fmt.Errorf("invalid child chunk size")
	}
	if config.ChunkOverlap < 0 || (config.ChunkSize == 0 && config.ChunkOverlap != 0) {
		return fmt.Errorf("invalid chunk overlap")
	}
	if config.ChunkSize != 0 && config.ChunkOverlap >= config.ChunkSize {
		return fmt.Errorf("chunk overlap must be smaller than chunk size")
	}
	strategy := strings.ToLower(strings.TrimSpace(config.Strategy))
	switch strategy {
	case "", "auto", "heading", "heuristic", "recursive":
	default:
		return fmt.Errorf("invalid chunking strategy")
	}
	parentSize := config.ParentChunkSize
	if parentSize == 0 {
		parentSize = defaultParentChunkSize
	}
	childSize := config.ChildChunkSize
	if childSize == 0 {
		childSize = config.ChunkSize
	}
	if childSize == 0 {
		childSize = defaultChildChunkSize
	}
	if parentSize < childSize {
		return fmt.Errorf("parent chunk size must be at least child chunk size")
	}
	return nil
}

func validChunkTarget(value int) bool {
	return value >= minChunkTarget && value <= maxChunkTarget
}

func ResolveParserEngine(rules []ParserEngineRule, contentType, filename string) string {
	rule := ResolveParserEngineRule(rules, contentType, filename)
	if rule == nil {
		return ""
	}
	return rule.Engine
}

func ResolveParserEngineRule(rules []ParserEngineRule, contentType, filename string) *ParserEngineRule {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(filename), "."))
	for _, rule := range rules {
		for _, fileType := range rule.FileTypes {
			fileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(fileType), "."))
			if fileType == "" {
				continue
			}
			if fileType == contentType || fileType == extension {
				matched := rule
				matched.Engine = strings.TrimSpace(matched.Engine)
				return &matched
			}
		}
	}
	return nil
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
	// OCRText and Caption are derived, bounded semantic representations. The
	// original image bytes remain owned by the asset store and are never put in
	// a chunk row.
	OCRText string
	Caption string
}

// ImageEnrichment contains optional VLM-derived representations for one
// image. OCRText is usually produced by the parser; Caption is optional and
// can be supplied by a separate image-description model.
type ImageEnrichment struct {
	OCRText string
	Caption string
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

type ParserEngineInfo struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	ContentTypes      []string `json:"content_types"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

type parserEngineDescriber interface {
	Description() string
}

type parserEngineAvailability interface {
	Available() (bool, string)
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

func (r *ParserRegistry) ListEngineInfos() []ParserEngineInfo {
	if r == nil {
		return nil
	}
	contentTypes := []string{"text/plain", "text/markdown", "text/html", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "image/png", "image/jpeg", "image/webp"}
	infos := make([]ParserEngineInfo, 0, len(r.engines))
	for _, engine := range r.engines {
		if engine == nil {
			continue
		}
		info := ParserEngineInfo{Name: engine.Name(), Description: engine.Name(), Available: true}
		if describer, ok := engine.(parserEngineDescriber); ok && strings.TrimSpace(describer.Description()) != "" {
			info.Description = describer.Description()
		}
		for _, contentType := range contentTypes {
			if engine.Supports(contentType) {
				info.ContentTypes = append(info.ContentTypes, contentType)
			}
		}
		if availability, ok := engine.(parserEngineAvailability); ok {
			info.Available, info.UnavailableReason = availability.Available()
		}
		infos = append(infos, info)
	}
	return infos
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

func (*simpleParserEngine) Description() string { return "Simple text and HTML parser" }

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

func (*officeParserEngine) Description() string { return "DOCX/PPTX/XLSX parser" }

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
		text, err = extractXLSXText(ctx, request.Content, optionEnabled(request.EngineOptions, "xlsx_first_row_as_header"))
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
	metadata := map[string]string{}
	if request.ContentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		metadata["parser_mode"] = "native_docx"
		metadata["text_source"] = "embedded_text"
	}
	return ParseResult{Markdown: text, Images: images, Metadata: metadata}, nil
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

func (*pdfParserEngine) Description() string { return "PDF text and scanned-page parser" }

func (*pdfParserEngine) Available() (bool, string) { return true, "" }

func (*pdfParserEngine) Supports(contentType string) bool { return contentType == "application/pdf" }

func (e *pdfParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if forceScanned(request.EngineOptions) {
		if e.scannedPDF == nil {
			return ParseResult{}, ErrEmptyText
		}
		text, err := e.scannedPDF.Extract(ctx, request.Content)
		if err != nil {
			return ParseResult{}, fmt.Errorf("forced OCR scanned PDF: %w", err)
		}
		return ParseResult{Markdown: text, Metadata: map[string]string{
			"parser_mode":       "ocr",
			"image_source_type": "scanned_pdf",
			"ocr_forced":        "true",
		}}, nil
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
	return ParseResult{Markdown: text, Metadata: map[string]string{
		"parser_mode": "native_pdf",
		"text_source": "embedded_text",
	}}, nil
}

func forceScanned(options map[string]string) bool {
	return optionEnabled(options, "pdf_force_scanned")
}

func optionEnabled(options map[string]string, key string) bool {
	if options == nil {
		return false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(options[key]))
	return err == nil && value
}

type imageParserEngine struct{ processor ImageProcessor }

func (*imageParserEngine) Name() string { return "image_ocr" }

func (e *imageParserEngine) Description() string { return "Image OCR parser" }

func (e *imageParserEngine) Available() (bool, string) {
	if e == nil || e.processor == nil {
		return false, "OCR image processor is not configured"
	}
	return true, ""
}

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
			OCRText:  text,
		}},
		Metadata: map[string]string{"parser_mode": "ocr"},
	}, nil
}
