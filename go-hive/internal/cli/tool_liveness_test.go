package cli

// The session and colony loaders are what hand the tool-timeout liveness pair
// to the runtimes they build. The tool executor itself is a plain func and
// cannot carry methods, so each catalogue's registry executor travels as the
// probe/kill value: this pins that the value is a real LivenessProber /
// ToolKiller (alias-resolving, "unknown" for a tool with no live MCP client)
// and that the colony worker surface returns it alongside its closure.
//
// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
// executor.kill_for_tool wired onto the tool executor)
// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
// handler: probe_for_tool / kill_for_tool)

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

func newToolLivenessHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(filepath.Join(testTempDir(t), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	return home
}

// Parity-exempt: Go-only session catalogue wiring — pins that the registry executor itself implements LivenessProber/ToolKiller; upstream framework/loader/tool_registry.py has no pytest for the attachment (only the handler policy is tested).
func TestSessionToolCatalogueExecutorCarriesToolLiveness(t *testing.T) {
	home := newToolLivenessHome(t)
	catalogue := newSessionToolCatalogue(t.TempDir(), builtin.New(""), home)
	liveness := catalogue.Registry().GetExecutor()
	if liveness == nil {
		t.Fatal("catalogue registry has no executor to probe with")
	}
	var prober tools.LivenessProber = liveness
	var killer tools.ToolKiller = liveness
	// No live MCP client behind the name: upstream's probe reads "unknown"
	// (the caller must not disconnect anything), and the kill is a no-op.
	if got := prober.ProbeForTool("not_an_mcp_tool"); got != tools.TimeoutVerdictUnknown {
		t.Fatalf("ProbeForTool = %q, want %q", got, tools.TimeoutVerdictUnknown)
	}
	if err := killer.KillForTool("not_an_mcp_tool"); err != nil {
		t.Fatalf("KillForTool: %v", err)
	}
}

// Parity-exempt: Go-only worker tool-surface wiring — pins that the colony surface returns the execute closure plus probe/kill handles; upstream has no pytest for this plumbing.
func TestColonyWorkerToolSurfaceReturnsToolLiveness(t *testing.T) {
	home := newToolLivenessHome(t)
	colonyPath := home.ColonyDir("researcher")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	_, _, execute, liveness := colonyWorkerToolSurfaceWithLiveness(colonyPath, builtin.New(colonyPath), home)
	if execute == nil {
		t.Fatal("worker surface has no executor")
	}
	if liveness == nil {
		t.Fatal("worker surface has no liveness handles; a timed-out worker tool would stay unknown")
	}
	if got := liveness.ProbeForTool("not_an_mcp_tool"); got != tools.TimeoutVerdictUnknown {
		t.Fatalf("worker ProbeForTool = %q, want %q", got, tools.TimeoutVerdictUnknown)
	}
}
