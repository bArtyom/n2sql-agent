package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

type catalogTool struct{ name, description string }

func (t catalogTool) Name() string        { return t.name }
func (t catalogTool) Description() string { return t.description }
func (t catalogTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t catalogTool) Call(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "ok"}, nil
}

func TestToolCatalogDiscoversMetadataBeforeResolvingImplementation(t *testing.T) {
	registry := agent.NewToolRegistry()
	if err := registry.Register(catalogTool{name: "knowledge_search", description: "search documents"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(catalogTool{name: "document_read", description: "read one document"}); err != nil {
		t.Fatal(err)
	}
	var catalog agent.ToolCatalog = registry
	descriptors := catalog.Discover("search", 5)
	if len(descriptors) != 1 || descriptors[0].Name != "knowledge_search" || descriptors[0].Tool != nil {
		t.Fatalf("descriptors = %#v, want metadata-only discovery", descriptors)
	}
	resolved, err := catalog.Resolve("knowledge_search")
	if err != nil || resolved == nil || resolved.Name() != "knowledge_search" {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
}
