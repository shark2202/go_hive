package sentinel

// Behavior tests for the sentinel_setup model tool. Every expected literal is
// copied from core/framework/agent_loop/internals/sentinel_tool.py; the tool
// sends no real notification — validation, getUpdates and test sends all run
// over the package's fake HTTP client.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSetupManager records detect/configure side effects without touching the
// process-global manager.
type fakeSetupManager struct {
	recent    map[string]any
	listening bool
	refreshes int
}

func (m *fakeSetupManager) RecentChat(channel string, maxAge ...float64) map[string]any {
	return m.recent
}

func (m *fakeSetupManager) IsListening(channel string) bool { return m.listening }

func (m *fakeSetupManager) RefreshListeners() { m.refreshes++ }

// setupDeps collects the seams a test needs to inspect after a call.
type setupToolHarness struct {
	deps      SentinelSetupDeps
	client    *fakeHTTPClient
	manager   *fakeSetupManager
	saved     []string
	saveErr   error
	publishes int
}

func newSetupToolHarness(t *testing.T, env map[string]string, responses ...fakeResponse) *setupToolHarness {
	t.Helper()
	notifier, client := newTestNotifier(t, env, responses...)
	harness := &setupToolHarness{
		client:  client,
		manager: &fakeSetupManager{},
	}
	harness.deps = SentinelSetupDeps{
		Store:    newTestStore(t, nil),
		Notifier: notifier,
		Manager:  harness.manager,
		SaveToken: func(credentialID, token string) error {
			if harness.saveErr != nil {
				return harness.saveErr
			}
			harness.saved = append(harness.saved, credentialID+"="+token)
			return nil
		},
		PublishRefresh: func() { harness.publishes++ },
		HTTP:           client,
	}
	return harness
}

func (h *setupToolHarness) run(toolInput map[string]any, defaultColonyID string) string {
	return HandleSentinelSetup(context.Background(), toolInput, defaultColonyID, h.deps)
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::render_help
// Parity: framework/agent_loop/internals/sentinel_tool.py::handle
func TestSentinelSetupHelpIsDefaultAndVerbatim(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	for _, input := range []map[string]any{
		nil,
		{},
		{"action": "help"},
		{"action": "HELP"},
		{"action": "  help  "},
		{"action": ""},
		{"action": nil},
		{"action": false},
	} {
		if got := harness.run(input, ""); got != RenderSentinelSetupHelp() {
			t.Fatalf("input %#v returned %q, want the help text", input, got)
		}
	}
	if harness.publishes != 0 {
		t.Fatalf("help published %d refresh events, want 0", harness.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::handle
func TestSentinelSetupUnknownAction(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	want := "ERROR: unknown sentinel_setup action 'bogus'. Call sentinel_setup() for usage."
	if got := harness.run(map[string]any{"action": "bogus"}, ""); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A non-string action is str()'d like upstream.
	wantNumber := "ERROR: unknown sentinel_setup action '123'. Call sentinel_setup() for usage."
	if got := harness.run(map[string]any{"action": 123}, ""); got != wantNumber {
		t.Fatalf("got %q, want %q", got, wantNumber)
	}
}

// TestSentinelSetupHelpMatchesRecordedPythonPayload pins the help text to the
// recorded Python runtime output captured in the tool-output golden. The file
// is absent in a standalone module checkout, where the local literals remain
// the only reference.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::render_help
func TestSentinelSetupHelpMatchesRecordedPythonPayload(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "e2e-assets", "tool-output", "results.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("tool-output golden not present (standalone checkout): %v", err)
	}
	var doc struct {
		Scenarios map[string]struct {
			Python struct {
				IsError bool   `json:"is_error"`
				Payload string `json:"payload"`
			} `json:"python"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode tool-output golden: %v", err)
	}
	scenario, ok := doc.Scenarios["sentinel_setup"]
	if !ok {
		t.Fatal("tool-output golden has no sentinel_setup scenario")
	}
	if scenario.Python.IsError {
		t.Fatal("recorded Python sentinel_setup call was an error; the capture expectation changed")
	}
	// The capture normalizer stores plain-text payloads with trailing newlines
	// stripped (tool_output_golden.py: body.rstrip("\n")); SENTINEL_TOOL_HELP
	// itself ends with one newline.
	got := strings.TrimRight(RenderSentinelSetupHelp(), "\n")
	if got != scenario.Python.Payload {
		t.Fatalf("help text differs from the recorded Python payload:\n got %q\nwant %q", got, scenario.Python.Payload)
	}
	if !strings.HasSuffix(RenderSentinelSetupHelp(), "\n") {
		t.Fatal("help text lost the upstream trailing newline")
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
func TestSentinelSetupStoreTokenValidation(t *testing.T) {
	cases := []struct {
		name     string
		input    map[string]any
		want     string
		wantSave string
	}{
		{
			name:  "unknown provider",
			input: map[string]any{"action": "store_token", "provider": "email", "token": "x"},
			want:  "ERROR: provider must be 'slack' (bot xoxb-), 'slack_app' (app-level xapp-), or 'telegram'.",
		},
		{
			name:  "missing provider",
			input: map[string]any{"action": "store_token", "token": "x"},
			want:  "ERROR: provider must be 'slack' (bot xoxb-), 'slack_app' (app-level xapp-), or 'telegram'.",
		},
		{
			name:  "missing token",
			input: map[string]any{"action": "store_token", "provider": "telegram"},
			want:  "ERROR: token is required.",
		},
		{
			name:  "blank token",
			input: map[string]any{"action": "store_token", "provider": "telegram", "token": "   "},
			want:  "ERROR: token is required.",
		},
		{
			name:  "slack bot token prefix",
			input: map[string]any{"action": "store_token", "provider": "slack", "token": "xoxp-1"},
			want:  "ERROR: a Slack bot token starts with 'xoxb-'. For the app-level token use provider='slack_app'.",
		},
		{
			name:  "slack app token prefix",
			input: map[string]any{"action": "store_token", "provider": "slack_app", "token": "xoxb-1"},
			want:  "ERROR: a Slack app-level token starts with 'xapp-'.",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			harness := newSetupToolHarness(t, nil)
			got := harness.run(testCase.input, "")
			if got != testCase.want {
				t.Fatalf("got %q, want %q", got, testCase.want)
			}
			if len(harness.saved) != 0 {
				t.Fatalf("saved %v on a validation failure", harness.saved)
			}
			if harness.publishes != 0 {
				t.Fatalf("published %d refresh events on an error", harness.publishes)
			}
			if len(harness.client.requests) != 0 {
				t.Fatalf("issued %d HTTP requests on a validation failure", len(harness.client.requests))
			}
		})
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
// Parity: framework/agent_loop/internals/sentinel_tool.py::_validate_slack
func TestSentinelSetupStoreSlackBotToken(t *testing.T) {
	harness := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true, "team": "Acme"}`})
	got := harness.run(map[string]any{"action": "store_token", "provider": "slack", "token": "xoxb-secret"}, "")
	want := "Stored Slack bot token — connected to workspace 'Acme'. Sentinel can now send to Slack."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.saved) != 1 || harness.saved[0] != "slack=xoxb-secret" {
		t.Fatalf("saved = %v", harness.saved)
	}
	if harness.publishes != 1 {
		t.Fatalf("published %d refresh events, want 1", harness.publishes)
	}
	if len(harness.client.requests) != 1 {
		t.Fatalf("requests = %d", len(harness.client.requests))
	}
	request := harness.client.requests[0]
	if request.method != "POST" || !strings.HasSuffix(request.url, "/auth.test") {
		t.Fatalf("auth.test request = %s %s", request.method, request.url)
	}
	if got := request.headers.Get("Authorization"); got != "Bearer xoxb-secret" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
// Parity: framework/agent_loop/internals/sentinel_tool.py::_validate_slack
func TestSentinelSetupStoreSlackBotTokenUnknownTeamAndRejection(t *testing.T) {
	harness := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true}`})
	got := harness.run(map[string]any{"action": "store_token", "provider": "slack", "token": "xoxb-secret"}, "")
	want := "Stored Slack bot token — connected to workspace 'unknown'. Sentinel can now send to Slack."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	rejected := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": false, "error": "invalid_auth"}`})
	got = rejected.run(map[string]any{"action": "store_token", "provider": "slack", "token": "xoxb-secret"}, "")
	if want := "ERROR: Slack rejected the bot token (auth.test): invalid_auth"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(rejected.saved) != 0 || rejected.publishes != 0 {
		t.Fatalf("rejected token was stored (saved=%v publishes=%d)", rejected.saved, rejected.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
func TestSentinelSetupStoreSlackAppTokenSkipsValidation(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	got := harness.run(map[string]any{"action": "store_token", "provider": "slack_app", "token": "xapp-secret"}, "")
	want := "Stored Slack app-level token. Sentinel can now receive replies over Socket Mode."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.saved) != 1 || harness.saved[0] != "slack_app=xapp-secret" {
		t.Fatalf("saved = %v", harness.saved)
	}
	if len(harness.client.requests) != 0 {
		t.Fatalf("slack_app validation issued %d requests, want 0", len(harness.client.requests))
	}
	if harness.publishes != 1 {
		t.Fatalf("published %d refresh events, want 1", harness.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
// Parity: framework/agent_loop/internals/sentinel_tool.py::_validate_telegram
func TestSentinelSetupStoreTelegramToken(t *testing.T) {
	harness := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true, "result": {"username": "mybot"}}`})
	got := harness.run(map[string]any{"action": "store_token", "provider": "telegram", "token": "123:abc"}, "")
	if want := "Stored Telegram bot token (@mybot)."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.saved) != 1 || harness.saved[0] != "telegram=123:abc" {
		t.Fatalf("saved = %v", harness.saved)
	}
	if len(harness.client.requests) != 1 {
		t.Fatalf("requests = %d", len(harness.client.requests))
	}
	if request := harness.client.requests[0]; request.method != "GET" || !strings.Contains(request.url, "/bot123:abc/getMe") {
		t.Fatalf("getMe request = %s %s", request.method, request.url)
	}

	firstName := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true, "result": {"first_name": "My Bot"}}`})
	got = firstName.run(map[string]any{"action": "store_token", "provider": "telegram", "token": "123:abc"}, "")
	if want := "Stored Telegram bot token (@My Bot)."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	noName := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true, "result": {}}`})
	got = noName.run(map[string]any{"action": "store_token", "provider": "telegram", "token": "123:abc"}, "")
	if want := "Stored Telegram bot token."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	rejected := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": false, "description": "Unauthorized"}`})
	got = rejected.run(map[string]any{"action": "store_token", "provider": "telegram", "token": "123:abc"}, "")
	if want := "ERROR: Telegram rejected the token (getMe): Unauthorized"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(rejected.saved) != 0 || rejected.publishes != 0 {
		t.Fatalf("rejected token was stored (saved=%v publishes=%d)", rejected.saved, rejected.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
func TestSentinelSetupStoreTokenSaveFailure(t *testing.T) {
	harness := newSetupToolHarness(t, nil, fakeResponse{body: `{"ok": true, "result": {"username": "mybot"}}`})
	harness.saveErr = errors.New("disk full")
	got := harness.run(map[string]any{"action": "store_token", "provider": "telegram", "token": "123:abc"}, "")
	if want := "ERROR: could not store the token: disk full"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if harness.publishes != 0 {
		t.Fatalf("published %d refresh events after a failed save", harness.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_status
func TestSentinelSetupStatus(t *testing.T) {
	harness := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"})
	got := harness.run(map[string]any{"action": "status"}, "")
	want := "Sentinel channel tokens:\n" +
		"  telegram        : configured\n" +
		"  slack bot (xoxb): not set\n" +
		"  slack app (xapp): not set\n" +
		"\n" +
		"No colony bound to this session — pass colony_id to configure/test."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	writeColonyConfig(t, harness.deps.Store, "demo", map[string]any{
		"sentinel_enabled": true,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "123"},
		"allowlist":        []any{"123"},
	})
	got = harness.run(map[string]any{"action": "status"}, "demo")
	want = "Sentinel channel tokens:\n" +
		"  telegram        : configured\n" +
		"  slack bot (xoxb): not set\n" +
		"  slack app (xapp): not set\n" +
		"\n" +
		"Colony 'demo': enabled=True channel=telegram target={'chat_id': '123'} allowlist=['123']"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_configure
func TestSentinelSetupConfigureErrors(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	if got, want := harness.run(map[string]any{"action": "configure"}, ""),
		"ERROR: no colony bound to this session — pass colony_id (the colony's on-disk directory name)."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := harness.run(map[string]any{"action": "configure", "colony_id": "demo", "enabled": true}, ""),
		"ERROR: channel must be 'telegram' or 'slack' when enabled=true."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := harness.run(map[string]any{"action": "configure", "colony_id": "demo", "allowlist": "123"}, ""),
		"ERROR: allowlist must be a list."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := harness.run(map[string]any{"action": "configure", "colony_id": "demo", "classify_after_seconds": "abc"}, ""),
		"ERROR: classify_after_seconds must be a number or null."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := harness.run(map[string]any{"action": "configure", "colony_id": "demo", "classify_after_seconds": -5}, ""),
		"ERROR: classify_after_seconds must be positive."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := harness.run(map[string]any{"action": "configure", "colony_id": "missing"}, ""),
		"ERROR: colony 'missing' not found on disk."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if harness.manager.refreshes != 0 {
		t.Fatalf("refresh_listeners ran %d times on error paths", harness.manager.refreshes)
	}
	if harness.publishes != 0 {
		t.Fatalf("published %d refresh events on error paths", harness.publishes)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_configure
func TestSentinelSetupConfigureWritesConfig(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	if err := os.MkdirAll(filepath.Join(harness.deps.Store.ColoniesDir, "demo"), 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	got := harness.run(map[string]any{
		"action":                 "configure",
		"channel":                "telegram",
		"target":                 map[string]any{"chat_id": "123"},
		"allowlist":              []any{"123", 456},
		"classify_after_seconds": "300",
		"enabled":                true,
	}, "demo")
	want := "Saved Sentinel config for colony 'demo': ENABLED, channel=telegram, target={'chat_id': '123'}."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if harness.manager.refreshes != 1 {
		t.Fatalf("refresh_listeners ran %d times, want 1", harness.manager.refreshes)
	}
	if harness.publishes != 1 {
		t.Fatalf("published %d refresh events, want 1", harness.publishes)
	}

	config := harness.deps.Store.LoadNotificationsConfig("demo")
	if !config.SentinelEnabled || config.Channel != "telegram" {
		t.Fatalf("config = %#v", config)
	}
	if got := PyStr(config.Target["chat_id"]); got != "123" {
		t.Fatalf("target chat_id = %q", got)
	}
	if strings.Join(config.Allowlist, ",") != "123,456" {
		t.Fatalf("allowlist = %v", config.Allowlist)
	}
	if config.ClassifyAfterSeconds == nil || *config.ClassifyAfterSeconds != 300 {
		t.Fatalf("classify_after_seconds = %v", config.ClassifyAfterSeconds)
	}

	// Disabling with no channel/target writes the upstream defaults and
	// renders "-"/"{}".
	got = harness.run(map[string]any{"action": "configure", "colony_id": "demo", "enabled": false}, "")
	want = "Saved Sentinel config for colony 'demo': disabled, channel=-, target={}."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	config = harness.deps.Store.LoadNotificationsConfig("demo")
	if config.SentinelEnabled {
		t.Fatalf("config still enabled: %#v", config)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_test
func TestSentinelSetupTest(t *testing.T) {
	harness := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": true, "result": {"message_id": 77}}`})
	missing := "ERROR: channel and target are required to send a test (pass them, or configure the colony first)."
	if got := harness.run(map[string]any{"action": "test"}, ""); got != missing {
		t.Fatalf("got %q, want %q", got, missing)
	}
	if got := harness.run(map[string]any{"action": "test", "channel": "email", "target": map[string]any{"chat_id": "9"}}, ""); got != missing {
		t.Fatalf("got %q, want %q", got, missing)
	}

	got := harness.run(map[string]any{"action": "test", "channel": "telegram", "target": map[string]any{"chat_id": "9"}}, "")
	if want := "Test message delivered to telegram {'chat_id': '9'}."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.client.requests) != 1 || !strings.Contains(harness.client.requests[0].url, "/sendMessage") {
		t.Fatalf("send request = %#v", harness.client.requests)
	}

	failed := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": false, "description": "chat not found"}`})
	got = failed.run(map[string]any{"action": "test", "channel": "telegram", "target": map[string]any{"chat_id": "9"}}, "")
	if want := "ERROR: test send failed: chat not found"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_test
func TestSentinelSetupTestUsesColonyConfigFallback(t *testing.T) {
	harness := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": true, "result": {"message_id": 1}}`})
	writeColonyConfig(t, harness.deps.Store, "demo", map[string]any{
		"sentinel_enabled": true,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "123"},
	})
	got := harness.run(map[string]any{"action": "test"}, "demo")
	if want := "Test message delivered to telegram {'chat_id': '123'}."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_detect_chat
// Parity: framework/agent_loop/internals/sentinel_tool.py::_format_detected
func TestSentinelSetupDetectChatFromManager(t *testing.T) {
	harness := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"})
	harness.manager.recent = map[string]any{
		"chat_id":     "123",
		"chat_title":  "Ops",
		"sender_id":   "45",
		"sender_name": "alice",
	}
	got := harness.run(map[string]any{"action": "detect_chat"}, "")
	want := `Detected Telegram chat "Ops": chat_id=123; sender=alice (id 45). ` +
		`Configure with target={"chat_id": "123"} and allowlist=["45"], then enable.`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.client.requests) != 0 {
		t.Fatalf("manager path issued %d getUpdates requests, want 0", len(harness.client.requests))
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_detect_chat
func TestSentinelSetupDetectChatPending(t *testing.T) {
	harness := newSetupToolHarness(t, nil)
	if got, want := harness.run(map[string]any{"action": "detect_chat"}, ""),
		"ERROR: store the Telegram bot token first (store_token provider=telegram)."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	listening := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"})
	listening.manager.listening = true
	got := listening.run(map[string]any{"action": "detect_chat"}, "")
	want := "PENDING: the Sentinel Telegram listener is running but hasn't seen a message yet. " +
		"Ask the user to open the bot and tap Start (or send any message), then run detect_chat again. " +
		"Don't decrypt tokens or poll the Telegram API by hand — the listener owns the update stream and will " +
		"surface the chat here."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	empty := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": true, "result": []}`})
	got = empty.run(map[string]any{"action": "detect_chat"}, "")
	want = "PENDING: no message yet — ask the user to open the bot in Telegram and send it any message (or tap Start), then run detect_chat again."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Parity: framework/agent_loop/internals/sentinel_tool.py::_detect_chat
func TestSentinelSetupDetectChatPollsUpdates(t *testing.T) {
	harness := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": true, "result": [{"message": {"chat": {"id": 123, "title": "Ops"}, "from": {"id": 45, "username": "alice"}}}]}`})
	got := harness.run(map[string]any{"action": "detect_chat"}, "")
	want := `Detected Telegram chat "Ops": chat_id=123; sender=alice (id 45). ` +
		`Configure with target={"chat_id": "123"} and allowlist=["45"], then enable.`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if len(harness.client.requests) != 1 {
		t.Fatalf("requests = %d", len(harness.client.requests))
	}
	request := harness.client.requests[0]
	if request.method != "GET" || !strings.Contains(request.url, "/getUpdates") {
		t.Fatalf("getUpdates request = %s %s", request.method, request.url)
	}
	if !strings.Contains(request.url, "offset=-1") || !strings.Contains(request.url, "limit=1") {
		t.Fatalf("getUpdates query = %s", request.url)
	}

	noChat := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": true, "result": [{"update_id": 1}]}`})
	got = noChat.run(map[string]any{"action": "detect_chat"}, "")
	if want := "PENDING: latest update has no chat — send a direct message to the bot and retry."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	rejected := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{body: `{"ok": false, "description": "Unauthorized"}`})
	got = rejected.run(map[string]any{"action": "detect_chat"}, "")
	if want := "ERROR: getUpdates rejected: Unauthorized"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	failed := newSetupToolHarness(t, map[string]string{"TELEGRAM_BOT_TOKEN": "telegram-token"},
		fakeResponse{err: errors.New("dial tcp: timeout")})
	got = failed.run(map[string]any{"action": "detect_chat"}, "")
	if !strings.HasPrefix(got, "ERROR: getUpdates failed: ") || !strings.Contains(got, "dial tcp: timeout") {
		t.Fatalf("got %q", got)
	}
}
