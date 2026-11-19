package server

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

func ptrPhase(value string) *string { return &value }

// Parity: framework/server/queen_orchestrator.py::prepare_queen_session (phase cascade)
func TestResolveQueenPhaseCascade(t *testing.T) {
	cases := []struct {
		name        string
		initial     *string
		persisted   string
		colonyBound bool
		want        string
	}{
		{name: "explicit independent wins over a bound colony", initial: ptrPhase("independent"), persisted: "colony", colonyBound: true, want: QueenPhaseIndependent},
		{name: "legacy alias is normalized", initial: ptrPhase("reviewing"), colonyBound: false, want: QueenPhaseColony},
		{name: "persisted beats bindings", persisted: "independent", colonyBound: true, want: QueenPhaseIndependent},
		{name: "bindings decide without a persisted answer", colonyBound: true, want: QueenPhaseColony},
		{name: "plain session defaults to independent", want: QueenPhaseIndependent},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ResolveQueenPhase(testCase.initial, testCase.persisted, testCase.colonyBound); got != testCase.want {
				t.Fatalf("phase = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.persist_phase
func TestQueenPhaseStatePersistsAcrossRestart(t *testing.T) {
	sessionDir := t.TempDir()
	metaPath := filepath.Join(sessionDir, "meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"queen_id":"queen_x"}`), 0o644); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	state := NewQueenPhaseState(QueenPhaseColony, "queen_x", map[string]any{"name": "X"}, metaPath, nil)
	if err := state.PersistPhase(); err != nil {
		t.Fatalf("PersistPhase: %v", err)
	}
	if got := ReadSessionPhase(sessionDir); got != QueenPhaseColony {
		t.Fatalf("persisted phase = %q, want colony", got)
	}
	// The merge keeps keys other writers own (colony_spawned, agent_path…).
	meta, err := readJSONObject(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta["queen_id"] != "queen_x" || meta["phase"] != QueenPhaseColony {
		t.Fatalf("meta = %#v, want queen_id preserved and phase stamped", meta)
	}

	// A fresh state built from the same directory (the cold-resume path)
	// starts in the persisted phase.
	restored := NewQueenPhaseState(ResolveQueenPhase(nil, ReadSessionPhase(sessionDir), false), "queen_x", nil, metaPath, nil)
	if restored.Phase() != QueenPhaseColony {
		t.Fatalf("restored phase = %q, want colony", restored.Phase())
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.switch_to_colony
func TestQueenPhaseStateSetPhasePersistsAndEmits(t *testing.T) {
	bus := eventbus.NewEventBus()
	sessionDir := t.TempDir()
	state := NewQueenPhaseState(QueenPhaseIndependent, "queen_x", nil, filepath.Join(sessionDir, "meta.json"), bus)

	var mu sync.Mutex
	phases := []string{}
	subscription := bus.Subscribe([]eventbus.EventType{eventbus.EventQueenPhaseChanged}, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		phase, _ := event.Data["phase"].(string)
		mu.Lock()
		phases = append(phases, phase)
		mu.Unlock()
	})
	t.Cleanup(func() { bus.Unsubscribe(subscription) })

	if err := state.SetPhase("colony"); err != nil {
		t.Fatalf("SetPhase: %v", err)
	}
	if state.Phase() != QueenPhaseColony {
		t.Fatalf("phase = %q, want colony", state.Phase())
	}
	if got := ReadSessionPhase(sessionDir); got != QueenPhaseColony {
		t.Fatalf("persisted phase = %q, want colony", got)
	}
	if !waitForServerCondition(time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(phases) == 1 && phases[0] == QueenPhaseColony
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("phase events = %v, want one colony", phases)
	}

	// The same phase again is not a transition: no second event.
	if err := state.SetPhase("colony"); err != nil {
		t.Fatalf("SetPhase (same): %v", err)
	}
	mu.Lock()
	count := len(phases)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("phase events = %d, want 1 after a no-op switch", count)
	}

	if err := state.SetPhase("bogus"); err == nil {
		t.Fatal("SetPhase accepted an invalid phase")
	}
}

// Parity: framework/server/session_manager.py::_create_session_core (phase stamped into meta.json)
func TestAttachSessionPhaseStateUsesBindingsAndStampsMeta(t *testing.T) {
	colonyDir := t.TempDir()
	colonySession := NewSession("s-colony", eventbus.NewEventBus())
	colonySession.ColonyID = "example-agent"
	state := AttachSessionPhaseState(colonySession, nil, colonyDir)
	if state == nil || state.Phase() != QueenPhaseColony {
		t.Fatalf("colony-bound phase = %#v, want colony", state)
	}
	if colonySession.PhaseState != state {
		t.Fatal("phase state was not attached to the session")
	}
	if got := ReadSessionPhase(colonyDir); got != QueenPhaseColony {
		t.Fatalf("meta phase = %q, want colony", got)
	}

	// An explicit initial_phase overrides the binding, as upstream.
	independentDir := t.TempDir()
	independentSession := NewSession("s-independent", eventbus.NewEventBus())
	independentSession.ColonyID = "example-agent"
	AttachSessionPhaseState(independentSession, ptrPhase("independent"), independentDir)
	if got := independentSession.PhaseState.(*QueenPhaseState).Phase(); got != QueenPhaseIndependent {
		t.Fatalf("phase = %q, want independent", got)
	}
	if got := ReadSessionPhase(independentDir); got != QueenPhaseIndependent {
		t.Fatalf("meta phase = %q, want independent", got)
	}
}

// Parity: framework/server/routes_sessions.py::session_to_live_dict (queen_phase)
func TestSessionToLiveDictReportsPhaseState(t *testing.T) {
	session := NewSession("s1", eventbus.NewEventBus())
	session.ColonyID = "example-agent"
	AttachSessionPhaseState(session, ptrPhase("independent"), t.TempDir())
	state := session.PhaseState.(*QueenPhaseState)
	state.SetQueenIdentity("queen_technology", map[string]any{"name": "Technology"})

	payload := sessionToLiveDict(session)
	if payload["queen_phase"] != QueenPhaseIndependent {
		t.Fatalf("queen_phase = %#v, want independent (not the staging fallback)", payload["queen_phase"])
	}
	if payload["queen_id"] != "queen_technology" {
		t.Fatalf("queen_id = %#v", payload["queen_id"])
	}
	if payload["queen_name"] != "Technology" {
		t.Fatalf("queen_name = %#v", payload["queen_name"])
	}
}

// Parity-exempt: Go allowlist wiring. The queen tool-library PATCH route only
// refreshes sessions whose phase state exists; the allowlist now reaches the
// session's live MCP tier instead of stopping at the sidecar file.
func TestApplyQueenMCPToolAllowlistFiltersChatTier(t *testing.T) {
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(nil)
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	searchable := tools.NamespacedMCPToolName(tools.MCPToolNamespace, "github", "search")
	allowed := tools.NamespacedMCPToolName(tools.MCPToolNamespace, "github", "list_repos")
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &fakeChatLLM{},
		Tools: []llm.ToolDef{
			{Name: "read_file"},
			{Name: searchable},
			{Name: allowed},
		},
		Execute: func(context.Context, string, map[string]any) (string, bool, error) {
			return "ok", false, nil
		},
		Root:      t.TempDir(),
		SessionID: "s-allowlist",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.toolTier == nil {
		t.Fatal("chat runtime built no MCP tier for a namespaced catalogue")
	}

	if !runtime.ApplyQueenMCPToolAllowlist([]string{"list_repos"}) {
		t.Fatal("ApplyQueenMCPToolAllowlist reported no refresh")
	}
	// MCP tools are searchable rather than eager, so the gated surface is the
	// tier's searchable set.
	names := tierToolNames(runtime.toolTier.statePtr().GetSearchableTools())
	if containsStringValue(names, searchable) {
		t.Fatalf("searchable tools = %v, want %s gated out", names, searchable)
	}
	if !containsStringValue(names, allowed) {
		t.Fatalf("searchable tools = %v, want the allowed MCP tool", names)
	}
	if eager := toolDefNames(runtime.toolTier.toolDefs()); !containsStringValue(eager, "read_file") {
		t.Fatalf("eager tools = %v, want the builtin untouched", eager)
	}

	// nil restores allow-all.
	if !runtime.ApplyQueenMCPToolAllowlist(nil) {
		t.Fatal("ApplyQueenMCPToolAllowlist(nil) reported no refresh")
	}
	if names := tierToolNames(runtime.toolTier.statePtr().GetSearchableTools()); !containsStringValue(names, searchable) {
		t.Fatalf("searchable tools = %v, want %s back", names, searchable)
	}
}

// Parity-exempt: Go-side allowlist guard. An absent tier or a malformed
// allowlist must report "not refreshed" rather than pretending a session was
// updated.
func TestRefreshQueenToolAllowlistWithoutTierReportsNoRefresh(t *testing.T) {
	session := NewSession("s1", eventbus.NewEventBus())
	if RefreshQueenToolAllowlist(session, []string{"anything"}) {
		t.Fatal("a session without a chat runtime reported a refresh")
	}
	// An unexpected body shape (number list) must not silently mean allow-all.
	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: &fakeChatLLM{}, Root: t.TempDir(), SessionID: "s2"})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.ApplyQueenMCPToolAllowlist([]any{1, 2}) {
		t.Fatal("a non-string allowlist reported a refresh")
	}
}

func toolDefNames(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

func tierToolNames(defs []tools.Tool) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

// Parity-exempt: Go wiring. POST /api/sessions now stamps PhaseState, so the
// queen tool-library routes can see a live session and the PATCH refresh reaches
// its MCP tier; a session without a phase state still reports no refresh.
func TestQueenToolsRoutesRefreshPhaseSession(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"files-tools": {
			{"name": "read_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	previous := tools.ProcessMCP()
	tools.SetProcessMCP(nil)
	t.Cleanup(func() { tools.SetProcessMCP(previous) })

	searchable := tools.NamespacedMCPToolName(tools.MCPToolNamespace, "github", "search")
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: &fakeChatLLM{},
		Tools: []llm.ToolDef{
			{Name: "read_file"},
			{Name: searchable},
		},
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      t.TempDir(),
		SessionID: "sess-phase",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	manager := NewSessionManager()
	session := NewSession("sess-phase", eventbus.NewEventBus())
	session.QueenName = "queen_technology"
	session.Chat = runtime
	AttachSessionPhaseState(session, ptrPhase(QueenPhaseColony), t.TempDir())
	manager.AddSession(session)
	// A live session with no phase state must stay untouched by the refresh.
	phaseless := NewSession("sess-phaseless", eventbus.NewEventBus())
	phaseless.QueenName = "queen_technology"
	phaseless.Chat = runtime
	manager.AddSession(phaseless)

	mux := queenToolTestMux(home, manager, func(deps *QueenToolRouteDeps) {
		deps.MCPCatalog = queenToolCatalog(catalog)
		deps.RefreshSessionTools = RefreshQueenToolAllowlist
	})

	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["queen_id"] != "queen_technology" {
		t.Fatalf("GET body = %#v", body)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":["read_file"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["refreshed_sessions"] != float64(1) {
		t.Fatalf("refreshed_sessions = %#v, want the phase-bearing session only", body["refreshed_sessions"])
	}
	if names := tierToolNames(runtime.toolTier.statePtr().GetSearchableTools()); containsStringValue(names, searchable) {
		t.Fatalf("searchable tools = %v, want the saved allowlist to gate %s", names, searchable)
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::normalize_legacy_phase
func TestReadSessionPhaseRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"phase":"bogus"}`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if got := ReadSessionPhase(dir); got != "" {
		t.Fatalf("garbage phase = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`not json`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if got := ReadSessionPhase(dir); got != "" {
		t.Fatalf("invalid meta phase = %q, want empty", got)
	}
}
