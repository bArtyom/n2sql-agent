package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bArtyom/n2sql-agent/internal/agent"
)

type stubTool struct {
	name string
}

type invalidDefinitionTool struct {
	stubTool
	parameters json.RawMessage
}

func (t invalidDefinitionTool) Parameters() json.RawMessage {
	return t.parameters
}

func (t stubTool) Name() string {
	return t.name
}

func (t stubTool) Description() string {
	return "测试工具"
}

func (t stubTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t stubTool) Call(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: "ok"}, nil
}

func TestToolRegistryReturnsRegisteredTool(t *testing.T) {
	registry := agent.NewToolRegistry()
	tool := stubTool{name: "knowledge_search"}

	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	got, ok := registry.Get("knowledge_search")
	if !ok {
		t.Fatal("Get() found = false, want true")
	}
	if got.Name() != tool.Name() {
		t.Fatalf("Get() name = %q, want %q", got.Name(), tool.Name())
	}
}

func TestToolRegistryRejectsDuplicateTool(t *testing.T) {
	registry := agent.NewToolRegistry()

	if err := registry.Register(stubTool{name: "knowledge_search"}); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	err := registry.Register(stubTool{name: "knowledge_search"})
	if !errors.Is(err, agent.ErrToolAlreadyRegistered) {
		t.Fatalf("second Register() error = %v, want %v", err, agent.ErrToolAlreadyRegistered)
	}
}

func TestToolRegistryRejectsInvalidTool(t *testing.T) {
	cases := []struct {
		name string
		tool agent.Tool
	}{
		{name: "nil tool", tool: nil},
		{name: "empty name", tool: stubTool{name: ""}},
		{name: "whitespace name", tool: stubTool{name: "  "}},
		{name: "leading whitespace", tool: stubTool{name: " knowledge_search"}},
		{name: "trailing whitespace", tool: stubTool{name: "knowledge_search "}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registry := agent.NewToolRegistry()
			if err := registry.Register(test.tool); !errors.Is(err, agent.ErrInvalidTool) {
				t.Fatalf("Register() error = %v, want %v", err, agent.ErrInvalidTool)
			}
		})
	}
}

func TestToolRegistryReportsMissingTool(t *testing.T) {
	registry := agent.NewToolRegistry()

	_, err := registry.Find("missing_tool")
	if !errors.Is(err, agent.ErrToolNotFound) {
		t.Fatalf("Find() error = %v, want %v", err, agent.ErrToolNotFound)
	}
}

func TestToolRegistryListsToolsInNameOrder(t *testing.T) {
	registry := agent.NewToolRegistry()
	for _, tool := range []stubTool{
		{name: "document_list"},
		{name: "knowledge_search"},
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("Register(%q) error = %v", tool.Name(), err)
		}
	}

	tools := registry.List()
	if len(tools) != 2 {
		t.Fatalf("List() length = %d, want 2", len(tools))
	}
	if tools[0].Name() != "document_list" || tools[1].Name() != "knowledge_search" {
		t.Fatalf("List() names = %q, %q, want document_list, knowledge_search", tools[0].Name(), tools[1].Name())
	}
}

func TestToolRegistryListsFunctionDefinitionsInNameOrder(t *testing.T) {
	registry := agent.NewToolRegistry()
	for _, tool := range []stubTool{
		{name: "knowledge_search"},
		{name: "document_list"},
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("Register(%q) error = %v", tool.Name(), err)
		}
	}

	definitions := registry.FunctionDefinitions()
	if len(definitions) != 2 {
		t.Fatalf("FunctionDefinitions() length = %d, want 2", len(definitions))
	}
	if definitions[0].Name != "document_list" || definitions[1].Name != "knowledge_search" {
		t.Fatalf("definition names = %q, %q, want document_list, knowledge_search", definitions[0].Name, definitions[1].Name)
	}
	if string(definitions[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("first definition parameters = %s", definitions[0].Parameters)
	}
}

func TestToolRegistryRejectsInvalidFunctionDefinition(t *testing.T) {
	cases := []struct {
		name       string
		parameters json.RawMessage
	}{
		{name: "empty schema", parameters: nil},
		{name: "malformed schema", parameters: json.RawMessage(`{"type":`)},
		{name: "non object schema", parameters: json.RawMessage(`[]`)},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			registry := agent.NewToolRegistry()
			err := registry.Register(invalidDefinitionTool{
				stubTool:   stubTool{name: "invalid_tool"},
				parameters: test.parameters,
			})
			if !errors.Is(err, agent.ErrInvalidToolParameters) {
				t.Fatalf("Register() error = %v, want %v", err, agent.ErrInvalidToolParameters)
			}
		})
	}
}
