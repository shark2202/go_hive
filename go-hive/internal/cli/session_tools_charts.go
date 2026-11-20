package cli

// chart_render wiring: the chart-tools harness group is served in process by
// internal/charts (headless Chrome against the bundled ECharts/Mermaid), not by
// the integration registry. The Tool Library and @server:chart-tools category
// read the registry catalog, so the group is recorded here the same way
// files-tools is.

import (
	"context"

	"github.com/aden-hive/hive-go/internal/charts"
	"github.com/aden-hive/hive-go/internal/tools"
)

// registerChartTool advertises chart_render and records the chart-tools
// harness group. An already-registered group (a real MCP server) wins.
func (c *sessionToolCatalogue) registerChartTool() {
	if _, taken := c.builtinNames[charts.ToolName]; taken {
		return
	}
	catalogued := false
	for _, def := range c.defs {
		if def.Name == charts.ToolName {
			catalogued = true
			break
		}
	}
	if !catalogued {
		c.defs = append(c.defs, charts.ToolDef())
	}
	if len(c.registry.GetServerToolNames("chart-tools")) > 0 {
		return
	}
	def := charts.ToolDef()
	c.registry.RecordBuiltinToolGroup("chart-tools", []tools.Tool{{
		Name:            def.Name,
		Description:     def.Description,
		Parameters:      def.Parameters,
		ConcurrencySafe: def.ConcurrencySafe,
	}})
}

// executeChartTool dispatches chart_render to the charts package. handled is
// false for every other tool name so the caller falls through to the registry.
func (c *sessionToolCatalogue) executeChartTool(ctx context.Context, name string, input map[string]any) (content string, isError, handled bool) {
	if name != charts.ToolName {
		return "", false, false
	}
	content, isError = charts.ExecuteTool(ctx, input, c.home.Root)
	return content, isError, true
}
