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
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRemoteParserMaxResponseBytes int64 = 20 << 20
	maxRemoteParserImageBytes           int64 = 5 << 20
)

// HTTPParserEngine adapts a remote parser to the same ParseResult contract as
// local engines. The endpoint contract is intentionally small and provider
// neutral, so a MinerU/PaddleOCR-VL adapter can be added without changing the
// indexing pipeline.
type HTTPParserEngine struct {
	name             string
	endpoint         string
	supportedTypes   map[string]bool
	client           *http.Client
	allowedHosts     map[string]bool
	maxResponseBytes int64
}

type remoteParserResponse struct {
	Markdown string            `json:"markdown"`
	Images   []remoteImage     `json:"images,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type remoteImage struct {
	Filename   string `json:"filename"`
	MIMEType   string `json:"mime_type"`
	DataBase64 string `json:"data_base64"`
	Page       int    `json:"page,omitempty"`
	Source     string `json:"source,omitempty"`
	Original   bool   `json:"original,omitempty"`
}

func NewHTTPParserEngine(name, endpoint string, supportedTypes, allowedHosts []string, client *http.Client) (*HTTPParserEngine, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("remote parser engine name is empty")
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("remote parser endpoint must be an http or https URL")
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := make(map[string]bool, len(allowedHosts))
	for _, value := range allowedHosts {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			allowed[value] = true
		}
	}
	if len(allowed) == 0 || !allowed[host] {
		return nil, fmt.Errorf("remote parser host %q is not allowed", host)
	}
	types := make(map[string]bool, len(supportedTypes))
	for _, value := range supportedTypes {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			types[value] = true
		}
	}
	if len(types) == 0 {
		return nil, errors.New("remote parser has no supported content types")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &HTTPParserEngine{
		name:             name,
		endpoint:         parsed.String(),
		supportedTypes:   types,
		client:           client,
		allowedHosts:     allowed,
		maxResponseBytes: defaultRemoteParserMaxResponseBytes,
	}, nil
}

func (e *HTTPParserEngine) Name() string { return e.name }

func (e *HTTPParserEngine) Supports(contentType string) bool {
	return e != nil && e.supportedTypes[strings.ToLower(strings.TrimSpace(contentType))]
}

func (e *HTTPParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if e == nil || e.client == nil {
		return ParseResult{}, errors.New("remote parser engine is not initialized")
	}
	if !e.Supports(request.ContentType) {
		return ParseResult{}, ErrUnsupportedType
	}
	parsed, err := url.Parse(e.endpoint)
	if err != nil || !e.allowedHosts[strings.ToLower(parsed.Hostname())] {
		return ParseResult{}, errors.New("remote parser endpoint host is not allowed")
	}
	body, contentType, err := multipartBody(request)
	if err != nil {
		return ParseResult{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, body)
	if err != nil {
		return ParseResult{}, fmt.Errorf("create remote parser request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", contentType)
	httpRequest.Header.Set("X-Parser-Engine", e.name)
	httpRequest.Header.Set("X-Document-Content-Type", request.ContentType)
	response, err := e.client.Do(httpRequest)
	if err != nil {
		return ParseResult{}, fmt.Errorf("call remote parser: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, e.maxResponseBytes+1))
	if err != nil {
		return ParseResult{}, fmt.Errorf("read remote parser response: %w", err)
	}
	if int64(len(data)) > e.maxResponseBytes {
		return ParseResult{}, errors.New("remote parser response is too large")
	}
	var parsedResponse remoteParserResponse
	if err := json.Unmarshal(data, &parsedResponse); err != nil {
		return ParseResult{}, fmt.Errorf("decode remote parser response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if parsedResponse.Error == "" {
			parsedResponse.Error = response.Status
		}
		return ParseResult{}, fmt.Errorf("remote parser failed: %s", parsedResponse.Error)
	}
	result := ParseResult{Markdown: parsedResponse.Markdown, Metadata: parsedResponse.Metadata}
	for index, image := range parsedResponse.Images {
		data, err := base64.StdEncoding.DecodeString(image.DataBase64)
		if err != nil {
			return ParseResult{}, fmt.Errorf("decode remote parser image %d: %w", index, err)
		}
		if int64(len(data)) > maxRemoteParserImageBytes {
			return ParseResult{}, fmt.Errorf("remote parser image %d is too large", index)
		}
		if image.Filename == "" || image.MIMEType == "" {
			return ParseResult{}, fmt.Errorf("remote parser image %d has no filename or MIME type", index)
		}
		result.Images = append(result.Images, ImageAsset{
			Filename: image.Filename,
			MIMEType: image.MIMEType,
			Data:     data,
			Page:     image.Page,
			Source:   image.Source,
			Original: image.Original,
		})
	}
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["parser_transport"] = "http"
	return result, nil
}

func multipartBody(request ParseRequest) (io.Reader, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(request.Filename))
	if err != nil {
		return nil, "", fmt.Errorf("create remote parser file field: %w", err)
	}
	if _, err := part.Write(request.Content); err != nil {
		return nil, "", fmt.Errorf("write remote parser file: %w", err)
	}
	if err := writer.WriteField("filename", filepath.Base(request.Filename)); err != nil {
		return nil, "", fmt.Errorf("write remote parser filename: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close remote parser request: %w", err)
	}
	return &body, writer.FormDataContentType(), nil
}
