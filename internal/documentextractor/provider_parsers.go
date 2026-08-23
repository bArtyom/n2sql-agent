package documentextractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type providerParserTransport struct {
	endpoint         string
	client           *http.Client
	allowedHosts     map[string]bool
	maxResponseBytes int64
}

func newProviderParserTransport(endpoint string, allowedHosts []string, client *http.Client) (providerParserTransport, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return providerParserTransport{}, errors.New("parser endpoint must be an http or https URL")
	}
	hosts := make(map[string]bool, len(allowedHosts))
	for _, host := range allowedHosts {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			hosts[host] = true
		}
	}
	if !hosts[strings.ToLower(parsed.Hostname())] {
		return providerParserTransport{}, fmt.Errorf("parser endpoint host %q is not allowed", parsed.Hostname())
	}
	if client == nil {
		client = http.DefaultClient
	}
	return providerParserTransport{endpoint: strings.TrimRight(parsed.String(), "/"), client: client, allowedHosts: hosts, maxResponseBytes: defaultRemoteParserMaxResponseBytes}, nil
}

func (t providerParserTransport) postJSON(ctx context.Context, suffix string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode parser request: %w", err)
	}
	return t.post(ctx, suffix, bytes.NewReader(body), "application/json")
}

func (t providerParserTransport) post(ctx context.Context, suffix string, body io.Reader, contentType string) ([]byte, error) {
	parsed, err := url.Parse(t.endpoint + suffix)
	if err != nil || !t.allowedHosts[strings.ToLower(parsed.Hostname())] {
		return nil, errors.New("parser endpoint host is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create parser request: %w", err)
	}
	request.Header.Set("Content-Type", contentType)
	response, err := t.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call parser: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, t.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read parser response: %w", err)
	}
	if int64(len(data)) > t.maxResponseBytes {
		return nil, errors.New("parser response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("parser returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func decodeProviderImage(value, filename, source string, page int) (ImageAsset, error) {
	mimeType := imageMIMEType(filename)
	encoded := strings.TrimSpace(value)
	if strings.HasPrefix(encoded, "data:") {
		parts := strings.SplitN(encoded, ",", 2)
		if len(parts) != 2 || !strings.HasSuffix(strings.ToLower(parts[0]), ";base64") {
			return ImageAsset{}, errors.New("invalid image data URI")
		}
		mimeType = strings.TrimPrefix(strings.TrimPrefix(parts[0], "data:"), "image/")
		mimeType = "image/" + mimeType
		encoded = parts[1]
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ImageAsset{}, fmt.Errorf("decode image: %w", err)
	}
	if int64(len(data)) > maxRemoteParserImageBytes {
		return ImageAsset{}, errors.New("parser image is too large")
	}
	if mimeType == "" || !strings.HasPrefix(mimeType, "image/") {
		return ImageAsset{}, errors.New("parser image MIME type is unsupported")
	}
	return ImageAsset{Filename: filepath.Base(filename), MIMEType: mimeType, Data: data, Page: page, Source: source}, nil
}

type MinerUParserEngine struct{ transport providerParserTransport }

func NewMinerUParserEngine(endpoint string, allowedHosts []string, client *http.Client) (*MinerUParserEngine, error) {
	transport, err := newProviderParserTransport(endpoint, allowedHosts, client)
	if err != nil {
		return nil, err
	}
	return &MinerUParserEngine{transport: transport}, nil
}

func (*MinerUParserEngine) Name() string { return "mineru" }

func (*MinerUParserEngine) Description() string { return "MinerU self-hosted parser" }

func (e *MinerUParserEngine) Available() (bool, string) {
	if e == nil || e.transport.client == nil {
		return false, "MinerU parser is not configured"
	}
	return true, ""
}

func (*MinerUParserEngine) Supports(contentType string) bool {
	switch contentType {
	case "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func (e *MinerUParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if !e.Supports(request.ContentType) {
		return ParseResult{}, ErrUnsupportedType
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"return_md": "true", "return_images": "true", "table_enable": "true", "formula_enable": "true",
		"parse_method": "auto", "backend": "pipeline", "response_format_zip": "false",
	} {
		if err := writer.WriteField(key, value); err != nil {
			return ParseResult{}, fmt.Errorf("write MinerU option: %w", err)
		}
	}
	filename := providerUploadFilename(request.Filename, request.ContentType)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		return ParseResult{}, fmt.Errorf("create MinerU file: %w", err)
	}
	if _, err := part.Write(request.Content); err != nil {
		return ParseResult{}, fmt.Errorf("write MinerU file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return ParseResult{}, fmt.Errorf("close MinerU request: %w", err)
	}
	data, err := e.transport.post(ctx, "/file_parse", &body, writer.FormDataContentType())
	if err != nil {
		return ParseResult{}, fmt.Errorf("MinerU file_parse: %w", err)
	}
	var response struct {
		Results map[string]struct {
			MDContent string            `json:"md_content"`
			Images    map[string]string `json:"images"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return ParseResult{}, fmt.Errorf("decode MinerU response: %w", err)
	}
	entry, ok := response.Results[strings.TrimSuffix(path.Base(filename), filepath.Ext(filename))]
	if !ok {
		for _, candidate := range response.Results {
			entry = candidate
			ok = true
			break
		}
	}
	if !ok || strings.TrimSpace(entry.MDContent) == "" {
		return ParseResult{}, ErrEmptyText
	}
	result := ParseResult{Markdown: entry.MDContent, Metadata: map[string]string{"parser_mode": "layout", "parser_provider": "mineru"}}
	for imageName, imageData := range entry.Images {
		asset, err := decodeProviderImage(imageData, path.Base(imageName), "mineru", 0)
		if err != nil {
			return ParseResult{}, fmt.Errorf("decode MinerU image %q: %w", imageName, err)
		}
		result.Images = append(result.Images, asset)
	}
	return result, nil
}

type PaddleOCRVLParserEngine struct{ transport providerParserTransport }

func NewPaddleOCRVLParserEngine(endpoint string, allowedHosts []string, client *http.Client) (*PaddleOCRVLParserEngine, error) {
	transport, err := newProviderParserTransport(endpoint, allowedHosts, client)
	if err != nil {
		return nil, err
	}
	return &PaddleOCRVLParserEngine{transport: transport}, nil
}

func (*PaddleOCRVLParserEngine) Name() string { return "paddleocr_vl" }

func (*PaddleOCRVLParserEngine) Description() string { return "PaddleOCR-VL layout parser" }

func (e *PaddleOCRVLParserEngine) Available() (bool, string) {
	if e == nil || e.transport.client == nil {
		return false, "PaddleOCR-VL parser is not configured"
	}
	return true, ""
}

func (*PaddleOCRVLParserEngine) Supports(contentType string) bool {
	return contentType == "application/pdf" || strings.HasPrefix(contentType, "image/")
}

func (e *PaddleOCRVLParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if !e.Supports(request.ContentType) {
		return ParseResult{}, ErrUnsupportedType
	}
	fileType := 1
	if request.ContentType == "application/pdf" {
		fileType = 0
	}
	data, err := e.transport.postJSON(ctx, "/layout-parsing", map[string]any{
		"file":               base64.StdEncoding.EncodeToString(request.Content),
		"fileType":           fileType,
		"visualize":          false,
		"useLayoutDetection": true,
		"mergeTables":        true,
		"restructurePages":   true,
	})
	if err != nil {
		return ParseResult{}, fmt.Errorf("PaddleOCR-VL layout-parsing: %w", err)
	}
	var response struct {
		ErrorCode int    `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
		Result    struct {
			Pages []struct {
				Markdown struct {
					Text   string            `json:"text"`
					Images map[string]string `json:"images"`
				} `json:"markdown"`
			} `json:"layoutParsingResults"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return ParseResult{}, fmt.Errorf("decode PaddleOCR-VL response: %w", err)
	}
	if response.ErrorCode != 0 {
		return ParseResult{}, fmt.Errorf("PaddleOCR-VL error %d: %s", response.ErrorCode, response.ErrorMsg)
	}
	result := ParseResult{Metadata: map[string]string{"parser_mode": "layout", "parser_provider": "paddleocr_vl"}}
	for pageIndex, page := range response.Result.Pages {
		if text := strings.TrimSpace(page.Markdown.Text); text != "" {
			result.Markdown += fmt.Sprintf("[Page %d]\n%s\n\n", pageIndex+1, text)
		}
		for imageName, imageData := range page.Markdown.Images {
			asset, err := decodeProviderImage(imageData, path.Base(imageName), "paddleocr_vl", pageIndex+1)
			if err != nil {
				return ParseResult{}, fmt.Errorf("decode PaddleOCR-VL image %q: %w", imageName, err)
			}
			result.Images = append(result.Images, asset)
		}
	}
	result.Markdown = strings.TrimSpace(result.Markdown)
	if result.Markdown == "" {
		return ParseResult{}, ErrEmptyText
	}
	return result, nil
}

// AnalyzePage sends one rendered page image through PaddleOCR-VL's layout
// endpoint. The provider may return Markdown text plus cropped figure images;
// both are normalized into page blocks so the PDF parser does not depend on
// the provider response shape.
func (e *PaddleOCRVLParserEngine) AnalyzePage(ctx context.Context, page PDFPage) ([]PDFPageBlock, error) {
	if e == nil || len(page.Image) == 0 {
		return nil, ErrEmptyText
	}
	data, err := e.transport.postJSON(ctx, "/layout-parsing", map[string]any{
		"file":               base64.StdEncoding.EncodeToString(page.Image),
		"fileType":           1,
		"visualize":          false,
		"useLayoutDetection": true,
		"mergeTables":        true,
		"restructurePages":   true,
	})
	if err != nil {
		return nil, fmt.Errorf("PaddleOCR-VL page layout-parsing: %w", err)
	}
	var response struct {
		ErrorCode int    `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
		Result    struct {
			Pages []struct {
				Markdown struct {
					Text   string            `json:"text"`
					Images map[string]string `json:"images"`
				} `json:"markdown"`
			} `json:"layoutParsingResults"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode PaddleOCR-VL page response: %w", err)
	}
	if response.ErrorCode != 0 {
		return nil, fmt.Errorf("PaddleOCR-VL page error %d: %s", response.ErrorCode, response.ErrorMsg)
	}
	if len(response.Result.Pages) == 0 {
		return nil, ErrEmptyText
	}
	pageResult := response.Result.Pages[0]
	blocks := make([]PDFPageBlock, 0, len(pageResult.Markdown.Images)+1)
	if text := strings.TrimSpace(pageResult.Markdown.Text); text != "" {
		kind := PDFBlockText
		if looksLikeMarkdownTable(text) {
			kind = PDFBlockTable
		}
		blocks = append(blocks, PDFPageBlock{Page: page.Number, Kind: kind, Text: text, Order: 0, Source: "paddleocr_vl"})
	}
	imageNames := make([]string, 0, len(pageResult.Markdown.Images))
	for imageName := range pageResult.Markdown.Images {
		imageNames = append(imageNames, imageName)
	}
	sort.Strings(imageNames)
	for index, imageName := range imageNames {
		asset, err := decodeProviderImage(pageResult.Markdown.Images[imageName], path.Base(imageName), "paddleocr_vl", page.Number)
		if err != nil {
			return nil, fmt.Errorf("decode PaddleOCR-VL page image %q: %w", imageName, err)
		}
		blocks = append(blocks, PDFPageBlock{Page: page.Number, Kind: PDFBlockFigure, Order: index + 1, Image: asset.Data, MIMEType: asset.MIMEType, Source: asset.Source})
	}
	if len(blocks) == 0 {
		return nil, ErrEmptyText
	}
	return blocks, nil
}

func looksLikeMarkdownTable(text string) bool {
	lines := strings.Split(text, "\n")
	for index := 0; index+1 < len(lines); index++ {
		if strings.Contains(lines[index], "|") && strings.Contains(lines[index+1], "---") {
			return true
		}
	}
	return false
}

func providerUploadFilename(filename, contentType string) string {
	filename = filepath.Base(strings.TrimSpace(filename))
	if filename == "" || filename == "." || filename == ".." {
		filename = "document"
	}
	if filepath.Ext(filename) != "" {
		return filename
	}
	extension := map[string]string{
		"application/pdf": ".pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
		"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp",
	}[contentType]
	return filename + extension
}
