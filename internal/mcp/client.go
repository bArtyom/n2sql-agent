package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Client is a small stateless MCP HTTP client for discovery and tool calls.
// It sends the protocol and routing headers required by the current revision.
type Client struct {
	endpoint string
	http     *http.Client
	nextID   atomic.Uint64
}

func NewClient(endpoint string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid MCP endpoint")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("MCP endpoint must use HTTP or HTTPS")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), http: httpClient}, nil
}

func (c *Client) Discover(ctx context.Context) (Discovery, error) {
	var discovery Discovery
	if err := c.call(ctx, "server/discover", "", nil, &discovery); err != nil {
		return Discovery{}, fmt.Errorf("discover MCP server: %w", err)
	}
	return discovery, nil
}

func (c *Client) ListTools(ctx context.Context) ([]ToolDefinition, error) {
	var result struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := c.call(ctx, "tools/list", "", nil, &result); err != nil {
		return nil, fmt.Errorf("list MCP tools: %w", err)
	}
	return result.Tools, nil
}

func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (ToolCallResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ToolCallResult{}, errors.New("MCP tool name is required")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	var result ToolCallResult
	if err := c.call(ctx, "tools/call", name, map[string]any{
		"name":      name,
		"arguments": arguments,
	}, &result); err != nil {
		return ToolCallResult{}, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method, toolName string, params any, destination any) error {
	if c == nil || c.http == nil || c.endpoint == "" {
		return errors.New("MCP client is unavailable")
	}
	requestID := c.nextID.Add(1)
	paramsRaw, err := marshalParams(params)
	if err != nil {
		return fmt.Errorf("encode MCP params: %w", err)
	}
	body, err := json.Marshal(rpcRequest{JSONRPC: jsonRPCVersion, ID: json.RawMessage(fmt.Sprintf("%d", requestID)), Method: method, Params: paramsRaw})
	if err != nil {
		return fmt.Errorf("encode MCP request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create MCP request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	request.Header.Set("Mcp-Method", method)
	if toolName != "" {
		request.Header.Set("Mcp-Name", toolName)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("send MCP request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBodyBytes))
	if err != nil {
		return fmt.Errorf("read MCP response: %w", err)
	}
	var envelope rpcResponse
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return fmt.Errorf("decode MCP response: %w", err)
	}
	expectedID := json.RawMessage(fmt.Sprintf("%d", requestID))
	if envelope.JSONRPC != jsonRPCVersion || !bytes.Equal(bytes.TrimSpace(envelope.ID), expectedID) {
		return errors.New("invalid MCP response envelope")
	}
	if envelope.Error != nil {
		return RPCError{Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("MCP server returned HTTP %d", response.StatusCode)
	}
	if destination == nil || len(envelope.Result) == 0 {
		return nil
	}
	return json.Unmarshal(envelope.Result, destination)
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// RPCError is a JSON-RPC protocol-level failure returned by an MCP server.
type RPCError struct {
	Code    int
	Message string
}

func (e RPCError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("MCP RPC error %d", e.Code)
	}
	return fmt.Sprintf("MCP RPC error %d: %s", e.Code, e.Message)
}
