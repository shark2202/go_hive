package cli

// Colony-phase task_create wiring and the two colony accept paths: the colony
// pivot field must be advertised as new_colony (and only there), a colony-source
// accept must spawn a lean-handoff sibling colony (goal/handoff/tasks seeded,
// no transcript copy, source unlocked), and the DM accept must wake the parked
// suggest_colony turn and publish COLONY_CREATED.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/agents"
	hivecolony "github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// Parity: framework/server/queen_orchestrator.py::_request_new_colony_with_plan (schema field)
// Parity: framework/tasks/tools/session_tools.py::PivotHandler (field_name is per handler)
func TestEnableColonyTaskPivotAdvertisesNewColonyAndStubRefusesExecution(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	root := home.QueenSessionDir("default", "colony-session")
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)

	enableColonyTaskPivot(catalogue)

	def := findDef(t, catalogue.Defs(), tasks.TaskCreate)
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("task_create properties = %#v", def.Parameters["properties"])
	}
	if _, present := props["new_colony"]; !present {
		t.Fatal("new_colony missing from the colony task_create schema")
	}
	if _, present := props["new_session"]; present {
		t.Fatal("new_session must not be advertised in the colony phase")
	}
	handoff, _ := props["handoff"].(map[string]any)
	if handoff["description"] != tasks.HandoffDescription {
		t.Fatal("handoff description is not the frozen contract text")
	}

	// The executor is a defensive stub: the framework intercept must fire
	// before it (upstream's _request_new_colony_with_plan).
	content, isError, err := catalogue.Execute(t.Context(), tasks.TaskCreate, map[string]any{
		"goal":       "new colony goal",
		"handoff":    "brief",
		"new_colony": true,
		"tasks":      []any{map[string]any{"subject": "one"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if isError || !strings.Contains(content, "agentloop synthetic intercept") || !strings.Contains(content, `"success": false`) {
		t.Fatalf("executor result = %q (isError=%v), want the wiring-bug refusal envelope", content, isError)
	}
}

// recordingQueenNode is the QueenNode seam the accept path wakes.
type recordingQueenNode struct {
	mu     sync.Mutex
	texts  []string
	client []bool
}

func (n *recordingQueenNode) InjectEvent(text string, isClientInput bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, text)
	n.client = append(n.client, isClientInput)
	return nil
}

func (n *recordingQueenNode) calls() ([]string, []bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...), append([]bool(nil), n.client...)
}

// Parity: framework/server/routes_sessions.py::_inject_colony_confirmation
// Parity: framework/server/routes_sessions.py::_create_colony_from_source (COLONY_CREATED)
func TestCreateColonyFromSourceWakesSourceQueenAndPublishesColonyCreated(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	source := hiveserver.NewSession("src", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	queen := &recordingQueenNode{}
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": queen}}
	fixture.manager.AddSession(source)

	var created []*eventbus.AgentEvent
	source.EventBus.Subscribe([]eventbus.EventType{eventbus.EventColonyCreated}, func(event *eventbus.AgentEvent) {
		created = append(created, event.Clone())
	})

	request := httptest.NewRequest(http.MethodPost, "/api/sessions",
		strings.NewReader(`{"colony_id":"forked","source_session_id":"src"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create colony status = %d body = %s", response.Code, response.Body.String())
	}

	texts, clients := queen.calls()
	if len(texts) != 1 || !strings.Contains(texts[0], "this session is now locked") {
		t.Fatalf("wake injections = %#v, want the locked-session confirmation", texts)
	}
	if !clients[0] {
		t.Fatal("confirmation was not injected as client input")
	}
	if len(created) != 1 {
		t.Fatalf("colony_created events = %d, want 1", len(created))
	}
	data := created[0].Data
	if data["colony_id"] != "forked" || data["is_new"] != true {
		t.Fatalf("colony_created data = %#v", data)
	}
	if id, _ := data["queen_session_id"].(string); id == "" {
		t.Fatalf("colony_created queen_session_id = %#v", data["queen_session_id"])
	}
}

// colonyPivotSession is a live colony-phase queen with the accept-path seam
// (a queen node to wake) and a queued pivot. The source is a hand-built live
// record: the accept path reads only the pivot, the phase and the queen
// executor, like upstream's session object.
func colonyPivotSession(t *testing.T, fixture *lifecycleFixture, sessionID, colonyID string) (*hiveserver.Session, *recordingQueenNode) {
	t.Helper()
	source := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	source.ColonyID = colonyID
	source.WorkerPath = fixture.home.ColonyDir(colonyID)
	source.QueenName = "queen_alpha"
	source.QueenDir = fixture.home.QueenSessionDir("queen_alpha", sessionID)
	if err := os.MkdirAll(source.QueenDir, 0o755); err != nil {
		t.Fatalf("mkdir source queen dir: %v", err)
	}
	queen := &recordingQueenNode{}
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": queen}}
	fixture.manager.AddSession(source)
	return source, queen
}

func postCreateColony(t *testing.T, fixture *lifecycleFixture, colonyID, sourceID string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions",
		strings.NewReader(`{"colony_id":"`+colonyID+`","source_session_id":"`+sourceID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	fixture.newApp().Handler().ServeHTTP(response, request)
	body := map[string]any{}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return response.Code, body
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
func TestCreateColonyFromSourceSpawnsLeanHandoffSibling(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	source, queen := colonyPivotSession(t, fixture, "src-colony", "existing_colony")

	goal := "audit the Q4 supplier invoices"
	handoff := "The user wants the off-goal invoice audit moved out of this colony.\nInvoices: /tmp/invoices. Flag every mismatch over $500."
	source.SetPendingColonyPivot(map[string]any{
		"goal":         goal,
		"handoff":      handoff,
		"source_phase": "colony",
		"tasks": []map[string]any{
			{"subject": "pull the invoice list", "description": "read /tmp/invoices"},
			{"subject": "flag mismatches", "description": "compare against the PO table"},
		},
	})

	var colonyCreated []*eventbus.AgentEvent
	source.EventBus.Subscribe([]eventbus.EventType{eventbus.EventColonyCreated}, func(event *eventbus.AgentEvent) {
		colonyCreated = append(colonyCreated, event.Clone())
	})
	// The new session's bus does not exist until the accept creates it, so the
	// manager's live-session listener is the seam that subscribes in time.
	liveTaskEvents := make([]*eventbus.AgentEvent, 0)
	unsubscribe := fixture.manager.AddSessionListener(func(session *hiveserver.Session) {
		if session == nil || session.ID == source.ID || session.EventBus == nil {
			return
		}
		session.EventBus.Subscribe([]eventbus.EventType{eventbus.EventTaskCreated}, func(event *eventbus.AgentEvent) {
			liveTaskEvents = append(liveTaskEvents, event.Clone())
		})
	})
	t.Cleanup(unsubscribe)

	status, body := postCreateColony(t, fixture, "sibling", "src-colony")
	if status != http.StatusCreated {
		t.Fatalf("create sibling status = %d body = %#v, want 201", status, body)
	}
	sessionID, _ := body["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("create sibling body = %#v, missing session_id", body)
	}
	if body["colony_id"] != "sibling" || body["queen_phase"] != "colony" {
		t.Fatalf("create sibling body = %#v, want colony_id=sibling queen_phase=colony", body)
	}
	created := fixture.manager.GetSession(sessionID)
	if created == nil {
		t.Fatalf("created session %q is not registered", sessionID)
	}

	// The new session is a live colony-phase queen with the pivot guard armed.
	if created.ColonyID != "sibling" || created.Colony == nil {
		t.Fatalf("created session colony = %q/%v, want the live sibling colony", created.ColonyID, created.Colony)
	}
	if phase := hiveserver.SessionPhaseLabel(created); phase != hiveserver.QueenPhaseColony {
		t.Fatalf("created session phase = %q, want colony", phase)
	}
	if !created.ForkKickoffPending {
		t.Fatal("created session did not arm fork_kickoff_pending")
	}

	// The source colony stays alive and NOT spawn-locked.
	if fixture.manager.GetSession("src-colony") == nil {
		t.Fatal("source colony session was stopped by the sibling accept")
	}
	if source.ColonySpawned || source.SpawnedColonyID != "" {
		t.Fatalf("source spawn lock = %v/%q, want unlocked", source.ColonySpawned, source.SpawnedColonyID)
	}
	if meta, err := os.ReadFile(filepath.Join(source.QueenDir, "meta.json")); err == nil {
		var decoded map[string]any
		if json.Unmarshal(meta, &decoded) == nil {
			if _, locked := decoded["colony_spawned"]; locked {
				t.Fatalf("source meta.json carries a spawn lock: %s", meta)
			}
		}
	}
	if source.PendingColonyPivot() != nil {
		t.Fatal("accepted pivot was not cleared from the source session")
	}

	// The source queen is woken with the informational confirmation, as client
	// input, so her parked task_create(new_colony=true) turn unblocks.
	texts, clients := queen.calls()
	if len(texts) != 1 {
		t.Fatalf("source wake injections = %#v, want 1", texts)
	}
	wantWake := "[New colony 'sibling' created with 2 task(s) (queen session " + sessionID + ")"
	if !strings.Contains(texts[0], wantWake) || !strings.Contains(texts[0], "you stay idle here") {
		t.Fatalf("source wake message = %q, want %q", texts[0], wantWake)
	}
	if !clients[0] {
		t.Fatal("source wake was not injected as client input")
	}

	// COLONY_CREATED on the source bus marks the colony-phase accept: skipped
	// compaction, source_phase colony, and the new session as the target.
	if len(colonyCreated) != 1 {
		t.Fatalf("colony_created events on the source bus = %d, want 1", len(colonyCreated))
	}
	createdData := colonyCreated[0].Data
	if createdData["colony_id"] != "sibling" || createdData["is_new"] != true {
		t.Fatalf("colony_created data = %#v", createdData)
	}
	if createdData["compaction_status"] != "skipped" || createdData["source_phase"] != "colony" {
		t.Fatalf("colony_created markers = %#v, want skipped/colony", createdData)
	}
	if createdData["queen_session_id"] != sessionID {
		t.Fatalf("colony_created queen_session_id = %#v, want %q", createdData["queen_session_id"], sessionID)
	}

	// The seeded plan is on the NEW session's bus and list.
	if len(liveTaskEvents) != 2 {
		t.Fatalf("task_created events on the new bus = %d, want 2", len(liveTaskEvents))
	}
	store := tasks.NewTaskStore(fixture.home.Root)
	seeded := store.ListTasks(sessionID, true)
	if len(seeded) != 2 || seeded[0].Subject != "pull the invoice list" || seeded[1].Subject != "flag mismatches" {
		t.Fatalf("seeded tasks = %#v, want the pivot's two tasks in order", seeded)
	}
	if meta := store.GetMeta(sessionID); meta == nil || meta.Goal == nil || *meta.Goal != goal {
		t.Fatalf("seeded task goal = %#v, want %q", meta, goal)
	}

	// The fresh queen transcript holds the handoff brief ALONE: one seq-0 user
	// message, nothing copied from the source conversation.
	seededStore := agentloop.NewFileConversationStore(filepath.Join(created.QueenDir, "conversations"))
	parts, err := seededStore.ReadParts()
	_ = seededStore.Close()
	if err != nil {
		t.Fatalf("read seeded transcript: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("seeded transcript parts = %d, want 1 (handoff only): %#v", len(parts), parts)
	}
	if parts[0]["role"] != "user" {
		t.Fatalf("seeded transcript role = %#v, want user", parts[0]["role"])
	}
	if want := "[Session handoff]\n" + handoff; parts[0]["content"] != want {
		t.Fatalf("seeded transcript content = %q, want %q", parts[0]["content"], want)
	}

	// Session meta records the fork provenance.
	sessionMeta, ok := readJSONObject(filepath.Join(created.QueenDir, "meta.json"))
	if !ok {
		t.Fatal("read colony session meta: unavailable")
	}
	if sessionMeta["forked_from"] != source.ID || sessionMeta["colony_fork"] != true {
		t.Fatalf("colony session meta provenance = %#v", sessionMeta)
	}
	if sessionMeta["phase"] != "colony" || sessionMeta["queen_id"] != "queen_alpha" {
		t.Fatalf("colony session meta identity = %#v", sessionMeta)
	}
	if sessionMeta["agent_path"] != fixture.home.ColonyDir("sibling") {
		t.Fatalf("colony session meta agent_path = %#v", sessionMeta["agent_path"])
	}

	// worker.json is the inherited queen surface, loadable as the colony's
	// worker definition, with the tracker binding in its input data.
	colonyPath := fixture.home.ColonyDir("sibling")
	if _, statErr := os.Stat(filepath.Join(colonyPath, "tracker", "tracker.db")); statErr != nil {
		t.Fatalf("sibling tracker.db missing: %v", statErr)
	}
	if _, loadErr := agents.LoadAgentSpec(colonyPath); loadErr != nil {
		t.Fatalf("sibling worker.json is not loadable: %v", loadErr)
	}
	worker, ok := readJSONObject(filepath.Join(colonyPath, "worker.json"))
	if !ok {
		t.Fatal("read sibling worker.json: unavailable")
	}
	if worker["name"] != "worker" || worker["queen_phase"] != "colony" || worker["spawned_from"] != source.ID {
		t.Fatalf("sibling worker.json identity = %#v", worker)
	}
	if _, ok := worker["tools"].([]any); !ok {
		t.Fatalf("sibling worker.json tools = %#v, want a list", worker["tools"])
	}
	if _, ok := worker["loop_config"].(map[string]any); !ok {
		t.Fatalf("sibling worker.json loop_config = %#v, want a dict", worker["loop_config"])
	}
	inputData, _ := worker["input_data"].(map[string]any)
	binding, _ := inputData["binding"].(map[string]any)
	if binding["name"] != "sibling" {
		t.Fatalf("sibling worker input_data binding = %#v", inputData)
	}
	trackerDB, _ := binding["tracker_db"].(string)
	if !strings.HasSuffix(trackerDB, filepath.Join("sibling", "tracker", "tracker.db")) {
		t.Fatalf("sibling worker tracker_db = %#v", binding["tracker_db"])
	}

	// Colony metadata.json is provenance only.
	metadata, ok := readJSONObject(filepath.Join(colonyPath, "metadata.json"))
	if !ok {
		t.Fatal("read sibling metadata.json: unavailable")
	}
	if metadata["colony_id"] != "sibling" || metadata["queen_session_id"] != sessionID {
		t.Fatalf("sibling metadata = %#v", metadata)
	}
	if metadata["source_session_id"] != source.ID || metadata["queen_name"] != "queen_alpha" {
		t.Fatalf("sibling metadata provenance = %#v", metadata)
	}
	workers, _ := metadata["workers"].(map[string]any)
	if _, present := workers["worker"]; !present {
		t.Fatalf("sibling metadata workers = %#v", metadata["workers"])
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (queen surface inheritance)
// Parity: framework/server/routes_execution.py::_resolve_queen_only_tools
func TestCreateColonyFromSourceSiblingInheritsQueenSurface(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	source, _ := colonyPivotSession(t, fixture, "src-colony", "existing_colony")

	// A live queen loop: its available-tool list and loop config are what the
	// sibling's worker.json inherits. The synthetic ask_user is part of the
	// live surface; the queen-only lifecycle names are not worker material.
	liveQueen, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM: &lifecycleTestLLM{},
		Tools: []llm.ToolDef{
			{Name: "terminal_exec", Description: "run a command"},
			{Name: "get_worker_status", Description: "queen-only monitor"},
			{Name: "run_worker", Description: "queen-only fan-out"},
			{Name: "suggest_colony", Description: "independent-phase only"},
			{Name: "my_mcp_tool", Description: "a curated MCP tool"},
		},
		Execute: func(context.Context, string, map[string]any) (string, bool, error) {
			return "ok", false, nil
		},
		Root:      t.TempDir(),
		Bus:       eventbus.NewEventBus(),
		SessionID: "src-chat",
	})
	if err != nil {
		t.Fatalf("build source queen runtime: %v", err)
	}
	t.Cleanup(func() { _ = liveQueen.Close() })
	source.Chat = liveQueen
	liveSurface := liveQueen.ForkSurfaceSnapshot()
	if !liveSurface.HasLoop || liveSurface.MaxIterations <= 0 {
		t.Fatalf("source queen surface has no loop config: %#v", liveSurface)
	}
	source.SetPendingColonyPivot(map[string]any{
		"goal": "goal", "handoff": "brief", "tasks": []map[string]any{{"subject": "one"}},
	})

	status, body := postCreateColony(t, fixture, "sibling", "src-colony")
	if status != http.StatusCreated {
		t.Fatalf("create sibling status = %d body = %#v, want 201", status, body)
	}
	worker, ok := readJSONObject(filepath.Join(fixture.home.ColonyDir("sibling"), "worker.json"))
	if !ok {
		t.Fatal("read sibling worker.json: unavailable")
	}
	inherited := map[string]bool{}
	rawTools, _ := worker["tools"].([]any)
	for _, entry := range rawTools {
		if name, isString := entry.(string); isString {
			inherited[name] = true
		}
	}
	if !inherited["terminal_exec"] || !inherited["my_mcp_tool"] {
		t.Fatalf("sibling worker tools = %v, want the queen's work tools inherited", rawTools)
	}
	for _, stripped := range []string{"get_worker_status", "run_worker", "suggest_colony"} {
		if inherited[stripped] {
			t.Fatalf("sibling worker tools = %v, %q should have been stripped", rawTools, stripped)
		}
	}
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(liveSurface.MaxIterations) {
		t.Fatalf("sibling loop_config = %#v, want the queen's max_iterations %d", loopConfig, liveSurface.MaxIterations)
	}
	if loopConfig["max_iterations"] == float64(hivecolony.DefaultWorkerLoopConfig["max_iterations"]) {
		t.Fatalf("sibling loop_config = %#v kept the worker default instead of the queen's config", loopConfig)
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (validation)
func TestCreateColonyFromSourcePivotRequiresGoalHandoffAndTasks(t *testing.T) {
	cases := []struct {
		name   string
		pivot  map[string]any
		errMsg string
	}{
		{
			name:   "missing goal",
			pivot:  map[string]any{"handoff": "brief", "tasks": []map[string]any{{"subject": "one"}}},
			errMsg: "missing `goal`",
		},
		{
			name:   "missing handoff",
			pivot:  map[string]any{"goal": "goal", "tasks": []map[string]any{{"subject": "one"}}},
			errMsg: "missing `handoff`",
		},
		{
			name:   "missing tasks",
			pivot:  map[string]any{"goal": "goal", "handoff": "brief"},
			errMsg: "missing `tasks`",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newLifecycleFixture(t)
			t.Cleanup(func() { fixture.cleanup(t) })
			source, _ := colonyPivotSession(t, fixture, "src-colony", "existing_colony")
			source.SetPendingColonyPivot(testCase.pivot)

			status, body := postCreateColony(t, fixture, "sibling", "src-colony")
			if status != http.StatusConflict {
				t.Fatalf("status = %d body = %#v, want 409", status, body)
			}
			errorText, _ := body["error"].(string)
			if !strings.Contains(errorText, testCase.errMsg) {
				t.Fatalf("error = %#v, want %q", body["error"], testCase.errMsg)
			}
			if _, statErr := os.Stat(fixture.home.ColonyDir("sibling")); !os.IsNotExist(statErr) {
				t.Fatalf("rejected pivot left a colony on disk (stat err %v)", statErr)
			}
			if source.PendingColonyPivot() == nil {
				t.Fatal("rejected pivot was cleared from the source session")
			}
		})
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (default queen scope)
func TestCreateColonyFromSourceSiblingResolvesDefaultQueenScope(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	source := hiveserver.NewSession("src-colony", eventbus.NewEventBus())
	source.ColonyID = "existing_colony"
	source.WorkerPath = fixture.home.ColonyDir("existing_colony")
	queen := &recordingQueenNode{}
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": queen}}
	fixture.manager.AddSession(source)
	source.SetPendingColonyPivot(map[string]any{
		"goal": "goal", "handoff": "brief", "tasks": []map[string]any{{"subject": "one"}},
	})

	status, body := postCreateColony(t, fixture, "sibling", "src-colony")
	if status != http.StatusCreated {
		t.Fatalf("create sibling status = %d body = %#v, want 201", status, body)
	}
	sessionID, _ := body["session_id"].(string)
	created := fixture.manager.GetSession(sessionID)
	if created == nil {
		t.Fatalf("created session %q is not registered", sessionID)
	}
	// The loader's create path resolves the shared default queen onto its
	// materialised profile; the seeded tree and the metadata must agree with
	// it so a restart can find the transcript.
	defaultQueen := hiveserver.DefaultQueenID()
	if want := fixture.home.QueenSessionDir(defaultQueen, sessionID); created.QueenDir != want {
		t.Fatalf("created session dir = %q, want %q", created.QueenDir, want)
	}
	seededStore := agentloop.NewFileConversationStore(filepath.Join(created.QueenDir, "conversations"))
	parts, err := seededStore.ReadParts()
	_ = seededStore.Close()
	if err != nil || len(parts) != 1 || parts[0]["content"] != "[Session handoff]\nbrief" {
		t.Fatalf("default-queen seeded transcript = %#v (err %v)", parts, err)
	}
	metadata, ok := readJSONObject(filepath.Join(fixture.home.ColonyDir("sibling"), "metadata.json"))
	if !ok || metadata["queen_name"] != defaultQueen {
		t.Fatalf("sibling metadata queen_name = %#v, want %q", metadata["queen_name"], defaultQueen)
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (queen executor guard)
func TestCreateColonyFromSourcePivotNeedsLiveSourceQueen(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	source := hiveserver.NewSession("src-colony", eventbus.NewEventBus())
	source.ColonyID = "existing_colony"
	source.WorkerPath = fixture.home.ColonyDir("existing_colony")
	fixture.manager.AddSession(source)
	source.SetPendingColonyPivot(map[string]any{
		"goal": "goal", "handoff": "brief", "tasks": []map[string]any{{"subject": "one"}},
	})

	status, body := postCreateColony(t, fixture, "sibling", "src-colony")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %#v, want 503", status, body)
	}
	errorText, _ := body["error"].(string)
	if !strings.Contains(errorText, "source colony queen isn't running") {
		t.Fatalf("error = %#v", body["error"])
	}
	if _, statErr := os.Stat(fixture.home.ColonyDir("sibling")); !os.IsNotExist(statErr) {
		t.Fatalf("503 pivot left a colony on disk (stat err %v)", statErr)
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (slug conflict)
func TestCreateColonyFromSourcePivotRefusesExistingColony(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	source, _ := colonyPivotSession(t, fixture, "src-colony", "existing_colony")
	source.SetPendingColonyPivot(map[string]any{
		"goal": "goal", "handoff": "brief", "tasks": []map[string]any{{"subject": "one"}},
	})

	// The HTTP route deduplicates an occupied slug before this hook runs, so
	// the conflict is exercised at the lifecycle seam it actually guards.
	existing := fixture.home.ColonyDir("sibling")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir existing colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "worker.json"), []byte(`{"name":"worker"}`), 0o644); err != nil {
		t.Fatalf("write existing worker.json: %v", err)
	}

	_, err := fixture.lifecycle.CreateColonyFromSource(hiveserver.CreateColonyFromSourceParams{
		SourceSessionID: "src-colony",
		ColonyID:        "sibling",
	})
	forkErr, ok := err.(*hiveserver.ColonyForkError)
	if !ok {
		t.Fatalf("CreateColonyFromSource error = %v, want a ColonyForkError", err)
	}
	if forkErr.Status != http.StatusConflict || !strings.Contains(forkErr.Message, "already exists") {
		t.Fatalf("fork error = %d %q, want 409 already exists", forkErr.Status, forkErr.Message)
	}
	if source.PendingColonyPivot() == nil {
		t.Fatal("refused accept cleared the pending pivot")
	}
	if source.ColonySpawned {
		t.Fatal("refused accept spawn-locked the source colony")
	}
}

// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony (no pivot branch)
func TestCreateColonyFromSourceColonyPhaseNeedsPendingPivot(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_, _ = colonyPivotSession(t, fixture, "src-colony", "existing_colony")

	status, body := postCreateColony(t, fixture, "sibling", "src-colony")
	if status != http.StatusConflict {
		t.Fatalf("status without pivot = %d body = %#v, want 409", status, body)
	}
	errorText, _ := body["error"].(string)
	if !strings.Contains(errorText, "no pending new_colony pivot") {
		t.Fatalf("error = %#v", body["error"])
	}
}
