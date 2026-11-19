package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

type fakeMCPAttacher struct {
	statuses []tools.MCPServerStatus
}

func (f *fakeMCPAttacher) Attach(*tools.ToolRegistry) []tools.ServerResolution { return nil }
func (f *fakeMCPAttacher) Reload() error                                       { return nil }
func (f *fakeMCPAttacher) Status() []tools.MCPServerStatus                     { return f.statuses }
func (f *fakeMCPAttacher) Generation() int64                                   { return 1 }

// Parity-exempt: Go-side host wiring. The process MCP loader names the MCP
// surface; the colony tools.json supplies the enabled subset. This pins the
// production path that made the allowlist effective.
func TestHostResolvesColonyMCPAllowlist(t *testing.T) {
	storage := t.TempDir()
	if err := os.WriteFile(filepath.Join(storage, "tools.json"), []byte(`{"enabled_mcp_tools":["mcp__files__read"]}`), 0o644); err != nil {
		t.Fatalf("write tools.json: %v", err)
	}
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(&fakeMCPAttacher{statuses: []tools.MCPServerStatus{{
		Name:      "files",
		Connected: true,
		ToolNames: []string{"mcp__files__read", "mcp__files__write"},
	}}})
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	built, err := New(Config{
		ColonyID:    "colony_mcp_allowlist",
		AgentSpec:   hostTestSpec(),
		StoragePath: storage,
		LLM:         &reportLLM{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	enabled, all, gated := built.Runtime().ToolAllowlistSnapshot()
	if !gated {
		t.Fatal("allowlist is not gated")
	}
	if _, ok := enabled["mcp__files__read"]; !ok || len(enabled) != 1 {
		t.Fatalf("enabled = %v, want only mcp__files__read", enabled)
	}
	if _, ok := all["mcp__files__write"]; !ok {
		t.Fatalf("all = %v, want both loaded MCP tools", all)
	}
}

// Parity-exempt: a colony with no tools.json keeps the pre-existing "all MCP
// tools enabled" behaviour.
func TestHostWithoutToolsSidecarLeavesAllowlistOpen(t *testing.T) {
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(&fakeMCPAttacher{statuses: []tools.MCPServerStatus{{
		Name:      "files",
		Connected: true,
		ToolNames: []string{"mcp__files__read"},
	}}})
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	built, err := New(Config{
		ColonyID:    "colony_mcp_open",
		AgentSpec:   hostTestSpec(),
		StoragePath: t.TempDir(),
		LLM:         &reportLLM{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, gated := built.Runtime().ToolAllowlistSnapshot(); gated {
		t.Fatal("allowlist gated without a tools.json sidecar")
	}
}
