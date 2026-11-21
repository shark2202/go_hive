package cli

// REST-created sessions carry a phase tool surface.
//
// The Python reference attaches a QueenPhaseState (owning the tool tiers) to
// every queen session, so GET /api/sessions/{id}/live_tools answers with the
// real tool surface for sessions made through POST /api/sessions. This pins
// the Go create path: once the chat runtime is built, the phase state exposes
// its tier, with the flat-surface shims (advertised without an executor) kept
// out and the loop's ask_user reported by the route instead.
//
// Parity: framework/server/routes_sessions.py::handle_session_live_tools
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_current_tools

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

func injectTestChatProvider(t *testing.T) {
	t.Helper()
	previous := chatProviderFactory
	t.Cleanup(func() { chatProviderFactory = previous })
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) {
		return &llm.Provider{Model: "surface-test-model"}, nil
	}
}

// Parity-exempt: Go wiring. The REST create path attaches the phase tool
// surface at registration; upstream does the same inside create_queen /
// prepare_queen_session (queen_orchestrator.py), for which there is no
// standalone unit test.
func TestCreateSessionWiresPhaseToolSurface(t *testing.T) {
	runner, home, manager, liveMu, live := newPhaseTestRunner(t)
	injectTestChatProvider(t)

	sessionID := "sess-surface"
	session, err := runner.createSessionWithRuntime(hiveserver.CreateSessionParams{
		SessionID: &sessionID,
	}, manager, liveMu, live, home, "", nil, "")
	if err != nil {
		t.Fatalf("createSessionWithRuntime: %v", err)
	}
	if session.Chat == nil {
		t.Fatal("chat runtime was not built for the created session")
	}
	state, ok := session.PhaseState.(*hiveserver.QueenPhaseState)
	if !ok || state == nil {
		t.Fatalf("phase state = %#v", session.PhaseState)
	}

	eager := map[string]struct{}{}
	for _, tool := range state.GetCurrentTools() {
		eager[tool.Name] = struct{}{}
	}
	for _, want := range []string{"search_tools", "terminal_exec", "browser_setup"} {
		if _, present := eager[want]; !present {
			t.Fatalf("phase eager tools missing %q: %v", want, eager)
		}
	}
	// The flat surface advertises these shims without executors; the phase
	// tool list must not present them as callable.
	for _, shim := range []string{"ask_user", "collect_result", "credentials", "sentinel_setup"} {
		if _, present := eager[shim]; present {
			t.Fatalf("phase eager tools carry the standalone shim %q: %v", shim, eager)
		}
	}

	// The default queen has no allowlist: enabled_mcp_tools is None and every
	// registered MCP name is allowed.
	if _, none := state.EnabledMCPTools(); !none {
		t.Fatal("default queen should report allow-all (enabled_mcp_tools=None)")
	}
	if len(state.UnregisteredAllowlistedNames()) != 0 {
		t.Fatalf("allow-all must report no unregistered names: %v", state.UnregisteredAllowlistedNames())
	}
	mcpNames := state.MCPToolNamesAll()
	if _, present := mcpNames["terminal_exec"]; !present {
		t.Fatalf("phase MCP universe missing terminal_exec: %v", mcpNames)
	}
	if len(state.GetSearchableTools()) == 0 {
		t.Fatal("phase searchable tier is empty; the lazy layer should defer tools")
	}
}

// TestCreatedSessionSurfaceAppliesQueenAllowlist pins the allowlisted-queen
// case the REST probe captures: a queen with a role-default allowlist reports
// enabled_mcp_tools, filters the searchable view to allowlisted names, and
// lists allowlisted-but-unregistered names honestly.
//
// Parity: tests/test_search_tools_unregistered.py::test_unregistered_allowlisted_names_flags_missing_server_tool
func TestCreatedSessionSurfaceAppliesQueenAllowlist(t *testing.T) {
	runner, home, manager, liveMu, live := newPhaseTestRunner(t)
	injectTestChatProvider(t)

	queenName := "queen_growth"
	sessionID := "sess-growth"
	session, err := runner.createSessionWithRuntime(hiveserver.CreateSessionParams{
		SessionID: &sessionID,
		QueenName: &queenName,
	}, manager, liveMu, live, home, "", nil, "")
	if err != nil {
		t.Fatalf("createSessionWithRuntime: %v", err)
	}
	state, ok := session.PhaseState.(*hiveserver.QueenPhaseState)
	if !ok || state == nil {
		t.Fatalf("phase state = %#v", session.PhaseState)
	}
	allowlist, none := state.EnabledMCPTools()
	if none || len(allowlist) == 0 {
		t.Fatalf("queen_growth allowlist = %v (none=%v), want the role-default list", allowlist, none)
	}
	allowed := map[string]struct{}{}
	for _, name := range allowlist {
		allowed[name] = struct{}{}
	}
	for _, tool := range state.GetSearchableTools() {
		if _, ok := allowed[tool.Name]; !ok {
			t.Fatalf("searchable tier leaked the non-allowlisted tool %q", tool.Name)
		}
	}
	// The senders tools are allowlisted by the role default but not registered
	// with the feature off: they must surface as unregistered, not as missing.
	unregistered := state.UnregisteredAllowlistedNames()
	if _, present := unregistered["list_senders"]; !present {
		t.Fatalf("unregistered allowlisted names = %v, want list_senders", unregistered)
	}
	for name := range unregistered {
		if _, registered := state.MCPToolNamesAll()[name]; registered {
			t.Fatalf("registered name %q reported as unregistered", name)
		}
	}
}
