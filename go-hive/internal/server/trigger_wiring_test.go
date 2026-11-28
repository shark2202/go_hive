package server

// Trigger wiring: a session's timer/webhook triggers must reach the live queen
// runtime. These tests drive the production path end to end (StartTriggerTimer /
// StartTriggerWebhook → session.QueenExecutor → ChatRuntime.InjectTrigger →
// a real agent turn) instead of the fake node the route tests use.

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

func writeTriggersJSON(t *testing.T, dir string, entries []map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("marshal triggers.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, triggers.TriggersFileName), data, 0o644); err != nil {
		t.Fatalf("write triggers.json: %v", err)
	}
}

func newWiredTriggerSession(t *testing.T, runtime *ChatRuntime) *Session {
	t.Helper()
	session := NewSession("session_trigger_wiring", eventbus.NewEventBus())
	session.ColonyID = "colony_trigger_wiring"
	session.WorkerPath = t.TempDir()
	session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": runtime}}
	t.Cleanup(session.StopAllTriggers)
	return session
}

func waitForTriggerFire(t *testing.T, session *Session, triggerID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if stat, ok := session.TriggerFireStats()[triggerID]; ok && stat.FireCount > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("trigger %q never fired", triggerID)
}

func waitForModelRequests(t *testing.T, model *fakeChatLLM, count int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if model.requestCount() >= count {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// Parity: core/tests/test_trigger_fires_into_queen.py::test_interval_timer_fires_inject_trigger_on_queen_node
func TestTimerTriggerWakesChatQueen(t *testing.T) {
	runtime, model := newTestChatRuntime(t, t.TempDir())
	session := newWiredTriggerSession(t, runtime)

	def := timerDefinition("wake", "check the inbox", "", 0.001)
	session.PutTrigger(def)
	if err := StartTriggerTimer(session, "wake", &def); err != nil {
		t.Fatalf("StartTriggerTimer: %v", err)
	}

	content := waitForTranscriptContent(t, runtime, "[TRIGGER: timer/wake]", 5*time.Second)
	if !strings.Contains(content, "Task: check the inbox") {
		t.Fatalf("trigger turn lost the task line:\n%s", content)
	}
	waitForTriggerFire(t, session, "wake")

	if !waitForModelRequests(t, model, 1) {
		t.Fatal("trigger never reached the model: no completion request was made")
	}
	parts, err := runtime.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	tagged := false
	for _, part := range parts {
		if isTrigger, _ := part["is_trigger"].(bool); isTrigger {
			tagged = true
			break
		}
	}
	if !tagged {
		t.Fatalf("trigger message was not tagged is_trigger: %v", parts)
	}
}

// Parity-exempt: restart recovery is assembled in the CLI loader over
// session_manager.load_colony; upstream has no dedicated test for the
// triggers.json rehydrate + autostart path.
func TestRestoreSessionTriggersFromDisk(t *testing.T) {
	session := NewSession("session_trigger_restore", eventbus.NewEventBus())
	session.ColonyID = "colony_trigger_restore"
	session.WorkerPath = t.TempDir()
	t.Cleanup(session.StopAllTriggers)

	writeTriggersJSON(t, session.WorkerPath, []map[string]any{
		{
			"id":             "enabled-timer",
			"name":           "hourly",
			"trigger_type":   "timer",
			"trigger_config": map[string]any{"interval_minutes": 60},
			"task":           "sweep",
			"enabled":        true,
		},
		{
			"id":             "disabled-timer",
			"name":           "off",
			"trigger_type":   "timer",
			"trigger_config": map[string]any{"interval_minutes": 60},
			"task":           "nothing",
			"enabled":        false,
		},
		{
			"id":             "deploy-hook",
			"name":           "deploy hook",
			"trigger_type":   "webhook",
			"trigger_config": map[string]any{"path": "/hooks/deploy", "methods": []any{"POST"}},
			"task":           "ship it",
			"enabled":        true,
		},
	})

	fake := &fakeWebhookServer{}
	previous := NewTriggerWebhookServer
	NewTriggerWebhookServer = func(*eventbus.EventBus, string, int) TriggerWebhookServer { return fake }
	t.Cleanup(func() { NewTriggerWebhookServer = previous })

	loaded, started := RestoreSessionTriggers(session)
	if loaded != 3 || started != 2 {
		t.Fatalf("RestoreSessionTriggers = (%d, %d), want (3, 2)", loaded, started)
	}
	if got := len(session.AvailableTriggers()); got != 3 {
		t.Fatalf("available triggers = %d, want 3", got)
	}
	if !session.IsTriggerActive("enabled-timer") {
		t.Fatal("enabled timer was not activated on restore")
	}
	if session.IsTriggerActive("disabled-timer") {
		t.Fatal("disabled timer was activated on restore")
	}
	if !session.IsTriggerActive("deploy-hook") {
		t.Fatal("enabled webhook was not activated on restore")
	}
	if _, ok := session.TriggerNextFire()["enabled-timer"]; !ok {
		t.Fatal("restored timer has no pending fire time")
	}
	routes := fake.registrations()
	if len(routes) != 1 || routes[0].path != "/hooks/deploy" || routes[0].sourceID != "deploy-hook" {
		t.Fatalf("webhook routes = %+v, want one /hooks/deploy route for deploy-hook", routes)
	}

	// The restored definition keeps its persisted fields, not just its id.
	def, ok := session.LookupTrigger("enabled-timer")
	if !ok || def.Description != "hourly" || def.Task != "sweep" || !def.Enabled {
		t.Fatalf("restored definition = %+v, want the persisted name/task/enabled", def)
	}
}

// Parity-exempt: restart recovery wiring; see TestRestoreSessionTriggersFromDisk.
func TestRestoredTimerFiresAfterRestart(t *testing.T) {
	runtime, model := newTestChatRuntime(t, t.TempDir())
	session := newWiredTriggerSession(t, runtime)

	writeTriggersJSON(t, session.WorkerPath, []map[string]any{
		{
			"id":             "resume-work",
			"name":           "resume",
			"trigger_type":   "timer",
			"trigger_config": map[string]any{"interval_minutes": 0.001},
			"task":           "resume the batch",
			"enabled":        true,
		},
	})

	loaded, started := RestoreSessionTriggers(session)
	if loaded != 1 || started != 1 {
		t.Fatalf("RestoreSessionTriggers = (%d, %d), want (1, 1)", loaded, started)
	}

	content := waitForTranscriptContent(t, runtime, "[TRIGGER: timer/resume-work]", 5*time.Second)
	if !strings.Contains(content, "Task: resume the batch") {
		t.Fatalf("restored trigger turn lost the task line:\n%s", content)
	}
	waitForTriggerFire(t, session, "resume-work")
	if !waitForModelRequests(t, model, 1) {
		t.Fatal("restored timer never woke the queen")
	}
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// Parity: core/tests/test_trigger_fires_into_queen.py::test_webhook_trigger_fires_inject_trigger
func TestWebhookRouteHitsRegisteredTrigger(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	session := newWiredTriggerSession(t, runtime)

	port := freeLoopbackPort(t)
	def := triggers.TriggerDefinition{
		ID:          "deploy-hook",
		TriggerType: "webhook",
		TriggerConfig: map[string]any{
			"path":    "/hooks/deploy",
			"methods": []any{"POST"},
			"port":    float64(port),
		},
		Task: "ship it",
	}
	session.PutTrigger(def)
	if err := StartTriggerWebhook(session, "deploy-hook", &def); err != nil {
		t.Fatalf("StartTriggerWebhook: %v", err)
	}

	response, err := http.Post(
		"http://127.0.0.1:"+strconv.Itoa(port)+"/hooks/deploy",
		"application/json",
		strings.NewReader(`{"event":"deploy"}`),
	)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	if response.StatusCode != http.StatusAccepted {
		_ = response.Body.Close()
		t.Fatalf("webhook status = %d, want 202", response.StatusCode)
	}
	_ = response.Body.Close()

	content := waitForTranscriptContent(t, runtime, "[TRIGGER: webhook/deploy-hook]", 5*time.Second)
	if !strings.Contains(content, "ship it") {
		t.Fatalf("webhook trigger turn lost the task text:\n%s", content)
	}
	if !strings.Contains(content, `"event":"deploy"`) {
		t.Fatalf("webhook payload was not forwarded to the queen:\n%s", content)
	}
	waitForTriggerFire(t, session, "deploy-hook")
}

// Parity-exempt: upstream load_colony hydrates triggers.json into a session
// that cannot already hold a colony (framework/server/session_manager.py:1027
// raises when colony_id is set). The Go loader re-attaches onto an existing
// session, so restore must replace the catalog; this asserts that contract on
// the real session fields.
func TestRestoreSessionTriggersReplacesCatalog(t *testing.T) {
	session := NewSession("session_trigger_replace", eventbus.NewEventBus())
	session.ColonyID = "colony_b"
	session.WorkerPath = t.TempDir()
	t.Cleanup(session.StopAllTriggers)

	// A previous binding left a definition, an armed timer, an active id and a
	// next-fire stamp behind.
	stale := timerDefinition("colony-a-timer", "old sweep", "", 60)
	stale.Description = "old sweep"
	session.PutTrigger(stale)
	if err := StartTriggerTimer(session, stale.ID, &stale); err != nil {
		t.Fatalf("StartTriggerTimer: %v", err)
	}
	session.ActivateTrigger(stale.ID)
	session.triggerMu.Lock()
	staleTimer := session.ActiveTimerTasks[stale.ID]
	session.triggerMu.Unlock()

	writeTriggersJSON(t, session.WorkerPath, []map[string]any{{
		"id":             "colony-b-timer",
		"name":           "new sweep",
		"trigger_type":   "timer",
		"trigger_config": map[string]any{"interval_minutes": 60},
		"task":           "sweep b",
		"enabled":        true,
	}})

	loaded, started := RestoreSessionTriggers(session)
	if loaded != 1 || started != 1 {
		t.Fatalf("RestoreSessionTriggers = (%d, %d), want (1, 1)", loaded, started)
	}
	available := session.AvailableTriggers()
	if len(available) != 1 || available[0].ID != "colony-b-timer" {
		t.Fatalf("available triggers = %+v, want only colony-b-timer", available)
	}
	if _, ok := session.LookupTrigger("colony-a-timer"); ok {
		t.Fatal("restore kept the previous colony's trigger definition")
	}
	if session.IsTriggerActive("colony-a-timer") {
		t.Fatal("restore kept the previous colony's trigger active")
	}
	if active := session.ActiveTriggerIDs(); len(active) != 1 {
		t.Fatalf("active triggers = %v, want only colony-b-timer", active)
	}
	if !session.IsTriggerActive("colony-b-timer") {
		t.Fatal("restored enabled trigger was not activated")
	}
	if nextFire := session.TriggerNextFire(); len(nextFire) != 1 {
		t.Fatalf("next-fire map = %v, want only colony-b-timer", nextFire)
	}
	if staleTimer != nil {
		select {
		case <-staleTimer.done:
		default:
			t.Fatal("previous colony's timer is still running after restore")
		}
	}
}

// Parity-exempt: Go-only replace contract. Upstream load_colony reads
// triggers.json into a fresh session's available_triggers
// (framework/server/session_manager.py:1048-1070); a Go restore can run with a
// stale catalog, which must end up empty rather than linger.
func TestRestoreSessionTriggersWithoutValidEntriesClearsCatalog(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, dir string)
	}{
		{
			name:  "missing file",
			write: func(t *testing.T, dir string) {},
		},
		{
			name: "empty file",
			write: func(t *testing.T, dir string) {
				writeTriggersJSON(t, dir, []map[string]any{})
			},
		},
		{
			name: "no valid entries",
			write: func(t *testing.T, dir string) {
				writeTriggersJSON(t, dir, []map[string]any{
					{"id": "", "trigger_type": "timer"},
					{"id": "unsupported", "trigger_type": "cron"},
				})
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session := NewSession("session_trigger_empty", eventbus.NewEventBus())
			session.ColonyID = "colony_b"
			session.WorkerPath = t.TempDir()
			t.Cleanup(session.StopAllTriggers)

			stale := timerDefinition("colony-a-timer", "old sweep", "", 60)
			session.PutTrigger(stale)
			session.ActivateTrigger(stale.ID)
			session.SetTriggerNextFire(stale.ID, session.NowMonotonic()+3600)
			session.RecordTriggerFire(stale.ID, triggers.TriggerFireStat{FireCount: 2})

			testCase.write(t, session.WorkerPath)

			loaded, started := RestoreSessionTriggers(session)
			if loaded != 0 || started != 0 {
				t.Fatalf("RestoreSessionTriggers = (%d, %d), want (0, 0)", loaded, started)
			}
			if available := session.AvailableTriggers(); len(available) != 0 {
				t.Fatalf("available triggers = %+v, want none", available)
			}
			if active := session.ActiveTriggerIDs(); len(active) != 0 {
				t.Fatalf("active triggers = %v, want none", active)
			}
			if nextFire := session.TriggerNextFire(); len(nextFire) != 0 {
				t.Fatalf("next-fire map = %v, want none", nextFire)
			}
			if _, ok := session.LookupTrigger("colony-a-timer"); ok {
				t.Fatal("stale definition survived a restore with no persisted entries")
			}
		})
	}
}

// Parity-exempt: upstream unload_colony emits TRIGGER_REMOVED per available
// trigger (framework/server/session_manager.py:1299-1302 via
// _emit_trigger_events:1885-1933) but has no Python test; asserted here against
// the real session state and event bus.
func TestRemoveAllTriggersPublishesRemovedAndClearsRuntime(t *testing.T) {
	bus := eventbus.NewEventBus()
	session := NewSession("session_remove_all", bus)
	session.ColonyID = "colony_a"
	session.WorkerPath = t.TempDir()
	t.Cleanup(session.StopAllTriggers)

	capture := newEventCapture(bus, eventbus.EventTriggerRemoved)

	// Webhook state: a real subscription id, so the teardown's unsubscribe is
	// observable.
	hookSubID := bus.Subscribe([]eventbus.EventType{eventbus.EventTriggerActivated}, func(*eventbus.AgentEvent) {})
	session.SetActiveWebhookSub("deploy-hook", hookSubID)
	if got := bus.SubscriptionCount(); got != 2 {
		t.Fatalf("subscriptions before teardown = %d, want 2 (capture + webhook)", got)
	}

	hourly := timerDefinition("hourly-sweep", "sweep the inbox", "", 60)
	hourly.Description = "Hourly sweep"
	session.PutTrigger(hourly)
	if err := StartTriggerTimer(session, hourly.ID, &hourly); err != nil {
		t.Fatalf("StartTriggerTimer: %v", err)
	}
	session.triggerMu.Lock()
	timer := session.ActiveTimerTasks[hourly.ID]
	session.triggerMu.Unlock()
	session.ActivateTrigger(hourly.ID)
	lastFiredAt := int64(1_700_000_000_000)
	session.RecordTriggerFire(hourly.ID, triggers.TriggerFireStat{FireCount: 3, LastFiredAt: &lastFiredAt})

	hook := triggers.TriggerDefinition{
		ID:            "deploy-hook",
		TriggerType:   "webhook",
		TriggerConfig: map[string]any{"path": "/hooks/deploy", "methods": []any{"POST"}},
		Task:          "ship it",
	}
	session.PutTrigger(hook)
	session.ActivateTrigger(hook.ID)

	// Unrelated session state must survive the trigger teardown.
	session.SetChannel("telegram")
	session.RecordPendingEscalation("req-1", map[string]any{"question": "ship?"})

	removed := session.RemoveAllTriggers()
	if len(removed) != 2 || removed[0].ID != "hourly-sweep" || removed[1].ID != "deploy-hook" {
		t.Fatalf("removed = %+v, want hourly-sweep then deploy-hook in registration order", removed)
	}
	if available := session.AvailableTriggers(); len(available) != 0 {
		t.Fatalf("available triggers = %+v, want none", available)
	}
	session.triggerMu.Lock()
	timers, subs, webhookServer := session.ActiveTimerTasks, session.ActiveWebhookSubs, session.WebhookServer
	session.triggerMu.Unlock()
	if timers != nil || subs != nil || webhookServer != nil {
		t.Fatalf("trigger runtime state survived: timers=%v subs=%v server=%v", timers, subs, webhookServer)
	}
	if active := session.ActiveTriggerIDs(); len(active) != 0 {
		t.Fatalf("active triggers = %v, want none", active)
	}
	if nextFire := session.TriggerNextFire(); len(nextFire) != 0 {
		t.Fatalf("next-fire map = %v, want none", nextFire)
	}
	if timer != nil {
		select {
		case <-timer.done:
		default:
			t.Fatal("timer still running after RemoveAllTriggers")
		}
	}
	if got := bus.SubscriptionCount(); got != 1 {
		t.Fatalf("subscriptions after teardown = %d, want 1 (capture only)", got)
	}
	if session.Channel() != "telegram" || session.PendingCount() != 1 {
		t.Fatalf("unrelated session state was cleared: channel=%q pending=%d", session.Channel(), session.PendingCount())
	}
	if session.ColonyID != "colony_a" || session.WorkerPath == "" {
		t.Fatalf("RemoveAllTriggers detached the colony: colony=%q path=%q", session.ColonyID, session.WorkerPath)
	}

	events := capture.eventsOf(eventbus.EventTriggerRemoved)
	if len(events) != 2 {
		t.Fatalf("trigger_removed events = %d, want 2", len(events))
	}
	byID := map[string]*eventbus.AgentEvent{}
	for _, event := range events {
		if event.StreamID != "queen" {
			t.Fatalf("trigger_removed stream = %q, want queen", event.StreamID)
		}
		if len(event.Data) != 4 {
			t.Fatalf("trigger_removed payload = %v, want exactly the four upstream keys", event.Data)
		}
		triggerID, _ := event.Data["trigger_id"].(string)
		byID[triggerID] = event
	}
	hourlyEvent, ok := byID["hourly-sweep"]
	if !ok {
		t.Fatal("no trigger_removed for hourly-sweep")
	}
	if hourlyEvent.Data["name"] != "Hourly sweep" || hourlyEvent.Data["trigger_type"] != "timer" {
		t.Fatalf("hourly payload = %v", hourlyEvent.Data)
	}
	config, ok := hourlyEvent.Data["trigger_config"].(map[string]any)
	if !ok {
		t.Fatalf("hourly trigger_config = %T, want map", hourlyEvent.Data["trigger_config"])
	}
	if config["interval_minutes"] != 60 || config["fire_count"] != 3 || config["last_fired_at"] != lastFiredAt {
		t.Fatalf("hourly trigger_config = %v, want interval/fire stats", config)
	}
	if remaining, ok := config["next_fire_in"].(float64); !ok || remaining <= 0 {
		t.Fatalf("hourly next_fire_in = %v, want a positive countdown", config["next_fire_in"])
	}
	if _, ok := config["next_fire_at"].(int64); !ok {
		t.Fatalf("hourly next_fire_at = %T, want epoch milliseconds", config["next_fire_at"])
	}
	hookEvent, ok := byID["deploy-hook"]
	if !ok {
		t.Fatal("no trigger_removed for deploy-hook")
	}
	if hookEvent.Data["name"] != "deploy-hook" || hookEvent.Data["trigger_type"] != "webhook" {
		t.Fatalf("hook payload = %v, want name fallback to the id", hookEvent.Data)
	}
	hookConfig, ok := hookEvent.Data["trigger_config"].(map[string]any)
	if !ok || hookConfig["path"] != "/hooks/deploy" {
		t.Fatalf("hook trigger_config = %v, want the persisted path", hookEvent.Data["trigger_config"])
	}
}

// Parity-exempt: Go-only nil-bus tolerance; upstream always has an event bus.
func TestRemoveAllTriggersWithoutBusStillClears(t *testing.T) {
	session := NewSession("session_remove_all_no_bus", nil)
	session.PutTrigger(timerDefinition("hourly-sweep", "sweep", "", 60))
	session.ActivateTrigger("hourly-sweep")

	removed := session.RemoveAllTriggers()

	if len(removed) != 1 || removed[0].ID != "hourly-sweep" {
		t.Fatalf("removed = %+v, want hourly-sweep", removed)
	}
	if available := session.AvailableTriggers(); len(available) != 0 {
		t.Fatalf("available triggers = %+v, want none", available)
	}
	if active := session.ActiveTriggerIDs(); len(active) != 0 {
		t.Fatalf("active triggers = %v, want none", active)
	}
}
