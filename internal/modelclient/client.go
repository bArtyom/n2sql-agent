package modelclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxEmbeddingResponseBytes = 2 << 20
	maxChatResponseBytes      = 1 << 20
	maxChatStreamBytes        = 8 << 20
	maxOCRResponseBytes       = 4 << 20
)

// ConnectionChecker verifies that an API endpoint accepts the configured key.
type ConnectionChecker interface {
	Check(context.Context, string, string) error
}

type Embedder interface {
	Embed(context.Context, string, string, EmbeddingRequest) (EmbeddingResponse, error)
}

type ChatCompleter interface {
	Chat(context.Context, string, string, ChatRequest) (ChatResponse, error)
}

type ChatStreamer interface {
	ChatStream(context.Context, string, string, ChatRequest, func(string) error) error
}

type OCRer interface {
	OCR(context.Context, string, string, OCRRequest) (OCRResponse, error)
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

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type ChatResponse struct {
	Message string `json:"message"`
}

type OCRRequest struct {
	Model  string
	Prompt string
	Image  []byte
}

type OCRResponse struct {
	Text string
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

func (c *HTTPClient) Chat(ctx context.Context, baseURL, apiKey string, chatRequest ChatRequest) (ChatResponse, error) {
	if err := validateChatRequest(chatRequest); err != nil {
		return ChatResponse{}, err
	}

	endpoint, err := apiEndpoint(baseURL, "chat/completions", c.allowedHosts)
	if err != nil {
		return ChatResponse{}, err
	}
	body, err := json.Marshal(chatRequest)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("encode chat request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("create chat request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("request chat endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return ChatResponse{}, fmt.Errorf("chat endpoint returned HTTP %d", response.StatusCode)
	}

	body, err = io.ReadAll(io.LimitReader(response.Body, maxChatResponseBytes+1))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read chat response: %w", err)
	}
	if len(body) > maxChatResponseBytes {
		return ChatResponse{}, fmt.Errorf("chat response is too large")
	}

	var payload struct {
		Choices []struct {
			Message ChatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(payload.Choices) == 0 || payload.Choices[0].Message.Content == "" {
		return ChatResponse{}, fmt.Errorf("chat response does not contain a message")
	}
	return ChatResponse{Message: payload.Choices[0].Message.Content}, nil
}

// OCR sends one JPEG page to an OpenAI-compatible vision chat endpoint and
// asks the model to return only the text visible in the image.
func (c *HTTPClient) OCR(ctx context.Context, baseURL, apiKey string, ocrRequest OCRRequest) (OCRResponse, error) {
	if ocrRequest.Model == "" || len(ocrRequest.Image) == 0 || strings.TrimSpace(ocrRequest.Prompt) == "" {
		return OCRResponse{}, fmt.Errorf("OCR model, prompt, and image are required")
	}

	endpoint, err := apiEndpoint(baseURL, "chat/completions", c.allowedHosts)
	if err != nil {
		return OCRResponse{}, err
	}
	payload := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url,omitempty"`
			} `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}{
		Model: ocrRequest.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url,omitempty"`
			} `json:"content"`
		}{
			{
				Role: "user",
				Content: []struct {
					Type     string `json:"type"`
					Text     string `json:"text,omitempty"`
					ImageURL *struct {
						URL string `json:"url"`
					} `json:"image_url,omitempty"`
				}{
					{Type: "text", Text: ocrRequest.Prompt},
					{Type: "image_url", ImageURL: &struct {
						URL string `json:"url"`
					}{URL: "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(ocrRequest.Image)}},
				},
			},
		},
		Stream: false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return OCRResponse{}, fmt.Errorf("encode OCR request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return OCRResponse{}, fmt.Errorf("create OCR request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return OCRResponse{}, fmt.Errorf("request OCR endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return OCRResponse{}, fmt.Errorf("OCR endpoint returned HTTP %d", response.StatusCode)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxOCRResponseBytes+1))
	if err != nil {
		return OCRResponse{}, fmt.Errorf("read OCR response: %w", err)
	}
	if len(responseBody) > maxOCRResponseBytes {
		return OCRResponse{}, fmt.Errorf("OCR response is too large")
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return OCRResponse{}, fmt.Errorf("decode OCR response: %w", err)
	}
	if len(result.Choices) == 0 {
		return OCRResponse{}, fmt.Errorf("OCR response does not contain a choice")
	}
	text, err := decodeOCRContent(result.Choices[0].Message.Content)
	if err != nil {
		return OCRResponse{}, err
	}
	if strings.TrimSpace(text) == "" {
		return OCRResponse{}, fmt.Errorf("OCR response does not contain text")
	}
	return OCRResponse{Text: text}, nil
}

func decodeOCRContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("decode OCR message content: %w", err)
	}
	var values []string
	for _, part := range parts {
		if strings.TrimSpace(part.Text) != "" {
			values = append(values, part.Text)
		}
	}
	return strings.Join(values, ""), nil
}

func (c *HTTPClient) ChatStream(ctx context.Context, baseURL, apiKey string, chatRequest ChatRequest, onDelta func(string) error) error {
	if err := validateChatRequest(chatRequest); err != nil {
		return err
	}
	if onDelta == nil {
		return fmt.Errorf("chat stream delta callback is required")
	}
	chatRequest.Stream = true
	endpoint, err := apiEndpoint(baseURL, "chat/completions", c.allowedHosts)
	if err != nil {
		return err
	}
	body, err := json.Marshal(chatRequest)
	if err != nil {
		return fmt.Errorf("encode chat stream request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create chat stream request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("request chat stream endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("chat stream endpoint returned HTTP %d", response.StatusCode)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxChatStreamBytes)
	readBytes := 0
	done := false
	dataLines := make([]string, 0, 1)
	processEvent := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			return true, nil
		}
		var payload struct {
			Choices []struct {
				Delta ChatMessage `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return false, fmt.Errorf("decode chat stream event: %w", err)
		}
		if len(payload.Choices) == 0 || payload.Choices[0].Delta.Content == "" {
			return false, nil
		}
		if err := onDelta(payload.Choices[0].Delta.Content); err != nil {
			return false, fmt.Errorf("handle chat stream delta: %w", err)
		}
		return false, nil
	}
	for scanner.Scan() {
		rawLine := scanner.Text()
		readBytes += len(rawLine)
		if readBytes > maxChatStreamBytes {
			return fmt.Errorf("chat stream response is too large")
		}
		line := strings.TrimSuffix(rawLine, "\r")
		if line == "" {
			var err error
			done, err = processEvent()
			if err != nil {
				return err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		if strings.HasPrefix(data, " ") {
			data = data[1:]
		}
		dataLines = append(dataLines, data)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read chat stream: %w", err)
	}
	if !done {
		var err error
		done, err = processEvent()
		if err != nil {
			return err
		}
	}
	if !done {
		return fmt.Errorf("chat stream ended before done event")
	}
	return nil
}

func validateChatRequest(chatRequest ChatRequest) error {
	if chatRequest.Model == "" || len(chatRequest.Messages) == 0 {
		return fmt.Errorf("chat model and messages are required")
	}
	for _, message := range chatRequest.Messages {
		if message.Role == "" || message.Content == "" {
			return fmt.Errorf("chat message role and content are required")
		}
	}
	return nil
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
