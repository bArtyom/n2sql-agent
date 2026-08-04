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
		text, err = extractPDFText(ctx, content)
		if err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", ErrEmptyText
	}
	return text, nil
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
