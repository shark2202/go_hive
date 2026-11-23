package cli

// Colony-phase task_create wiring and the DM accept wake-up: the colony pivot
// field must be advertised as new_colony (and only there), a colony-source
// accept must not fall through to the DM transcript fork, and the DM accept
// must wake the parked suggest_colony turn and publish COLONY_CREATED.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
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

// Parity: framework/server/routes_sessions.py::_create_colony_from_source (phase branches)
func TestCreateColonyFromSourceRefusesColonyPivotAcceptPath(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	source := hiveserver.NewSession("src-colony", eventbus.NewEventBus())
	source.ColonyID = "existing_colony"
	source.WorkerPath = fixture.home.ColonyDir("existing_colony")
	fixture.manager.AddSession(source)

	post := func() (int, map[string]any) {
		request := httptest.NewRequest(http.MethodPost, "/api/sessions",
			strings.NewReader(`{"colony_id":"sibling","source_session_id":"src-colony"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		fixture.newApp().Handler().ServeHTTP(response, request)
		var body map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response.Code, body
	}

	// No stashed pivot: the popup was never opened.
	status, body := post()
	if status != http.StatusConflict {
		t.Fatalf("status without pivot = %d body = %#v, want 409", status, body)
	}
	errorText, _ := body["error"].(string)
	if !strings.Contains(errorText, "no pending new_colony pivot") {
		t.Fatalf("error = %#v", body["error"])
	}

	// With a pivot: the lean-handoff accept path is a registered gap, not a
	// silent DM-style fork of the colony session.
	source.SetPendingColonyPivot(map[string]any{
		"goal": "goal", "handoff": "handoff", "source_phase": "colony",
		"tasks": []map[string]any{{"subject": "one"}},
	})
	status, body = post()
	if status != http.StatusNotImplemented {
		t.Fatalf("status with pivot = %d body = %#v, want 501", status, body)
	}
	if source.ColonySpawned {
		t.Fatal("the refused pivot accept still locked the source colony")
	}
}
