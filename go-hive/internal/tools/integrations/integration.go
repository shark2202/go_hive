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
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

var (
	// ErrUnknownTool is returned when no integration owns the requested name.
	ErrUnknownTool = errors.New("integrations: unknown tool")
	// ErrUnknownAction is returned when a tool has no such action.
	ErrUnknownAction = errors.New("integrations: unknown action")
)

// ActionFunc executes one action. Returning an error is reserved for
// infrastructure failures; user-visible failures belong in the returned map.
type ActionFunc func(context.Context, *Runtime, map[string]any) (any, error)

// Action is one upstream MCP tool function.
type Action struct {
	Name     string
	Summary  string
	Source   string
	Params   map[string]any
	Required []string
	Requires []string
}

// Tool is one upstream integration module exposed as a single Go tool with
// an "action" selector (implicit when the module has exactly one action).
type Tool struct {
	ID          string
	Name        string
	DisplayName string
	Summary     string
	Actions     []Action
}

// ActionNames returns the action names in catalog order.
func (t *Tool) ActionNames() []string {
	out := make([]string, 0, len(t.Actions))
	for _, a := range t.Actions {
		out = append(out, a.Name)
	}
	return out
}

// Action looks up an action by name.
func (t *Tool) Action(name string) (Action, bool) {
	for _, a := range t.Actions {
		if a.Name == name {
			return a, true
		}
	}
	return Action{}, false
}

// Schema is the JSON schema published to the model. Parameters are the union
// of every action's parameters; action-specific required arguments are listed
// in the description of each action and enforced at execution time.
func (t *Tool) Schema() map[string]any {
	props := map[string]any{}
	for _, a := range t.Actions {
		for name, schema := range a.Params {
			if _, exists := props[name]; exists {
				continue
			}
			props[name] = schema
		}
	}
	actionNames := t.ActionNames()
	if len(actionNames) > 1 {
		props["action"] = map[string]any{
			"type":        "string",
			"enum":        actionNames,
			"description": "Action to invoke. See the tool description for per-action arguments.",
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(actionNames) > 1 {
		schema["required"] = []string{"action"}
	}
	return schema
}

// Description renders the model-facing description: summary plus the action
// menu with required arguments.
func (t *Tool) Description() string {
	if len(t.Actions) == 1 {
		return t.Actions[0].Summary
	}
	var b strings.Builder
	b.WriteString(t.Summary)
	b.WriteString("\n\nActions:")
	for _, a := range t.Actions {
		b.WriteString("\n- ")
		b.WriteString(a.Name)
		if len(a.Required) > 0 {
			b.WriteString(" (required: ")
			b.WriteString(strings.Join(a.Required, ", "))
			b.WriteString(")")
		}
		if a.Summary != "" {
			b.WriteString(": ")
			b.WriteString(a.Summary)
		}
	}
	return b.String()
}

// Runtime carries the injectable dependencies shared by all actions.
type Runtime struct {
	// HTTP is used by REST actions and network scanners. Never nil after New.
	HTTP *httpClient
	// ResolveHost resolves a URL host for SSRF checks. Nil uses the system
	// resolver; tests inject a stub so no network is touched.
	ResolveHost func(context.Context, string) ([]string, error)
	// Now supplies timestamps; defaults to time.Now.
	Now func() time.Time
	// Roots are the only directories file-backed tools may touch.
	Roots []string
	// Accounts is the credential-store view used by get_account_info.
	Accounts AccountSource
	// Modules holds per-module credential and endpoint configuration.
	Modules map[string]*ModuleConfig
	// Getenv resolves credentials; defaults to os.Getenv.
	Getenv func(string) string

	mu      sync.RWMutex
	clients map[string]*restClient
}

// AccountSource lists connected accounts for get_account_info.
type AccountSource interface {
	Accounts(provider string) []map[string]any
}

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

// Option mutates the Runtime during New.
type Option func(*Runtime)

// WithHTTPClient swaps the underlying transport (tests, proxies).
func WithHTTPClient(client *http.Client) Option {
	return func(rt *Runtime) { rt.HTTP = newHTTPClient(client) }
}

// WithBaseURL points one module at a different API base (tests).
func WithBaseURL(moduleID, baseURL string) Option {
	return func(rt *Runtime) {
		cfg := rt.Modules[moduleID]
		if cfg == nil {
			cfg = &ModuleConfig{ID: moduleID}
			rt.Modules[moduleID] = cfg
		}
		cfg.BaseURL = strings.TrimRight(baseURL, "/")
	}
}

// WithCredential supplies a credential for one module without touching env.
func WithCredential(moduleID, value string) Option {
	return func(rt *Runtime) {
		cfg := rt.Modules[moduleID]
		if cfg == nil {
			cfg = &ModuleConfig{ID: moduleID}
			rt.Modules[moduleID] = cfg
		}
		cfg.token = value
	}
}

// WithRoots overrides the filesystem sandbox roots.
func WithRoots(roots ...string) Option {
	return func(rt *Runtime) { rt.Roots = roots }
}

// WithClock overrides the clock (deterministic timestamps).
func WithClock(now func() time.Time) Option {
	return func(rt *Runtime) { rt.Now = now }
}

// WithAccountSource installs the credential-store view.
func WithAccountSource(source AccountSource) Option {
	return func(rt *Runtime) { rt.Accounts = source }
}

// WithGetenv overrides environment lookup (tests).
func WithGetenv(fn func(string) string) Option {
	return func(rt *Runtime) { rt.Getenv = fn }
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
	if impl := r.impls[toolName][actionName]; impl != nil {
		return guard(func() (any, error) { return impl(ctx, r.runtime, withDefaults(action, input)) })
	}
	if spec, ok := r.specs[toolName][actionName]; ok {
		return guard(func() (any, error) { return r.runtime.callREST(ctx, tool, action, spec, withDefaults(action, input)) })
	}
	// A catalog entry without an implementation is a port bug, not a model
	// error: fail loudly instead of silently returning a stub.
	return nil, fmt.Errorf("integrations: %s.%s has no implementation", toolName, actionName)
}

func guard(fn func() (any, error)) (result any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			result, err = nil, fmt.Errorf("integrations: panic: %v", rec)
		}
	}()
	return fn()
}

// withDefaults fills omitted parameters from the catalog defaults so action
// handlers can rely on upstream defaults.
func withDefaults(action Action, input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	for name, raw := range action.Params {
		if _, present := out[name]; present {
			continue
		}
		schema, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if def, ok := schema["default"]; ok {
			out[name] = def
		}
	}
	return out
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
			return r.Execute(context.Background(), toolName, inputs)
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

// jsonString marshals a value, falling back to its fmt form.
func jsonString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}
