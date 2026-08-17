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

	"github.com/bArtyom/n2sql-agent/internal/usage"
)

const (
	maxEmbeddingResponseBytes = 2 << 20
	maxChatResponseBytes      = 1 << 20
	maxChatStreamBytes        = 8 << 20
	maxOCRResponseBytes       = 4 << 20
	maxRerankResponseBytes    = 2 << 20
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

type Reranker interface {
	Rerank(context.Context, string, string, RerankRequest) (RerankResponse, error)
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
	Data  []Embedding `json:"data"`
	Usage *TokenUsage `json:"usage,omitempty"`
}

type RerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type RerankResponse struct {
	Results []RerankResult `json:"results"`
	Usage   *TokenUsage    `json:"usage,omitempty"`
}

type TokenUsage = usage.TokenUsage

type ChatMessage struct {
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall        `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ContentParts     []ChatContentPart `json:"-"`
}

// ChatContentPart is an OpenAI-compatible multimodal content part. It is
// intentionally kept separate from Content so ordinary text messages keep
// their existing wire shape.
type ChatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ChatImageURL `json:"image_url,omitempty"`
}

type ChatImageURL struct {
	URL string `json:"url"`
}

func (m ChatMessage) MarshalJSON() ([]byte, error) {
	content := any(m.Content)
	if len(m.ContentParts) > 0 {
		content = m.ContentParts
	}
	if m.Content == "" && len(m.ToolCalls) > 0 {
		content = nil
	}
	return json.Marshal(struct {
		Role             string     `json:"role"`
		Content          any        `json:"content"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
	}{
		Role:             m.Role,
		Content:          content,
		ReasoningContent: m.ReasoningContent,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
	})
}

type ChatRequest struct {
	Model               string           `json:"model"`
	Messages            []ChatMessage    `json:"messages"`
	Stream              bool             `json:"stream"`
	ReasoningEffort     string           `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens int              `json:"max_completion_tokens,omitempty"`
	Tools               []ToolDefinition `json:"tools,omitempty"`
}

type ChatResponse struct {
	Message          string      `json:"message"`
	ReasoningContent string      `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall  `json:"tool_calls,omitempty"`
	Usage            *TokenUsage `json:"usage,omitempty"`
}

// ToolDefinition is the OpenAI-compatible function tool shape sent with a
// chat request. Function contains the JSON Schema the model uses for args.
type ToolDefinition struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type OCRRequest struct {
	Model  string
	Prompt string
	Image  []byte
}

type OCRResponse struct {
	Text string
}

type visionContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type visionMessage struct {
	Role    string          `json:"role"`
	Content []visionContent `json:"content"`
}

type ocrChatRequest struct {
	Model    string          `json:"model"`
	Messages []visionMessage `json:"messages"`
	Stream   bool            `json:"stream"`
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
		Usage *TokenUsage `json:"usage"`
		Data  []struct {
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

	result := EmbeddingResponse{Data: make([]Embedding, len(embeddingRequest.Input)), Usage: payload.Usage}
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

// Rerank asks a rerank model to score the already-recalled candidate chunks.
// The qwen3-rerank endpoint is OpenAI-compatible and returns candidate indexes
// in descending relevance order.
func (c *HTTPClient) Rerank(ctx context.Context, baseURL, apiKey string, rerankRequest RerankRequest) (RerankResponse, error) {
	if strings.TrimSpace(rerankRequest.Model) == "" || strings.TrimSpace(rerankRequest.Query) == "" || len(rerankRequest.Documents) == 0 {
		return RerankResponse{}, fmt.Errorf("rerank model, query, and documents are required")
	}
	if len(rerankRequest.Documents) > 500 {
		return RerankResponse{}, fmt.Errorf("rerank documents exceed 500 candidates")
	}
	if rerankRequest.TopN <= 0 || rerankRequest.TopN > len(rerankRequest.Documents) {
		return RerankResponse{}, fmt.Errorf("rerank top_n must be between 1 and document count")
	}

	endpoint, err := apiEndpoint(baseURL, "reranks", c.allowedHosts)
	if err != nil {
		return RerankResponse{}, err
	}
	body, err := json.Marshal(rerankRequest)
	if err != nil {
		return RerankResponse{}, fmt.Errorf("encode rerank request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return RerankResponse{}, fmt.Errorf("create rerank request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return RerankResponse{}, fmt.Errorf("request rerank endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return RerankResponse{}, fmt.Errorf("rerank endpoint returned HTTP %d", response.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, maxRerankResponseBytes+1))
	if err != nil {
		return RerankResponse{}, fmt.Errorf("read rerank response: %w", err)
	}
	if len(body) > maxRerankResponseBytes {
		return RerankResponse{}, fmt.Errorf("rerank response is too large")
	}
	var payload struct {
		Results []RerankResult `json:"results"`
		Usage   *TokenUsage    `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return RerankResponse{}, fmt.Errorf("decode rerank response: %w", err)
	}
	if len(payload.Results) == 0 {
		return RerankResponse{}, fmt.Errorf("rerank response does not contain results")
	}
	seen := make(map[int]struct{}, len(payload.Results))
	for _, result := range payload.Results {
		if result.Index < 0 || result.Index >= len(rerankRequest.Documents) {
			return RerankResponse{}, fmt.Errorf("rerank response index %d is out of range", result.Index)
		}
		if _, ok := seen[result.Index]; ok {
			return RerankResponse{}, fmt.Errorf("rerank response contains duplicate index %d", result.Index)
		}
		seen[result.Index] = struct{}{}
	}
	return RerankResponse{Results: payload.Results, Usage: payload.Usage}, nil
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
		Usage   *TokenUsage `json:"usage"`
		Choices []struct {
			Message ChatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response does not contain a message")
	}
	message := payload.Choices[0].Message
	if message.Content == "" && len(message.ToolCalls) == 0 {
		return ChatResponse{}, fmt.Errorf("chat response does not contain a message")
	}
	for _, toolCall := range message.ToolCalls {
		if err := validateToolCall(toolCall); err != nil {
			return ChatResponse{}, fmt.Errorf("chat response contains invalid tool call: %w", err)
		}
	}
	return ChatResponse{Message: message.Content, ReasoningContent: message.ReasoningContent, ToolCalls: message.ToolCalls, Usage: payload.Usage}, nil
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
	payload := ocrChatRequest{
		Model: ocrRequest.Model,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionContent{
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
	if len(chatRequest.Tools) > 0 {
		return fmt.Errorf("chat stream tool calls are not supported")
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
	if chatRequest.MaxCompletionTokens < 0 {
		return fmt.Errorf("max completion tokens must not be negative")
	}
	for _, message := range chatRequest.Messages {
		if strings.TrimSpace(message.Role) == "" {
			return fmt.Errorf("chat message role is required")
		}
		if message.Content == "" && len(message.ToolCalls) == 0 {
			return fmt.Errorf("chat message content is required unless it contains tool calls")
		}
		for _, toolCall := range message.ToolCalls {
			if err := validateToolCall(toolCall); err != nil {
				return fmt.Errorf("chat message contains invalid tool call: %w", err)
			}
		}
	}
	for _, tool := range chatRequest.Tools {
		if tool.Type != "function" {
			return fmt.Errorf("chat tool type must be function")
		}
		if strings.TrimSpace(tool.Function.Name) == "" {
			return fmt.Errorf("chat tool function name is required")
		}
		if !validToolSchema(tool.Function.Parameters) {
			return fmt.Errorf("chat tool function parameters must be an object JSON Schema")
		}
	}
	return nil
}

func validateToolCall(toolCall ToolCall) error {
	if strings.TrimSpace(toolCall.ID) == "" {
		return fmt.Errorf("tool call ID is required")
	}
	if toolCall.Type != "function" {
		return fmt.Errorf("tool call type must be function")
	}
	if strings.TrimSpace(toolCall.Function.Name) == "" {
		return fmt.Errorf("tool call function name is required")
	}
	if !validToolArguments(json.RawMessage(toolCall.Function.Arguments)) {
		return fmt.Errorf("tool call arguments must be an object JSON value")
	}
	return nil
}

func validToolSchema(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return false
	}
	schemaType, ok := object["type"]
	if !ok {
		return false
	}
	var value string
	if err := json.Unmarshal(schemaType, &value); err != nil || value != "object" {
		return false
	}
	return true
}

func validToolArguments(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return false
	}
	return true
}

func apiEndpoint(baseURL, resource string, allowedHosts map[string]struct{}) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid model provider base URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return "", fmt.Errorf("model provider base URL must use HTTPS")
	}
	if _, allowed := allowedHosts[strings.ToLower(parsed.Hostname())]; !allowed {
		return "", fmt.Errorf("model provider base URL host is not allowed")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + resource
	parsed.RawQuery = ""
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
