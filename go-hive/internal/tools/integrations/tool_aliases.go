package integrations

// Shared plumbing for upstream tool-name aliases.
//
// Upstream MCP registers one tool per operation (`csv_read`,
// `similarweb_v5_website_rank`, ...). The Go port publishes aggregate module
// tools with an `action` selector instead, which keeps the catalog small but
// hides the upstream names from allowlists, prompts and audits. The alias
// installers (spreadsheet_aliases.go, similarweb_aliases.go) publish those
// names as flat single-action registry tools and delegate execution to the
// aggregate tool, so there is exactly one implementation and one behaviour.

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// singleActionSchema is the alias schema: the aggregate action's parameters
// plus that action's required list, with no `action` selector. Upstream
// registers the operation as its own MCP tool, so its schema never carries a
// selector either.
func singleActionSchema(action Action) map[string]any {
	properties := make(map[string]any, len(action.Params))
	for name, schema := range action.Params {
		properties[name] = schema
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(action.Required) > 0 {
		schema["required"] = action.Required
	}
	return schema
}

// executeAggregateAction runs one aggregate tool action through the same
// registry the model would call directly, so sandbox roots, defaults and error
// envelopes are identical. A ToolResult passes through untouched, so an error
// result stays an error.
func executeAggregateAction(target *tools.ToolRegistry, aggregate, action string, inputs map[string]any) (any, error) {
	merged := make(map[string]any, len(inputs)+1)
	for name, value := range inputs {
		if name == "action" {
			continue
		}
		merged[name] = value
	}
	merged["action"] = action
	result, err := target.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name:  aggregate,
		Input: merged,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
