package agentloop

import (
	"encoding/json"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

const searchToolsName = "search_tools"

// SearchToolsToolDef is the schema a tiered worker sees for the loader.
//
// Parity: core/framework/tools/tool_tiers.py::build_search_tools
func SearchToolsToolDef() llm.ToolDef {
	schema, _ := tools.BuildSearchTools(nil)
	return llm.ToolDef{
		Name:        schema.Name,
		Description: schema.Description,
		Parameters:  schema.Parameters,
	}
}

func searchToolsProviderFrom(agentCtx *AgentContext) (tools.SearchToolsProvider, bool) {
	if agentCtx == nil || agentCtx.ToolTierState == nil {
		return nil, false
	}
	switch provider := agentCtx.ToolTierState.(type) {
	case *tools.ToolTierState:
		if provider == nil {
			return nil, false
		}
		return provider, true
	case tools.SearchToolsProvider:
		if provider == nil {
			return nil, false
		}
		return provider, true
	}
	return nil, false
}

// ensureSearchToolsTool exposes the loader exactly when the agent carries a
// tool tier, matching the worker-side branch that appends search_tools to the
// advertised surface.
//
// Parity: core/framework/agent_loop/agent_loop.py::AgentLoop._execute_impl
func (l *AgentLoop) ensureSearchToolsTool(agentCtx *AgentContext, list []llm.ToolDef) []llm.ToolDef {
	if _, ok := searchToolsProviderFrom(agentCtx); !ok {
		return list
	}
	for _, def := range list {
		if def.Name == searchToolsName {
			return list
		}
	}
	return append(list, SearchToolsToolDef())
}

// executeTieredTool runs search_tools in-loop against the worker's own tier and
// refuses a searchable-but-not-loaded tool before dispatch, so a deferred
// schema cannot execute without ever being advertised.
//
// Parity: core/framework/agent_loop/agent_loop.py::AgentLoop._execute_tool
func (l *AgentLoop) executeTieredTool(agentCtx *AgentContext, req ToolCallRequest) (ToolResult, bool) {
	provider, ok := searchToolsProviderFrom(agentCtx)
	if !ok {
		return ToolResult{}, false
	}
	if req.Name == searchToolsName {
		query, _ := req.Input["query"].(string)
		maxResults := 5
		if value, present := req.Input["max_results"]; present && value != nil {
			if n, valid := searchToolsInt(value); valid {
				maxResults = n
			}
		}
		content, err := tools.RunSearchTools(provider, query, maxResults)
		if err != nil {
			return ToolResult{Content: err.Error(), IsError: true}, true
		}
		return l.applyToolResultSpillover(ToolResult{Content: content}, req.Name), true
	}
	if _, searchable := searchableToolNameSet(provider)[req.Name]; searchable {
		payload, err := json.Marshal(map[string]any{
			"error": fmt.Sprintf(
				"Tool '%s' is available but its schema is not loaded yet. "+
					"Call search_tools(query=\"select:%s\") first, then retry — "+
					"it stays loaded for the rest of the session.",
				req.Name, req.Name),
		})
		content := ""
		if err == nil {
			content = string(payload)
		}
		return ToolResult{Content: content, IsError: true}, true
	}
	return ToolResult{}, false
}

func searchableToolNameSet(provider tools.SearchToolsProvider) map[string]struct{} {
	out := map[string]struct{}{}
	for _, tool := range provider.GetSearchableTools() {
		out[tool.Name] = struct{}{}
	}
	return out
}

func searchToolsInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
	}
	return 0, false
}
