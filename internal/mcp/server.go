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
	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

// NewKnowledgeBaseHandler exposes a stateless, HTTP JSON-RPC MCP endpoint.
// The knowledge-base ID is taken from the route and is never accepted from
// tool arguments, so every request receives a scoped read-only tool.
func NewKnowledgeBaseHandler(searcher retrieval.Searcher, documents document.Reader, knowledgeBases knowledgebase.Store, maxResultBytes int) http.Handler {
	if maxResultBytes < 2 {
		maxResultBytes = agent.DefaultMaxToolResultBytes
	}
	return &knowledgeBaseHandler{searcher: searcher, documents: documents, knowledgeBases: knowledgeBases, maxResultBytes: maxResultBytes}
}

type knowledgeBaseHandler struct {
	searcher       retrieval.Searcher
	documents      document.Reader
	knowledgeBases knowledgebase.Store
	maxResultBytes int
}

var documentListParameters = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "additionalProperties": false,
  "description": "列出当前知识库中的文档及处理状态"
}`)

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
		code := invalidRequestCode
		var protocolErr protocolError
		if errors.As(err, &protocolErr) {
			code = protocolErr.code
		}
		writeRPCError(w, http.StatusBadRequest, request.ID, code, err.Error())
		return
	}
	if len(request.ID) == 0 {
		if strings.HasPrefix(request.Method, "notifications/") {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCError(w, http.StatusBadRequest, json.RawMessage("null"), invalidRequestCode, "request id is required")
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
			Instructions:      "This server exposes read-only, knowledge-base-scoped search and document listing tools.",
			Meta:              map[string]any{serverInfoMeta: ServerInfo{Name: defaultServerName, Version: defaultServerVersion}},
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
		return protocolError{code: unsupportedVersionCode, message: fmt.Sprintf("unsupported MCP protocol version %q", version)}
	}
	metadata, err := requestMetadata(request.Params)
	var bodyVersion string
	if err != nil || json.Unmarshal(metadata[protocolVersionMeta], &bodyVersion) != nil || strings.TrimSpace(bodyVersion) != version {
		return protocolError{code: headerMismatchCode, message: "MCP request metadata does not match protocol header"}
	}
	if _, ok := metadata[clientInfoMeta]; !ok {
		return protocolError{code: headerMismatchCode, message: "MCP request metadata is missing client info"}
	}
	if _, ok := metadata[clientCapabilitiesMeta]; !ok {
		return protocolError{code: headerMismatchCode, message: "MCP request metadata is missing client capabilities"}
	}
	if method := strings.TrimSpace(r.Header.Get("Mcp-Method")); method == "" || method != request.Method {
		return protocolError{code: headerMismatchCode, message: "Mcp-Method header does not match JSON-RPC method"}
	}
	if request.Method == "tools/call" {
		name := strings.TrimSpace(r.Header.Get("Mcp-Name"))
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || name == "" || name != params.Name {
			return protocolError{code: headerMismatchCode, message: "Mcp-Name header does not match tool name"}
		}
	}
	return nil
}

type protocolError struct {
	code    int
	message string
}

func (e protocolError) Error() string { return e.message }

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
	if err := h.authorizeKnowledgeBase(request.Context(), knowledgeBaseID); err != nil {
		return nil, err
	}
	tool, err := agent.NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(h.searcher, knowledgeBaseID, h.maxResultBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"resultType": "complete",
		"tools": []ToolDefinition{{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: tool.Parameters(),
		}, {
			Name:        "document_list",
			Description: "列出当前知识库中的文档及处理状态",
			InputSchema: append(json.RawMessage(nil), documentListParameters...),
		}},
	}, nil
}

func (h *knowledgeBaseHandler) callTool(ctx context.Context, request *http.Request, raw json.RawMessage) (ToolCallResult, error) {
	knowledgeBaseID, err := routeKnowledgeBaseID(request)
	if err != nil {
		return ToolCallResult{}, err
	}
	if err := h.authorizeKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return ToolCallResult{}, err
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil || strings.TrimSpace(params.Name) == "" {
		return ToolCallResult{}, errInvalidParams
	}
	arguments := params.Arguments
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage(`{}`)
	}
	if params.Name == "document_list" {
		if err := validateEmptyArguments(arguments); err != nil {
			return ToolCallResult{}, err
		}
		if h.documents == nil {
			return ToolCallResult{}, errors.New("MCP document reader is unavailable")
		}
		documents, err := h.documents.List(ctx, knowledgeBaseID)
		if err != nil {
			slog.ErrorContext(ctx, "mcp_tool_call_failed", "tool_name", params.Name, "knowledge_base_id", knowledgeBaseID, "error_kind", "document_list_failed")
			return ToolCallResult{ResultType: "complete", Content: []ContentBlock{{Type: "text", Text: "document listing failed"}}, IsError: true}, nil
		}
		encoded, err := json.Marshal(documents)
		if err != nil {
			return ToolCallResult{}, fmt.Errorf("encode document list: %w", err)
		}
		return ToolCallResult{
			ResultType:        "complete",
			Content:           []ContentBlock{{Type: "text", Text: string(encoded)}},
			StructuredContent: map[string]any{"documents": documents},
		}, nil
	}
	if params.Name != "knowledge_search" {
		return ToolCallResult{}, fmt.Errorf("%w: unknown tool", errInvalidParams)
	}
	tool, err := agent.NewKnowledgeSearchToolForKnowledgeBaseWithMaxBytes(h.searcher, knowledgeBaseID, h.maxResultBytes)
	if err != nil {
		return ToolCallResult{}, err
	}
	toolResult, err := tool.Call(ctx, arguments)
	if err != nil {
		slog.ErrorContext(ctx, "mcp_tool_call_failed", "tool_name", params.Name, "knowledge_base_id", knowledgeBaseID, "error_kind", classifyToolError(err))
		message := "knowledge search failed"
		if errors.Is(err, agent.ErrInvalidKnowledgeSearchInput) {
			message = "invalid knowledge search arguments"
		}
		return ToolCallResult{ResultType: "complete", Content: []ContentBlock{{Type: "text", Text: message}}, IsError: true}, nil
	}
	structured := make(map[string]any, len(toolResult.Metadata))
	for key, value := range toolResult.Metadata {
		structured[key] = value
	}
	return ToolCallResult{
		ResultType:        "complete",
		Content:           []ContentBlock{{Type: "text", Text: toolResult.Content}},
		StructuredContent: structured,
		IsError:           false,
	}, nil
}

func validateEmptyArguments(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arguments map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&arguments); err != nil {
		return errInvalidParams
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errInvalidParams
	}
	if len(arguments) != 0 {
		return errInvalidParams
	}
	return nil
}

func (h *knowledgeBaseHandler) authorizeKnowledgeBase(ctx context.Context, id int64) error {
	if h.knowledgeBases == nil {
		return errors.New("MCP knowledge base scope is unavailable")
	}
	knowledgeBases, err := h.knowledgeBases.List(ctx)
	if err != nil {
		return fmt.Errorf("check MCP knowledge base scope: %w", err)
	}
	for _, knowledgeBase := range knowledgeBases {
		if knowledgeBase.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%w: %d", errInvalidKnowledgeBase, id)
}

func classifyToolError(err error) string {
	if errors.Is(err, agent.ErrInvalidKnowledgeSearchInput) {
		return "invalid_arguments"
	}
	return "knowledge_search_failed"
}

func requestMetaVersion(raw json.RawMessage) string {
	metadata, err := requestMetadata(raw)
	if err != nil {
		return ""
	}
	var version string
	if err := json.Unmarshal(metadata[protocolVersionMeta], &version); err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}

func requestMetadata(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var params struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(raw) == 0 {
		return nil, errors.New("missing request metadata")
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Meta == nil {
		return nil, errors.New("invalid request metadata")
	}
	return params.Meta, nil
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
