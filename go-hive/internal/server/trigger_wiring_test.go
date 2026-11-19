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

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/triggers"
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
