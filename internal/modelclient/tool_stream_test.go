package modelclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/modelclient"
)

func TestHTTPClientStreamsTextAndAssemblesSplitToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"先\"}}]}\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"knowledge_search","arguments":"{\"query\":\""}}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"年假\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"答案\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := modelclient.NewHTTPClient(server.Client(), []string{parsed.Hostname()})
	var deltas []modelclient.ChatStreamDelta
	err = client.ChatStreamWithTools(context.Background(), server.URL+"/v1", "secret", modelclient.ChatRequest{
		Model:    "test-model",
		Messages: []modelclient.ChatMessage{{Role: "user", Content: "年假"}},
		Tools:    []modelclient.ToolDefinition{{Type: "function", Function: modelclient.FunctionDefinition{Name: "knowledge_search", Parameters: []byte(`{"type":"object"}`)}}},
	}, func(delta modelclient.ChatStreamDelta) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatStreamWithTools() error = %v", err)
	}
	if len(deltas) != 4 || deltas[0].Content != "先" || deltas[3].Content != "答案" {
		t.Fatalf("deltas = %#v, want text and tool fragments", deltas)
	}
	if deltas[1].ToolCallID != "call-1" || deltas[1].ToolName != "knowledge_search" || deltas[1].ToolArguments+deltas[2].ToolArguments != `{"query":"年假` {
		t.Fatalf("tool deltas = %#v, want split tool call", deltas)
	}
}
