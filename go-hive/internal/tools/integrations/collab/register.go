package collab

// Family-level assembly: NewRegistry builds the union of both lanes
// (Atlassian first, then Notion/Linear) and Register attaches the same tools
// to an existing kernel registry. The two per-lane constructors
// (NewAtlassianRegistry / NewNotionRegistry) keep the historical
// InstallBatch5Lane1 / InstallBatch4 registries byte-identical.

import (
	"os"
	"time"
)

// NewRegistry builds a registry containing the whole collaboration family:
// jira, confluence, asana, notion and linear, sharing the Runtime surface used
// by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: allModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	return Register(newRegistry(runtime))
}

// Register adds the family catalogs and their action implementations to reg.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{jiraTool, confluenceTool, asanaTool, notionTool, linearTool} {
		reg.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"jira", jiraImpls()},
		{"confluence", confluenceImpls()},
		{"asana", asanaImpls()},
		{"notion", notionImpls()},
		{"linear", linearImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			reg.AddImpl(entry.tool, action, impl)
		}
	}
	return reg
}

// allModules merges the two lane module maps; module ids are disjoint.
func allModules() map[string]*ModuleConfig {
	merged := modules()
	for id, cfg := range notionModules() {
		merged[id] = cfg
	}
	return merged
}
