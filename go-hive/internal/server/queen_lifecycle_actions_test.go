package server

// Tests for the trigger tools, the worker-profile tool, list_browser_profiles
// and the chat runtime's colony gating of the lifecycle defs.
//
// Parity: tests/test_trigger_fires_into_queen.py::test_remove_trigger_cleans_up_webhook_subscription
// Parity: tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/triggers"
)

// fakeTriggerWebhookServer is the TriggerWebhookServer seam: it records the
// routes the webhook trigger registers without binding a real port.
type fakeTriggerWebhookServer struct {
	mu      sync.Mutex
	routes  []string
	running bool
}

func (f *fakeTriggerWebhookServer) AddRoute(sourceID, path string, methods []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes = append(f.routes, sourceID+" "+path+" "+strings.Join(methods, ","))
}

func (f *fakeTriggerWebhookServer) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = true
	return nil
}

func (f *fakeTriggerWebhookServer) Running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestSetTriggerTimerAndListRemove(t *testing.T) {
	session, binding, _ := lifecycleTestSession(t)
	session.WorkerPath = t.TempDir()
	t.Cleanup(session.StopAllTriggers)

	// Unknown id without a type/config is refused with the upstream message.
	content, isError := binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{"trigger_id": "nightly"})
	if !isError || !strings.Contains(content, "not found. Provide trigger_type and trigger_config") {
		t.Fatalf("missing trigger = (%v, %q)", isError, content)
	}

	// A task is mandatory before activation.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id":     "nightly",
		"trigger_type":   "timer",
		"trigger_config": map[string]any{"interval_minutes": 5},
	})
	if !isError || !strings.Contains(content, "has no task configured") {
		t.Fatalf("missing task = (%v, %q)", isError, content)
	}

	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id":     "nightly",
		"trigger_type":   "timer",
		"trigger_config": map[string]any{"interval_minutes": 5},
		"task":           "Process inbox emails",
	})
	if isError {
		t.Fatalf("set_trigger failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["status"] != "activated" || payload["trigger_id"] != "nightly" || payload["trigger_type"] != "timer" {
		t.Fatalf("set_trigger payload = %#v", payload)
	}
	if !session.IsTriggerActive("nightly") {
		t.Fatal("trigger must be active after set_trigger")
	}
	stored := triggers.ReadAgentTriggersJSON(session.WorkerPath)
	if len(stored) != 1 || stored[0]["id"] != "nightly" || stored[0]["enabled"] != true {
		t.Fatalf("triggers.json = %#v", stored)
	}

	// Already active is refused instead of double-starting the timer.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{"trigger_id": "nightly"})
	if !isError || !strings.Contains(content, "is already active") {
		t.Fatalf("already active = (%v, %q)", isError, content)
	}

	list := decodeToolPayload(t, binding.listTriggers())
	rows := list["triggers"].([]any)
	if len(rows) != 1 {
		t.Fatalf("list_triggers rows = %#v", rows)
	}
	row := rows[0].(map[string]any)
	if row["id"] != "nightly" || row["enabled"] != true || row["task"] != "Process inbox emails" {
		t.Fatalf("list row = %#v", row)
	}

	removeContent, isError := binding.Execute(context.Background(), QueenRemoveTriggerToolName, map[string]any{"trigger_id": "nightly"})
	if isError {
		t.Fatalf("remove_trigger failed: %s", removeContent)
	}
	if payload := decodeToolPayload(t, removeContent); payload["status"] != "deactivated" {
		t.Fatalf("remove payload = %#v", payload)
	}
	if session.IsTriggerActive("nightly") {
		t.Fatal("trigger must be inactive after remove_trigger")
	}
	if rows := decodeToolPayload(t, binding.listTriggers())["triggers"].([]any); rows[0].(map[string]any)["enabled"] != false {
		t.Fatalf("list after remove = %#v", rows)
	}
	if stored := triggers.ReadAgentTriggersJSON(session.WorkerPath); len(stored) != 0 {
		t.Fatalf("triggers.json after remove = %#v", stored)
	}

	content, isError = binding.Execute(context.Background(), QueenRemoveTriggerToolName, map[string]any{"trigger_id": "nightly"})
	if !isError || !strings.Contains(content, "is not active") {
		t.Fatalf("second remove = (%v, %q)", isError, content)
	}
}

// Parity: tests/test_trigger_fires_into_queen.py::test_remove_trigger_cleans_up_webhook_subscription
func TestSetTriggerValidationAndWebhook(t *testing.T) {
	session, binding, _ := lifecycleTestSession(t)
	t.Cleanup(session.StopAllTriggers)
	original := NewTriggerWebhookServer
	fake := &fakeTriggerWebhookServer{}
	NewTriggerWebhookServer = func(_ *eventbus.EventBus, _ string, _ int) TriggerWebhookServer { return fake }
	t.Cleanup(func() { NewTriggerWebhookServer = original })

	// Unsupported type.
	content, isError := binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "weird", "trigger_type": "carrier-pigeon", "trigger_config": map[string]any{"path": "/x"}, "task": "t",
	})
	if !isError || !strings.Contains(content, "Unsupported trigger type: carrier-pigeon") {
		t.Fatalf("unsupported type = (%v, %q)", isError, content)
	}

	// Timer needs cron or interval (a pre-seeded timer with an empty config).
	session.PutTrigger(triggers.TriggerDefinition{ID: "bare", TriggerType: "timer"})
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "bare", "task": "t",
	})
	if !isError || !strings.Contains(content, "needs 'cron' or 'interval_minutes'") {
		t.Fatalf("bare timer = (%v, %q)", isError, content)
	}

	// Invalid cron.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "badcron", "trigger_type": "timer", "trigger_config": map[string]any{"cron": "not a cron"}, "task": "t",
	})
	if !isError || !strings.Contains(content, "Invalid cron expression") {
		t.Fatalf("bad cron = (%v, %q)", isError, content)
	}

	// A negative interval is truthy, so it reaches the > 0 check.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "zerointerval", "trigger_type": "timer", "trigger_config": map[string]any{"interval_minutes": -5}, "task": "t",
	})
	if !isError || !strings.Contains(content, "interval_minutes must be > 0") {
		t.Fatalf("negative interval = (%v, %q)", isError, content)
	}

	// Webhook needs a rooted path.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "hookless", "trigger_type": "webhook", "trigger_config": map[string]any{"path": "hooks/x"}, "task": "t",
	})
	if !isError || !strings.Contains(content, "requires 'path' starting with '/'") {
		t.Fatalf("bad path = (%v, %q)", isError, content)
	}

	// Webhook methods are validated.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "badmethod", "trigger_type": "webhook",
		"trigger_config": map[string]any{"path": "/hooks/x", "methods": []any{"POST", "FETCH"}}, "task": "t",
	})
	if !isError || !strings.Contains(content, "Invalid HTTP methods: [FETCH]") {
		t.Fatalf("bad methods = (%v, %q)", isError, content)
	}

	// A valid webhook activates and reports its URL.
	content, isError = binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
		"trigger_id": "gh-hook", "trigger_type": "webhook",
		"trigger_config": map[string]any{"path": "/hooks/github", "port": 8091}, "task": "handle push",
	})
	if isError {
		t.Fatalf("webhook setup failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["status"] != "activated" || payload["webhook_url"] != "http://127.0.0.1:8091/hooks/github" {
		t.Fatalf("webhook payload = %#v", payload)
	}
	if len(fake.routes) != 1 || !strings.Contains(fake.routes[0], "gh-hook /hooks/github POST") {
		t.Fatalf("webhook routes = %#v", fake.routes)
	}

	removeContent, isError := binding.Execute(context.Background(), QueenRemoveTriggerToolName, map[string]any{"trigger_id": "gh-hook"})
	if isError {
		t.Fatalf("remove webhook failed: %s", removeContent)
	}
	if session.EventBus.SubscriptionCount() != 0 {
		t.Fatalf("webhook subscription survived remove_trigger: %d", session.EventBus.SubscriptionCount())
	}
}

// Parity-exempt: upstream has no test for update_worker_profile. The behavior
// is traced to framework/tools/queen_lifecycle_tools.py::update_worker_profile
// and framework/host/worker_profiles.py::upsert_worker_profile, whose merge and
// validation rules the route tests already cover.
func TestUpdateWorkerProfile(t *testing.T) {
	session, binding, home := lifecycleTestSession(t)
	_ = session
	colonyDir := filepath.Join(home.ColoniesDir(), "alpha")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	content, isError := binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id":       "alpha",
		"profile_name":    "slack-work",
		"integrations":    map[string]any{"slack": "work-2"},
		"task":            "triage",
		"browser_profile": "blue-window",
	})
	if isError {
		t.Fatalf("update_worker_profile failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["ok"] != true || payload["colony_id"] != "alpha" || payload["profile_name"] != "slack-work" {
		t.Fatalf("payload = %#v", payload)
	}
	profiles := listWorkerProfilesWithHome(home, "alpha")
	if len(profiles) != 2 { // default + slack-work
		t.Fatalf("profiles = %#v", profiles)
	}
	var saved WorkerProfile
	for _, profile := range profiles {
		if profile.Name == "slack-work" {
			saved = profile
		}
	}
	if saved.Integrations["slack"] != "work-2" || saved.Task != "triage" || saved.BrowserProfile != "blue-window" {
		t.Fatalf("saved profile = %#v", saved)
	}

	// A later partial update keeps the untouched fields and preserves siblings.
	content, isError = binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id":    "alpha",
		"profile_name": "slack-work",
		"task":         "escalate",
	})
	if isError {
		t.Fatalf("partial update failed: %s", content)
	}
	profiles = listWorkerProfilesWithHome(home, "alpha")
	for _, profile := range profiles {
		if profile.Name == "slack-work" {
			if profile.Task != "escalate" || profile.Integrations["slack"] != "work-2" || profile.BrowserProfile != "blue-window" {
				t.Fatalf("partial update dropped fields: %#v", profile)
			}
		}
	}

	// Empty string clears the browser binding; empty tool_filter clears it.
	content, isError = binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id":       "alpha",
		"profile_name":    "slack-work",
		"browser_profile": "",
		"tool_filter":     []any{},
	})
	if isError {
		t.Fatalf("clear update failed: %s", content)
	}
	for _, profile := range listWorkerProfilesWithHome(home, "alpha") {
		if profile.Name == "slack-work" && (profile.BrowserProfile != "" || profile.ToolFilter != nil) {
			t.Fatalf("clear did not apply: %#v", profile)
		}
	}

	// Error paths: bad colony id, bad profile name, unknown colony.
	content, isError = binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id": "Alpha!", "profile_name": "p",
	})
	if !isError || !strings.Contains(content, "colony_id must be lowercase alphanumeric") {
		t.Fatalf("bad colony id = (%v, %q)", isError, content)
	}
	content, isError = binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id": "alpha", "profile_name": "Bad Name",
	})
	if !isError || !strings.Contains(content, "profile name must be lowercase alphanumeric") {
		t.Fatalf("bad profile name = (%v, %q)", isError, content)
	}
	content, isError = binding.Execute(context.Background(), QueenUpdateWorkerProfileToolName, map[string]any{
		"colony_id": "ghost", "profile_name": "p",
	})
	if !isError || !strings.Contains(content, "Colony 'ghost' not found") {
		t.Fatalf("unknown colony = (%v, %q)", isError, content)
	}
}

// serveBridgeProfiles answers one HTTP/1.0 request with the given /profiles
// payload, mimicking the bridge's status server.
func serveBridgeProfiles(t *testing.T, payload string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil || strings.TrimSpace(line) == "" {
						break
					}
				}
				body := payload
				_, _ = io.WriteString(conn, "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n"+body)
			}(conn)
		}
	}()
	return listener.Addr().String()
}

// Parity-exempt: upstream has no test for list_browser_profiles. The probe
// contract is traced to framework/tools/queen_lifecycle_tools.py::list_browser_profiles.
func TestListBrowserProfiles(t *testing.T) {
	_, binding, _ := lifecycleTestSession(t)

	original := queenBrowserBridgeStatusAddrs
	t.Cleanup(func() { queenBrowserBridgeStatusAddrs = original })

	// No bridge: an explicit empty list plus the reachability error, never a
	// fabricated profile.
	queenBrowserBridgeStatusAddrs = func() []string {
		return []string{net.JoinHostPort("127.0.0.1", "1")}
	}
	content, isError := binding.Execute(context.Background(), QueenListBrowserProfilesToolName, nil)
	if isError {
		t.Fatalf("list_browser_profiles raised: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["ok"] != false || payload["error"] != "browser bridge not reachable" {
		t.Fatalf("unreachable payload = %#v", payload)
	}
	if profiles := payload["profiles"].([]any); len(profiles) != 0 {
		t.Fatalf("unreachable profiles = %#v", profiles)
	}

	// Reachable with a connected profile.
	addr := serveBridgeProfiles(t, `{"ok":true,"profiles":[{"label":"blue-window","is_default":true,"starred":false,"version":"1.2.3","protocol_version":2}]}`)
	queenBrowserBridgeStatusAddrs = func() []string { return []string{addr} }
	payload = decodeToolPayload(t, binding.listBrowserProfiles(context.Background()))
	if payload["ok"] != true {
		t.Fatalf("reachable payload = %#v", payload)
	}
	profiles := payload["profiles"].([]any)
	if len(profiles) != 1 {
		t.Fatalf("profiles = %#v", profiles)
	}
	profile := profiles[0].(map[string]any)
	if profile["label"] != "blue-window" || profile["is_default"] != true || profile["starred"] != false || profile["version"] != "1.2.3" {
		t.Fatalf("profile row = %#v", profile)
	}

	// Reachable but no connected profile: ok with the enablement hint.
	emptyAddr := serveBridgeProfiles(t, `{"ok":true,"profiles":[]}`)
	queenBrowserBridgeStatusAddrs = func() []string { return []string{emptyAddr} }
	payload = decodeToolPayload(t, binding.listBrowserProfiles(context.Background()))
	if payload["ok"] != true || len(payload["profiles"].([]any)) != 0 {
		t.Fatalf("empty payload = %#v", payload)
	}
	if _, present := payload["hint"]; !present {
		t.Fatalf("empty payload missing hint: %#v", payload)
	}
}

// Parity-exempt: the colony gating of these defs is a tool-surface assertion
// this port makes on the chat runtime (upstream gates through the phase
// registry, which the Go chat session does not model).
func TestChatRuntimeHidesColonyLifecycleToolsWithoutColony(t *testing.T) {
	runtime, _ := newTestChatRuntime(t, t.TempDir())
	runtime.tools = append(runtime.tools, QueenLifecycleToolDefs()...)

	names := queenToolNameSet(runtime.toolsForTurn())
	for _, name := range []string{QueenGetWorkerStatusToolName, QueenInjectMessageToolName, QueenStopWorkerToolName} {
		if names[name] {
			t.Fatalf("%s advertised to a session with no colony", name)
		}
	}
	for _, name := range []string{QueenListWorkerQuestionsToolName, QueenSetTriggerToolName, QueenListTriggersToolName, QueenListBrowserProfilesToolName, QueenUpdateWorkerProfileToolName} {
		if !names[name] {
			t.Fatalf("%s missing without a colony", name)
		}
	}

	colonyRuntime := colony.NewColonyRuntime(colony.RuntimeOptions{EventBus: eventbus.NewEventBus(), StreamID: "colony_alpha"})
	runtime.BindColony(colonyHostStub{runtime: colonyRuntime})
	names = queenToolNameSet(runtime.toolsForTurn())
	for _, name := range QueenLifecycleToolNames() {
		if !names[name] {
			t.Fatalf("%s missing after the colony bound", name)
		}
	}
}

// Parity-exempt: internal wiring assertion. The lifecycle binding's executor
// context must accept a cancelled-parent context without cancelling the stop
// path (workers belong to the colony, not to the chat turn).
func TestStopWorkerIgnoresCancelledToolContext(t *testing.T) {
	provider := newLifecycleGateLLM()
	runtime := lifecycleTestColony(t, eventbus.NewEventBus(), provider)
	session, binding, _ := lifecycleTestSession(t)
	session.Colony = colonyHostStub{runtime: runtime}
	_ = spawnGatedWorker(t, runtime, "hold")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	content, isError := binding.Execute(cancelled, QueenStopWorkerToolName, map[string]any{"grace_seconds": 0})
	if isError {
		t.Fatalf("stop under cancelled ctx failed: %s", content)
	}
	if payload := decodeToolPayload(t, content); payload["status"] != "stopped" {
		t.Fatalf("status = %v", payload["status"])
	}
	provider.release()
	runtime.Stop()
}
