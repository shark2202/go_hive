package server

// Parity: tests/test_live_tools_endpoint.py::test_actual_is_eager_plus_ask_user_not_the_registry
// File-level anchor: each test below cites its own upstream case.

import (
	"net/http"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// fakeLivePhaseState mirrors the Python QueenPhaseState fixture: a real
// ToolTierState supplies the eager/searchable/unregistered split, and the
// extra methods expose it through the optional provider interfaces the
// handler duck-types.
type fakeLivePhaseState struct {
	*tools.ToolTierState
	phase    string
	mcpNames map[string]struct{}
}

func (f *fakeLivePhaseState) Phase() string { return f.phase }

func (f *fakeLivePhaseState) MCPToolNamesAll() map[string]struct{} { return f.mcpNames }

func (f *fakeLivePhaseState) EnabledMCPTools() ([]string, bool) {
	if f.EnabledAllowlist == nil {
		return nil, true
	}
	return f.EnabledAllowlist, false
}

func (f *fakeLivePhaseState) IndependentTools() []tools.Tool { return f.Pool }

func (f *fakeLivePhaseState) FilteredIndependentTools() []tools.Tool {
	out := append([]tools.Tool{}, f.GetCurrentTools()...)
	return append(out, f.GetSearchableTools()...)
}

func liveToolsFixturePhaseState() *fakeLivePhaseState {
	tool := func(name string) tools.Tool {
		return tools.Tool{
			Name:        name,
			Description: "desc of " + name,
			Parameters:  map[string]any{"type": "object"},
		}
	}
	pool := []tools.Tool{
		tool("chart_render"),
		tool("gmail_send"),
		tool("web_scrape"),
		tool("get_current_time"),
	}
	mcpNames := map[string]struct{}{
		"chart_render": {},
		"gmail_send":   {},
		"web_scrape":   {},
	}
	tier := &tools.ToolTierState{
		Pool:               pool,
		AlwaysEnabledNames: map[string]struct{}{"web_scrape": {}},
		GateableNames:      mcpNames,
		EnabledAllowlist:   []string{"chart_render", "gmail_send", "web_scrape", "ghost_tool"},
	}
	tier.Rebuild()
	tier.PromoteSearchedTools([]string{"chart_render"})
	return &fakeLivePhaseState{ToolTierState: tier, phase: "independent", mcpNames: mcpNames}
}

func liveToolsRequest(t *testing.T, deps SessionRouteDeps, sessionID string) (int, map[string]any) {
	t.Helper()
	mux := sessionRoutesMux(deps)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/sessions/"+sessionID+"/live_tools", "")
	return recorder.Code, body
}

func liveToolsDepsWith(session *Session) SessionRouteDeps {
	manager := NewSessionManager()
	if session != nil {
		manager.AddSession(session)
	}
	return SessionRouteDeps{Manager: manager}
}

func liveToolsNames(entries any) map[string]struct{} {
	out := map[string]struct{}{}
	list, _ := entries.([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := entry["name"].(string); ok {
			out[name] = struct{}{}
		}
	}
	return out
}

func liveToolsStatusByName(entries any) map[string]string {
	out := map[string]string{}
	list, _ := entries.([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		status, _ := entry["status"].(string)
		out[name] = status
	}
	return out
}

// Parity: tests/test_live_tools_endpoint.py::test_actual_is_eager_plus_ask_user_not_the_registry
func TestLiveToolsActualIsEagerPlusAskUserNotTheRegistry(t *testing.T) {
	session := &Session{ID: "s1", PhaseState: liveToolsFixturePhaseState()}
	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "s1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ready, _ := data["phase_state_ready"].(bool); !ready {
		t.Fatalf("phase_state_ready = %#v, want true", data["phase_state_ready"])
	}
	if _, ok := data["executor"]; ok {
		t.Fatal("live_tools must not report a second executor field")
	}

	actual := liveToolsNames(data["actual_tools"])
	want := map[string]struct{}{
		"chart_render":     {},
		"web_scrape":       {},
		"get_current_time": {},
		"ask_user":         {},
	}
	if len(actual) != len(want) {
		t.Fatalf("actual_tools = %#v, want %#v", actual, want)
	}
	for name := range want {
		if _, ok := actual[name]; !ok {
			t.Fatalf("actual_tools missing %q: %#v", name, actual)
		}
	}
	if _, ok := actual["gmail_send"]; ok {
		t.Fatalf("searchable gmail_send must not be callable-now")
	}
	if _, ok := actual["ghost_tool"]; ok {
		t.Fatalf("unregistered ghost_tool must not be callable-now")
	}

	// tools stays a back-compat alias of actual_tools.
	toolsView := liveToolsNames(data["tools"])
	actualView := liveToolsNames(data["actual_tools"])
	if len(toolsView) != len(actualView) {
		t.Fatalf("tools = %#v, actual_tools = %#v", toolsView, actualView)
	}
	for name := range actualView {
		if _, ok := toolsView[name]; !ok {
			t.Fatalf("tools differs from actual_tools: %#v vs %#v", toolsView, actualView)
		}
	}
}

// Parity: tests/test_live_tools_endpoint.py::test_expected_surface_is_status_tagged
func TestLiveToolsExpectedSurfaceIsStatusTagged(t *testing.T) {
	session := &Session{ID: "s1", PhaseState: liveToolsFixturePhaseState()}
	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "s1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	byName := liveToolsStatusByName(data["expected_tools"])
	want := map[string]string{
		"chart_render":     "callable",
		"web_scrape":       "callable",
		"get_current_time": "callable",
		"gmail_send":       "searchable",
		"ghost_tool":       "unregistered",
	}
	for name, wantStatus := range want {
		if byName[name] != wantStatus {
			t.Fatalf("expected_tools[%q].status = %q, want %q", name, byName[name], wantStatus)
		}
	}
}

// Parity: tests/test_live_tools_endpoint.py::test_no_phase_state_reports_not_ready_without_registry_fallback
func TestLiveToolsNoPhaseStateReportsNotReady(t *testing.T) {
	session := &Session{ID: "s1"}
	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "s1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if ready, _ := data["phase_state_ready"].(bool); ready {
		t.Fatalf("phase_state_ready = %#v, want false", data["phase_state_ready"])
	}
	if list, ok := data["actual_tools"].([]any); !ok || len(list) != 0 {
		t.Fatalf("actual_tools = %#v, want []", data["actual_tools"])
	}
	if list, ok := data["expected_tools"].([]any); !ok || len(list) != 0 {
		t.Fatalf("expected_tools = %#v, want []", data["expected_tools"])
	}
}

// Parity: tests/test_live_tools_endpoint.py::test_missing_session_returns_404
func TestLiveToolsMissingSessionReturns404(t *testing.T) {
	status, data := liveToolsRequest(t, liveToolsDepsWith(nil), "nope")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if data["error"] != "session 'nope' not found" {
		t.Fatalf("error = %#v", data["error"])
	}
}

// fakeLiveRegistry exercises the registry-side diagnostic surfaces
// (admission-gate snapshot + MCP server tool map) the handler reads through
// optional interfaces.
type fakeLiveRegistry struct {
	tpm   map[string]string
	provs []string
	tools map[string][]string
}

func (f *fakeLiveRegistry) ComputeMCPGateCredSnapshot() (map[string]string, []string) {
	return f.tpm, f.provs
}

func (f *fakeLiveRegistry) MCPServerTools() map[string][]string { return f.tools }

// fakeLiveTierColony satisfies ColonyRuntime plus the worker tiering provider.
type fakeLiveTierColony struct {
	tiering []colony.WorkerToolTiering
}

func (fakeLiveTierColony) StreamID() string { return "queen" }

func (fakeLiveTierColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (fakeLiveTierColony) InjectInput(workerID, content string) error { return nil }

func (f fakeLiveTierColony) WorkerToolTiering() []colony.WorkerToolTiering { return f.tiering }

// Parity-exempt: Go glue with no upstream test; covers the live-tools
// diagnostic snapshot and the worker tiering view, which Python exercises only
// through the running app.
func TestLiveToolsDiagnosticAndWorkerTiering(t *testing.T) {
	session := &Session{ID: "s1", PhaseState: liveToolsFixturePhaseState()}
	session.QueenToolRegistry = &fakeLiveRegistry{
		tpm: map[string]string{
			"chart_render": "google",
			"web_scrape":   "google",
			"ghost_tool":   "google",
			"other_tool":   "github",
		},
		provs: []string{"google", "github"},
		tools: map[string][]string{
			"google": {"chart_render", "web_scrape"},
		},
	}
	session.Colony = fakeLiveTierColony{tiering: []colony.WorkerToolTiering{
		{WorkerID: "w-eager", Enabled: true, EagerTotal: 2, SearchableTotal: 3, LoadedViaSearch: []string{"chart_render"}},
		{WorkerID: "w-flat", Enabled: false, EagerTotal: 7},
	}}

	status, data := liveToolsRequest(t, liveToolsDepsWith(session), "s1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	diagnostic, ok := data["diagnostic"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostic = %#v", data["diagnostic"])
	}
	if got, _ := diagnostic["gate_tool_provider_map_size"].(float64); got != 4 {
		t.Fatalf("gate_tool_provider_map_size = %#v, want 4", diagnostic["gate_tool_provider_map_size"])
	}
	if got, _ := diagnostic["registry_mcp_total"].(float64); got != 2 {
		t.Fatalf("registry_mcp_total = %#v, want 2", diagnostic["registry_mcp_total"])
	}
	admitted, _ := diagnostic["admitted_by_provider"].(map[string]any)
	if got, _ := admitted["google"].(float64); got != 2 {
		t.Fatalf("admitted_by_provider[google] = %#v, want 2", admitted["google"])
	}
	dropped, _ := diagnostic["dropped_by_provider"].(map[string]any)
	googleDropped, _ := dropped["google"].(map[string]any)
	if got, _ := googleDropped["count"].(float64); got != 1 {
		t.Fatalf("dropped_by_provider[google].count = %#v, want 1", googleDropped["count"])
	}
	phaseDiag, _ := diagnostic["phase_state"].(map[string]any)
	if got, _ := phaseDiag["google_admitted_total"].(float64); got != 2 {
		t.Fatalf("phase_state.google_admitted_total = %#v, want 2", phaseDiag["google_admitted_total"])
	}
	if got, _ := phaseDiag["allowlist_total"].(float64); got != 4 {
		t.Fatalf("phase_state.allowlist_total = %#v, want 4", phaseDiag["allowlist_total"])
	}
	if none, _ := phaseDiag["allowlist_is_none"].(bool); none {
		t.Fatalf("allowlist_is_none = true, want false")
	}
	if got, _ := phaseDiag["google_in_allowlist"].(float64); got != 2 {
		t.Fatalf("phase_state.google_in_allowlist = %#v, want 2", phaseDiag["google_in_allowlist"])
	}

	workers, ok := data["workers"].([]any)
	if !ok || len(workers) != 2 {
		t.Fatalf("workers = %#v", data["workers"])
	}
	first, _ := workers[0].(map[string]any)
	if first["worker_id"] != "w-eager" {
		t.Fatalf("workers[0].worker_id = %#v", first["worker_id"])
	}
	firstTier, _ := first["tiering"].(map[string]any)
	if enabled, _ := firstTier["enabled"].(bool); !enabled {
		t.Fatalf("workers[0].tiering.enabled = %#v, want true", firstTier["enabled"])
	}
	if got, _ := firstTier["searchable_total"].(float64); got != 3 {
		t.Fatalf("workers[0].tiering.searchable_total = %#v, want 3", firstTier["searchable_total"])
	}
	second, _ := workers[1].(map[string]any)
	secondTier, _ := second["tiering"].(map[string]any)
	if enabled, _ := secondTier["enabled"].(bool); enabled {
		t.Fatalf("workers[1].tiering.enabled = %#v, want false", secondTier["enabled"])
	}
	if got, _ := secondTier["eager_total"].(float64); got != 7 {
		t.Fatalf("workers[1].tiering.eager_total = %#v, want 7", secondTier["eager_total"])
	}
	if _, present := secondTier["searchable_total"]; present {
		t.Fatalf("disabled worker must not carry searchable_total: %#v", secondTier)
	}
}
