package mcp

import "encoding/json"

// ProtocolVersion is the stateless MCP revision used by this adapter.
// The adapter also accepts the older initialize handshake for compatibility.
const ProtocolVersion = "2026-07-28"

const legacyProtocolVersion = "2025-06-18"

const (
	jsonRPCVersion         = "2.0"
	parseErrorCode         = -32700
	invalidRequestCode     = -32600
	methodNotFoundCode     = -32601
	invalidParamsCode      = -32602
	internalErrorCode      = -32603
	headerMismatchCode     = -32020
	unsupportedVersionCode = -32022
	maxRequestBodyBytes    = 64 * 1024
	defaultServerName      = "n2sql-agent"
	defaultServerVersion   = "0.1.0"
	protocolVersionMeta    = "io.modelcontextprotocol/protocolVersion"
	clientInfoMeta         = "io.modelcontextprotocol/clientInfo"
	clientCapabilitiesMeta = "io.modelcontextprotocol/clientCapabilities"
	serverInfoMeta         = "io.modelcontextprotocol/serverInfo"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ToolDefinition is the subset of an MCP tool definition exposed by this
// adapter. InputSchema is kept as JSON because it is supplied by the tool.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ContentBlock is the text-only content shape used by tools/call.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolCallResult is the normalized result returned by an MCP tool call.
type ToolCallResult struct {
	ResultType        string         `json:"resultType,omitempty"`
	Content           []ContentBlock `json:"content"`
	StructuredContent map[string]any `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

// Discovery describes the server capabilities returned by server/discover.
type Discovery struct {
	ResultType        string         `json:"resultType"`
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      map[string]any `json:"capabilities"`
	ServerInfo        ServerInfo     `json:"serverInfo"`
	Instructions      string         `json:"instructions,omitempty"`
	Meta              map[string]any `json:"_meta,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
