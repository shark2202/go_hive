package agentloop

// Dynamic tool refresh: a host provider's live tool list replaces the
// synthetic tools the loop would otherwise keep (ask_user, escalate, ...).
// Split from loop.go to keep the turn-loop file inside the repo's size
// budget.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._refresh_dynamic_tools

import "github.com/aden-hive/hive-go/internal/llm"

// Parity: AgentLoop._DYNAMIC_REFRESH_SYNTHETIC_NAMES.
var DynamicRefreshSyntheticNames = map[string]struct{}{
	"ask_user":         {},
	"credentials":      {},
	"sentinel_setup":   {},
	"escalate":         {},
	"collect_result":   {},
	"report_to_parent": {},
	"search_tools":     {},
}

// Parity: AgentLoop._refresh_dynamic_tools. Returns the live list unchanged
func (l *AgentLoop) RefreshDynamicTools(agentCtx *AgentContext, tools []llm.ToolDef) []llm.ToolDef {
	if agentCtx == nil {
		return tools
	}
	provided, ok := dynamicToolsFrom(agentCtx.DynamicToolsProvider)
	if !ok {
		return tools
	}
	providedNames := make(map[string]struct{}, len(provided))
	for _, tool := range provided {
		providedNames[tool.Name] = struct{}{}
	}
	out := make([]llm.ToolDef, 0, len(provided)+len(tools))
	out = append(out, provided...)
	for _, tool := range tools {
		if _, synthetic := DynamicRefreshSyntheticNames[tool.Name]; !synthetic {
			continue
		}
		if _, dup := providedNames[tool.Name]; dup {
			continue
		}
		out = append(out, tool)
	}
	return out
}

func dynamicToolsFrom(provider any) ([]llm.ToolDef, bool) {
	switch p := provider.(type) {
	case nil:
		return nil, false
	case func() []llm.ToolDef:
		return p(), true
	case []llm.ToolDef:
		return p, true
	case func() []Tool:
		return toolDefsFromReminderTools(p()), true
	case []Tool:
		return toolDefsFromReminderTools(p), true
	case func() []string:
		names := p()
		out := make([]llm.ToolDef, 0, len(names))
		for _, name := range names {
			out = append(out, llm.ToolDef{Name: name})
		}
		return out, true
	case []string:
		out := make([]llm.ToolDef, 0, len(p))
		for _, name := range p {
			out = append(out, llm.ToolDef{Name: name})
		}
		return out, true
	default:
		return nil, false
	}
}

func toolDefsFromReminderTools(tools []Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(tools))
	for _, tool := range tools {
		out = append(out, llm.ToolDef{Name: tool.Name, Description: tool.Description})
	}
	return out
}
