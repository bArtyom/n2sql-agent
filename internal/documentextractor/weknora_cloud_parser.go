package documentextractor

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultWeKnoraCloudParserURL = "https://weknora.weixin.qq.com/api/v1/doc"

type WeKnoraCloudParserEngine struct {
	endpoint    string
	appID       string
	apiKey      string
	client      *http.Client
	allowedHost map[string]bool
	pollInitial time.Duration
	pollMax     time.Duration
	pollTimeout time.Duration
}

func NewWeKnoraCloudParserEngine(endpoint, appID, apiKey string, allowedHosts []string, client *http.Client) (*WeKnoraCloudParserEngine, error) {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultWeKnoraCloudParserURL
	}
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("WeKnora Cloud app ID and API key are required")
	}
	parsed, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("WeKnora Cloud parser endpoint must be an HTTPS URL")
	}
	hosts := make(map[string]bool, len(allowedHosts)+1)
	for _, host := range allowedHosts {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			hosts[host] = true
		}
	}
	hosts["weknora.weixin.qq.com"] = true
	if !hosts[strings.ToLower(parsed.Hostname())] {
		return nil, fmt.Errorf("WeKnora Cloud host %q is not allowed", parsed.Hostname())
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Minute}
	}
	return &WeKnoraCloudParserEngine{
		endpoint:    strings.TrimRight(parsed.String(), "/"),
		appID:       appID,
		apiKey:      apiKey,
		client:      client,
		allowedHost: hosts,
		pollInitial: 500 * time.Millisecond,
		pollMax:     10 * time.Second,
		pollTimeout: 20 * time.Minute,
	}, nil
}

func (*WeKnoraCloudParserEngine) Name() string { return "weknoracloud" }

func (*WeKnoraCloudParserEngine) Description() string { return "WeKnora Cloud signed document parser" }

func (e *WeKnoraCloudParserEngine) Available() (bool, string) {
	if e == nil || e.client == nil {
		return false, "WeKnora Cloud parser is not configured"
	}
	return true, ""
}

func (*WeKnoraCloudParserEngine) Supports(contentType string) bool {
	switch contentType {
	case "text/plain", "text/markdown", "text/html", "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return true
	default:
		return false
	}
}

func (e *WeKnoraCloudParserEngine) Parse(ctx context.Context, request ParseRequest) (ParseResult, error) {
	if !e.Supports(request.ContentType) {
		return ParseResult{}, ErrUnsupportedType
	}
	payload := map[string]any{
		"file_content": base64.StdEncoding.EncodeToString(request.Content),
		"file_name":    filepath.Base(request.Filename),
		"file_type":    request.ContentType,
		"config":       map[string]any{"parser_engine": request.EngineName},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ParseResult{}, fmt.Errorf("encode WeKnora Cloud request: %w", err)
	}
	submit, err := e.signedJSON(ctx, http.MethodPost, e.endpoint+"/reader", body)
	if err != nil {
		return ParseResult{}, fmt.Errorf("submit WeKnora Cloud parse: %w", err)
	}
	var task struct {
		TaskID string `json:"task_id"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(submit, &task); err != nil {
		return ParseResult{}, fmt.Errorf("decode WeKnora Cloud submit: %w", err)
	}
	if task.TaskID == "" {
		if task.Error == "" {
			task.Error = "missing task_id"
		}
		return ParseResult{}, fmt.Errorf("WeKnora Cloud submit failed: %s", task.Error)
	}
	return e.poll(ctx, task.TaskID)
}

func (e *WeKnoraCloudParserEngine) poll(ctx context.Context, taskID string) (ParseResult, error) {
	pollCtx := ctx
	if _, ok := ctx.Deadline(); !ok && e.pollTimeout > 0 {
		var cancel context.CancelFunc
		pollCtx, cancel = context.WithTimeout(ctx, e.pollTimeout)
		defer cancel()
	}
	interval := e.pollInitial
	for {
		data, err := e.signedJSON(pollCtx, http.MethodGet, e.endpoint+"/"+url.PathEscape(taskID), nil)
		if err != nil {
			return ParseResult{}, fmt.Errorf("poll WeKnora Cloud parse: %w", err)
		}
		var response struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Result *struct {
				Markdown string `json:"markdown_content"`
				Images   []struct {
					Filename string `json:"filename"`
					MIMEType string `json:"mime_type"`
					Data     []byte `json:"image_data"`
					Page     int    `json:"page"`
				} `json:"image_refs"`
				Metadata map[string]string `json:"metadata"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return ParseResult{}, fmt.Errorf("decode WeKnora Cloud task: %w", err)
		}
		switch strings.ToLower(response.Status) {
		case "completed", "succeeded", "success":
			if response.Result == nil {
				return ParseResult{}, ErrEmptyText
			}
			result := ParseResult{Markdown: response.Result.Markdown, Metadata: response.Result.Metadata}
			if result.Metadata == nil {
				result.Metadata = map[string]string{}
			}
			result.Metadata["parser_provider"] = "weknoracloud"
			for index, image := range response.Result.Images {
				if int64(len(image.Data)) > maxRemoteParserImageBytes {
					return ParseResult{}, fmt.Errorf("WeKnora Cloud image %d is too large", index)
				}
				result.Images = append(result.Images, ImageAsset{Filename: filepath.Base(image.Filename), MIMEType: image.MIMEType, Data: image.Data, Page: image.Page, Source: "weknoracloud"})
			}
			if strings.TrimSpace(result.Markdown) == "" {
				return ParseResult{}, ErrEmptyText
			}
			return result, nil
		case "failed", "error", "cancelled", "canceled":
			if response.Error == "" {
				response.Error = "remote task failed"
			}
			return ParseResult{}, errors.New("WeKnora Cloud task failed: " + response.Error)
		}
		if err := pollCtx.Err(); err != nil {
			return ParseResult{}, err
		}
		select {
		case <-pollCtx.Done():
			return ParseResult{}, pollCtx.Err()
		case <-time.After(interval):
		}
		interval = time.Duration(float64(interval) * 1.5)
		if interval > e.pollMax {
			interval = e.pollMax
		}
	}
}

func (e *WeKnoraCloudParserEngine) signedJSON(ctx context.Context, method, endpoint string, body []byte) ([]byte, error) {
	if len(body) == 0 {
		body = []byte("{}")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !e.allowedHost[strings.ToLower(parsed.Hostname())] {
		return nil, errors.New("WeKnora Cloud endpoint host is not allowed")
	}
	requestID, err := randomRequestID()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range cloudSignatureHeaders(e.appID, e.apiKey, requestID, body) {
		request.Header.Set(key, value)
	}
	response, err := e.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, defaultRemoteParserMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > defaultRemoteParserMaxResponseBytes {
		return nil, errors.New("WeKnora Cloud response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("WeKnora Cloud returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func cloudSignatureHeaders(appID, apiKey, requestID string, body []byte) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 12)
	_, _ = rand.Read(nonceBytes)
	nonce := hex.EncodeToString(nonceBytes)
	bodyHash := md5.Sum(body)
	params := map[string]string{"body": hex.EncodeToString(bodyHash[:]), "x-api-key": apiKey, "x-appid": appID, "x-nonce": nonce, "x-request-id": requestID, "x-timestamp": timestamp}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, rfc3986Encode(key)+"="+rfc3986Encode(params[key]))
	}
	signature := md5.Sum([]byte(strings.Join(parts, "&")))
	return map[string]string{"X-APPID": appID, "X-API-Key": apiKey, "X-Request-ID": requestID, "X-Timestamp": timestamp, "X-Nonce": nonce, "X-Signature": hex.EncodeToString(signature[:])}
}

func rfc3986Encode(value string) string {
	const hexChars = "0123456789ABCDEF"
	var builder strings.Builder
	for _, char := range []byte(value) {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == '~' {
			builder.WriteByte(char)
		} else {
			builder.WriteByte('%')
			builder.WriteByte(hexChars[char>>4])
			builder.WriteByte(hexChars[char&15])
		}
	}
	return builder.String()
}

func randomRequestID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate cloud request ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}
