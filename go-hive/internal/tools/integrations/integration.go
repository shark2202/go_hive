// Package integrations ports the upstream aden_tools integration surface to
// Go. It is deliberately independent of the server and agent loop: callers
// build a Registry, install it into tools.ToolRegistry, and the model-facing
// schemas come from the same catalog data that drives execution.
//
// Design:
//
//   - One Tool per upstream integration module (e.g. stripe_tool -> "stripe").
//   - One Action per upstream @mcp.tool function, with the upstream parameter
//     list and defaults preserved in the JSON schema.
//   - Execution is either a local Go implementation (files, CSV, time, risk
//     scoring) or a declarative REST/GraphQL spec executed by a shared client
//     with an injectable http.Client. Every action is therefore exercisable
//     against a fake server without real credentials.
//   - Tool-level failures are returned as {"error": ...} values, matching the
//     upstream convention of returning error dicts rather than raising; only
//     unknown tools/actions and cancelled contexts surface as Go errors.
//
// The transport, credential, catalog-type and parameter primitives live in
// internal/tools/integrationkit. This package keeps the provider catalog,
// registry assembly and the historical type aliases in kit_aliases.go.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

var (
	// ErrUnknownTool is returned when no integration owns the requested name.
	ErrUnknownTool = errors.New("integrations: unknown tool")
	// ErrUnknownAction is returned when a tool has no such action.
	ErrUnknownAction = errors.New("integrations: unknown action")
)

// New builds the registry for the ported modules. It never returns nil.
func New(opts ...Option) *Registry {
	rt := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: moduleConfigs(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(rt)
	}
	reg := &Registry{runtime: rt, tools: map[string]*Tool{}, specs: map[string]map[string]RestSpec{}, impls: map[string]map[string]ActionFunc{}}
	for _, tool := range builtinCatalog {
		reg.tools[tool.Name] = tool
		reg.order = append(reg.order, tool.Name)
	}
	registerREST(reg)
	registerLocal(reg)
	return reg
}

// Registry owns the ported tools and their execution specs.
type Registry struct {
	runtime *Runtime
	tools   map[string]*Tool
	order   []string
	specs   map[string]map[string]RestSpec
	impls   map[string]map[string]ActionFunc
}

// Runtime exposes the configured runtime (clients, roots, clock).
func (r *Registry) Runtime() *Runtime { return r.runtime }

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
	actionName := stringArg(input, "action")
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
		alias = stringArg(input, "account")
	}
	rt := r.runtime
	if source != nil || alias != "" {
		rt = r.runtime.ForCall(source, alias)
	}
	if impl := r.impls[toolName][actionName]; impl != nil {
		return integrationkit.Guard(func() (any, error) {
			return impl(ctx, rt, integrationkit.WithDefaults(action, input))
		})
	}
	if spec, ok := r.specs[toolName][actionName]; ok {
		return integrationkit.Guard(func() (any, error) {
			return rt.CallREST(ctx, tool, action, spec, integrationkit.WithDefaults(action, input))
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
