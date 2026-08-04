package documentextractor

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const maxExtractedTextBytes int64 = 10 << 20

var (
	ErrInvalidStoragePath = errors.New("invalid document storage path")
	ErrUnsupportedType    = errors.New("unsupported document content type")
	ErrInvalidPDF         = errors.New("invalid PDF document")
	ErrEmptyText          = errors.New("document contains no text")
)

type Extractor struct{ root string }

func New(root string) *Extractor { return &Extractor{root: root} }

func (e *Extractor) Extract(ctx context.Context, storagePath, contentType string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if contentType != "text/plain" && contentType != "text/markdown" && contentType != "application/pdf" {
		return "", ErrUnsupportedType
	}
	if filepath.IsAbs(storagePath) || filepath.Clean(storagePath) != storagePath || filepath.Dir(storagePath) != "documents" {
		return "", ErrInvalidStoragePath
	}
	file, err := os.Open(filepath.Join(e.root, storagePath))
	if err != nil {
		return "", fmt.Errorf("open document: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxExtractedTextBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > maxExtractedTextBytes {
		return "", fmt.Errorf("extracted text is too large")
	}
	text := string(content)
	if contentType == "application/pdf" {
		text, err = extractPDFText(content)
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	return text, nil
}

func extractPDFText(content []byte) (string, error) {
	if !bytes.HasPrefix(content, []byte("%PDF-")) || !bytes.Contains(content, []byte("%%EOF")) {
		return "", ErrInvalidPDF
	}

	var fragments []string
	streamCount := 0
	searchFrom := 0
	for searchFrom < len(content) {
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
		endStream := bytes.Index(content[dataStart:], []byte("endstream"))
		if endStream < 0 {
			return "", ErrInvalidPDF
		}
		endStream += dataStart
		streamData := content[dataStart:endStream]
		if bytes.Contains(content[max(0, streamIndex-512):streamIndex], []byte("/FlateDecode")) {
			decoded, err := inflatePDFStream(streamData)
			if err != nil {
				return "", fmt.Errorf("decode PDF stream: %w", err)
			}
			streamData = decoded
		}
		fragments = append(fragments, extractPDFTextOperators(streamData)...)
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

func inflatePDFStream(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	decompressed, readErr := io.ReadAll(io.LimitReader(reader, maxExtractedTextBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(decompressed)) > maxExtractedTextBytes {
		return nil, fmt.Errorf("decoded PDF stream is too large")
	}
	return decompressed, nil
}

func extractPDFTextOperators(data []byte) []string {
	var fragments []string
	for index := 0; index < len(data); index++ {
		if data[index] != '(' {
			continue
		}
		value, end, ok := parsePDFLiteral(data, index)
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

func max(first, second int) int {
	if first > second {
		return first
	}
	return second
}
