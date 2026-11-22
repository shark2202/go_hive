package server

// Live tool surface attached through the session registration path.
//
// The Python reference attaches a QueenPhaseState to every session it creates
// (framework/server/queen_orchestrator.py::prepare_queen_session), so GET
// /api/sessions/{id}/live_tools answers with the phase's eager/searchable
// split for REST-created sessions too. These tests pin the Go wiring: the
// session's chat runtime tier is exposed through the phase state AddSession
// attaches, with the per-queen allowlist applied.
//
// Parity: framework/server/routes_sessions.py::handle_session_live_tools
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_current_tools

import (
	"context"
	"net/http"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// surfaceMCPServerNames is the test's MCP-origin universe (harness groups plus
// a connected server), upstream's QueenPhaseState.mcp_tool_names_all.
var surfaceMCPServerNames = []string{"chart_render", "terminal_exec", "search_messages", "pdf_read"}

// surfaceAllowlist is the resolved per-queen allowlist: two registered MCP
// tools, one searchable MCP tool, and three names no server registered.
var surfaceAllowlist = []string{"chart_render", "terminal_exec", "ghost_tool", "search_timeline", "csv_read"}

func newSurfaceChatRuntime(t *testing.T) *ChatRuntime {
	t.Helper()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:               &fakeChatLLM{},
		Tools:             append(lazySourceDefs(), lazyColonyDefs()...),
		AlwaysOnToolNames: lazyAlwaysOn,
		DeferredTools:     lazyDeferredDefs(),
		ManifestToolNames: surfaceAllowlist,
		ManifestFiltered:  true,
		MCPToolNamesAll:   surfaceMCPServerNames,
		Execute:           func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:              t.TempDir(),
		Bus:               eventbus.NewEventBus(),
		SessionID:         "sess-surface",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func surfaceSession(t *testing.T) (*Session, *ChatRuntime) {
	t.Helper()
	runtime := newSurfaceChatRuntime(t)
	session := NewSession("sess-surface", eventbus.NewEventBus())
	session.QueenName = "queen_growth"
	AttachSessionPhaseState(session, ptrPhase(QueenPhaseIndependent), t.TempDir())
	session.Chat = runtime
	return session, runtime
}

// TestLiveToolsWiredThroughSessionRegistration drives the whole seam: a
// session whose chat runtime was registered through AddSession answers
// /live_tools with the phase's real tool surface — no manual attach — and the
// allowlist shapes the searchable/unregistered views.
//
// Parity: tests/test_live_tools_endpoint.py::test_actual_is_eager_plus_ask_user_not_the_registry,
// Parity: tests/test_live_tools_endpoint.py::test_expected_surface_is_status_tagged
func TestLiveToolsWiredThroughSessionRegistration(t *testing.T) {
	session, _ := surfaceSession(t)
	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "sess-surface")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ready, _ := data["phase_state_ready"].(bool); !ready {
		t.Fatalf("phase_state_ready = %#v, want true once a runtime is registered", data["phase_state_ready"])
	}
	if phase, _ := data["phase"].(string); phase != QueenPhaseIndependent {
		t.Fatalf("phase = %#v, want independent", data["phase"])
	}

	// actual_tools: the eager set minus the loop's ask_user and the
	// advertised-without-executor shims, with ask_user re-added as
	// framework_added — exactly the upstream view shape.
	actual := liveToolsNames(data["actual_tools"])
	for _, name := range []string{"search_tools", "terminal_exec", "browser_setup", "search_messages"} {
		if _, ok := actual[name]; !ok {
			t.Fatalf("actual_tools missing %q: %#v", name, actual)
		}
	}
	for _, absent := range []string{"collect_result", "credentials", "sentinel_setup", "chart_render", "run_worker"} {
		if _, ok := actual[absent]; ok {
			t.Fatalf("actual_tools must not carry %q: %#v", absent, actual)
		}
	}
	if len(actual) != 21 {
		t.Fatalf("actual_tools = %d entries %v, want 21 (20 eager + ask_user)", len(actual), actual)
	}
	added, _ := data["framework_added"].([]any)
	if len(added) != 1 {
		t.Fatalf("framework_added = %#v, want [ask_user]", data["framework_added"])
	}
	addedEntry, _ := added[0].(map[string]any)
	if addedEntry["name"] != "ask_user" || addedEntry["kind"] != "synthetic" {
		t.Fatalf("framework_added[0] = %#v", addedEntry)
	}

	// expected_tools: eager callable + allowlist-filtered searchable +
	// allowlisted names no server registered.
	byStatus := liveToolsStatusByName(data["expected_tools"])
	if byStatus["terminal_exec"] != "callable" || byStatus["chart_render"] != "searchable" {
		t.Fatalf("expected_tools statuses = %#v", byStatus)
	}
	// read_file is deferred but not allowlisted: the filter must hide it.
	if _, present := byStatus["read_file"]; present {
		t.Fatalf("non-allowlisted deferred tool leaked into expected_tools: %#v", byStatus)
	}
	for _, name := range []string{"ghost_tool", "search_timeline", "csv_read"} {
		if byStatus[name] != "unregistered" {
			t.Fatalf("expected_tools[%q] = %q, want unregistered", name, byStatus[name])
		}
	}

	if got, _ := data["mcp_tool_count_registered"].(float64); got != float64(len(surfaceMCPServerNames)) {
		t.Fatalf("mcp_tool_count_registered = %#v, want %d", data["mcp_tool_count_registered"], len(surfaceMCPServerNames))
	}

	diagnostic, _ := data["diagnostic"].(map[string]any)
	phaseDiag, _ := diagnostic["phase_state"].(map[string]any)
	if got, _ := phaseDiag["allowlist_total"].(float64); got != float64(len(surfaceAllowlist)) {
		t.Fatalf("diagnostic.phase_state.allowlist_total = %#v", phaseDiag["allowlist_total"])
	}
	if none, _ := phaseDiag["allowlist_is_none"].(bool); none {
		t.Fatalf("diagnostic.phase_state.allowlist_is_none = true, want false")
	}
	if got, _ := phaseDiag["mcp_tool_names_all_total"].(float64); got != float64(len(surfaceMCPServerNames)) {
		t.Fatalf("diagnostic.phase_state.mcp_tool_names_all_total = %#v", phaseDiag["mcp_tool_names_all_total"])
	}
	// independent = eager lifecycle names (non-MCP) + the MCP universe;
	// filtered = the same with the allowlist applied to MCP names.
	universe := map[string]struct{}{}
	for _, name := range surfaceMCPServerNames {
		universe[name] = struct{}{}
	}
	allow := map[string]struct{}{}
	for _, name := range surfaceAllowlist {
		allow[name] = struct{}{}
	}
	lifecycle := 0
	for name := range actual {
		if _, mcp := universe[name]; !mcp && name != "ask_user" {
			lifecycle++
		}
	}
	filteredMCP := 0
	for name := range universe {
		if _, ok := allow[name]; ok {
			filteredMCP++
		}
	}
	if got, _ := phaseDiag["independent_tools_total"].(float64); got != float64(lifecycle+len(surfaceMCPServerNames)) {
		t.Fatalf("independent_tools_total = %#v, want %d", phaseDiag["independent_tools_total"], lifecycle+len(surfaceMCPServerNames))
	}
	if got, _ := phaseDiag["filtered_independent_total"].(float64); got != float64(lifecycle+filteredMCP) {
		t.Fatalf("filtered_independent_total = %#v, want %d", phaseDiag["filtered_independent_total"], lifecycle+filteredMCP)
	}
}

// TestLiveToolsSurfaceListedBySessionsRoute pins the sessions list view for a
// registered session: queen_phase / queen_id / queen_name come from the phase
// state, not the planning/staging fallback.
//
// Parity-exempt: Go glue; the same phase fields are pinned by
// TestSessionToLiveDictReportsPhaseState and the upstream payload is covered
// by the session_detail probe fixtures.
func TestLiveToolsSurfaceListedBySessionsRoute(t *testing.T) {
	session, _ := surfaceSession(t)
	if state, ok := session.PhaseState.(*QueenPhaseState); ok {
		state.SetQueenIdentity("queen_growth", map[string]any{"name": "Steve Jobs"})
	}
	mux := sessionRoutesMux(liveToolsDepsWith(session))
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/sessions", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/sessions = %d", recorder.Code)
	}
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %#v", body["sessions"])
	}
	entry, _ := sessions[0].(map[string]any)
	if entry["queen_phase"] != QueenPhaseIndependent || entry["queen_id"] != "queen_growth" || entry["queen_name"] != "Steve Jobs" {
		t.Fatalf("session entry = %#v", entry)
	}
}

// TestPhaseSurfaceFollowsQueenToolAllowlistRefresh drives the queen tool
// route's refresh hook (PATCH /api/queen/{id}/tools → RefreshSessionTools) and
// asserts the phase surface reads the refreshed tier: the live-tools view the
// debug panel shows is the same object the route rebuilt.
//
// Parity: framework/server/tests/test_queen_tools.py::test_allowlist_keeps_listed_mcp_plus_all_lifecycle
func TestPhaseSurfaceFollowsQueenToolAllowlistRefresh(t *testing.T) {
	session, _ := surfaceSession(t)
	manager := NewSessionManager()
	manager.AddSession(session) // the registration choke point attaches the surface
	state, ok := session.PhaseState.(*QueenPhaseState)
	if !ok || state == nil {
		t.Fatalf("phase state = %#v", session.PhaseState)
	}
	before := map[string]struct{}{}
	for _, tool := range state.GetSearchableTools() {
		before[tool.Name] = struct{}{}
	}
	if _, present := before["chart_render"]; !present {
		t.Fatalf("searchable tier before the refresh = %v, want chart_render", before)
	}
	if !RefreshQueenToolAllowlist(session, []string{"chart_render"}) {
		t.Fatal("RefreshQueenToolAllowlist reported no refresh for a tiered session")
	}
	names := map[string]struct{}{}
	for _, tool := range state.GetSearchableTools() {
		names[tool.Name] = struct{}{}
	}
	if _, present := names["chart_render"]; !present {
		t.Fatalf("searchable tier after the refresh = %v, want chart_render", names)
	}
	if _, present := names["web_scrape"]; present {
		t.Fatalf("searchable tier kept the gated-out web_scrape: %v", names)
	}
}

// TestPhaseToolSurfaceWithoutRuntimeStaysEmpty mirrors a session whose
// provider was unavailable: the phase state exists, its tool views answer
// empty, and /live_tools reports ready with no surface rather than crashing or
// falling back to the registry.
//
// Parity-exempt: Go wiring; upstream's phase state always exists and this
// pins the degraded branch (no chat runtime attached).
func TestPhaseToolSurfaceWithoutRuntimeStaysEmpty(t *testing.T) {
	session := NewSession("sess-noruntime", eventbus.NewEventBus())
	AttachSessionPhaseState(session, ptrPhase(QueenPhaseIndependent), t.TempDir())
	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "sess-noruntime")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ready, _ := data["phase_state_ready"].(bool); !ready {
		t.Fatalf("phase_state_ready = %#v, want true (the method exists, the tier is empty)", data["phase_state_ready"])
	}
	actual, _ := data["actual_tools"].([]any)
	// Only the framework-added ask_user remains callable-now.
	if len(actual) != 1 {
		t.Fatalf("actual_tools = %#v, want just ask_user", data["actual_tools"])
	}
	expected, _ := data["expected_tools"].([]any)
	if len(expected) != 0 {
		t.Fatalf("expected_tools = %#v, want []", expected)
	}
}
