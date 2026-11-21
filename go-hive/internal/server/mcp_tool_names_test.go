package server

// The session tool tier maps the MCP loader's statuses onto the names the
// catalogue advertises. Status.ToolNames holds the names the server advertises
// (tools/list), so each one must be namespaced with the namespace the loader
// actually used — a custom configuration.json mcp.namespace makes the default
// "mcp" prefix wrong.

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// Parity-exempt: the process-wide MCP loader and its namespace are Go-side
// wiring (upstream loads servers per agent session and never prefixes names).
func TestMCPToolNamesUsesLoaderNamespace(t *testing.T) {
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(nil)
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	installFakeAttacher(t, &fakeMCPAttacher{statuses: []tools.MCPServerStatus{
		{Name: "github", Connected: true, ToolNames: []string{"search"}},
		{Name: "custom", Connected: true, Namespace: "remote", ToolNames: []string{"ping"}},
		{Name: "broken", Connected: false, ToolNames: []string{"nope"}},
	}})

	names := mcpToolNames()
	if _, ok := names["mcp__github__search"]; !ok {
		t.Fatalf("default namespace tool missing: %v", names)
	}
	if _, ok := names["remote__custom__ping"]; !ok {
		t.Fatalf("custom namespace tool missing: %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("unexpected MCP tool set: %v", names)
	}
}
