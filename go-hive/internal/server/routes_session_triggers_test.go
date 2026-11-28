package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

func doJSON(t *testing.T, app *App, method, target string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	app.ServeHTTP(recorder, request)
	var decoded map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.NewDecoder(recorder.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode %s body: %v", target, err)
		}
	}
	return recorder.Code, decoded, recorder.Header()
}

func doRawJSON(t *testing.T, app *App, method, target, body string) (int, map[string]any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(recorder, request)
	var decoded map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.NewDecoder(recorder.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode %s body: %v", target, err)
		}
	}
	return recorder.Code, decoded
}

func newTriggerTestSession(t *testing.T, id string, bus *eventbus.EventBus) *Session {
	t.Helper()
	session := NewSession(id, bus)
	session.ColonyID = "colony-" + id
	session.WorkerPath = t.TempDir()
	session.LoadedAt = 1000000.0
	return session
}

type fakeQueenNode struct {
	mu        sync.Mutex
	events    []agentloop.TriggerEvent
	texts     []string
	injectErr error
}

func (f *fakeQueenNode) InjectEvent(text string, isClientInput bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.injectErr != nil {
		return f.injectErr
	}
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeQueenNode) InjectTrigger(event agentloop.TriggerEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.injectErr != nil {
		return f.injectErr
	}
	f.events = append(f.events, event)
	return nil
}

func (f *fakeQueenNode) triggerEvents() []agentloop.TriggerEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agentloop.TriggerEvent, len(f.events))
	copy(out, f.events)
	return out
}

func installFakeQueen(session *Session, node *fakeQueenNode) {
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}
}

func stopTriggerTimer(session *Session, triggerID string) {
	if timer := session.PopActiveTimer(triggerID); timer != nil {
		timer.Stop()
	}
}

type eventCapture struct {
	mu     sync.Mutex
	events []*eventbus.AgentEvent
}

func newEventCapture(bus *eventbus.EventBus, types ...eventbus.EventType) *eventCapture {
	capture := &eventCapture{}
	bus.Subscribe(types, func(event *eventbus.AgentEvent) {
		capture.mu.Lock()
		capture.events = append(capture.events, event.Clone())
		capture.mu.Unlock()
	})
	return capture
}

func (c *eventCapture) eventsOf(eventType eventbus.EventType) []*eventbus.AgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*eventbus.AgentEvent, 0, len(c.events))
	for _, event := range c.events {
		if event.Type == eventType {
			out = append(out, event)
		}
	}
	return out
}

func (c *eventCapture) lastOf(eventType eventbus.EventType) *eventbus.AgentEvent {
	matches := c.eventsOf(eventType)
	if len(matches) == 0 {
		return nil
	}
	return matches[len(matches)-1]
}

type fakeWebhookServer struct {
	mu       sync.Mutex
	routes   []webhookRegistration
	started  bool
	running  bool
	startErr error
}

type webhookRegistration struct {
	sourceID string
	path     string
	methods  []string
}

func (f *fakeWebhookServer) AddRoute(sourceID, path string, methods []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, webhookRegistration{
		sourceID: sourceID,
		path:     path,
		methods:  append([]string(nil), methods...),
	})
}

func (f *fakeWebhookServer) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}

func (f *fakeWebhookServer) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeWebhookServer) registrations() []webhookRegistration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]webhookRegistration, len(f.routes))
	copy(out, f.routes)
	return out
}

func timerDefinition(id, task, cron string, interval any) triggers.TriggerDefinition {
	config := map[string]any{}
	if cron != "" {
		config["cron"] = cron
	}
	if interval != nil {
		config["interval_minutes"] = interval
	}
	return triggers.TriggerDefinition{
		ID:            id,
		TriggerType:   "timer",
		TriggerConfig: config,
		Task:          task,
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_update_trigger_task
func TestUpdateTriggerTask(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	def := timerDefinition("daily", "Old task", "0 5 * * *", nil)
	session.PutTrigger(def)
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{
		"task": "New task",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["task"] != "New task" {
		t.Fatalf("task = %#v, want New task", body["task"])
	}
	config, ok := body["trigger_config"].(map[string]any)
	if !ok || config["cron"] != "0 5 * * *" {
		t.Fatalf("trigger_config = %#v", body["trigger_config"])
	}
	stored, ok := session.LookupTrigger("daily")
	if !ok || stored.Task != "New task" {
		t.Fatalf("stored task = %#v, want New task", stored.Task)
	}

	entries := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(entries) != 1 || entries[0]["task"] != "New task" {
		t.Fatalf("persisted entries = %#v", entries)
	}
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_update_trigger_cron_restarts_active_timer
func TestUpdateTriggerCronRestartsActiveTimer(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	def := timerDefinition("daily", "Run task", "0 5 * * *", nil)
	def.Enabled = true
	session.PutTrigger(def)
	session.ActivateTrigger("daily")
	if err := StartTriggerTimer(session, "daily", &def); err != nil {
		t.Fatalf("start timer: %v", err)
	}
	t.Cleanup(func() { stopTriggerTimer(session, "daily") })
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{
		"trigger_config": map[string]any{"cron": "0 6 * * *"},
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	config, ok := body["trigger_config"].(map[string]any)
	if !ok || config["cron"] != "0 6 * * *" {
		t.Fatalf("trigger_config = %#v, want cron 0 6 * * *", body["trigger_config"])
	}
	restarted := session.PopActiveTimer("daily")
	if restarted == nil {
		t.Fatalf("active timer was not restarted")
	}
	restarted.Stop()
	stored, ok := session.LookupTrigger("daily")
	if !ok {
		t.Fatalf("daily disappeared")
	}
	if stored.TriggerConfig["cron"] != "0 6 * * *" {
		t.Fatalf("stored cron = %#v", stored.TriggerConfig["cron"])
	}
	if _, present := session.TriggerNextFire()["daily"]; !present {
		t.Fatalf("next fire was not re-seeded")
	}
}

// Parity-exempt: Python's update handler lets a start failure escape as an
// unhandled 500 while the id stays in active_trigger_ids; Go must report the
// same 500 shape and drop the dead trigger from the active set instead of
// claiming the new config is live.
func TestUpdateActiveWebhookRestartOutcomes(t *testing.T) {
	t.Run("start failure is reported and deactivates", func(t *testing.T) {
		bus := eventbus.NewEventBus()
		capture := newEventCapture(bus, eventbus.EventTriggerUpdated)
		session := newTriggerTestSession(t, "test_agent", bus)
		startErr := errors.New("listen tcp 127.0.0.1:8090: bind: address already in use")
		session.WebhookServer = &fakeWebhookServer{startErr: startErr}
		def := triggers.TriggerDefinition{
			ID:            "hook",
			TriggerType:   "webhook",
			TriggerConfig: map[string]any{"path": "/old", "port": 8090},
			Task:          "handle",
			Enabled:       true,
		}
		session.PutTrigger(def)
		session.ActivateTrigger("hook")
		if err := saveTriggerToAgent(session, "hook", &def); err != nil {
			t.Fatalf("seed triggers.json: %v", err)
		}
		subID := bus.Subscribe(
			[]eventbus.EventType{eventbus.EventWebhookReceived},
			func(*eventbus.AgentEvent) {},
			eventbus.WithFilterStream("hook"),
		)
		session.SetActiveWebhookSub("hook", subID)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/hook", map[string]any{
			"trigger_config": map[string]any{"path": "/new"},
		})
		if status != 500 {
			t.Fatalf("status = %d, want 500 (body %#v)", status, body)
		}
		wantError := "Failed to start trigger: " + startErr.Error()
		if body["error"] != wantError {
			t.Fatalf("error = %#v, want %q", body["error"], wantError)
		}
		if _, claimed := body["trigger_id"]; claimed {
			t.Fatalf("failure body reported success fields: %#v", body)
		}
		if session.IsTriggerActive("hook") {
			t.Fatalf("trigger still active after failed restart")
		}
		stored, ok := session.LookupTrigger("hook")
		if !ok || stored.Enabled {
			t.Fatalf("stored definition = %#v, want present and disabled", stored)
		}
		if events := capture.eventsOf(eventbus.EventTriggerUpdated); len(events) != 0 {
			t.Fatalf("TRIGGER_UPDATED published on failed restart: %d", len(events))
		}
		// Only the EventTriggerUpdated capture may remain: the pre-restart
		// subscription and the half-installed replacement are both gone.
		if count := bus.SubscriptionCount(); count != 1 {
			t.Fatalf("subscriptions = %d, want 1 (capture only)", count)
		}
		entries := triggers.ReadAgentTriggersJSON(session.WorkerPath)
		if len(entries) != 1 || entries[0]["enabled"] != false {
			t.Fatalf("persisted entries = %#v, want one disabled entry", entries)
		}
	})

	t.Run("successful restart keeps the trigger active", func(t *testing.T) {
		bus := eventbus.NewEventBus()
		capture := newEventCapture(bus, eventbus.EventTriggerUpdated)
		session := newTriggerTestSession(t, "test_agent", bus)
		fakeServer := &fakeWebhookServer{}
		session.WebhookServer = fakeServer
		session.PutTrigger(triggers.TriggerDefinition{
			ID:            "hook",
			TriggerType:   "webhook",
			TriggerConfig: map[string]any{"path": "/old"},
			Task:          "handle",
			Enabled:       true,
		})
		t.Cleanup(func() {
			if subID := session.PopActiveWebhookSub("hook"); subID != "" {
				bus.Unsubscribe(subID)
			}
		})
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/hook/activate", nil)
		if status != 200 {
			t.Fatalf("activate = %d/%#v", status, body)
		}

		status, body, _ = doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/hook", map[string]any{
			"trigger_config": map[string]any{"path": "/new"},
		})
		if status != 200 {
			t.Fatalf("status = %d, want 200 (body %#v)", status, body)
		}
		if body["trigger_id"] != "hook" {
			t.Fatalf("trigger_id = %#v", body["trigger_id"])
		}
		config, ok := body["trigger_config"].(map[string]any)
		if !ok || config["path"] != "/new" {
			t.Fatalf("trigger_config = %#v, want path /new", body["trigger_config"])
		}
		if !session.IsTriggerActive("hook") {
			t.Fatalf("trigger deactivated by successful restart")
		}
		stored, _ := session.LookupTrigger("hook")
		if !stored.Enabled {
			t.Fatalf("stored definition disabled by successful restart: %#v", stored)
		}
		registrations := fakeServer.registrations()
		if len(registrations) != 2 || registrations[0].path != "/old" || registrations[1].path != "/new" {
			t.Fatalf("registrations = %#v, want /old then /new", registrations)
		}
		if !fakeServer.Running() {
			t.Fatalf("listener not running after successful restart")
		}
		event := capture.lastOf(eventbus.EventTriggerUpdated)
		if event == nil {
			t.Fatalf("TRIGGER_UPDATED not published on successful restart")
		}
		eventConfig, _ := event.Data["trigger_config"].(map[string]any)
		if event.Data["trigger_id"] != "hook" || eventConfig["path"] != "/new" {
			t.Fatalf("event data = %#v", event.Data)
		}
		if count := bus.SubscriptionCount(); count != 2 {
			t.Fatalf("subscriptions = %d, want 2 (capture + live route)", count)
		}
	})
}

// Parity: framework/server/tests/test_api.py::TestSessionCRUD::test_update_trigger_cron_rejects_invalid_expression
func TestUpdateTriggerCronRejectsInvalidExpression(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	session.PutTrigger(timerDefinition("daily", "Run task", "0 5 * * *", nil))
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{
		"trigger_config": map[string]any{"cron": "not a cron"},
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	stored, _ := session.LookupTrigger("daily")
	if stored.TriggerConfig["cron"] != "0 5 * * *" {
		t.Fatalf("config changed on rejected patch: %#v", stored.TriggerConfig)
	}
}

// Parity: tests/test_missed_triggers.py::test_resolve_missed_fire_latest_injects_catch_up
func TestResolveMissedFireLatestInjectsCatchUp(t *testing.T) {
	session := newTriggerTestSession(t, "colD", nil)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	original := "2026-05-18T09:00:00+00:00"
	def := timerDefinition("z", "send invites", "0 9 * * *", nil)
	def.Enabled = true
	def.LastFiredAt = &original
	session.PutTrigger(def)

	results, err := ResolveMissed(session, map[string]any{"z": "fire_latest"})
	if err != nil {
		t.Fatalf("resolve missed: %v", err)
	}
	if len(results) != 1 || results["z"] != "fired" {
		t.Fatalf("results = %#v, want {z: fired}", results)
	}
	events := node.triggerEvents()
	if len(events) != 1 {
		t.Fatalf("injected %d events, want 1", len(events))
	}
	if events[0].Payload["catch_up"] != true {
		t.Fatalf("catch_up = %#v, want true", events[0].Payload["catch_up"])
	}
	stored, _ := session.LookupTrigger("z")
	if stored.LastFiredAt == nil || *stored.LastFiredAt == original {
		t.Fatalf("last_fired_at = %#v, want advanced", stored.LastFiredAt)
	}
}

// Parity: tests/test_missed_triggers.py::test_resolve_missed_skip_advances_anchor_without_firing
func TestResolveMissedSkipAdvancesAnchorWithoutFiring(t *testing.T) {
	session := newTriggerTestSession(t, "colE", nil)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	original := "2026-01-01T00:00:00+00:00"
	def := timerDefinition("z", "go", "", 60)
	def.Enabled = true
	def.LastFiredAt = &original
	session.PutTrigger(def)

	results, err := ResolveMissed(session, map[string]any{"z": "skip"})
	if err != nil {
		t.Fatalf("resolve missed: %v", err)
	}
	if len(results) != 1 || results["z"] != "skipped" {
		t.Fatalf("results = %#v, want {z: skipped}", results)
	}
	if len(node.triggerEvents()) != 0 {
		t.Fatalf("skip injected %d events, want 0", len(node.triggerEvents()))
	}
	stored, _ := session.LookupTrigger("z")
	if stored.LastFiredAt == nil || *stored.LastFiredAt == original {
		t.Fatalf("last_fired_at = %#v, want advanced", stored.LastFiredAt)
	}
}

// Parity: tests/test_missed_triggers.py::test_resolve_missed_reschedule_sets_future_next_due
func TestResolveMissedRescheduleSetsFutureNextDue(t *testing.T) {
	session := newTriggerTestSession(t, "colF", nil)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	original := "2026-01-01T00:00:00+00:00"
	def := timerDefinition("z", "go", "0 9 * * *", nil)
	def.Enabled = true
	def.LastFiredAt = &original
	session.PutTrigger(def)

	results, err := ResolveMissed(session, map[string]any{"z": "reschedule"})
	if err != nil {
		t.Fatalf("resolve missed: %v", err)
	}
	if len(results) != 1 || results["z"] != "rescheduled" {
		t.Fatalf("results = %#v, want {z: rescheduled}", results)
	}
	if len(node.triggerEvents()) != 0 {
		t.Fatalf("reschedule injected %d events, want 0", len(node.triggerEvents()))
	}
	stored, _ := session.LookupTrigger("z")
	if stored.NextDueAt == nil {
		t.Fatalf("next_due_at is nil, want a future stamp")
	}
	parsed := triggers.ParseISO(stored.NextDueAt)
	if parsed == nil || !parsed.After(time.Now().UTC()) {
		t.Fatalf("next_due_at = %#v, want a future time", stored.NextDueAt)
	}
}

// Parity: tests/test_missed_triggers.py::test_resolve_missed_unknown_trigger_returns_marker
func TestResolveMissedUnknownTriggerReturnsMarker(t *testing.T) {
	session := newTriggerTestSession(t, "colG", nil)

	results, err := ResolveMissed(session, map[string]any{"ghost": "fire_latest"})
	if err != nil {
		t.Fatalf("resolve missed: %v", err)
	}
	if len(results) != 1 || results["ghost"] != "unknown_trigger" {
		t.Fatalf("results = %#v, want {ghost: unknown_trigger}", results)
	}
}

// Parity: tests/test_missed_triggers.py::test_resolve_missed_invalid_decision_returns_marker
func TestResolveMissedInvalidDecisionReturnsMarker(t *testing.T) {
	session := newTriggerTestSession(t, "colH", nil)
	def := timerDefinition("z", "go", "", 5)
	session.PutTrigger(def)

	results, err := ResolveMissed(session, map[string]any{"z": "explode"})
	if err != nil {
		t.Fatalf("resolve missed: %v", err)
	}
	if len(results) != 1 || results["z"] != "invalid_decision:explode" {
		t.Fatalf("results = %#v, want invalid_decision:explode", results)
	}
}

// Parity-exempt: handle_create_trigger's validation ladder and activation event
func TestCreateTriggerValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   any
		status int
		errMsg string
	}{
		{
			name:   "name required",
			body:   map[string]any{"name": "   ", "task": "t", "trigger_config": map[string]any{"interval_minutes": 5}},
			status: 400,
			errMsg: "'name' is required",
		},
		{
			name:   "task required",
			body:   map[string]any{"name": "n", "task": "  ", "trigger_config": map[string]any{"interval_minutes": 5}},
			status: 400,
			errMsg: "'task' is required",
		},
		{
			name:   "timer only",
			body:   map[string]any{"name": "n", "task": "t", "trigger_type": "webhook", "trigger_config": map[string]any{"interval_minutes": 5}},
			status: 400,
			errMsg: "Only 'timer' schedules can be created here.",
		},
		{
			name:   "config object",
			body:   map[string]any{"name": "n", "task": "t", "trigger_config": "nope"},
			status: 400,
			errMsg: "'trigger_config' must be an object",
		},
		{
			name:   "cron type",
			body:   map[string]any{"name": "n", "task": "t", "trigger_config": map[string]any{"cron": 5}},
			status: 400,
			errMsg: "'trigger_config.cron' must be a string",
		},
		{
			name:   "cron invalid",
			body:   map[string]any{"name": "n", "task": "t", "trigger_config": map[string]any{"cron": "not a cron"}},
			status: 400,
			errMsg: "Invalid cron expression: not a cron",
		},
		{
			name:   "schedule missing",
			body:   map[string]any{"name": "n", "task": "t", "trigger_config": map[string]any{}},
			status: 400,
			errMsg: "Timer trigger needs 'cron' or 'interval_minutes' in trigger_config.",
		},
		{
			name:   "interval not positive",
			body:   map[string]any{"name": "n", "task": "t", "trigger_config": map[string]any{"interval_minutes": 0}},
			status: 400,
			errMsg: "'trigger_config.interval_minutes' must be > 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := newTriggerTestSession(t, "test_agent", nil)
			app := newAppWithSession(t, session)
			status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d (body %#v)", status, tc.status, body)
			}
			if body["error"] != tc.errMsg {
				t.Fatalf("error = %#v, want %q", body["error"], tc.errMsg)
			}
			if len(session.AvailableTriggers()) != 0 {
				t.Fatalf("a rejected create stored a trigger")
			}
		})
	}
}

// Parity-exempt: handle_create_trigger writes the definition, starts the timer,
func TestCreateTriggerSuccess(t *testing.T) {
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventTriggerActivated)
	session := newTriggerTestSession(t, "test_agent", bus)
	app := newAppWithSession(t, session)
	t.Cleanup(func() { stopTriggerTimer(session, "user-daily-digest") })

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", map[string]any{
		"name":           "Daily Digest",
		"task":           "summarize",
		"trigger_config": map[string]any{"interval_minutes": 30},
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["trigger_id"] != "user-daily-digest" {
		t.Fatalf("trigger_id = %#v, want user-daily-digest", body["trigger_id"])
	}
	if body["name"] != "Daily Digest" || body["trigger_type"] != "timer" {
		t.Fatalf("identity = %#v", body)
	}
	config, ok := body["trigger_config"].(map[string]any)
	if !ok || config["interval_minutes"] != float64(30) {
		t.Fatalf("trigger_config = %#v", body["trigger_config"])
	}

	stored, ok := session.LookupTrigger("user-daily-digest")
	if !ok || !stored.Enabled || stored.Task != "summarize" {
		t.Fatalf("stored def = %#v", stored)
	}
	if !session.IsTriggerActive("user-daily-digest") {
		t.Fatalf("trigger not activated")
	}
	timer := session.PopActiveTimer("user-daily-digest")
	if timer == nil {
		t.Fatalf("no timer started")
	}
	timer.Stop()

	event := capture.lastOf(eventbus.EventTriggerActivated)
	if event == nil {
		t.Fatalf("TRIGGER_ACTIVATED not published")
	}
	if event.Data["trigger_id"] != "user-daily-digest" || event.Data["trigger_type"] != "timer" {
		t.Fatalf("event data = %#v", event.Data)
	}
	if event.Data["name"] != "Daily Digest" {
		t.Fatalf("event name = %#v", event.Data["name"])
	}
	if entry, ok := event.Data["entry_node"]; !ok || entry != nil {
		t.Fatalf("entry_node = %#v, want present nil", event.Data["entry_node"])
	}
	eventConfig, ok := event.Data["trigger_config"].(map[string]any)
	if minutes, numeric := numericValue(eventConfig["interval_minutes"]); !ok || !numeric || minutes != 30 {
		t.Fatalf("event config = %#v", event.Data["trigger_config"])
	}
	if _, present := eventConfig["next_fire_in"]; !present {
		t.Fatalf("event config missing next_fire_in: %#v", eventConfig)
	}

	entries := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(entries) != 1 || entries[0]["id"] != "user-daily-digest" || entries[0]["enabled"] != true {
		t.Fatalf("persisted entries = %#v", entries)
	}
}

// Parity-exempt: slug collisions append -2, -3 to the already-prefixed
func TestCreateTriggerIDSuffixOnCollision(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	existing := timerDefinition("user-daily-digest", "old", "", 60)
	session.PutTrigger(existing)
	app := newAppWithSession(t, session)
	t.Cleanup(func() { stopTriggerTimer(session, "user-daily-digest-2") })

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", map[string]any{
		"name":           "Daily Digest",
		"task":           "new",
		"trigger_config": map[string]any{"interval_minutes": 60},
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["trigger_id"] != "user-daily-digest-2" {
		t.Fatalf("trigger_id = %#v, want user-daily-digest-2", body["trigger_id"])
	}
}

// Parity-exempt: malformed JSON is handle_create_trigger's own 400.
func TestCreateTriggerInvalidJSON(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	app := newAppWithSession(t, session)

	status, body := doRawJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", "{")
	if status != 400 || body["error"] != "Invalid JSON body" {
		t.Fatalf("status/error = %d/%#v", status, body["error"])
	}
}

// Parity-exempt: handle_run_trigger's guard ladder, forced payload, and
func TestRunTriggerGuards(t *testing.T) {
	t.Run("colony not loaded", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.ColonyID = ""
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/run", nil)
		if status != 409 || body["error"] != "Colony not loaded" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("queen not ready", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/run", nil)
		if status != 409 || body["error"] != "Queen not ready" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("unknown trigger", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/ghost/run", nil)
		if status != 404 || body["error"] != "Trigger 'ghost' not found" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
}

// Parity-exempt: a manual run injects a forced event and bumps the fire stats.
func TestRunTriggerFiresForcedEvent(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "test_agent", bus)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	def := timerDefinition("daily", "Run task", "0 5 * * *", nil)
	session.PutTrigger(def)
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/run", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["status"] != "fired" || body["trigger_id"] != "daily" {
		t.Fatalf("body = %#v", body)
	}
	events := node.triggerEvents()
	if len(events) != 1 {
		t.Fatalf("injected %d events, want 1", len(events))
	}
	if events[0].TriggerType != "timer" || events[0].SourceID != "daily" {
		t.Fatalf("event identity = %#v", events[0])
	}
	if events[0].Payload["forced"] != true || events[0].Payload["task"] != "Run task" {
		t.Fatalf("payload = %#v", events[0].Payload)
	}
	stat := session.TriggerFireStats()["daily"]
	if stat.FireCount != 1 {
		t.Fatalf("fire_count = %d, want 1", stat.FireCount)
	}
}

// Parity-exempt: an injection failure answers the upstream 500 message.
func TestRunTriggerInjectionFailure(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	node := &fakeQueenNode{injectErr: errors.New("boom")}
	installFakeQueen(session, node)
	session.PutTrigger(timerDefinition("daily", "t", "", 60))
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/run", nil)
	if status != 500 || body["error"] != "Failed to fire trigger: boom" {
		t.Fatalf("status/error = %d/%#v", status, body["error"])
	}
}

// Parity-exempt: handle_activate/deactivate_trigger's status bodies, timer
func TestActivateDeactivateTimerTrigger(t *testing.T) {
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventTriggerActivated, eventbus.EventTriggerDeactivated)
	session := newTriggerTestSession(t, "test_agent", bus)
	session.PutTrigger(timerDefinition("daily", "Run task", "", 30))
	app := newAppWithSession(t, session)
	t.Cleanup(func() { stopTriggerTimer(session, "daily") })

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/activate", nil)
	if status != 200 || body["status"] != "activated" {
		t.Fatalf("activate status/body = %d/%#v", status, body)
	}
	if !session.IsTriggerActive("daily") {
		t.Fatalf("trigger not active")
	}
	activatedTimer := session.PopActiveTimer("daily")
	if activatedTimer == nil {
		t.Fatalf("no timer after activate")
	}
	activatedTimer.Stop()
	if _, present := session.TriggerNextFire()["daily"]; !present {
		t.Fatalf("next fire not seeded")
	}
	activated := capture.lastOf(eventbus.EventTriggerActivated)
	if activated == nil || activated.Data["trigger_id"] != "daily" || activated.Data["trigger_type"] != "timer" {
		t.Fatalf("TRIGGER_ACTIVATED = %#v", activated)
	}

	status, body, _ = doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/activate", nil)
	if status != 200 || body["status"] != "already_active" {
		t.Fatalf("second activate = %d/%#v", status, body)
	}

	status, body, _ = doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/deactivate", nil)
	if status != 200 || body["status"] != "deactivated" {
		t.Fatalf("deactivate = %d/%#v", status, body)
	}
	if session.IsTriggerActive("daily") {
		t.Fatalf("trigger still active after deactivate")
	}
	stored, _ := session.LookupTrigger("daily")
	if stored.Enabled {
		t.Fatalf("definition still enabled")
	}
	deactivated := capture.lastOf(eventbus.EventTriggerDeactivated)
	if deactivated == nil || deactivated.Data["trigger_id"] != "daily" {
		t.Fatalf("TRIGGER_DEACTIVATED = %#v", deactivated)
	}
	if name, ok := deactivated.Data["name"]; ok && name != "daily" {
		t.Fatalf("deactivate name = %#v, want daily", name)
	}

	status, body, _ = doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/daily/deactivate", nil)
	if status != 200 || body["status"] != "already_inactive" {
		t.Fatalf("second deactivate = %d/%#v", status, body)
	}
}

// Parity-exempt: activation rejects an unknown type with the upstream 400.
func TestActivateTriggerUnsupportedType(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "odd",
		TriggerType:   "email",
		TriggerConfig: map[string]any{},
	})
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/odd/activate", nil)
	if status != 400 || body["error"] != "Unsupported trigger type: email" {
		t.Fatalf("status/error = %d/%#v", status, body["error"])
	}
	if session.IsTriggerActive("odd") {
		t.Fatalf("unsupported trigger became active")
	}
}

// Parity-exempt: a webhook activation registers the route and subscribes, and
func TestActivateWebhookTriggerRegistersRoute(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "test_agent", bus)
	fakeServer := &fakeWebhookServer{}
	session.WebhookServer = fakeServer
	config := map[string]any{"path": "/hook", "methods": []any{"post"}}
	session.PutTrigger(triggers.TriggerDefinition{
		ID:            "hook",
		TriggerType:   "webhook",
		TriggerConfig: config,
		Task:          "handle",
	})
	t.Cleanup(func() {
		if subID := session.PopActiveWebhookSub("hook"); subID != "" {
			bus.Unsubscribe(subID)
		}
	})
	app := newAppWithSession(t, session)

	status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/hook/activate", nil)
	if status != 200 || body["status"] != "activated" {
		t.Fatalf("activate = %d/%#v", status, body)
	}
	registrations := fakeServer.registrations()
	if len(registrations) != 1 {
		t.Fatalf("registrations = %#v, want one", registrations)
	}
	if registrations[0].sourceID != "hook" || registrations[0].path != "/hook" {
		t.Fatalf("registration = %#v", registrations[0])
	}
	if len(registrations[0].methods) != 1 || registrations[0].methods[0] != "POST" {
		t.Fatalf("methods = %#v, want [POST]", registrations[0].methods)
	}
	if !fakeServer.Running() {
		t.Fatalf("listener not started")
	}
	if bus.SubscriptionCount() != 1 {
		t.Fatalf("subscriptions = %d, want 1", bus.SubscriptionCount())
	}

	status, body, _ = doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers/hook/deactivate", nil)
	if status != 200 || body["status"] != "deactivated" {
		t.Fatalf("deactivate = %d/%#v", status, body)
	}
	if bus.SubscriptionCount() != 0 {
		t.Fatalf("subscription not removed on deactivate")
	}
}

// Parity-exempt: StartTriggerWebhook reports ErrNoWebhookServer when no
func TestStartTriggerWebhookWithoutServer(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "test_agent", bus)
	previous := NewTriggerWebhookServer
	NewTriggerWebhookServer = nil
	t.Cleanup(func() { NewTriggerWebhookServer = previous })

	def := triggers.TriggerDefinition{
		ID:            "hook",
		TriggerType:   "webhook",
		TriggerConfig: map[string]any{"path": "/hook"},
	}
	t.Cleanup(func() {
		if subID := session.PopActiveWebhookSub("hook"); subID != "" {
			bus.Unsubscribe(subID)
		}
	})
	if err := StartTriggerWebhook(session, "hook", &def); !errors.Is(err, ErrNoWebhookServer) {
		t.Fatalf("err = %v, want ErrNoWebhookServer", err)
	}
}

// Parity-exempt: a matching webhook_received event reaches the queen and bumps
func TestWebhookTriggerDispatchesMatchingEvent(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "test_agent", bus)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	fakeServer := &fakeWebhookServer{}
	session.WebhookServer = fakeServer
	def := triggers.TriggerDefinition{
		ID:            "hook",
		TriggerType:   "webhook",
		TriggerConfig: map[string]any{"path": "/hook", "methods": []any{"POST"}},
		Description:   "Hook",
		Task:          "handle",
	}
	session.PutTrigger(def)
	if err := StartTriggerWebhook(session, "hook", &def); err != nil {
		t.Fatalf("start webhook: %v", err)
	}
	t.Cleanup(func() {
		if subID := session.PopActiveWebhookSub("hook"); subID != "" {
			bus.Unsubscribe(subID)
		}
	})

	bus.EmitWebhookReceived("hook", "/elsewhere", "POST", map[string]string{}, map[string]any{}, map[string]string{})
	if len(node.triggerEvents()) != 0 {
		t.Fatalf("mismatched path fired the trigger")
	}

	bus.EmitWebhookReceived("hook", "/hook", "POST",
		map[string]string{"X-Probe": "yes"},
		map[string]any{"k": "v"},
		map[string]string{"q": "1"},
	)
	events := node.triggerEvents()
	if len(events) != 1 {
		t.Fatalf("injected %d events, want 1", len(events))
	}
	payload := events[0].Payload
	if payload["task"] != "handle" || payload["path"] != "/hook" || payload["method"] != "POST" {
		t.Fatalf("payload = %#v", payload)
	}
	headers, _ := payload["headers"].(map[string]any)
	if headers["X-Probe"] != "yes" {
		t.Fatalf("headers = %#v", payload["headers"])
	}
	body, _ := payload["payload"].(map[string]any)
	if body["k"] != "v" {
		t.Fatalf("payload body = %#v", payload["payload"])
	}
	query, _ := payload["query_params"].(map[string]any)
	if query["q"] != "1" {
		t.Fatalf("query_params = %#v", payload["query_params"])
	}
	if stat := session.TriggerFireStats()["hook"]; stat.FireCount != 1 {
		t.Fatalf("fire_count = %d, want 1", stat.FireCount)
	}
}

// Parity-exempt: the resolve_missed route's 409/400/500 envelope and Python
func TestResolveMissedRoute(t *testing.T) {
	t.Run("colony not loaded", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.ColonyID = ""
		app := newAppWithSession(t, session)

		status, body := doRawJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed", "{")
		if status != 409 || body["error"] != "Colony not loaded" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body := doRawJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed", "{")
		if status != 400 || body["error"] != "invalid JSON body" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("decisions not object", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed", map[string]any{
			"decisions": "nope",
		})
		if status != 400 || body["error"] != "decisions must be an object {trigger_id: decision}" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("empty decisions", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		for _, body := range []any{
			map[string]any{},
			map[string]any{"decisions": map[string]any{}},
			map[string]any{"decisions": []any{}},
			map[string]any{"decisions": nil},
		} {
			status, decoded, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed", body)
			if status != 200 {
				t.Fatalf("status = %d, want 200 (body %#v)", status, decoded)
			}
			results, ok := decoded["results"].(map[string]any)
			if !ok || len(results) != 0 {
				t.Fatalf("results = %#v, want empty", decoded["results"])
			}
		}
	})
	t.Run("non-string decision values", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body := doRawJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed",
			`{"decisions": {"a": 5, "b": 5.0, "c": null, "d": true}}`)
		if status != 200 {
			t.Fatalf("status = %d, want 200 (body %#v)", status, body)
		}
		results, _ := body["results"].(map[string]any)
		want := map[string]string{
			"a": "invalid_decision:5",
			"b": "invalid_decision:5.0",
			"c": "invalid_decision:None",
			"d": "invalid_decision:True",
		}
		for key, expected := range want {
			if results[key] != expected {
				t.Fatalf("results[%s] = %#v, want %q", key, results[key], expected)
			}
		}
	})
	t.Run("unhashable decision", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/colony/resolve_missed", map[string]any{
			"decisions": map[string]any{"z": []any{1, 2}},
		})
		if status != 500 || body["error"] != "Failed to resolve missed triggers: unhashable type: 'list'" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
}

// Parity-exempt: PATCH's guard ladder and config merge semantics.
func TestUpdateTriggerGuardsAndMerge(t *testing.T) {
	t.Run("unknown trigger", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/ghost", map[string]any{"task": "x"})
		if status != 404 || body["error"] != "Trigger 'ghost' not found" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body := doRawJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", "{")
		if status != 400 || body["error"] != "Invalid JSON body" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("task must be string", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{"task": 5})
		if status != 400 || body["error"] != "'task' must be a string" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("config must be object", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{"trigger_config": "x"})
		if status != 400 || body["error"] != "'trigger_config' must be an object" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("nothing to update", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		session.PutTrigger(timerDefinition("daily", "t", "", 60))
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{})
		if status != 400 || body["error"] != "Provide at least one of 'task' or 'trigger_config'" {
			t.Fatalf("status/error = %d/%#v", status, body["error"])
		}
	})
	t.Run("cron displaces interval", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		def := timerDefinition("daily", "t", "", 30)
		session.PutTrigger(def)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPatch, "/api/sessions/test_agent/triggers/daily", map[string]any{
			"trigger_config": map[string]any{"cron": "0 5 * * *"},
		})
		if status != 200 {
			t.Fatalf("status = %d, want 200 (body %#v)", status, body)
		}
		config, _ := body["trigger_config"].(map[string]any)
		if config["cron"] != "0 5 * * *" {
			t.Fatalf("cron = %#v", config["cron"])
		}
		if _, present := config["interval_minutes"]; present {
			t.Fatalf("interval_minutes survived the cron merge: %#v", config)
		}
	})
}

// Parity-exempt: _emit_trigger_fired's payload and stat bump.
func TestEmitTriggerFiredPayload(t *testing.T) {
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventTriggerFired)
	session := NewSession("test_agent", bus)
	session.TriggerClock = fakeTriggerClock{mono: 1000.0, wall: 5000.0}
	def := timerDefinition("daily", "Run task", "0 5 * * *", nil)
	def.Description = "Daily"
	session.PutTrigger(def)
	session.SetTriggerNextFire("daily", 1030.0)

	EmitTriggerFired(session, "daily", "timer")

	event := capture.lastOf(eventbus.EventTriggerFired)
	if event == nil {
		t.Fatalf("TRIGGER_FIRED not published")
	}
	data := event.Data
	if data["trigger_id"] != "daily" || data["trigger_type"] != "timer" {
		t.Fatalf("data = %#v", data)
	}
	if data["name"] != "Daily" || data["task"] != "Run task" {
		t.Fatalf("name/task = %#v/%#v", data["name"], data["task"])
	}
	if data["last_fired_at"] != int64(5000000) {
		t.Fatalf("last_fired_at = %#v, want 5000000", data["last_fired_at"])
	}
	if data["fire_count"] != 1 {
		t.Fatalf("fire_count = %#v, want 1", data["fire_count"])
	}
	if data["next_fire_in"] != 30.0 || data["next_fire_at"] != int64(5030000) {
		t.Fatalf("next fire = %#v/%#v", data["next_fire_in"], data["next_fire_at"])
	}
	if stat := session.TriggerFireStats()["daily"]; stat.FireCount != 1 {
		t.Fatalf("stat fire_count = %d, want 1", stat.FireCount)
	}
}

// Parity-exempt: _persist_active_triggers rewrites enabled flags and task
func TestPersistActiveTriggers(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	entries := []map[string]any{
		{"id": "a", "enabled": false, "task": "old"},
		{"id": "b", "enabled": true, "task": "keep"},
	}
	if err := triggers.WriteAgentTriggersJSON(session.WorkerPath, entries); err != nil {
		t.Fatalf("seed triggers: %v", err)
	}
	session.PutTrigger(timerDefinition("a", "new", "", 60))
	session.ActivateTrigger("a")

	PersistActiveTriggers(session, "test_agent")

	persisted := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(persisted) != 2 {
		t.Fatalf("persisted = %#v", persisted)
	}
	byID := map[string]map[string]any{}
	for _, entry := range persisted {
		byID[entry["id"].(string)] = entry
	}
	if byID["a"]["enabled"] != true || byID["a"]["task"] != "new" {
		t.Fatalf("entry a = %#v", byID["a"])
	}
	if byID["b"]["enabled"] != false || byID["b"]["task"] != "keep" {
		t.Fatalf("entry b = %#v", byID["b"])
	}
}

// Parity-exempt: a fast interval timer injects and bumps the stats, proving the
func TestTimerFiresAndStampsStats(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := newTriggerTestSession(t, "test_agent", bus)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	def := timerDefinition("fast", "tick", "", 0.0005)
	session.PutTrigger(def)
	if err := StartTriggerTimer(session, "fast", &def); err != nil {
		t.Fatalf("start timer: %v", err)
	}
	t.Cleanup(func() { stopTriggerTimer(session, "fast") })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if session.TriggerFireStats()["fast"].FireCount > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stat := session.TriggerFireStats()["fast"]; stat.FireCount < 1 {
		t.Fatalf("fire_count = %d, want >= 1", stat.FireCount)
	}
	events := node.triggerEvents()
	if len(events) < 1 {
		t.Fatalf("timer injected no events")
	}
	if events[0].TriggerType != "timer" || events[0].SourceID != "fast" {
		t.Fatalf("injected event = %#v", events[0])
	}
}

// Parity-exempt: _next_due_from's cron, interval, and failure branches.
func TestNextDueFrom(t *testing.T) {
	anchor := time.Date(2026, time.May, 18, 10, 0, 0, 0, time.UTC)

	cronDef := timerDefinition("c", "t", "0 9 * * *", nil)
	if next := NextDueFrom(&cronDef, anchor); next == nil || *next != "2026-05-19T09:00:00+00:00" {
		t.Fatalf("cron next = %#v", next)
	}

	intervalDef := timerDefinition("i", "t", "", 60)
	if next := NextDueFrom(&intervalDef, anchor); next == nil || *next != "2026-05-18T11:00:00+00:00" {
		t.Fatalf("interval next = %#v", next)
	}

	emptyDef := triggers.TriggerDefinition{ID: "e", TriggerConfig: map[string]any{}}
	if next := NextDueFrom(&emptyDef, anchor); next != nil {
		t.Fatalf("empty config next = %#v, want nil", next)
	}

	invalidDef := timerDefinition("bad", "t", "not a cron", nil)
	if next := NextDueFrom(&invalidDef, anchor); next != nil {
		t.Fatalf("invalid cron next = %#v, want nil", next)
	}
}

// Parity: core/tests/test_cron_seconds_parity.py::test_croniter_seconds_reference_locks_assumptions
func TestNextDueFromCronSecondsField(t *testing.T) {
	anchor := time.Date(2026, time.May, 18, 9, 0, 0, 0, time.UTC)

	secDef := timerDefinition("s", "t", "0 9 * * * 30", nil)
	if next := NextDueFrom(&secDef, anchor); next == nil || *next != "2026-05-18T09:00:30+00:00" {
		t.Fatalf("seconds cron next = %#v, want 2026-05-18T09:00:30+00:00", next)
	}

	stepDef := timerDefinition("s5", "t", "* * * * * */5", nil)
	if next := NextDueFrom(&stepDef, anchor); next == nil || *next != "2026-05-18T09:00:05+00:00" {
		t.Fatalf("stepped seconds next = %#v, want 2026-05-18T09:00:05+00:00", next)
	}
}

// Parity: core/tests/test_cron_seconds_parity.py::test_croniter_seconds_reference_locks_assumptions
func TestStartTriggerTimerUsesCronSeconds(t *testing.T) {
	session := newTriggerTestSession(t, "test_agent", nil)
	def := timerDefinition("s5", "tick", "* * * * * */5", nil)
	session.PutTrigger(def)
	t.Cleanup(func() { stopTriggerTimer(session, "s5") })

	if err := StartTriggerTimer(session, "s5", &def); err != nil {
		t.Fatalf("start timer: %v", err)
	}
	next, ok := session.TriggerNextFire()["s5"]
	if !ok {
		t.Fatalf("no next-fire seeded for the seconds trigger")
	}
	// A 5-second cron must seed a delay inside the current minute; the old
	// minute-granularity path could only produce delays up to 60s.
	if remaining := next - session.NowMonotonic(); remaining <= 0 || remaining > 6 {
		t.Fatalf("next fire in %.3fs, want (0, 6s] for a */5 seconds cron", remaining)
	}
}

// Parity: core/tests/test_cron_seconds_parity.py::test_invalid_cron_corpus_rejected_by_croniter
func TestCreateTriggerCronSecondsValidation(t *testing.T) {
	t.Run("accepts six-field seconds", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)
		t.Cleanup(func() { stopTriggerTimer(session, "user-tick-every-five-seconds") })

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", map[string]any{
			"name":           "Tick Every Five Seconds",
			"task":           "tick",
			"trigger_config": map[string]any{"cron": "* * * * * */5"},
		})
		if status != 200 {
			t.Fatalf("status = %d, want 200 (body %#v)", status, body)
		}
		config, ok := body["trigger_config"].(map[string]any)
		if !ok || config["cron"] != "* * * * * */5" {
			t.Fatalf("trigger_config = %#v, want the six-field cron", body["trigger_config"])
		}
	})

	t.Run("rejects seconds out of range", func(t *testing.T) {
		session := newTriggerTestSession(t, "test_agent", nil)
		app := newAppWithSession(t, session)

		status, body, _ := doJSON(t, app, http.MethodPost, "/api/sessions/test_agent/triggers", map[string]any{
			"name":           "Bad Seconds",
			"task":           "tick",
			"trigger_config": map[string]any{"cron": "* * * * * 60"},
		})
		if status != 400 || body["error"] != "Invalid cron expression: * * * * * 60" {
			t.Fatalf("status = %d body = %#v, want invalid-cron rejection", status, body)
		}
		if len(session.AvailableTriggers()) != 0 {
			t.Fatalf("a rejected create stored a trigger")
		}
	})
}

// Parity-exempt: _slugify_trigger_id's normalization, truncation, and collision
func TestSlugifyTriggerID(t *testing.T) {
	cases := []struct {
		name     string
		existing map[string]struct{}
		want     string
	}{
		{"Daily Digest!", nil, "user-daily-digest"},
		{"  --Hello   World--  ", nil, "user-hello-world"},
		{"", nil, "user-schedule"},
		{"!!!", nil, "user-schedule"},
		{"Café", nil, "user-café"},
		{"Digest", map[string]struct{}{"user-digest": {}}, "user-digest-2"},
		{"Digest", map[string]struct{}{"user-digest": {}, "user-digest-2": {}}, "user-digest-3"},
		{strings.Repeat("a", 50), nil, "user-" + strings.Repeat("a", 40)},
	}
	for _, tc := range cases {
		if got := slugifyTriggerID(tc.name, tc.existing); got != tc.want {
			t.Errorf("slugifyTriggerID(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Parity-exempt: framework/host/webhook_server.py's HTTP contract has no pytest
func TestWebhookServerResponses(t *testing.T) {
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventWebhookReceived)
	server := newWebhookServer(bus, "127.0.0.1", 0).(*webhookServer)
	server.AddRoute("src", "/hook", []string{"POST"})
	server.AddRouteWithSecret("src", "/secure", "topsecret", []string{"POST"})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	port := server.Port()
	if port == 0 {
		t.Fatalf("bound port is 0")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)

	request := func(method, path, body string, headers map[string]string) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequest(method, url+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		return resp.StatusCode, string(data), resp.Header
	}

	if status, _, _ := request(http.MethodGet, "/missing", "", nil); status != 404 {
		t.Fatalf("unknown path status = %d, want 404", status)
	}
	status, body, headers := request(http.MethodGet, "/hook", "", nil)
	if status != 405 {
		t.Fatalf("wrong method status = %d, want 405", status)
	}
	if body != "405: Method Not Allowed" {
		t.Fatalf("wrong method body = %q", body)
	}
	if !strings.HasPrefix(headers.Get("Content-Type"), "text/plain") {
		t.Fatalf("wrong method content-type = %q", headers.Get("Content-Type"))
	}

	status, body, _ = request(http.MethodPost, "/hook?x=1&x=2", `{"hello":"world"}`, map[string]string{"X-Probe": "yes"})
	if status != 202 {
		t.Fatalf("accepted status = %d, want 202 (body %q)", status, body)
	}
	// Parity: aiohttp web.json_response({"status": "accepted"}) — json.dumps
	// default separators, no trailing newline (22 bytes).
	if body != `{"status": "accepted"}` {
		t.Fatalf("accepted body = %q (%d bytes), want %q (22 bytes)", body, len(body), `{"status": "accepted"}`)
	}
	event := capture.lastOf(eventbus.EventWebhookReceived)
	if event == nil || event.StreamID != "src" {
		t.Fatalf("webhook event = %#v", event)
	}
	if event.Data["path"] != "/hook" || event.Data["method"] != "POST" {
		t.Fatalf("event data = %#v", event.Data)
	}
	payload, _ := event.Data["payload"].(map[string]any)
	if payload["hello"] != "world" {
		t.Fatalf("payload = %#v", payload)
	}
	headersMap, _ := event.Data["headers"].(map[string]string)
	if headersMap["X-Probe"] != "yes" {
		t.Fatalf("headers = %#v", event.Data["headers"])
	}
	if headersMap["Host"] == "" {
		t.Fatalf("Host header was not re-added: %#v", headersMap)
	}
	query, _ := event.Data["query_params"].(map[string]string)
	// Parity: upstream dict(request.query) keeps the FIRST value for a
	// repeated parameter (measured with real aiohttp, ?x=1&x=2 -> "1").
	if query["x"] != "1" {
		t.Fatalf("query_params = %#v, want the first value", query)
	}

	status, _, _ = request(http.MethodPost, "/hook", "not json", nil)
	if status != 202 {
		t.Fatalf("raw body status = %d, want 202", status)
	}
	rawEvent := capture.lastOf(eventbus.EventWebhookReceived)
	rawPayload, _ := rawEvent.Data["payload"].(map[string]any)
	if rawPayload["raw_body"] != "not json" {
		t.Fatalf("raw_body = %#v", rawPayload)
	}

	status, _, _ = request(http.MethodPost, "/hook", "\xff", nil)
	if status != 202 {
		t.Fatalf("invalid utf8 status = %d, want 202", status)
	}
	invalidEvent := capture.lastOf(eventbus.EventWebhookReceived)
	invalidPayload, _ := invalidEvent.Data["payload"].(map[string]any)
	if invalidPayload["raw_body"] != "\uFFFD" {
		t.Fatalf("invalid utf8 raw_body = %#v", invalidPayload["raw_body"])
	}

	if status, _, _ := request(http.MethodPost, "/secure", "{}", nil); status != 401 {
		t.Fatalf("unsigned secure status = %d, want 401", status)
	}
	if status, _, _ := request(http.MethodPost, "/secure", "{}", map[string]string{"X-Hub-Signature-256": "sha256=deadbeef"}); status != 401 {
		t.Fatalf("bad signature status = %d, want 401", status)
	}
	signedBody := `{"ok":true}`
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(signedBody))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if status, _, _ := request(http.MethodPost, "/secure", signedBody, map[string]string{"X-Hub-Signature-256": signature}); status != 202 {
		t.Fatalf("signed secure status = %d, want 202", status)
	}

	if err := server.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if server.Running() {
		t.Fatalf("listener still running after stop")
	}
}

// Parity-exempt: Start is a no-op when no routes are registered, as upstream
func TestWebhookServerStartWithoutRoutes(t *testing.T) {
	server := newWebhookServer(eventbus.NewEventBus(), "127.0.0.1", 0).(*webhookServer)
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if server.Running() {
		t.Fatalf("listener started without routes")
	}
	if server.Port() != 0 {
		t.Fatalf("port = %d, want 0", server.Port())
	}
	if err := server.Stop(); err != nil {
		t.Fatalf("stop before start: %v", err)
	}
}
