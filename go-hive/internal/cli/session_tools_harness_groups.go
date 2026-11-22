package cli

// Harness-group catalog for chat sessions and colony workers.
//
// Upstream serves five in-process harness groups (core/framework/tools/
// harness_tools.py: terminal-tools, files-tools, chart-tools, memory-tools,
// hive_tools). The tools themselves run through the builtin sandbox, the
// session task tables or the integration registry — not through MCP — but the
// Tool Library, `@server:<group>` category references and per-colony gating all
// read the registry's server bookkeeping. Recording each group explicitly keeps
// those surfaces complete and keyed by the upstream names.
//
// files-tools is recorded next to this file's caller (recordFilesToolGroup);
// the group's dispatch already existed before this catalog. A group name that a
// real MCP server already owns is never overwritten, matching the files-tools
// precedent.

import (
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

const (
	harnessGroupTerminal = "terminal-tools"
	harnessGroupChart    = "chart-tools"
	harnessGroupMemory   = "memory-tools"
	harnessGroupHive     = "hive_tools"
)

// terminalHarnessToolNames is the upstream terminal-tools membership in the
// order the tool library lists it: exec, search, output, jobs, PTY.
var terminalHarnessToolNames = []string{
	"terminal_exec",
	"terminal_rg",
	"terminal_glob",
	"terminal_output_get",
	"terminal_job_start",
	"terminal_job_logs",
	"terminal_job_manage",
	"terminal_pty_open",
	"terminal_pty_run",
	"terminal_pty_close",
}

// chartRenderToolName is the chart-tools group's only member. The renderer
// itself lands with the chart lane; this catalogue only records the name so the
// `charts` category and @server:chart-tools resolve.
const chartRenderToolName = "chart_render"

// hiveCoreHarnessToolNames is the upstream hive_tools membership this build
// serves, in harness_tools.py registration order: web/pdf/attach/time/image,
// account info, the two file tools re-exported for the group, then the CSV and
// Excel per-tool names. csv_sql / excel_sql are absent on purpose: upstream
// gen_catalog.py excludes csv_sql and DuckDB is not ported, so no stub is
// advertised (see docs/harness-parity.md).
var hiveCoreHarnessToolNames = []string{
	"web_scrape",
	"pdf_read",
	"attach_file",
	"get_current_time",
	"image_generate",
	"get_account_info",
	"read_file",
	"edit_file",
	"csv_read",
	"csv_write",
	"csv_append",
	"csv_info",
	"excel_read",
	"excel_write",
	"excel_append",
	"excel_info",
	"excel_sheet_list",
	"excel_search",
}

// recordHarnessGroups records the four remaining harness groups (files-tools is
// recorded by recordFilesToolGroup). Each recorder is a no-op when an MCP
// server of the same name is already registered: a real server wins.
func (c *sessionToolCatalogue) recordHarnessGroups() {
	c.recordTerminalToolGroup()
	c.recordChartToolGroup()
	c.recordMemoryToolGroup()
	c.recordHiveToolGroup()
}

// recordTerminalToolGroup records the ten terminal tools the builtin sandbox
// executes.
func (c *sessionToolCatalogue) recordTerminalToolGroup() {
	if c.harnessGroupTaken(harnessGroupTerminal) {
		return
	}
	group := c.resolveHarnessTools(terminalHarnessToolNames)
	if len(group) == 0 {
		return
	}
	c.registry.RecordBuiltinToolGroup(harnessGroupTerminal, group)
}

// recordChartToolGroup records chart_render. When the chart lane's renderer is
// linked in (registry or builtin toolbox) its real schema is used; until then
// the catalog carries the upstream schema so the Tool Library entry is complete
// and executeHarnessPlaceholder answers with an explicit not_implemented error.
func (c *sessionToolCatalogue) recordChartToolGroup() {
	if c.harnessGroupTaken(harnessGroupChart) {
		return
	}
	if group := c.resolveHarnessTools([]string{chartRenderToolName}); len(group) > 0 {
		c.registry.RecordBuiltinToolGroup(harnessGroupChart, group)
		return
	}
	c.registry.RecordBuiltinToolGroup(harnessGroupChart, []tools.Tool{{
		Name: chartRenderToolName,
		Description: "Render a chart or Mermaid diagram to a high-DPI PNG and return the live " +
			"spec plus a file URL (catalog entry only until the chart renderer is linked).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":        map[string]any{"type": "string", "enum": []string{"echarts", "mermaid"}},
				"spec":        map[string]any{"type": []string{"object", "string"}},
				"title":       map[string]any{"type": "string"},
				"width":       map[string]any{"type": "integer", "default": 1600},
				"height":      map[string]any{"type": "integer", "default": 900},
				"dpi":         map[string]any{"type": "integer", "default": 300},
				"output_path": map[string]any{"type": []string{"string", "null"}},
			},
			"required": []string{"kind", "spec"},
		},
	}})
}

// recordMemoryToolGroup records search_messages, and search_timeline only when
// the memory_timeline feature is on — the same gate searchToolDefs applies to
// the advertised tools (upstream register_search_timeline is likewise
// conditional on get_memory_timeline_enabled()).
func (c *sessionToolCatalogue) recordMemoryToolGroup() {
	if c.harnessGroupTaken(harnessGroupMemory) {
		return
	}
	defs := searchToolDefs(c.home)
	if len(defs) == 0 {
		return
	}
	group := make([]tools.Tool, 0, len(defs))
	for _, def := range defs {
		group = append(group, harnessToolFromDef(def))
	}
	c.registry.RecordBuiltinToolGroup(harnessGroupMemory, group)
}

// recordHiveToolGroup records the core Hive tools under hive_tools. Members are
// resolved from the surfaces this build actually serves: read_file/edit_file
// through the builtin toolbox, the rest through the integration registry.
func (c *sessionToolCatalogue) recordHiveToolGroup() {
	if c.harnessGroupTaken(harnessGroupHive) {
		return
	}
	group := c.resolveHarnessTools(hiveCoreHarnessToolNames)
	if len(group) == 0 {
		return
	}
	c.registry.RecordBuiltinToolGroup(harnessGroupHive, group)
}

// harnessGroupTaken reports whether an MCP server (or an earlier recording)
// already owns the group name, in which case the built-in catalog leaves it
// untouched.
func (c *sessionToolCatalogue) harnessGroupTaken(group string) bool {
	return len(c.registry.GetServerToolNames(group)) > 0
}

// resolveHarnessTools looks each requested name up on the registry first (the
// integration/executor surface), then on the builtin toolbox. Missing names are
// skipped; the recorder's caller decides whether an empty group is recorded.
func (c *sessionToolCatalogue) resolveHarnessTools(names []string) []tools.Tool {
	builtins := map[string]llm.ToolDef{}
	for _, def := range c.toolbox.Defs() {
		builtins[def.Name] = def
	}
	group := make([]tools.Tool, 0, len(names))
	for _, name := range names {
		if tool, ok := c.registry.GetTool(name); ok {
			group = append(group, tool)
			continue
		}
		if def, ok := builtins[name]; ok {
			group = append(group, harnessToolFromDef(def))
		}
	}
	return group
}

// harnessToolFromDef converts an advertised definition into the catalog shape.
func harnessToolFromDef(def llm.ToolDef) tools.Tool {
	return tools.Tool{
		Name:            def.Name,
		Description:     def.Description,
		Parameters:      def.Parameters,
		ProducesImage:   def.ProducesImage,
		ConcurrencySafe: def.ConcurrencySafe,
	}
}

// executeHarnessPlaceholder answers tools whose catalog entry exists without a
// local implementation. Today that is chart_render only, while the chart lane
// lands the renderer. A tool the registry, the builtin sandbox or the
// advertised definitions already own is left alone (handled=false), so the
// normal dispatch — including a future chart implementation — wins.
func (c *sessionToolCatalogue) executeHarnessPlaceholder(name string) (content string, isError bool, handled bool) {
	if name != chartRenderToolName {
		return "", false, false
	}
	if c.registry.HasTool(name) {
		return "", false, false
	}
	if _, builtin := c.builtinNames[name]; builtin {
		return "", false, false
	}
	for _, def := range c.defs {
		if def.Name == name {
			return "", false, false
		}
	}
	return tools.PythonJSONDumps(map[string]any{
		"error": "not_implemented",
		"message": "chart_render is registered in the chart-tools catalog but has no renderer in this " +
			"build; render the chart another way (for example with a script) and attach the result",
	}), true, true
}
