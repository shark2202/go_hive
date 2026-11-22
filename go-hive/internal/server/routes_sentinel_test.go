package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type fakeSentinelStore struct {
	mu       sync.Mutex
	configs  map[string]SentinelNotificationsConfig
	updates  []SentinelNotificationsUpdate
	storeErr error
}

func sentinelDefaultConfig() SentinelNotificationsConfig {
	return SentinelNotificationsConfig{
		SentinelEnabled: true,
		Channel:         "hive",
		Target:          map[string]any{},
		Allowlist:       []string{},
		Thread:          map[string]any{},
	}
}

func (f *fakeSentinelStore) LoadNotificationsConfig(colonyID string) SentinelNotificationsConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg, ok := f.configs[colonyID]; ok {
		return cfg
	}
	return sentinelDefaultConfig()
}

func (f *fakeSentinelStore) UpdateNotificationsConfig(colonyID string, update SentinelNotificationsUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeErr != nil {
		return f.storeErr
	}
	f.updates = append(f.updates, update)
	thread := map[string]any{}
	if cfg, ok := f.configs[colonyID]; ok && cfg.Thread != nil {
		thread = cfg.Thread
	}
	if f.configs == nil {
		f.configs = map[string]SentinelNotificationsConfig{}
	}
	channel := "hive"
	if update.Channel != nil {
		channel = *update.Channel
	}
	f.configs[colonyID] = SentinelNotificationsConfig{
		SentinelEnabled:      update.SentinelEnabled,
		Channel:              channel,
		Target:               update.Target,
		Allowlist:            update.Allowlist,
		Thread:               thread,
		ClassifyAfterSeconds: update.ClassifyAfterSeconds,
	}
	return nil
}

type fakeSentinelNotifier struct {
	telegramToken string
	slackBot      string
	slackApp      string
	sendResult    SentinelSendResult
	sendErr       error
	sentChannel   string
	sentTarget    map[string]any
	sentText      string
	sentThread    map[string]any
}

func (f *fakeSentinelNotifier) TelegramToken() string { return f.telegramToken }
func (f *fakeSentinelNotifier) SlackBotToken() string { return f.slackBot }
func (f *fakeSentinelNotifier) SlackAppToken() string { return f.slackApp }

func (f *fakeSentinelNotifier) Send(channel string, target map[string]any, text string, threadRef map[string]any) (SentinelSendResult, error) {
	f.sentChannel = channel
	f.sentTarget = target
	f.sentText = text
	f.sentThread = threadRef
	return f.sendResult, f.sendErr
}

func sentinelTestMux(t *testing.T, store SentinelStore, notifier SentinelNotifier, telegramBase, slackBase string, refresh func()) *http.ServeMux {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	colonyDir := filepath.Join(home.ColoniesDir(), "c1")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	mux := http.NewServeMux()
	RegisterSentinelRoutes(mux, SentinelRouteDeps{
		Home:             home,
		Store:            store,
		Notifier:         notifier,
		TelegramAPIBase:  telegramBase,
		SlackAPIBase:     slackBase,
		RefreshListeners: refresh,
	})
	return mux
}

// Parity-exempt: Go-only hardening; colony ids become directory names, so
// anything outside [a-z0-9_] is refused before the store is touched.
func TestSentinelRejectsInvalidColonyID(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := http.NewServeMux()
	RegisterSentinelRoutes(mux, SentinelRouteDeps{Home: home, Store: &fakeSentinelStore{}})
	for _, path := range []string{
		"/api/colony/Bad/notifications",
		"/api/colony/a-b/notifications",
		"/api/colony/a.b/notifications",
	} {
		recorder, _ := doSessionJSON(t, mux, http.MethodGet, path, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, recorder.Code)
		}
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_get_notifications_default
func TestSentinelGetNotificationsDefault(t *testing.T) {
	store := &fakeSentinelStore{}
	mux := sentinelTestMux(t, store, &fakeSentinelNotifier{}, "", "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/colony/c1/notifications", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["sentinel_enabled"] != true {
		t.Fatalf("sentinel_enabled = %#v", body["sentinel_enabled"])
	}
	if body["channel"] != "hive" {
		t.Fatalf("channel = %#v", body["channel"])
	}
	credentials, _ := body["credentials"].(map[string]any)
	telegram, _ := credentials["telegram"].(map[string]any)
	if telegram["configured"] != false {
		t.Fatalf("telegram configured = %#v", telegram["configured"])
	}
	if body["classify_after_seconds"] != nil {
		t.Fatalf("classify_after_seconds = %#v", body["classify_after_seconds"])
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_get_notifications_404
func TestSentinelGetNotificationsMissingColonyReturns404(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodGet, "/api/colony/ghost/notifications", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_put_notifications_saves
func TestSentinelPutNotificationsSaves(t *testing.T) {
	store := &fakeSentinelStore{}
	notifier := &fakeSentinelNotifier{telegramToken: "T"}
	mux := sentinelTestMux(t, store, notifier, "", "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":true,"channel":"telegram","target":{"chat_id":"9"},"allowlist":["42"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["channel"] != "telegram" || body["sentinel_enabled"] != true {
		t.Fatalf("body = %#v", body)
	}
	if len(store.updates) != 1 {
		t.Fatalf("updates = %#v", store.updates)
	}
	update := store.updates[0]
	if update.Target["chat_id"] != "9" || len(update.Allowlist) != 1 || update.Allowlist[0] != "42" {
		t.Fatalf("update = %#v", update)
	}
	if !update.SentinelEnabled || update.Channel == nil || *update.Channel != "telegram" {
		t.Fatalf("update = %#v", update)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_put_rejects_enabled_without_channel
func TestSentinelPutRejectsEnabledWithoutChannel(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications", `{"sentinel_enabled":true}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_credential_status
func TestSentinelCredentialStatus(t *testing.T) {
	notifier := &fakeSentinelNotifier{telegramToken: "T", slackBot: "xoxb"}
	mux := sentinelTestMux(t, &fakeSentinelStore{}, notifier, "", "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/sentinel/credentials", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	telegram, _ := body["telegram"].(map[string]any)
	slack, _ := body["slack"].(map[string]any)
	if telegram["configured"] != true || slack["bot"] != true || slack["app"] != false {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_telegram_validate_ok
func TestSentinelTelegramValidateOK(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true,"result":{"username":"mybot","first_name":"My Bot"}}`))
	}))
	defer server.Close()

	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, server.URL, "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/telegram/validate", `{"token":"123:abc"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if gotPath != "/bot123:abc/getMe" {
		t.Fatalf("path = %q", gotPath)
	}
	if body["ok"] != true || body["bot_username"] != "mybot" || body["bot_name"] != "My Bot" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_telegram_validate_bad_token
func TestSentinelTelegramValidateBadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer server.Close()

	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, server.URL, "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/telegram/validate", `{"token":"bad"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["ok"] != false {
		t.Fatalf("ok = %#v", body["ok"])
	}
	if description, _ := body["error"].(string); !strings.Contains(description, "Unauthorized") {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_telegram_detect_chat
func TestSentinelTelegramDetectChat(t *testing.T) {
	var queryOffset string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryOffset = r.URL.Query().Get("offset")
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message":{"chat":{"id":555,"first_name":"Rich"},"from":{"id":555,"username":"rich"}}}]}`))
	}))
	defer server.Close()

	notifier := &fakeSentinelNotifier{telegramToken: "T"}
	mux := sentinelTestMux(t, &fakeSentinelStore{}, notifier, server.URL, "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/telegram/detect-chat", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if queryOffset != "-1" {
		t.Fatalf("offset = %q", queryOffset)
	}
	if body["ok"] != true || body["chat_id"] != "555" || body["sender_id"] != "555" {
		t.Fatalf("body = %#v", body)
	}
	if body["chat_title"] != "Rich" || body["sender_name"] != "rich" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_telegram_detect_chat_pending
func TestSentinelTelegramDetectChatPending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer server.Close()

	notifier := &fakeSentinelNotifier{telegramToken: "T"}
	mux := sentinelTestMux(t, &fakeSentinelStore{}, notifier, server.URL, "", nil)
	_, body := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/telegram/detect-chat", `{}`)
	if body["ok"] != false || body["pending"] != true {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_detect_chat_requires_token
func TestSentinelDetectChatRequiresToken(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/telegram/detect-chat", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_test_send
func TestSentinelTestSend(t *testing.T) {
	notifier := &fakeSentinelNotifier{sendResult: SentinelSendResult{OK: true, MessageID: "1"}}
	mux := sentinelTestMux(t, &fakeSentinelStore{}, notifier, "", "", nil)
	_, body := doSessionJSON(t, mux, http.MethodPost, "/api/colony/c1/notifications/test",
		`{"channel":"telegram","target":{"chat_id":"9"}}`)
	if body["ok"] != true || body["message_id"] != "1" {
		t.Fatalf("body = %#v", body)
	}
	if notifier.sentChannel != "telegram" || notifier.sentTarget["chat_id"] != "9" {
		t.Fatalf("sent = %#v %#v", notifier.sentChannel, notifier.sentTarget)
	}
	if !strings.Contains(notifier.sentText, `colony "c1"`) {
		t.Fatalf("text = %q", notifier.sentText)
	}
}

// Parity: core/tests/test_sentinel_routes.py::test_test_send_requires_target
func TestSentinelTestSendRequiresTarget(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/colony/c1/notifications/test", `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: invalid-JSON 400 branch has no dedicated Python route test (aiohttp request.json raises inside the same handler).
func TestSentinelPutRejectsInvalidJSON(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications", `{`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: allowlist type guard is exercised only indirectly upstream; no Python route test asserts the 400.
func TestSentinelPutRejectsBadAllowlist(t *testing.T) {
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":false,"allowlist":"nope"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: per-colony idle-budget validation has no Python route test.
func TestSentinelPutClassifyAfterSeconds(t *testing.T) {
	store := &fakeSentinelStore{}
	mux := sentinelTestMux(t, store, &fakeSentinelNotifier{}, "", "", nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":false,"classify_after_seconds":42.5}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["classify_after_seconds"] != 42.5 {
		t.Fatalf("classify_after_seconds = %#v", body["classify_after_seconds"])
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":false,"classify_after_seconds":0}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("zero status = %d", recorder.Code)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":false,"classify_after_seconds":"nope"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("string status = %d", recorder.Code)
	}
}

// Parity-exempt: the aiohttp route refreshes listeners on save; no Python route test asserts the callback.
func TestSentinelPutRefreshesListeners(t *testing.T) {
	calls := 0
	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", "", func() { calls++ })
	recorder, _ := doSessionJSON(t, mux, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if calls != 1 {
		t.Fatalf("refresh calls = %d", calls)
	}
}

// Parity-exempt: Slack token validation has no Python route test in test_sentinel_routes.py.
func TestSentinelSlackValidate(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true,"team":"Hive","user":"bot"}`))
	}))
	defer server.Close()

	mux := sentinelTestMux(t, &fakeSentinelStore{}, &fakeSentinelNotifier{}, "", server.URL, nil)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/sentinel/slack/validate", `{"token":"xoxb-1"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if authorization != "Bearer xoxb-1" {
		t.Fatalf("authorization = %q", authorization)
	}
	if body["ok"] != true || body["team"] != "Hive" || body["bot_user"] != "bot" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: Go-specific seam guard for a nil engine handle; Python always has the store module loaded.
func TestSentinelStoreUnavailable(t *testing.T) {
	mux := sentinelTestMux(t, nil, &fakeSentinelNotifier{}, "", "", nil)
	recorder, _ := doSessionJSON(t, mux, http.MethodGet, "/api/colony/c1/notifications", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}
