package integrationkit

// Registry is the generic tool registry: catalog entries, declarative REST
// specs and hand-written implementations, plus the dispatch rules that turn a
// model tool call into one action execution.
//
// The registry knows nothing about individual providers. The integrations root
// package constructs it (NewRegistry), adds the generated catalog
// (AddTool), attaches declarative specs (AddSpecs) and implementations
// (AddImpl), and publishes the result into internal/tools (Install). Domain
// subpackages only receive *Registry to register themselves, so they never
// depend on the root package or on internal/tools.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aden-hive/hive-go/internal/tools"
)

var (
	// ErrUnknownTool is returned when no integration owns the requested name.
	ErrUnknownTool = errors.New("integrations: unknown tool")
	// ErrUnknownAction is returned when a tool has no such action.
	ErrUnknownAction = errors.New("integrations: unknown action")
)

// Registry owns the ported tools and their execution specs.
type Registry struct {
	runtime *Runtime
	tools   map[string]*Tool
	order   []string
	specs   map[string]map[string]RestSpec
	impls   map[string]map[string]ActionFunc
}

// NewRegistry returns an empty registry bound to rt. It never returns nil.
func NewRegistry(rt *Runtime) *Registry {
	return &Registry{
		runtime: rt,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
}

// Runtime exposes the configured runtime (clients, roots, clock).
func (r *Registry) Runtime() *Runtime { return r.runtime }

// AddTool appends one catalog tool in registration order. A duplicate name
// replaces the map entry and appends the name again, matching the historical
// in-package construction exactly; assembly code owns uniqueness.
func (r *Registry) AddTool(tool *Tool) {
	if tool == nil {
		return
	}
	r.tools[tool.Name] = tool
	r.order = append(r.order, tool.Name)
}

// AddSpecs attaches declarative REST specs for one tool.
func (r *Registry) AddSpecs(tool string, specs map[string]RestSpec) {
	if r.specs[tool] == nil {
		r.specs[tool] = map[string]RestSpec{}
	}
	for action, spec := range specs {
		r.specs[tool][action] = spec
	}
}

// AddImpl attaches one hand-written action implementation.
func (r *Registry) AddImpl(tool, action string, fn ActionFunc) {
	if r.impls[tool] == nil {
		r.impls[tool] = map[string]ActionFunc{}
	}
	r.impls[tool][action] = fn
}

// Names returns the registered tool names in catalog order.
func (r *Registry) Names() []string { return append([]string(nil), r.order...) }

// Tools returns the registered tools in catalog order.
func (r *Registry) Tools() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// Lookup finds a tool by registry name.
func (r *Registry) Lookup(name string) (*Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// Impls exposes the implementations registered for one tool. It exists for
// tests and diagnostics; callers must not mutate the returned map.
func (r *Registry) Impls(tool string) map[string]ActionFunc { return r.impls[tool] }

// Specs exposes the REST specs registered for one tool. It exists for tests
// and diagnostics; callers must not mutate the returned map.
func (r *Registry) Specs(tool string) map[string]RestSpec { return r.specs[tool] }

// ActionCount returns the number of executable actions (one per upstream
// MCP tool function) across all registered tools.
func (r *Registry) ActionCount() int {
	total := 0
	for _, tool := range r.tools {
		total += len(tool.Actions)
	}
	return total
}

// Execute runs one tool action. Expected failures are values, not errors.
func (r *Registry) Execute(ctx context.Context, toolName string, input map[string]any) (any, error) {
	// Standalone callers (tests, adapters) carry no session view; the runtime's
	// own WithAccountSource, if any, still applies inside execute.
	return r.execute(ctx, nil, toolName, input)
}

// execute is Execute plus the session's credential view. The view travels
// separately from the runtime because it is attached to the shared registry the
// session owns (tools.ToolRegistry.SetAccountSource), not to this batch.
func (r *Registry) execute(ctx context.Context, source AccountSource, toolName string, input map[string]any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tool, ok := r.tools[toolName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownTool, toolName)
	}
	if input == nil {
		input = map[string]any{}
	}
	actionName := StringArg(input, "action")
	if actionName == "" {
		if len(tool.Actions) != 1 {
			return map[string]any{
				"error":   fmt.Sprintf("%s: action is required", toolName),
				"actions": tool.ActionNames(),
			}, nil
		}
		actionName = tool.Actions[0].Name
	}
	action, ok := tool.Action(actionName)
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s", ErrUnknownAction, toolName, actionName)
	}
	// The session's credential view (attached to the shared registry) applies to
	// every call; an action that declares the upstream account selector also
	// carries its alias into the credential lookup. Both travel on a per-call
	// view, so the shared runtime is never mutated and the common path (no
	// session view, no alias) stays allocation-free.
	alias := ""
	if _, declares := action.Params["account"]; declares {
		alias = StringArg(input, "account")
	}
	rt := r.runtime
	if source != nil || alias != "" {
		rt = r.runtime.ForCall(source, alias)
	}
	if impl := r.impls[toolName][actionName]; impl != nil {
		return Guard(func() (any, error) {
			return impl(ctx, rt, WithDefaults(action, input))
		})
	}
	if spec, ok := r.specs[toolName][actionName]; ok {
		return Guard(func() (any, error) {
			return rt.CallREST(ctx, tool, action, spec, WithDefaults(action, input))
		})
	}
	// A catalog entry without an implementation is a port bug, not a model
	// error: fail loudly instead of silently returning a stub.
	return nil, fmt.Errorf("integrations: %s.%s has no implementation", toolName, actionName)
}

// SortedNames returns tool names alphabetically (for manifests and docs).
func (r *Registry) SortedNames() []string {
	names := r.Names()
	sort.Strings(names)
	return names
}

// Install publishes every ported tool into a shared tool registry. It returns
// the number of tools registered.
func (r *Registry) Install(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	for _, name := range r.order {
		tool := r.tools[name]
		toolName := name
		target.Register(toolName, tools.Tool{
			Name:            toolName,
			Description:     tool.Description(),
			Parameters:      tool.Schema(),
			ConcurrencySafe: false,
		}, func(inputs map[string]any) (any, error) {
			// The session attaches its credential view to the shared registry
			// (SetAccountSource), so it is read per call: attaching after
			// installation still reaches get_account_info and account routing.
			return r.execute(context.Background(), target.AccountSource(), toolName, inputs)
		})
	}
	return len(r.order)
}

// ImplementedActions reports how many catalog actions have a real
// implementation (local or declarative spec) rather than none.
func (r *Registry) ImplementedActions() int {
	total := 0
	for name, tool := range r.tools {
		for _, action := range tool.Actions {
			if r.impls[name][action.Name] != nil {
				total++
				continue
			}
			if _, ok := r.specs[name][action.Name]; ok {
				total++
			}
		}
	}
	return total
}
