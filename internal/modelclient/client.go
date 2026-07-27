package modelclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxEmbeddingResponseBytes = 2 << 20

// ConnectionChecker verifies that an API endpoint accepts the configured key.
type ConnectionChecker interface {
	Check(context.Context, string, string) error
}

type Embedder interface {
	Embed(context.Context, string, string, EmbeddingRequest) (EmbeddingResponse, error)
}

type EmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type Embedding struct {
	Index  int       `json:"index"`
	Vector []float32 `json:"vector"`
}

type EmbeddingResponse struct {
	Data []Embedding `json:"data"`
}

type HTTPClient struct {
	client       *http.Client
	allowedHosts map[string]struct{}
}

func NewHTTPClient(client *http.Client, allowedHosts []string) *HTTPClient {
	if client == nil {
		client = http.DefaultClient
	}
	noRedirectClient := *client
	noRedirectClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	trustedHosts := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		trustedHosts[strings.ToLower(host)] = struct{}{}
	}
	return &HTTPClient{client: &noRedirectClient, allowedHosts: trustedHosts}
}

func (c *HTTPClient) Check(ctx context.Context, baseURL, apiKey string) error {
	endpoint, err := apiEndpoint(baseURL, "models", c.allowedHosts)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)

	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("request models endpoint: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("models endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *HTTPClient) Embed(ctx context.Context, baseURL, apiKey string, embeddingRequest EmbeddingRequest) (EmbeddingResponse, error) {
	if embeddingRequest.Model == "" || len(embeddingRequest.Input) == 0 {
		return EmbeddingResponse{}, fmt.Errorf("embedding model and input are required")
	}

	endpoint, err := apiEndpoint(baseURL, "embeddings", c.allowedHosts)
	if err != nil {
		return EmbeddingResponse{}, err
	}
	body, err := json.Marshal(embeddingRequest)
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("encode embedding request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("create embeddings request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("request embeddings endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return EmbeddingResponse{}, fmt.Errorf("embeddings endpoint returned HTTP %d", response.StatusCode)
	}

	body, err = io.ReadAll(io.LimitReader(response.Body, maxEmbeddingResponseBytes+1))
	if err != nil {
		return EmbeddingResponse{}, fmt.Errorf("read embeddings response: %w", err)
	}
	if len(body) > maxEmbeddingResponseBytes {
		return EmbeddingResponse{}, fmt.Errorf("embeddings response is too large")
	}

	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return EmbeddingResponse{}, fmt.Errorf("decode embeddings response: %w", err)
	}

	if len(payload.Data) != len(embeddingRequest.Input) {
		return EmbeddingResponse{}, fmt.Errorf("embeddings response count = %d, want %d", len(payload.Data), len(embeddingRequest.Input))
	}

	result := EmbeddingResponse{Data: make([]Embedding, len(embeddingRequest.Input))}
	seenIndexes := make(map[int]struct{}, len(payload.Data))
	for _, embedding := range payload.Data {
		if embedding.Index < 0 || embedding.Index >= len(embeddingRequest.Input) {
			return EmbeddingResponse{}, fmt.Errorf("embeddings response index %d is out of range", embedding.Index)
		}
		if _, seen := seenIndexes[embedding.Index]; seen {
			return EmbeddingResponse{}, fmt.Errorf("embeddings response contains duplicate index %d", embedding.Index)
		}
		if len(embedding.Embedding) == 0 {
			return EmbeddingResponse{}, fmt.Errorf("embeddings response vector at index %d is empty", embedding.Index)
		}
		seenIndexes[embedding.Index] = struct{}{}
		result.Data[embedding.Index] = Embedding{Index: embedding.Index, Vector: embedding.Embedding}
	}
	return result, nil
}

func apiEndpoint(baseURL, resource string, allowedHosts map[string]struct{}) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model provider base URL")
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("model provider base URL must use HTTPS")
	}
	if _, allowed := allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
		return "", fmt.Errorf("model provider base URL host is not allowed")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + resource
	parsed.RawQuery = ""
	return parsed.String(), nil
}
