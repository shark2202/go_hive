package hive

import (
	"context"
	"errors"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

var (
	errNilLoop          = errors.New("hive: nil loop")
	errNilConversation  = errors.New("hive: nil conversation")
	errNilAgentContext  = errors.New("hive: nil agent context")
	errNilToolRegistry  = errors.New("hive: nil tool registry")
	errNilContext       = errors.New("hive: nil context")
	errMissingHome      = errors.New("hive: a home directory is required")
	errMissingColonyID  = errors.New("hive: a colony id is required")
	errMissingColonyDir = errors.New("hive: the colony directory does not exist")
	errMissingAgentSpec = errors.New("hive: an agent spec id is required")
	errMissingModel     = errors.New("hive: a model is required")
)

// Tool is one model-facing tool definition: a name, a description and a JSON
// Schema for its inputs.
//
// Parity: framework/llm/provider.py::Tool
type Tool struct {
	Name            string
	Description     string
	Parameters      map[string]any
	ProducesImage   bool
	ConcurrencySafe bool
}

// NewTool builds a tool definition without a parameter schema.
//
// Parity: framework/llm/provider.py::Tool
func NewTool(name, description string) Tool {
	return Tool{Name: name, Description: description}
}

// Schema renders the tool the way the runtime hands it to a provider.
//
// Parity: framework/llm/provider.py::Tool
func (t Tool) Schema() map[string]any {
	params := t.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"name":         t.Name,
		"description":  t.Description,
		"input_schema": params,
	}
}

func (t Tool) toLLM() llm.ToolDef {
	return llm.ToolDef{
		Name:            t.Name,
		Description:     t.Description,
		Parameters:      t.Parameters,
		ProducesImage:   t.ProducesImage,
		ConcurrencySafe: t.ConcurrencySafe,
	}
}

func (t Tool) toRegistry() tools.Tool {
	return tools.Tool{
		Name:            t.Name,
		Description:     t.Description,
		Parameters:      t.Parameters,
		ProducesImage:   t.ProducesImage,
		ConcurrencySafe: t.ConcurrencySafe,
	}
}

func toolFromLLM(def llm.ToolDef) Tool {
	return Tool{
		Name:            def.Name,
		Description:     def.Description,
		Parameters:      def.Parameters,
		ProducesImage:   def.ProducesImage,
		ConcurrencySafe: def.ConcurrencySafe,
	}
}

func toolFromRegistry(entry tools.Tool) Tool {
	return Tool{
		Name:            entry.Name,
		Description:     entry.Description,
		Parameters:      entry.Parameters,
		ProducesImage:   entry.ProducesImage,
		ConcurrencySafe: entry.ConcurrencySafe,
	}
}

func toolDefs(list []Tool) []llm.ToolDef {
	if list == nil {
		return nil
	}
	out := make([]llm.ToolDef, 0, len(list))
	for _, tool := range list {
		out = append(out, tool.toLLM())
	}
	return out
}

// ToolFunc is the implementation registered for a tool: it receives the
// model's arguments and returns the content the model sees.
//
// Parity: framework/loader/tool_registry.py tool executor
type ToolFunc func(inputs map[string]any) (any, error)

// ToolParam is one positional parameter declaration used by RegisterFunc.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.register_function
type ToolParam = tools.FuncParam

// ToolRegistry is the runtime's name-to-executor registry.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry
type ToolRegistry struct {
	inner *tools.ToolRegistry
}

// NewToolRegistry builds an empty registry.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{inner: tools.NewToolRegistry()}
}

// Register adds (or replaces) one tool and its executor.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.register
func (r *ToolRegistry) Register(tool Tool, fn ToolFunc) {
	if r == nil || r.inner == nil {
		return
	}
	r.inner.Register(tool.Name, tool.toRegistry(), tools.ToolExecutor(fn))
}

// RegisterFunc is Register for a Python-style positional function: params
// describe the argument names and types, and the runtime coerces the model's
// input before calling fn.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.register_function
func (r *ToolRegistry) RegisterFunc(name, description string, params []ToolParam, fn ToolFunc) {
	if r == nil || r.inner == nil {
		return
	}
	r.inner.RegisterFunction(name, description, params, tools.ToolExecutor(fn))
}

// Get returns one registered tool definition.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.get_tools
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	if r == nil || r.inner == nil {
		return Tool{}, false
	}
	entry, ok := r.inner.GetTool(name)
	if !ok {
		return Tool{}, false
	}
	return toolFromRegistry(entry), true
}

// Names lists every registered tool name.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.get_registered_names
func (r *ToolRegistry) Names() []string {
	if r == nil || r.inner == nil {
		return nil
	}
	return r.inner.RegisteredNames()
}

// Definitions lists every registered tool definition.
//
// Parity-exempt: Go-only projection over ToolRegistry.GetTools.
func (r *ToolRegistry) Definitions() []Tool {
	if r == nil || r.inner == nil {
		return nil
	}
	entries := r.inner.GetTools()
	out := make([]Tool, 0, len(entries))
	for _, entry := range entries {
		out = append(out, toolFromRegistry(entry))
	}
	return out
}

// Execute runs one tool use through the registry and returns the runtime's
// full result shape.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.get_executor
func (r *ToolRegistry) Execute(ctx context.Context, use ToolUse) (ToolResult, error) {
	if r == nil || r.inner == nil {
		return ToolResult{}, errNilToolRegistry
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := r.inner.GetExecutor().Execute(ctx, tools.ToolUse{
		ID:    use.ID,
		Name:  use.Name,
		Input: use.Input,
	})
	if err != nil {
		return ToolResult{}, err
	}
	return toolResultFromRegistry(result), nil
}

// LoopExecutor adapts the registry to a loop ToolExecutor. The loop's
// executor signature has no context, so execution uses context.Background();
// this is the same adapter the colony trigger pipeline uses.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry.get_executor
func (r *ToolRegistry) LoopExecutor() ToolExecutor {
	if r == nil || r.inner == nil {
		return nil
	}
	return func(use ToolUse) (ToolResult, error) {
		return r.Execute(context.Background(), use)
	}
}

func toolResultFromRegistry(result tools.ToolResult) ToolResult {
	return ToolResult{
		ToolUseID:      result.ToolUseID,
		Content:        result.Content,
		IsError:        result.IsError,
		ImageContent:   result.ImageContent,
		IsSkillContent: result.IsSkillContent,
		SpilloverPath:  result.SpilloverPath,
		Attachments:    result.Attachments,
	}
}
