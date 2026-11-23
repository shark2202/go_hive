package server

// The session tool tier maps the MCP loader's statuses onto the names the
// catalogue advertises. Status.ToolNames holds the raw names the server
// advertises (tools/list), and each one maps to its registry key through the
// loader's namespace: bare by default (Python parity), prefixed only when an
// operator configured mcp.namespace.

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Parity-exempt: the process-wide MCP loader and its namespace are Go-side
// wiring (upstream loads servers per agent session and never prefixes names).
func TestMCPToolNamesUsesLoaderNamespace(t *testing.T) {
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(nil)
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	installFakeAttacher(t, &fakeMCPAttacher{statuses: []tools.MCPServerStatus{
		// No namespace (the default): the advertised name IS the registry key,
		// so distinct names that would fold together stay distinct.
		{Name: "github", Connected: true, ToolNames: []string{"search", "x-y", "x_y"}},
		// Explicit namespace: the prefixed form is the registry key.
		{Name: "custom", Connected: true, Namespace: "remote", ToolNames: []string{"ping"}},
		{Name: "broken", Connected: false, ToolNames: []string{"nope"}},
	}})

	names := mcpToolNames()
	for _, want := range []string{"search", "x-y", "x_y", "remote__custom__ping"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("MCP tool %q missing: %v", want, names)
		}
	}
	if _, ok := names["mcp__github__search"]; ok {
		t.Fatalf("default mode prefixed a bare server tool: %v", names)
	}
	if len(names) != 4 {
		t.Fatalf("unexpected MCP tool set: %v", names)
	}
}
