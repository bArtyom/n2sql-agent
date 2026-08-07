package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrInvalidTool           = errors.New("invalid tool")
	ErrInvalidToolParameters = errors.New("invalid tool parameters")
	ErrToolAlreadyRegistered = errors.New("tool already registered")
	ErrToolNotFound          = errors.New("tool not found")
)

// Tool is a capability that an Agent can discover and execute.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Call(context.Context, json.RawMessage) (ToolResult, error)
}

// ToolResult is the normalized result returned by a tool.
type ToolResult struct {
	Content  string         `json:"content"`
	Metadata map[string]any `json:"-"`
}

// FunctionDefinition describes a tool in the shape expected by model adapters.
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolRegistry stores the tools exposed to an Agent.
type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

func (r *ToolRegistry) Register(tool Tool) error {
	if tool == nil {
		return ErrInvalidTool
	}
	name := tool.Name()
	if name == "" || strings.TrimSpace(name) != name {
		return ErrInvalidTool
	}
	if !validFunctionParameters(tool.Parameters()) {
		return fmt.Errorf("%w: %s", ErrInvalidToolParameters, name)
	}
	if r.tools == nil {
		r.tools = make(map[string]Tool)
	}
	if _, exists := r.tools[name]; exists {
		return ErrToolAlreadyRegistered
	}
	r.tools[name] = tool
	return nil
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func (r *ToolRegistry) Find(name string) (Tool, error) {
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
	return tool, nil
}

func (r *ToolRegistry) List() []Tool {
	names := r.sortedToolNames()

	tools := make([]Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, r.tools[name])
	}
	return tools
}

func (r *ToolRegistry) FunctionDefinitions() []FunctionDefinition {
	names := r.sortedToolNames()

	definitions := make([]FunctionDefinition, 0, len(names))
	for _, name := range names {
		tool := r.tools[name]
		definitions = append(definitions, FunctionDefinition{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Parameters(),
		})
	}
	return definitions
}

func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validFunctionParameters(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(raw, &schema); err != nil {
		return false
	}
	var schemaType string
	if err := json.Unmarshal(schema["type"], &schemaType); err != nil {
		return false
	}
	return schemaType == "object"
}
