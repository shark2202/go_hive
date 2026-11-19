package server

// MCP allowlist on the chat queen's tool surface.
//
// The colony's enabled_mcp_tools list is a capability boundary: an MCP tool
// the operator left out must not be advertised to the model, and a model that
// guesses its name must not be able to execute it. The colony runtime owns the
// policy; this file applies the same snapshot to the session catalogue, which
// is built before a colony attaches (so the filter has to run per turn).

import (
	"fmt"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/llm"
)

// mcpToolBlocked reports whether the bound colony's allowlist excludes name.
// Non-MCP tools and an ungated runtime are never blocked.
func (r *ChatRuntime) mcpToolBlocked(name string) bool {
	if r == nil || name == "" {
		return false
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil {
		return false
	}
	enabled, all, gated := runtime.ToolAllowlistSnapshot()
	if !gated {
		return false
	}
	if _, isMCP := all[name]; !isMCP {
		return false
	}
	_, allowed := enabled[name]
	return !allowed
}

// filterToolsByColonyAllowlist drops MCP defs the bound colony has not
// enabled. Everything else (builtins, integrations, lifecycle tools) passes.
func filterToolsByColonyAllowlist(runtime *colony.ColonyRuntime, defs []llm.ToolDef) []llm.ToolDef {
	if runtime == nil {
		return defs
	}
	enabled, all, gated := runtime.ToolAllowlistSnapshot()
	if !gated || len(all) == 0 {
		return defs
	}
	out := make([]llm.ToolDef, 0, len(defs))
	for _, def := range defs {
		if _, isMCP := all[def.Name]; isMCP {
			if _, allowed := enabled[def.Name]; !allowed {
				continue
			}
		}
		out = append(out, def)
	}
	return out
}

// mcpToolRefusal is the tool result an excluded MCP name gets when the model
// calls it anyway. It is an actionable refusal, not a silent no-op.
func mcpToolRefusal(name string) agentloop.ToolResult {
	return agentloop.ToolResult{
		Content: fmt.Sprintf("Tool '%s' is not enabled for this colony. Ask the operator to enable it in the colony tool settings.", name),
		IsError: true,
	}
}
