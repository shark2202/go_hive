package server

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
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
)

func doWiringJSON(t *testing.T, app *App, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

// Parity: framework/server/routes_messages.py (manager.build_llm)
//
// The classify routes must get their model from the app assembly, not from a
// nil hook: without it every production classify answered 500.
func TestMessageRoutesBuildLLMThroughTheApp(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_technology","reason":"build"}`}
	app := newAppForTest(t, AppDeps{
		Home:     hivehome.Home{Root: t.TempDir()},
		BuildLLM: func() llm.Completer { return provider },
	})
	recorder, body := doWiringJSON(t, app, http.MethodPost, "/api/messages/classify", `{"message":"Build me a scraper"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["queen_id"] != "queen_technology" {
		t.Fatalf("body = %#v", body)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("classifier calls = %d, want 1 (BuildLLM not wired)", len(provider.calls))
	}
}

// Parity: framework/server/routes_messages.py (manager.build_llm)
//
// classify-colony shares the assembly's builder: the second message route must
// answer from the injected provider too, not only /api/messages/classify.
func TestClassifyColonyBuildsLLMThroughTheApp(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_growth","colony_name":"Growth Lab","reason":"growth work"}`}
	app := newAppForTest(t, AppDeps{
		Home:     hivehome.Home{Root: t.TempDir()},
		BuildLLM: func() llm.Completer { return provider },
	})
	recorder, body := doWiringJSON(t, app, http.MethodPost, "/api/messages/classify-colony", `{"message":"Grow signups"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["queen_id"] != "queen_growth" || body["colony_name"] != "growth_lab" || body["reason"] != "growth work" {
		t.Fatalf("body = %#v", body)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("classifier calls = %d, want 1 (BuildLLM not wired)", len(provider.calls))
	}
}

// Parity: framework/server/routes_messages.py (manager.build_llm)
//
// The assembly's default BuildLLM is the configuration resolver: a configured
// main slot yields a provider instead of the explicit "not configured" 500.
func TestMessageRouteLLMResolvesTheConfiguredMainSlot(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-x", "api_key": "sk-main"},
	})
	provider := messageRouteLLM(AppDeps{Home: home})()
	if provider == nil {
		t.Fatal("messageRouteLLM returned nil with a configured main slot")
	}
	if namer, ok := provider.(llm.ModelNamer); ok && namer.ModelName() == "" {
		t.Fatal("resolved provider has no model")
	}
}

// Parity: framework/server/routes_queens.py::handle_put_queen_llm
//
// PUT .../llm runs the provider key check the config routes use; a rejected
// key answers 400 and stores nothing.
func TestQueenPutLLMValidatesTheProviderKeyThroughTheApp(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_growth", map[string]any{"name": "Growth", "title": "Head of Growth"})
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"provider": "anthropic", "model": "claude-x"}})

	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer keyServer.Close()

	app := newAppForTest(t, AppDeps{Home: home})
	body := `{"section":{"provider":"openai","model":"gpt-x","api_base":` +
		strconvQuote(keyServer.URL+"/v1") + `,"api_key":"sk-bad"}}`
	recorder, response := doWiringJSON(t, app, http.MethodPut, "/api/queen/queen_growth/llm", body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(queenStr(response["error"]), "Key check failed") {
		t.Fatalf("error = %#v", response["error"])
	}
	config := readFeatureConfig(t, home)
	if _, present := config["queen_llm"]; present {
		t.Fatalf("queen_llm was written despite a rejected key: %#v", config)
	}
}

// Parity: framework/server/routes_queens.py::_refresh_live_queen_identity
//
// A profile edit re-materializes every live session of the queen: the phase
// state keeps the new profile, the runtime's static prompt is rebased, and
// QUEEN_IDENTITY_SELECTED reaches the conversation header.
func TestQueenProfilePatchRefreshesLiveIdentity(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", map[string]any{"name": "Fox", "title": "The painter"})

	bus := eventbus.NewEventBus()
	phase := NewQueenPhaseState("independent", "queen_x", map[string]any{"name": "Fox", "title": "The painter"}, "", bus)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeMessageLLM{response: "ok"},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "sess_live",
		QueenName: "queen_x",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	manager := NewSessionManager()
	manager.AddSession(&Session{
		ID:         "sess_live",
		QueenName:  "queen_x",
		LoadedAt:   1,
		EventBus:   bus,
		PhaseState: phase,
		Chat:       runtime,
	})
	t.Cleanup(func() { _ = runtime.Close() })

	identityEvents := make(chan *eventbus.AgentEvent, 4)
	bus.Subscribe([]eventbus.EventType{eventbus.EventQueenIdentitySelected}, func(event *eventbus.AgentEvent) {
		identityEvents <- event
	})

	app := newAppForTest(t, AppDeps{Home: home, Manager: manager})
	recorder, body := doWiringJSON(t, app, http.MethodPatch, "/api/queen/queen_x/profile", `{"name":"Fox II"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "Fox II" {
		t.Fatalf("body = %#v", body)
	}
	if got, _ := phase.QueenProfile()["name"].(string); got != "Fox II" {
		t.Fatalf("phase profile name = %q, want Fox II", got)
	}
	if prompt := runtime.conv.SystemPrompt(); !strings.Contains(prompt, "your name is Fox II") {
		t.Fatalf("live system prompt kept the old identity: %s", prompt)
	}

	select {
	case event := <-identityEvents:
		if event.Data["queen_id"] != "queen_x" || event.Data["name"] != "Fox II" {
			t.Fatalf("identity event = %#v", event.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no QUEEN_IDENTITY_SELECTED event was published")
	}
}

// Parity: framework/server/routes_queens.py::handle_put_persona
//
// The persona editor is the second writer that re-materializes a live
// identity (the profile PATCH is the first): a saved persona must reach the
// phase state, the cached system prompt and the conversation header.
func TestQueenPersonaPutRefreshesLiveIdentity(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_x", map[string]any{"name": "Fox", "title": "The painter"})

	bus := eventbus.NewEventBus()
	phase := NewQueenPhaseState("independent", "queen_x", map[string]any{"name": "Fox", "title": "The painter"}, "", bus)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeMessageLLM{response: "ok"},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "sess_live",
		QueenName: "queen_x",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	manager := NewSessionManager()
	manager.AddSession(&Session{
		ID:         "sess_live",
		QueenName:  "queen_x",
		LoadedAt:   1,
		EventBus:   bus,
		PhaseState: phase,
		Chat:       runtime,
	})
	t.Cleanup(func() { _ = runtime.Close() })

	identityEvents := make(chan *eventbus.AgentEvent, 4)
	bus.Subscribe([]eventbus.EventType{eventbus.EventQueenIdentitySelected}, func(event *eventbus.AgentEvent) {
		identityEvents <- event
	})

	app := newAppForTest(t, AppDeps{Home: home, Manager: manager})
	recorder, body := doWiringJSON(t, app, http.MethodPut, "/api/queen/queen_x/persona", `{"persona":{"name":"Fox II"}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	persona, _ := body["persona"].(map[string]any)
	if persona["name"] != "Fox II" {
		t.Fatalf("body = %#v", body)
	}
	if got, _ := phase.QueenProfile()["name"].(string); got != "Fox II" {
		t.Fatalf("phase profile name = %q, want Fox II", got)
	}
	if prompt := runtime.conv.SystemPrompt(); !strings.Contains(prompt, "your name is Fox II") {
		t.Fatalf("live system prompt kept the old identity: %s", prompt)
	}

	select {
	case event := <-identityEvents:
		if event.Data["queen_id"] != "queen_x" || event.Data["name"] != "Fox II" {
			t.Fatalf("identity event = %#v", event.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no QUEEN_IDENTITY_SELECTED event was published")
	}
}

// sentinelWiringNotifier is the manager's channel-token source. The token is
// empty at manager start and set by the test once the app saved the config,
// which is the two-way case the refresh hook exists for.
type sentinelWiringNotifier struct {
	mu       sync.Mutex
	telegram string
}

func (n *sentinelWiringNotifier) Send(context.Context, string, map[string]any, string, map[string]any, map[string]any) sentinel.NotifierResult {
	return sentinel.NotifierResult{OK: true}
}

func (n *sentinelWiringNotifier) TelegramToken() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.telegram
}

func (n *sentinelWiringNotifier) SlackAppToken() string { return "" }

func (n *sentinelWiringNotifier) HiveConnected() bool { return false }

func (n *sentinelWiringNotifier) setTelegram(token string) {
	n.mu.Lock()
	n.telegram = token
	n.mu.Unlock()
}

type sentinelWiringListener struct {
	started chan<- string
}

func (l sentinelWiringListener) Run(ctx context.Context) error {
	l.started <- "telegram"
	<-ctx.Done()
	return nil
}

// Parity: framework/server/routes_config.py::handle_update_sentinel_config
//
// The global sentinel block is the second writer that can connect a channel.
// Python refreshes the manager after that save too, so a token connected while
// the server was already running starts listening without a restart; the app
// assembly must supply the same production hook as the per-colony route.
func TestGlobalSentinelConfigSaveRefreshesListeners(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}

	notifier := &sentinelWiringNotifier{}
	started := make(chan string, 4)
	manager := sentinel.NewManager(sentinel.ManagerOptions{
		Store:    sentinel.NewStore(home.Root),
		Notifier: notifier,
		ListenerFactory: func(name string, _ *sentinel.Manager) sentinel.Listener {
			return sentinelWiringListener{started: started}
		},
	})
	previous := sentinel.GetSentinelManager()
	sentinel.SetSentinelManager(manager)
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(previous)
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("sentinel start: %v", err)
	}
	notifier.setTelegram("test-token")

	app := newAppForTest(t, AppDeps{Home: home})
	recorder, body := doWiringJSON(t, app, http.MethodPut, "/api/config/sentinel",
		`{"sentinel":{"classify_after_seconds":30}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	block, _ := body["sentinel"].(map[string]any)
	if block["classify_after_seconds"] != float64(30) {
		t.Fatalf("body = %#v", body)
	}
	select {
	case name := <-started:
		if name != "telegram" {
			t.Fatalf("listener started for %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("global sentinel config save did not start the telegram listener (RefreshListeners not wired)")
	}
}

// Parity: framework/server/routes_sentinel.py::handle_put_notifications
//
// Saving a notification config must re-evaluate the inbound listeners: a
// channel whose token appeared after the manager started begins listening
// without a server restart.
func TestSentinelNotificationSaveRefreshesListeners(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colonyDir := filepath.Join(home.ColoniesDir(), "c1")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if err := os.WriteFile(filepath.Join(colonyDir, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	notifier := &sentinelWiringNotifier{}
	started := make(chan string, 4)
	manager := sentinel.NewManager(sentinel.ManagerOptions{
		Store:    sentinel.NewStore(home.Root),
		Notifier: notifier,
		ListenerFactory: func(name string, _ *sentinel.Manager) sentinel.Listener {
			return sentinelWiringListener{started: started}
		},
	})
	previous := sentinel.GetSentinelManager()
	sentinel.SetSentinelManager(manager)
	t.Cleanup(func() {
		manager.Stop()
		sentinel.SetSentinelManager(previous)
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("sentinel start: %v", err)
	}
	notifier.setTelegram("test-token")

	app := newAppForTest(t, AppDeps{Home: home})
	recorder, body := doWiringJSON(t, app, http.MethodPut, "/api/colony/c1/notifications",
		`{"sentinel_enabled":true,"channel":"telegram","target":{"chat_id":"1"},"allowlist":[]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["channel"] != "telegram" {
		t.Fatalf("body = %#v", body)
	}
	select {
	case name := <-started:
		if name != "telegram" {
			t.Fatalf("listener started for %q", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification save did not start the telegram listener (RefreshListeners not wired)")
	}
}
