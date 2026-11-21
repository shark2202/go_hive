package cli

// browser_tools_gate_test.go pins the browser feature switch's effect on the
// session catalogue. The 22 gcu browser tool definitions are a lazy-tier
// extension of the Go bridge surface; upstream retires the bundled gcu-tools
// MCP server, so a session without the browser feature must not register or
// record them — only the in-process browser_setup stays, mirroring the
// reference registry.
//
// Parity: core/framework/tools/browser_tools.py::register_browser_tools
// Parity: core/framework/loader/mcp_registry.py::_RETIRED_BUNDLED_SERVERS

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

// Parity: core/framework/tools/browser_tools.py::register_browser_tools
func TestBrowserToolsFollowBridgeSwitch(t *testing.T) {
	t.Setenv("HIVE_BROWSER_BRIDGE", "0")
	off := queenFlatCatalogue(t)
	if off.registry.HasTool("browser_open") {
		t.Fatalf("browser_open registered with the bridge switched off")
	}
	members := off.registry.GetServerToolNames("browser")
	if len(members) != 1 {
		t.Fatalf("browser group with bridge off = %d members, want just %s", len(members), integrations.BrowserSetupToolName)
	}
	if _, ok := members[integrations.BrowserSetupToolName]; !ok {
		t.Fatalf("browser group with bridge off = %v, want %s", members, integrations.BrowserSetupToolName)
	}

	t.Setenv("HIVE_BROWSER_BRIDGE", "1")
	on := queenFlatCatalogue(t)
	if !on.registry.HasTool("browser_open") {
		t.Fatalf("browser_open missing with the bridge switched on")
	}
	if got := len(on.registry.GetServerToolNames("browser")); got != 22 {
		t.Fatalf("browser group with bridge on = %d members, want 22", got)
	}
}
