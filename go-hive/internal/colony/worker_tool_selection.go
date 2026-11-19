package colony

import (
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

// selectWorkerDeclaredTools merges a worker's declared tool set into the
// colony's base worker surface.
//
// A colony worker definition's "tools" list is the worker's declared tool set
// (framework/loader/agent_loader.py reads it into NodeSpec.tools). The base
// surface — the builtins a worker needs to act at all — is always kept, and
// every declared name selects one optional tool from declared (an
// integration/MCP tool the colony host resolved). Names the host could not
// resolve are dropped instead of advertised, matching upstream's
// declared-tools ∩ registered-tools filter; base definitions win a name
// collision so a worker never loses the sandboxed builtin implementation.
//
// An empty declaration returns the base surface unchanged: that is the
// pre-existing default for colony worker.json files (including scaffolds,
// which write "tools": []).
//
// Parity: framework/orchestrator/context.py::_resolve_available_tools
// Parity-exempt: the builtin base stays eager for colony workers; upstream
// node specs declare their complete tool list, while worker.json (every forked
// colony included) carries only the optional set.
func selectWorkerDeclaredTools(base, declared []llm.ToolDef, wanted []string) []llm.ToolDef {
	out := append([]llm.ToolDef(nil), base...)
	if len(wanted) == 0 {
		return out
	}
	selected := declaredToolNameSet(wanted)
	if len(selected) == 0 {
		return out
	}
	taken := make(map[string]struct{}, len(out))
	for _, def := range out {
		taken[def.Name] = struct{}{}
	}
	for _, def := range declared {
		if _, ok := selected[def.Name]; !ok {
			continue
		}
		if _, dup := taken[def.Name]; dup {
			continue
		}
		taken[def.Name] = struct{}{}
		out = append(out, def)
	}
	return out
}

// declaredToolNameSet is the set a worker's declaration filters the pool with.
// A name that is a tool category expands to the tools the category stands for:
// a worker.json forked from a queen profile carries the profile's category
// labels (the Go fork path has no live queen tool snapshot), and a label the
// port itself wrote must select the tools behind it. A label that is also a
// concrete tool name keeps that name. Queen-lifecycle names never enter the
// set: a worker can neither dispatch nor switch phases.
//
// Parity-exempt: Go-side category expansion — upstream worker.json already
// carries the queen's concrete tool names, so no expansion step exists there.
func declaredToolNameSet(wanted []string) map[string]struct{} {
	selected := make(map[string]struct{}, len(wanted))
	for _, name := range wanted {
		if name == RunWorkerToolName {
			continue
		}
		selected[name] = struct{}{}
		if _, isCategory := tools.ToolCategories[name]; !isCategory {
			continue
		}
		for _, resolved := range tools.ResolveCategoryTools(name, nil) {
			if resolved == RunWorkerToolName {
				continue
			}
			selected[resolved] = struct{}{}
		}
	}
	return selected
}

// appendMissingToolDefs appends extra definitions whose names are not already
// present, keeping the first definition for a name (the declared-tool pool and
// the role tables can name the same tool).
func appendMissingToolDefs(base []llm.ToolDef, extra ...llm.ToolDef) []llm.ToolDef {
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, def := range base {
		seen[def.Name] = struct{}{}
	}
	for _, def := range extra {
		if _, dup := seen[def.Name]; dup {
			continue
		}
		seen[def.Name] = struct{}{}
		base = append(base, def)
	}
	return base
}
