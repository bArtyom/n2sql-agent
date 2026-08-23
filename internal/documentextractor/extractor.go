package documentextractor

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"
)

const maxExtractedTextBytes int64 = 10 << 20

var (
	ErrInvalidStoragePath = errors.New("invalid document storage path")
	ErrUnsupportedType    = errors.New("unsupported document content type")
	ErrInvalidPDF         = errors.New("invalid PDF document")
	ErrEmptyText          = errors.New("document contains no text")
)

type ScannedPDFProcessor interface {
	Extract(context.Context, []byte) (string, error)
}

type ImageProcessor interface {
	ExtractImage(context.Context, string, []byte) (string, error)
}

type Extractor struct {
	root     string
	registry *ParserRegistry
}

func New(root string) *Extractor {
	return &Extractor{root: root, registry: NewDefaultParserRegistry(nil, nil)}
}

func NewWithOCR(root string, scannedPDF ScannedPDFProcessor) *Extractor {
	return &Extractor{root: root, registry: NewDefaultParserRegistry(scannedPDF, nil)}
}

func NewWithOCRAndImages(root string, scannedPDF ScannedPDFProcessor, image ImageProcessor) *Extractor {
	return &Extractor{root: root, registry: NewDefaultParserRegistry(scannedPDF, image)}
}

func NewWithParserRegistry(root string, registry *ParserRegistry) *Extractor {
	return &Extractor{root: root, registry: registry}
}

func (e *Extractor) Extract(ctx context.Context, storagePath, contentType string) (string, error) {
	result, err := e.ExtractResult(ctx, storagePath, contentType)
	if err != nil {
		return "", err
	}
	return result.Markdown, nil
}

func (e *Extractor) ExtractResult(ctx context.Context, storagePath, contentType string) (ParseResult, error) {
	if err := ctx.Err(); err != nil {
		return ParseResult{}, err
	}
	if !supportedContentType(contentType) || e == nil || e.registry == nil {
		return ParseResult{}, ErrUnsupportedType
	}
	normalizedPath := filepath.FromSlash(storagePath)
	if filepath.IsAbs(normalizedPath) || filepath.Clean(normalizedPath) != normalizedPath || filepath.Dir(normalizedPath) != "documents" {
		return ParseResult{}, ErrInvalidStoragePath
	}
	file, err := os.Open(filepath.Join(e.root, normalizedPath))
	if err != nil {
		return ParseResult{}, fmt.Errorf("open document: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxExtractedTextBytes+1))
	if err != nil {
		return ParseResult{}, err
	}
	if int64(len(content)) > maxExtractedTextBytes {
		return ParseResult{}, fmt.Errorf("extracted text is too large")
	}
	return e.registry.Parse(ctx, ParseRequest{Content: content, ContentType: contentType, Filename: filepath.Base(normalizedPath)})
}

func supportedContentType(contentType string) bool {
	switch contentType {
	case "text/plain", "text/markdown", "text/html", "application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func extractPPTXText(ctx context.Context, content []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("invalid PPTX archive: %w", err)
	}
	slides := make([]*zip.File, 0)
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "ppt/slides/slide") && strings.HasSuffix(file.Name, ".xml") {
			slides = append(slides, file)
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].Name < slides[j].Name })
	if len(slides) == 0 {
		return "", errors.New("PPTX contains no slides")
	}
	sections := make([]string, 0, len(slides))
	for index, file := range slides {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		reader, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open PPTX slide: %w", err)
		}
		slide, parseErr := parsePPTXSlide(ctx, io.LimitReader(reader, maxExtractedTextBytes+1))
		_ = reader.Close()
		if parseErr != nil {
			return "", fmt.Errorf("parse PPTX slide %d: %w", index+1, parseErr)
		}
		if strings.TrimSpace(slide) != "" {
			sections = append(sections, fmt.Sprintf("# Slide %d\n%s", index+1, slide))
		}
	}
	if len(sections) == 0 {
		return "", ErrEmptyText
	}
	return joinExtractedText(sections, "PPTX")
}

func parsePPTXSlide(ctx context.Context, source io.Reader) (string, error) {
	decoder := xml.NewDecoder(source)
	paragraphs := make([]string, 0)
	var current strings.Builder
	inParagraph := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local == "p" {
				inParagraph = true
				current.Reset()
			} else if element.Name.Local == "t" && inParagraph {
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return "", err
				}
				current.WriteString(value)
			}
		case xml.EndElement:
			if element.Name.Local == "p" && inParagraph {
				if value := strings.TrimSpace(current.String()); value != "" {
					paragraphs = append(paragraphs, value)
				}
				inParagraph = false
			}
		}
	}
	return strings.Join(paragraphs, "\n"), nil
}

func extractXLSXText(ctx context.Context, content []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("invalid XLSX archive: %w", err)
	}
	sharedStrings := []string{}
	for _, file := range archive.File {
		if file.Name == "xl/sharedStrings.xml" {
			reader, openErr := file.Open()
			if openErr != nil {
				return "", fmt.Errorf("open XLSX shared strings: %w", openErr)
			}
			sharedStrings, err = parseXLSXSharedStrings(ctx, io.LimitReader(reader, maxExtractedTextBytes+1))
			_ = reader.Close()
			if err != nil {
				return "", fmt.Errorf("parse XLSX shared strings: %w", err)
			}
			break
		}
	}
	sheets := make([]*zip.File, 0)
	for _, file := range archive.File {
		if strings.HasPrefix(file.Name, "xl/worksheets/sheet") && strings.HasSuffix(file.Name, ".xml") {
			sheets = append(sheets, file)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].Name < sheets[j].Name })
	if len(sheets) == 0 {
		return "", errors.New("XLSX contains no worksheets")
	}
	sections := make([]string, 0, len(sheets))
	for index, file := range sheets {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		reader, err := file.Open()
		if err != nil {
			return "", fmt.Errorf("open XLSX worksheet: %w", err)
		}
		rows, parseErr := parseXLSXSheet(ctx, io.LimitReader(reader, maxExtractedTextBytes+1), sharedStrings)
		_ = reader.Close()
		if parseErr != nil {
			return "", fmt.Errorf("parse XLSX worksheet %d: %w", index+1, parseErr)
		}
		if len(rows) > 0 {
			sections = append(sections, fmt.Sprintf("# Sheet %d\n%s", index+1, strings.Join(rows, "\n")))
		}
	}
	if len(sections) == 0 {
		return "", ErrEmptyText
	}
	return joinExtractedText(sections, "XLSX")
}

func parseXLSXSharedStrings(ctx context.Context, source io.Reader) ([]string, error) {
	decoder := xml.NewDecoder(source)
	values := make([]string, 0)
	var current strings.Builder
	inItem := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "t":
				if inItem {
					var value string
					if err := decoder.DecodeElement(&value, &element); err != nil {
						return nil, err
					}
					current.WriteString(value)
				}
			}
		case xml.EndElement:
			if element.Name.Local == "si" && inItem {
				values = append(values, current.String())
				inItem = false
			}
		}
	}
	return values, nil
}

func parseXLSXSheet(ctx context.Context, source io.Reader, sharedStrings []string) ([]string, error) {
	decoder := xml.NewDecoder(source)
	rows := make([]string, 0)
	var cells []string
	var cellValue strings.Builder
	cellType := ""
	inRow := false
	inCell := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "row":
				inRow = true
				cells = nil
			case "c":
				if inRow {
					inCell = true
					cellValue.Reset()
					cellType = ""
					for _, attr := range element.Attr {
						if attr.Name.Local == "t" {
							cellType = attr.Value
						}
					}
				}
			case "v", "t":
				if inCell {
					var value string
					if err := decoder.DecodeElement(&value, &element); err != nil {
						return nil, err
					}
					cellValue.WriteString(value)
				}
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "c":
				if inCell {
					value := cellValue.String()
					if cellType == "s" {
						index, parseErr := strconv.Atoi(value)
						if parseErr != nil || index < 0 || index >= len(sharedStrings) {
							return nil, errors.New("XLSX shared string index is invalid")
						}
						value = sharedStrings[index]
					}
					cells = append(cells, strings.TrimSpace(value))
					inCell = false
				}
			case "row":
				if inRow {
					for len(cells) > 0 && cells[len(cells)-1] == "" {
						cells = cells[:len(cells)-1]
					}
					if len(cells) > 0 {
						rows = append(rows, strings.Join(cells, "\t"))
					}
					inRow = false
				}
			}
		}
	}
	return rows, nil
}

func joinExtractedText(sections []string, format string) (string, error) {
	text := strings.TrimSpace(strings.Join(sections, "\n"))
	if int64(len(text)) > maxExtractedTextBytes {
		return "", fmt.Errorf("%s text is too large", format)
	}
	return text, nil
}

func extractHTMLText(ctx context.Context, content []byte) (string, error) {
	root, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}
	var lines []string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node == nil || ctx.Err() != nil {
			return
		}
		if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style" || node.Data == "head") {
			return
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "table":
				if table := renderHTMLTable(node); table != "" {
					lines = append(lines, table)
				}
				return
			case "h1", "h2", "h3", "h4", "h5", "h6", "p", "li", "blockquote":
				value := strings.TrimSpace(strings.Join(strings.Fields(htmlText(node)), " "))
				if value != "" {
					if node.Data[0] == 'h' {
						value = strings.Repeat("#", int(node.Data[1]-'0')) + " " + value
					}
					lines = append(lines, value)
				}
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return "", ErrEmptyText
	}
	if int64(len(text)) > maxExtractedTextBytes {
		return "", errors.New("HTML text is too large")
	}
	return text, nil
}

func renderHTMLTable(table *html.Node) string {
	var rows [][]string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "tr" {
			var row []string
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.ElementNode || (child.Data != "th" && child.Data != "td") {
					continue
				}
				row = append(row, normalizeTableCell(htmlText(child)))
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(table)
	return renderMarkdownTable(rows)
}

func htmlText(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}
	if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style" || node.Data == "head") {
		return ""
	}
	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(htmlText(child))
	}
	return builder.String()
}

func extractDOCXText(ctx context.Context, content []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return "", fmt.Errorf("invalid DOCX archive: %w", err)
	}
	var document *zip.File
	for _, file := range archive.File {
		if file.Name == "word/document.xml" {
			document = file
			break
		}
	}
	if document == nil {
		return "", errors.New("DOCX document.xml is missing")
	}
	if document.UncompressedSize64 > uint64(maxExtractedTextBytes) {
		return "", errors.New("DOCX document is too large")
	}
	reader, err := document.Open()
	if err != nil {
		return "", fmt.Errorf("open DOCX document.xml: %w", err)
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, maxExtractedTextBytes+1))
	var paragraphs []string
	var current strings.Builder
	paragraphDepth := 0
	heading := ""
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			break
		}
		if tokenErr != nil {
			return "", fmt.Errorf("parse DOCX document.xml: %w", tokenErr)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "tbl":
				var table docxTable
				if err := decoder.DecodeElement(&table, &element); err != nil {
					return "", fmt.Errorf("read DOCX table: %w", err)
				}
				if markdown := renderDOCXTable(table); markdown != "" {
					paragraphs = append(paragraphs, markdown)
				}
			case "p":
				paragraphDepth++
				if paragraphDepth == 1 {
					current.Reset()
					heading = ""
				}
			case "pStyle":
				if paragraphDepth == 1 {
					for _, attr := range element.Attr {
						if attr.Name.Local == "val" {
							heading = headingPrefix(attr.Value)
							break
						}
					}
				}
			case "t":
				var text string
				if err := decoder.DecodeElement(&text, &element); err != nil {
					return "", fmt.Errorf("read DOCX text: %w", err)
				}
				current.WriteString(text)
			}
		case xml.EndElement:
			if element.Name.Local == "p" && paragraphDepth == 1 {
				paragraph := strings.TrimSpace(current.String())
				if paragraph != "" {
					paragraphs = append(paragraphs, heading+paragraph)
				}
				paragraphDepth = 0
			}
		}
	}
	text := strings.TrimSpace(strings.Join(paragraphs, "\n"))
	if text == "" {
		return "", ErrEmptyText
	}
	return text, nil
}

type docxTable struct {
	Rows []docxRow `xml:"tr"`
}

type docxRow struct {
	Cells []docxCell `xml:"tc"`
}

type docxCell struct {
	Paragraphs []docxParagraph `xml:"p"`
}

type docxParagraph struct {
	Texts []string `xml:"r>t"`
}

func renderDOCXTable(table docxTable) string {
	rows := make([][]string, 0, len(table.Rows))
	for _, sourceRow := range table.Rows {
		row := make([]string, 0, len(sourceRow.Cells))
		for _, cell := range sourceRow.Cells {
			var values []string
			for _, paragraph := range cell.Paragraphs {
				values = append(values, strings.Join(paragraph.Texts, ""))
			}
			row = append(row, normalizeTableCell(strings.Join(values, " ")))
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return renderMarkdownTable(rows)
}

func normalizeTableCell(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return strings.ReplaceAll(value, "|", `\|`)
}

func renderMarkdownTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	columnCount := 0
	for _, row := range rows {
		if len(row) > columnCount {
			columnCount = len(row)
		}
	}
	if columnCount == 0 {
		return ""
	}
	for index := range rows {
		for len(rows[index]) < columnCount {
			rows[index] = append(rows[index], "")
		}
	}
	lines := []string{markdownTableRow(rows[0])}
	separator := make([]string, columnCount)
	for index := range separator {
		separator[index] = "---"
	}
	lines = append(lines, markdownTableRow(separator))
	for _, row := range rows[1:] {
		lines = append(lines, markdownTableRow(row))
	}
	return strings.Join(lines, "\n")
}

func markdownTableRow(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
}

func headingPrefix(style string) string {
	if !strings.HasPrefix(style, "Heading") {
		return ""
	}
	level, err := strconv.Atoi(strings.TrimPrefix(style, "Heading"))
	if err != nil || level < 1 || level > 6 {
		return ""
	}
	return strings.Repeat("#", level) + " "
}

func extractPDFText(ctx context.Context, content []byte) (string, error) {
	if !bytes.HasPrefix(content, []byte("%PDF-")) || !bytes.Contains(content, []byte("%%EOF")) {
		return "", ErrInvalidPDF
	}

	var fragments []string
	streamCount := 0
	var processedStreamBytes int64
	var extractedTextBytes int64
	searchFrom := 0
	for searchFrom < len(content) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		streamIndex := bytes.Index(content[searchFrom:], []byte("stream"))
		if streamIndex < 0 {
			break
		}
		streamIndex += searchFrom
		streamCount++
		dataStart := streamIndex + len("stream")
		if dataStart < len(content) && content[dataStart] == '\r' {
			dataStart++
		}
		if dataStart < len(content) && content[dataStart] == '\n' {
			dataStart++
		}
		streamLength, ok := pdfStreamLength(content, streamIndex)
		if !ok || streamLength < 0 || dataStart+streamLength > len(content) {
			return "", ErrInvalidPDF
		}
		streamData := content[dataStart : dataStart+streamLength]
		if processedStreamBytes+int64(len(streamData)) > maxExtractedTextBytes {
			return "", fmt.Errorf("PDF streams are too large")
		}
		if pdfStreamUsesFlate(content, streamIndex) {
			decoded, err := inflatePDFStream(streamData, maxExtractedTextBytes-processedStreamBytes)
			if err != nil {
				return "", fmt.Errorf("decode PDF stream: %w", err)
			}
			streamData = decoded
		}
		processedStreamBytes += int64(len(streamData))
		for _, fragment := range extractPDFTextOperators(streamData) {
			extractedTextBytes += int64(len(fragment))
			if extractedTextBytes > maxExtractedTextBytes {
				return "", fmt.Errorf("extracted text is too large")
			}
			fragments = append(fragments, fragment)
		}
		endStream := skipPDFWhitespace(content, dataStart+streamLength)
		if !bytes.HasPrefix(content[endStream:], []byte("endstream")) {
			return "", ErrInvalidPDF
		}
		searchFrom = endStream + len("endstream")
	}
	if streamCount == 0 {
		return "", ErrInvalidPDF
	}
	if len(fragments) == 0 {
		return "", ErrEmptyText
	}
	text := strings.TrimSpace(strings.Join(fragments, "\n"))
	if int64(len(text)) > maxExtractedTextBytes {
		return "", fmt.Errorf("extracted text is too large")
	}
	return text, nil
}

func inflatePDFStream(data []byte, limit int64) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	decompressed, readErr := io.ReadAll(io.LimitReader(reader, limit+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(decompressed)) > limit {
		return nil, fmt.Errorf("decoded PDF stream is too large")
	}
	return decompressed, nil
}

func pdfStreamLength(content []byte, streamIndex int) (int, bool) {
	dictionaryStart := bytes.LastIndex(content[:streamIndex], []byte("<<"))
	if dictionaryStart < 0 {
		return 0, false
	}
	dictionary := content[dictionaryStart:streamIndex]
	searchFrom := 0
	for searchFrom < len(dictionary) {
		lengthIndex := bytes.Index(dictionary[searchFrom:], []byte("/Length"))
		if lengthIndex < 0 {
			return 0, false
		}
		lengthIndex += searchFrom + len("/Length")
		if lengthIndex >= len(dictionary) || !isPDFWhitespace(dictionary[lengthIndex]) {
			searchFrom = lengthIndex
			continue
		}
		lengthIndex = skipPDFWhitespace(dictionary, lengthIndex)
		start := lengthIndex
		for lengthIndex < len(dictionary) && dictionary[lengthIndex] >= '0' && dictionary[lengthIndex] <= '9' {
			lengthIndex++
		}
		if start == lengthIndex {
			return 0, false
		}
		length := 0
		for _, digit := range dictionary[start:lengthIndex] {
			length = length*10 + int(digit-'0')
			if length > int(maxExtractedTextBytes) {
				return 0, false
			}
		}
		return length, true
	}
	return 0, false
}

func pdfStreamUsesFlate(content []byte, streamIndex int) bool {
	dictionaryStart := bytes.LastIndex(content[:streamIndex], []byte("<<"))
	return dictionaryStart >= 0 && bytes.Contains(content[dictionaryStart:streamIndex], []byte("/FlateDecode"))
}

func extractPDFTextOperators(data []byte) []string {
	var fragments []string
	for index := 0; index < len(data); index++ {
		var value []byte
		var end int
		var ok bool
		switch {
		case data[index] == '(':
			value, end, ok = parsePDFLiteral(data, index)
		case data[index] == '<' && (index+1 >= len(data) || data[index+1] != '<'):
			value, end, ok = parsePDFHexLiteral(data, index)
		default:
			continue
		}
		if !ok {
			continue
		}
		operator := skipPDFWhitespace(data, end)
		if bytes.HasPrefix(data[operator:], []byte("Tj")) {
			fragments = append(fragments, decodePDFText(value))
			index = end - 1
			continue
		}
		if isPDFTJString(data, index, end) {
			fragments = append(fragments, decodePDFText(value))
		}
		index = end - 1
	}
	return fragments
}

func parsePDFHexLiteral(data []byte, start int) ([]byte, int, bool) {
	if start >= len(data) || data[start] != '<' || start+1 < len(data) && data[start+1] == '<' {
		return nil, 0, false
	}
	value := make([]byte, 0, 16)
	nibble := -1
	for index := start + 1; index < len(data); index++ {
		switch data[index] {
		case '>':
			if nibble >= 0 {
				value = append(value, byte(nibble<<4))
			}
			return value, index + 1, true
		case ' ', '\t', '\r', '\n':
			continue
		}
		digit, ok := pdfHexDigit(data[index])
		if !ok {
			return nil, 0, false
		}
		if nibble < 0 {
			nibble = digit
		} else {
			value = append(value, byte(nibble<<4|digit))
			nibble = -1
		}
	}
	return nil, 0, false
}

func pdfHexDigit(value byte) (int, bool) {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0'), true
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10, true
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10, true
	default:
		return 0, false
	}
}

func isPDFTJString(data []byte, start, end int) bool {
	arrayStart := bytes.LastIndex(data[:start], []byte("["))
	if arrayStart < 0 {
		return false
	}
	arrayEnd := bytes.Index(data[end:], []byte("]"))
	if arrayEnd < 0 {
		return false
	}
	operator := skipPDFWhitespace(data, end+arrayEnd+1)
	return bytes.HasPrefix(data[operator:], []byte("TJ"))
}

func parsePDFLiteral(data []byte, start int) ([]byte, int, bool) {
	var value []byte
	depth := 0
	for index := start; index < len(data); index++ {
		switch data[index] {
		case '(':
			depth++
			if depth > 1 {
				value = append(value, data[index])
			}
		case ')':
			depth--
			if depth == 0 {
				return value, index + 1, true
			}
			value = append(value, data[index])
		case '\\':
			if index+1 >= len(data) {
				return nil, 0, false
			}
			index++
			switch data[index] {
			case 'n':
				value = append(value, '\n')
			case 'r':
				value = append(value, '\r')
			case 't':
				value = append(value, '\t')
			case 'b':
				value = append(value, '\b')
			case 'f':
				value = append(value, '\f')
			case '\r':
				if index+1 < len(data) && data[index+1] == '\n' {
					index++
				}
			case '\n':
			case '(', ')', '\\':
				value = append(value, data[index])
			default:
				if data[index] >= '0' && data[index] <= '7' {
					octal := int(data[index] - '0')
					for count := 1; count < 3 && index+1 < len(data) && data[index+1] >= '0' && data[index+1] <= '7'; count++ {
						index++
						octal = octal*8 + int(data[index]-'0')
					}
					value = append(value, byte(octal))
				} else {
					value = append(value, data[index])
				}
			}
		default:
			value = append(value, data[index])
		}
	}
	return nil, 0, false
}

func skipPDFWhitespace(data []byte, index int) int {
	for index < len(data) && (data[index] == ' ' || data[index] == '\t' || data[index] == '\r' || data[index] == '\n') {
		index++
	}
	return index
}

func isPDFWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func decodePDFText(data []byte) string {
	if len(data) < 2 || data[0] != 0xfe || data[1] != 0xff {
		return string(data)
	}
	data = data[2:]
	codeUnits := make([]uint16, 0, len(data)/2)
	for index := 0; index+1 < len(data); index += 2 {
		codeUnits = append(codeUnits, binary.BigEndian.Uint16(data[index:index+2]))
	}
	return string(utf16.Decode(codeUnits))
}
