package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

// NewKnowledgeBaseHandler exposes a stateless, HTTP JSON-RPC MCP endpoint.
// The knowledge-base ID is taken from the route and is never accepted from
// tool arguments, so every request receives a scoped read-only tool.
func NewKnowledgeBaseHandler(searcher retrieval.Searcher, maxResultBytes int) http.Handler {
	if maxResultBytes < 2 {
		maxResultBytes = agent.DefaultMaxToolResultBytes
	}
	return &knowledgeBaseHandler{searcher: searcher, maxResultBytes: maxResultBytes}
}

type knowledgeBaseHandler struct {
	searcher       retrieval.Searcher
	maxResultBytes int
}

func (h *knowledgeBaseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	request, err := decodeRPCRequest(w, r)
	if err != nil {
		status := http.StatusBadRequest
		code := parseErrorCode
		message := "invalid JSON-RPC request"
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			status = http.StatusRequestEntityTooLarge
			message = "MCP request is too large"
		} else if !errors.Is(err, errMalformedJSON) {
			code = invalidRequestCode
		}
		writeRPCError(w, status, json.RawMessage("null"), code, message)
		return
	}
	if request.JSONRPC != jsonRPCVersion || strings.TrimSpace(request.Method) == "" {
		writeRPCError(w, http.StatusBadRequest, request.ID, invalidRequestCode, "invalid JSON-RPC request")
		return
	}
	if err := validateRoutingHeaders(r, request); err != nil {
		writeRPCError(w, http.StatusBadRequest, request.ID, invalidRequestCode, err.Error())
		return
	}
	if len(request.ID) == 0 && strings.HasPrefix(request.Method, "notifications/") {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var result any
	switch request.Method {
	case "server/discover":
		result = Discovery{
			ResultType:        "complete",
			SupportedVersions: []string{ProtocolVersion, legacyProtocolVersion},
			Capabilities:      map[string]any{"tools": map[string]any{}},
			ServerInfo:        ServerInfo{Name: defaultServerName, Version: defaultServerVersion},
			Instructions:      "This server exposes a read-only, knowledge-base-scoped knowledge_search tool.",
		}
	case "initialize":
		result, err = initializeResult(request.Params)
		if err != nil {
			writeRPCError(w, http.StatusBadRequest, request.ID, invalidParamsCode, err.Error())
			return
		}
	case "tools/list":
		result, err = h.listTools(r)
		if err != nil {
			if errors.Is(err, errInvalidKnowledgeBase) {
				writeRPCError(w, http.StatusBadRequest, request.ID, invalidParamsCode, err.Error())
				return
			}
			writeRPCError(w, http.StatusInternalServerError, request.ID, internalErrorCode, "unable to list MCP tools")
			return
		}
	case "tools/call":
		result, err = h.callTool(r.Context(), r, request.Params)
		if err != nil {
			if errors.Is(err, errInvalidParams) || errors.Is(err, errInvalidKnowledgeBase) {
				writeRPCError(w, http.StatusBadRequest, request.ID, invalidParamsCode, err.Error())
				return
			}
			writeRPCError(w, http.StatusInternalServerError, request.ID, internalErrorCode, "unable to call MCP tool")
			return
		}
	default:
		writeRPCError(w, http.StatusNotFound, request.ID, methodNotFoundCode, "method not found")
		return
	}

	writeRPCResult(w, request.ID, result)
}

var (
	errMalformedJSON        = errors.New("malformed JSON")
	errInvalidParams        = errors.New("invalid MCP tool parameters")
	errInvalidKnowledgeBase = errors.New("invalid knowledge base ID")
)

func decodeRPCRequest(w http.ResponseWriter, r *http.Request) (rpcRequest, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	var request rpcRequest
	if err := decoder.Decode(&request); err != nil {
		if errors.Is(err, io.EOF) {
			return rpcRequest{}, fmt.Errorf("%w: empty body", errMalformedJSON)
		}
		return rpcRequest{}, fmt.Errorf("%w: %v", errMalformedJSON, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return rpcRequest{}, errors.New("multiple JSON values")
		}
		return rpcRequest{}, fmt.Errorf("multiple JSON values: %w", err)
	}
	return request, nil
}

func validateRoutingHeaders(r *http.Request, request rpcRequest) error {
	version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	if version == "" {
		return nil
	}
	if version != ProtocolVersion {
		return fmt.Errorf("unsupported MCP protocol version %q", version)
	}
	if method := strings.TrimSpace(r.Header.Get("Mcp-Method")); method == "" || method != request.Method {
		return errors.New("Mcp-Method header does not match JSON-RPC method")
	}
	if request.Method == "tools/call" {
		name := strings.TrimSpace(r.Header.Get("Mcp-Name"))
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || name == "" || name != params.Name {
			return errors.New("Mcp-Name header does not match tool name")
		}
	}
	return nil
}

func initializeResult(raw json.RawMessage) (map[string]any, error) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("decode initialize params: %w", errInvalidParams)
		}
	}
	version := legacyProtocolVersion
	if params.ProtocolVersion == ProtocolVersion {
		version = ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      ServerInfo{Name: defaultServerName, Version: defaultServerVersion},
	}, nil
}

func (h *knowledgeBaseHandler) listTools(request *http.Request) (map[string]any, error) {
	knowledgeBaseID, err := routeKnowledgeBaseID(request)
	if err != nil {
		return nil, err
	}
	tool, err := agent.NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(h.searcher, knowledgeBaseID, h.maxResultBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tools": []ToolDefinition{{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.Parameters(),
		}},
	}, nil
}

func (h *knowledgeBaseHandler) callTool(ctx context.Context, request *http.Request, raw json.RawMessage) (ToolCallResult, error) {
	knowledgeBaseID, err := routeKnowledgeBaseID(request)
	if err != nil {
		return ToolCallResult{}, err
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil || strings.TrimSpace(params.Name) == "" {
		return ToolCallResult{}, errInvalidParams
	}
	if params.Name != "knowledge_search" {
		return ToolCallResult{}, fmt.Errorf("%w: unknown tool", errInvalidParams)
	}
	arguments := params.Arguments
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage(`{}`)
	}
	tool, err := agent.NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(h.searcher, knowledgeBaseID, h.maxResultBytes)
	if err != nil {
		return ToolCallResult{}, err
	}
	toolResult, err := tool.Call(ctx, arguments)
	if err != nil {
		slog.ErrorContext(ctx, "mcp_tool_call_failed", "tool_name", params.Name, "knowledge_base_id", knowledgeBaseID, "error", err)
		message := "knowledge search failed"
		if errors.Is(err, agent.ErrInvalidKnowledgeSearchInput) {
			message = "invalid knowledge search arguments"
		}
		return ToolCallResult{Content: []ContentBlock{{Type: "text", Text: message}}, IsError: true}, nil
	}
	structured := make(map[string]any, len(toolResult.Metadata))
	for key, value := range toolResult.Metadata {
		structured[key] = value
	}
	return ToolCallResult{
		Content:           []ContentBlock{{Type: "text", Text: toolResult.Content}},
		StructuredContent: structured,
		IsError:           false,
	}, nil
}

func routeKnowledgeBaseID(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.PathValue("id"))
	knowledgeBaseID, err := strconv.ParseInt(value, 10, 64)
	if err != nil || knowledgeBaseID <= 0 {
		return 0, fmt.Errorf("%w: %q", errInvalidKnowledgeBase, value)
	}
	return knowledgeBaseID, nil
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		writeRPCError(w, http.StatusInternalServerError, id, internalErrorCode, "unable to encode MCP result")
		return
	}
	writeRPC(w, http.StatusOK, rpcResponse{JSONRPC: jsonRPCVersion, ID: responseID(id), Result: encoded})
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writeRPC(w, status, rpcResponse{JSONRPC: jsonRPCVersion, ID: responseID(id), Error: &rpcError{Code: code, Message: message}})
}

func writeRPC(w http.ResponseWriter, status int, response rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("mcp_response_write_failed", "error", err)
	}
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
