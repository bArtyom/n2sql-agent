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
	ErrInvalidToolAllowlist  = errors.New("invalid tool allowlist")
	ErrToolAlreadyRegistered = errors.New("tool already registered")
	ErrToolNotAllowed        = errors.New("tool not allowed")
	ErrToolNotFound          = errors.New("tool not found")
)

// Tool is a capability that an Agent can discover and execute.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Call(context.Context, json.RawMessage) (ToolResult, error)
}

// ApprovalAwareTool optionally marks tools that can change state or trigger
// external side effects. Read-only tools do not need to implement it.
type ApprovalAwareTool interface {
	RequiresApproval() bool
}

// RetryableTool optionally declares whether a tool may be automatically
// retried after an ambiguous failure. Ordinary read-only tools are retryable
// by default. A tool that requires approval is non-retryable by default unless
// it explicitly proves that repeating the operation is safe (for example by
// using an idempotency key in the downstream system).
type RetryableTool interface {
	Retryable() bool
}

// ParallelSafeTool explicitly opts a tool into concurrent execution when the
// model returns multiple calls in one response. The default is false: a tool
// must prove that calls are independent and do not share mutable state.
type ParallelSafeTool interface {
	ParallelSafe() bool
}

// ToolResult is the normalized result returned by a tool.
type ToolResult struct {
	Content           string         `json:"content"`
	Metadata          map[string]any `json:"-"`
	NoRelevantResults bool           `json:"-"`
	FallbackAnswer    string         `json:"-"`
}

// FunctionDefinition describes a tool in the shape expected by model adapters.
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolRegistry stores the tools exposed to an Agent.
type ToolRegistry struct {
	tools        map[string]Tool
	allowedTools map[string]struct{}
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

// NewToolRegistryWithAllowlist creates a registry that only accepts the named
// tools. An empty allowlist is valid and creates a deny-all registry. The
// unrestricted NewToolRegistry remains available for low-level callers that
// intentionally assemble their own complete tool set.
func NewToolRegistryWithAllowlist(toolNames ...string) (*ToolRegistry, error) {
	allowedTools := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		if name == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf("%w: %q", ErrInvalidToolAllowlist, name)
		}
		allowedTools[name] = struct{}{}
	}
	return &ToolRegistry{
		tools:        make(map[string]Tool),
		allowedTools: allowedTools,
	}, nil
}

func (r *ToolRegistry) Register(tool Tool) error {
	if tool == nil {
		return ErrInvalidTool
	}
	name := tool.Name()
	if name == "" || strings.TrimSpace(name) != name {
		return ErrInvalidTool
	}
	if !r.isAllowed(name) {
		return fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
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

// AllowAndRegister adds a tool to a deliberately scoped registry. It is used
// by service composition code when a registry was created with an allowlist
// and an optional, read-only capability is enabled for that request.
func (r *ToolRegistry) AllowAndRegister(tool Tool) error {
	if r == nil || tool == nil {
		return ErrInvalidTool
	}
	if r.allowedTools != nil {
		r.allowedTools[tool.Name()] = struct{}{}
	}
	return r.Register(tool)
}

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// SetKnowledgeSearchFolderScope applies a request-level folder boundary to
// the read-only knowledge_search tool. The model cannot widen this scope from
// its function arguments; it is fixed by the user's Chat/Agent request.
func (r *ToolRegistry) SetKnowledgeSearchFolderScope(folderPath *string, recursive bool) error {
	return r.SetFolderScope(folderPath, recursive)
}

// SetFolderScope applies a request-level folder boundary to every registered
// tool that can expose knowledge-base documents. Tools without this optional
// capability remain unchanged.
func (r *ToolRegistry) SetFolderScope(folderPath *string, recursive bool) error {
	if r == nil {
		return ErrInvalidTool
	}
	found := false
	for _, tool := range r.tools {
		scoped, ok := tool.(interface {
			SetFolderScope(*string, bool)
		})
		if !ok {
			continue
		}
		scoped.SetFolderScope(folderPath, recursive)
		found = true
	}
	if !found {
		return ErrToolNotFound
	}
	return nil
}

// SetTagScope applies a request-level tag boundary to every registered
// knowledge-base tool that supports it.
func (r *ToolRegistry) SetTagScope(tagIDs []int64) error {
	if r == nil {
		return ErrInvalidTool
	}
	found := false
	for _, tool := range r.tools {
		scoped, ok := tool.(interface{ SetTagScope([]int64) error })
		if !ok {
			continue
		}
		if err := scoped.SetTagScope(tagIDs); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return ErrToolNotFound
	}
	return nil
}

func (r *ToolRegistry) Find(name string) (Tool, error) {
	if !r.isAllowed(name) {
		return nil, fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
	}
	tool, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
	return tool, nil
}

// RequiresApproval reports whether a registered tool should pass through the
// user approval gate. The safe default is false for existing read-only tools.
func (r *ToolRegistry) RequiresApproval(name string) bool {
	tool, ok := r.Get(name)
	if !ok {
		return false
	}
	aware, ok := tool.(ApprovalAwareTool)
	return ok && aware.RequiresApproval()
}

// Retryable reports whether the Agent may feed a failed call back to the
// model for another automatic attempt. Unknown tools are not retryable.
func (r *ToolRegistry) Retryable(name string) bool {
	tool, ok := r.Get(name)
	if !ok {
		return false
	}
	if retryable, ok := tool.(RetryableTool); ok {
		return retryable.Retryable()
	}
	return !r.RequiresApproval(name)
}

// ParallelSafe reports whether a tool has explicitly opted into same-turn
// parallel execution. Unknown tools and tools without the marker are kept
// sequentially for safety.
func (r *ToolRegistry) ParallelSafe(name string) bool {
	tool, ok := r.Get(name)
	if !ok {
		return false
	}
	parallel, ok := tool.(ParallelSafeTool)
	return ok && parallel.ParallelSafe()
}

func (r *ToolRegistry) isAllowed(name string) bool {
	if r == nil || r.allowedTools == nil {
		return r != nil
	}
	_, ok := r.allowedTools[name]
	return ok
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
