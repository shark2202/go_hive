package integrations

// SimilarWeb tool-name aliases: the upstream per-tool surface.
//
// Upstream `hive_tools` registers each SimilarWeb V5 call as its own MCP tool
// (`similarweb_v5_traffic_and_engagement`, ... `similarweb_v5_segment_analysis`,
// 29 in total). The Go port publishes one aggregate `similarweb` tool with an
// `action` selector, and that grouped definition stays the model-facing
// contract in the frozen flat surface. Callers that enumerate the upstream
// names — credential/provider maps
// (internal/credentials/tool_provider_map_gen.go already maps all 29), agent
// prompts, allowlists and delivery audits — still need each name to resolve as
// a tool.
//
// This file publishes those names as independent registry tools on top of the
// aggregate executor, exactly like InstallSpreadsheetAliases: schema and
// required arguments come from the action itself (no `action` selector), and
// execution delegates to the aggregate tool so there is one implementation and
// one behaviour. The aliases are additive registry tools; they are not part of
// the frozen 39-name default model surface.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/webintel"
)

// similarWebAggregate returns the aggregate SimilarWeb tool the aliases mirror.
func similarWebAggregate() *Tool {
	for _, tool := range webintel.Catalog() {
		if tool.Name == "similarweb" {
			return tool
		}
	}
	return nil
}

// InstallSimilarWebAliases publishes the 29 upstream SimilarWeb tool names
// into a shared registry and returns the number of tools registered. An alias
// already registered (a real MCP server, a later lane) wins; execution is
// delegated to the aggregate `similarweb` tool so there is exactly one
// implementation.
func InstallSimilarWebAliases(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	aggregate := similarWebAggregate()
	if aggregate == nil {
		return 0
	}
	installed := 0
	for _, action := range aggregate.Actions {
		name := action.Name
		if target.HasTool(name) {
			continue
		}
		action := action
		target.Register(name, tools.Tool{
			Name:        name,
			Description: action.Summary,
			Parameters:  singleActionSchema(action),
		}, func(inputs map[string]any) (any, error) {
			return executeAggregateAction(target, aggregate.Name, action.Name, inputs)
		})
		installed++
	}
	return installed
}
