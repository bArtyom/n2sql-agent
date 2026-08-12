package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/document"
	"github.com/bArtyom/n2sql-agent/internal/knowledgebase"
	"github.com/bArtyom/n2sql-agent/internal/retrieval"
)

type searcherStub struct {
	knowledgeBaseID int64
	query           string
	limit           int
}

type failingSearcher struct{}

type documentReaderStub struct{}

func (documentReaderStub) List(context.Context, int64) ([]document.Document, error) {
	return []document.Document{{ID: 21, OriginalFilename: "guide.md", ProcessingStatus: "succeeded"}}, nil
}

func (failingSearcher) Search(context.Context, int64, string, int) ([]retrieval.Result, error) {
	return nil, errors.New("upstream provider secret detail")
}

type knowledgeBaseScopeStub struct {
	ids []int64
}

func (s knowledgeBaseScopeStub) Create(context.Context, knowledgebase.CreateInput) (knowledgebase.KnowledgeBase, error) {
	return knowledgebase.KnowledgeBase{}, errors.New("not implemented")
}

func (s knowledgeBaseScopeStub) List(context.Context) ([]knowledgebase.KnowledgeBase, error) {
	knowledgeBases := make([]knowledgebase.KnowledgeBase, 0, len(s.ids))
	for _, id := range s.ids {
		knowledgeBases = append(knowledgeBases, knowledgebase.KnowledgeBase{ID: id})
	}
	return knowledgeBases, nil
}

func (s knowledgeBaseScopeStub) Delete(context.Context, int64) error { return nil }

func (s *searcherStub) Search(_ context.Context, knowledgeBaseID int64, query string, limit int) ([]retrieval.Result, error) {
	s.knowledgeBaseID = knowledgeBaseID
	s.query = query
	s.limit = limit
	return []retrieval.Result{{DocumentID: 11, Content: "Go 使用 context 管理请求生命周期"}}, nil
}

func newTestServer(searcher retrieval.Searcher) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("POST /api/knowledge-bases/{id}/mcp", NewKnowledgeBaseHandler(searcher, documentReaderStub{}, knowledgeBaseScopeStub{ids: []int64{7, 42}}, 32*1024))
	return httptest.NewServer(mux)
}

func TestKnowledgeBaseHandlerDiscoversAndListsScopedTool(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	client, err := NewClient(server.URL+"/api/knowledge-bases/7/mcp", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	discovery, err := client.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(discovery.SupportedVersions) == 0 || discovery.SupportedVersions[0] != ProtocolVersion {
		t.Fatalf("supported versions = %#v, want %q", discovery.SupportedVersions, ProtocolVersion)
	}
	if _, ok := discovery.Meta[serverInfoMeta]; !ok {
		t.Fatalf("discovery metadata = %#v, want server info", discovery.Meta)
	}

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "knowledge_search" || tools[1].Name != "document_list" {
		t.Fatalf("tools = %#v, want knowledge_search and document_list", tools)
	}
	if string(tools[0].InputSchema) == "" || containsJSONField(tools[0].InputSchema, "knowledge_base_id") {
		t.Fatalf("scoped input schema = %s, should not expose knowledge_base_id", tools[0].InputSchema)
	}
}

func TestClientCallsScopedDocumentList(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	client, err := NewClient(server.URL+"/api/knowledge-bases/7/mcp", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.CallTool(context.Background(), "document_list", nil)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "guide.md") {
		t.Fatalf("document result = %#v, want guide.md", result)
	}
}

func TestClientCallsScopedKnowledgeSearch(t *testing.T) {
	searcher := &searcherStub{}
	server := newTestServer(searcher)
	defer server.Close()

	client, err := NewClient(server.URL+"/api/knowledge-bases/42/mcp", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.CallTool(context.Background(), "knowledge_search", map[string]any{
		"query": "如何管理请求生命周期？",
		"limit": 3,
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].Text == "" {
		t.Fatalf("tool result = %#v, want successful text result", result)
	}
	if searcher.knowledgeBaseID != 42 || searcher.query != "如何管理请求生命周期？" || searcher.limit != 3 {
		t.Fatalf("search call = (%d, %q, %d), want (42, query, 3)", searcher.knowledgeBaseID, searcher.query, searcher.limit)
	}
}

func TestKnowledgeBaseHandlerRejectsMismatchedRoutingHeader(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/knowledge-bases/7/mcp", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	request.Header.Set("Mcp-Method", "tools/call")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func TestKnowledgeBaseHandlerRejectsModernRequestWithoutMetadata(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/knowledge-bases/7/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	request.Header.Set("Mcp-Method", "tools/list")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	var envelope rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response = %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != headerMismatchCode {
		t.Fatalf("error = %#v, want header mismatch %d", envelope.Error, headerMismatchCode)
	}
}

func TestKnowledgeBaseHandlerRejectsNonNotificationWithoutID(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/knowledge-bases/7/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"server/discover"}`))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()
	var envelope rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response = %v", err)
	}
	if envelope.Error == nil || envelope.Error.Code != invalidRequestCode {
		t.Fatalf("error = %#v, want invalid request", envelope.Error)
	}
}

func TestClientReturnsToolFailureAsMCPResult(t *testing.T) {
	server := newTestServer(failingSearcher{})
	defer server.Close()

	client, err := NewClient(server.URL+"/api/knowledge-bases/42/mcp", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.CallTool(context.Background(), "knowledge_search", map[string]any{"query": "问题"})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	if !result.IsError || len(result.Content) != 1 || result.Content[0].Text != "knowledge search failed" {
		t.Fatalf("tool failure result = %#v, want sanitized MCP tool error", result)
	}
	if strings.Contains(result.Content[0].Text, "upstream provider") {
		t.Fatalf("tool failure leaked provider detail: %q", result.Content[0].Text)
	}
}

func TestKnowledgeBaseHandlerRejectsInvalidScope(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/knowledge-bases/not-a-number/mcp", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func TestKnowledgeBaseHandlerRejectsKnowledgeBaseOutsideCurrentAdmin(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	client, err := NewClient(server.URL+"/api/knowledge-bases/99/mcp", server.Client())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.ListTools(context.Background()); err == nil {
		t.Fatal("ListTools() error = nil, want scope rejection")
	}
}

func TestKnowledgeBaseHandlerSupportsLegacyInitialize(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	response, err := server.Client().Post(server.URL+"/api/knowledge-bases/7/mcp", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("Post() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var envelope struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response = %v", err)
	}
	if envelope.Result.ProtocolVersion != legacyProtocolVersion {
		t.Fatalf("protocol version = %q, want %q", envelope.Result.ProtocolVersion, legacyProtocolVersion)
	}
}

func TestKnowledgeBaseHandlerRejectsMismatchedBodyMetadata(t *testing.T) {
	server := newTestServer(&searcherStub{})
	defer server.Close()

	requestBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-06-18"}}}`
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/knowledge-bases/7/mcp", strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	request.Header.Set("Mcp-Method", "tools/list")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
}

func containsJSONField(raw json.RawMessage, field string) bool {
	var value struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value.Properties[field]
	return ok
}
