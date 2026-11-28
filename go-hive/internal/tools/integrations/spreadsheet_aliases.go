package integrations

// Spreadsheet tool-name aliases: the upstream per-tool surface for CSV and
// Excel.
//
// Upstream `hive_tools` exposes each spreadsheet operation as its own MCP tool
// (`csv_read`, `csv_write`, ... / `excel_read`, ...). The Go port first shipped
// the modules as one aggregate tool per module (`csv`/`excel` with an `action`
// selector), which keeps the catalog small but does not match the upstream tool
// names an allowlist, a tier category (`spreadsheet_advanced`) or an agent
// prompt refers to. This file publishes the upstream per-tool names as
// independent registry tools on top of the aggregate executors, so both names
// resolve to the same implementation and behaviour.
//
// Deriving the alias list from the agent catalogs means:
//   - schema, required arguments and defaults are the action's own;
//   - `csv_sql` / `excel_sql` stay unsupported: the catalogs already exclude
//     them (upstream gen_catalog.py excludes csv_sql too, and DuckDB is not a
//     dependency this build can take), so no stub tool is invented here.

import (
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/excel"
)

// spreadsheetAliasAction couples one upstream per-tool name with the aggregate
// action that implements it.
type spreadsheetAliasAction struct {
	aggregate string
	action    Action
}

// spreadsheetAggregates returns the aggregate tools the aliases mirror: "csv"
// from the generated provider catalog and "excel" from its domain subpackage.
// A module that is not linked into this build is skipped rather than
// half-registered.
func spreadsheetAggregates() []*Tool {
	out := make([]*Tool, 0, 2)
	for _, tool := range builtinCatalog {
		if tool.Name == "csv" {
			out = append(out, tool)
			break
		}
	}
	out = append(out, excel.Catalog()...)
	return out
}

// spreadsheetAliasActions flattens the aggregate catalogs into the alias list.
func spreadsheetAliasActions() []spreadsheetAliasAction {
	var actions []spreadsheetAliasAction
	for _, tool := range spreadsheetAggregates() {
		for _, action := range tool.Actions {
			actions = append(actions, spreadsheetAliasAction{aggregate: tool.Name, action: action})
		}
	}
	return actions
}

// InstallSpreadsheetAliases publishes the upstream per-tool spreadsheet names
// into a shared registry and returns the number of tools registered. An alias
// already registered (a real MCP server, a later lane) wins; execution is
// delegated to the aggregate tool so there is exactly one implementation.
func InstallSpreadsheetAliases(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	installed := 0
	for _, alias := range spreadsheetAliasActions() {
		name := alias.action.Name
		if target.HasTool(name) {
			continue
		}
		action := alias.action
		aggregate := alias.aggregate
		target.Register(name, tools.Tool{
			Name:        name,
			Description: action.Summary,
			Parameters:  singleActionSchema(action),
		}, func(inputs map[string]any) (any, error) {
			return executeAggregateAction(target, aggregate, action.Name, inputs)
		})
		installed++
	}
	return installed
}
