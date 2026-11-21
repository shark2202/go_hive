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
// Structure (verified by docs/integrations-package-structure.md):
//
//   - internal/tools/integrationkit owns the shared kernel: Registry, Runtime,
//     Tool/Action/RestSpec, credential/OAuth resolution, parameter helpers,
//     JSON-schema builders and the bounded HTTP helpers.
//   - This package is the assembly root: the generated catalog
//     (catalog_gen.go), the per-provider files, and the New/InstallBatch*
//     entrypoints that attach specs and implementations to the kernel
//     registry. Historical unqualified names stay available through the thin
//     aliases and forwards in kit_aliases.go.
//   - Domain subpackages (starting with excel/) own one family end to end:
//     catalog, implementations and tests, registering through
//     integrationkit only. The root imports them; they never import the root.
package integrations

import (
	"os"
	"time"
)

// New builds the registry for the ported core modules. It never returns nil.
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
	reg := NewRegistry(rt)
	for _, tool := range builtinCatalog {
		reg.AddTool(tool)
	}
	registerREST(reg)
	registerLocal(reg)
	return reg
}
