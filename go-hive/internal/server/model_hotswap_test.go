package server

// Model-slot hot switching: a PUT rebuilds the session's provider in place,
// the running turn keeps the provider it started with, and the next turn runs
// on the rebuilt one. Covers the shared walkers (queen route, config routes,
// Models page) and the worker_llm write staying new-session-only, matching
// upstream's _write_role_section / _write_slot behaviour.
//
// Parity: framework/server/routes_config.py::_hot_swap_sessions,
// framework/server/queen_models.py::apply

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// gatedCompleter blocks each completion until release is closed and records
// how many calls reached it, so a test can hold a turn mid-flight while a
// model-slot PUT lands.
type gatedCompleter struct {
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func newGatedCompleter(name string) *gatedCompleter {
	return &gatedCompleter{name: name, started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (g *gatedCompleter) ModelName() string { return g.name }

func (g *gatedCompleter) AComplete(ctx context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
	}
	return llm.Completion{Content: "reply from " + g.name, Model: g.name, StopReason: "end_turn"}, nil
}

func (g *gatedCompleter) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *gatedCompleter) releaseNow() {
	g.once.Do(func() { close(g.release) })
}

func waitForGatedCall(t *testing.T, started chan struct{}, label string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for a completion on the %s", label)
	}
}

// TestAModelSlotSwapPinsTheRunningTurnAndTheNextTurnUsesIt is the core
// session-level hot-switch contract: the in-flight turn finishes on the
// provider it started with (no tearing), and the first turn started after the
// PUT runs on the rebuilt provider.
// Parity-exempt: 上游 reconfigure 原地改共享 LiteLLMProvider，没有「进行中回合
// pin」语义；本测试锁定 Go 的分层实现（回合级 pin + 下一回合解析新 provider）。
func TestAModelSlotSwapPinsTheRunningTurnAndTheNextTurnUsesIt(t *testing.T) {
	first := newGatedCompleter("gated-first")
	second := newGatedCompleter("gated-second")
	slot := llm.NewProviderSlot(first, func(fullModel string, apiKey, apiBase *string, extraBody any) llm.Completer {
		return second
	})

	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: slot, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() {
		first.releaseNow()
		second.releaseNow()
		_ = runtime.Close()
	})

	if err := runtime.Deliver("first message"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForGatedCall(t, first.started, "original provider")

	// The PUT lands while the turn is in flight.
	slot.Reconfigure("openai/rebuilt", nil, nil, nil)

	// The turn keeps its pin: nothing re-pointed the loop mid-turn.
	if runtime.pinnedLLM != llm.Completer(first) {
		t.Fatalf("in-flight turn was re-pinned away from its provider")
	}

	first.releaseNow()
	parts := waitForAssistantParts(t, runtime, 1)
	if got := parts[len(parts)-1]["content"]; got != "reply from gated-first" {
		t.Fatalf("first turn reply = %#v, want the pinned provider's reply", got)
	}

	// The next turn resolves the rebuilt provider.
	if err := runtime.Deliver("second message"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForGatedCall(t, second.started, "rebuilt provider")
	second.releaseNow()
	waitForAssistantParts(t, runtime, 2)

	if first.callCount() != 1 || second.callCount() != 1 {
		t.Fatalf("calls: original=%d rebuilt=%d, want 1/1", first.callCount(), second.callCount())
	}
	if runtime.model != "gated-second" {
		t.Fatalf("runtime model = %q, want the rebuilt provider's model", runtime.model)
	}
}

// TestConcurrentModelSlotWritesCannotTearARunningTurn holds a turn in flight
// while several writers hammer the slot, then completes the turn and proves
// the next one runs on the last rebuilt provider.
// Parity-exempt: Go 侧并发写要求（上游单事件循环，无并发 PUT/回合竞态）；
// 本测试在 -race 下多写者重建 + 回合进行中，锁定不撕裂与下一次生效。
func TestConcurrentModelSlotWritesCannotTearARunningTurn(t *testing.T) {
	first := newGatedCompleter("gated-first")
	rebuilt := &fakeChatLLM{}
	slot := llm.NewProviderSlot(first, func(fullModel string, apiKey, apiBase *string, extraBody any) llm.Completer {
		return rebuilt
	})

	runtime, err := NewChatRuntime(ChatRuntimeDeps{LLM: slot, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() {
		first.releaseNow()
		_ = runtime.Close()
	})

	if err := runtime.Deliver("first message"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForGatedCall(t, first.started, "original provider")

	var writers sync.WaitGroup
	for writer := 0; writer < 3; writer++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 30; i++ {
				slot.Reconfigure("openai/rebuilt", nil, nil, nil)
			}
		}()
	}
	writers.Wait()

	first.releaseNow()
	parts := waitForAssistantParts(t, runtime, 1)
	if got := parts[len(parts)-1]["content"]; got != "reply from gated-first" {
		t.Fatalf("torn turn: reply = %#v", got)
	}

	if err := runtime.Deliver("second message"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 2)
	if rebuilt.requestCount() == 0 {
		t.Fatal("the next turn never reached the rebuilt provider")
	}
}

// TestAQueenModelPutRebuildsHerRunningSessionsProvider covers PUT
// /api/queen/{id}/llm through its production default (no ApplyQueenModel
// hook): her sessions are switched in place and clearing her section returns
// them to the main model.
// Parity: core/framework/server/tests/test_queen_models.py::test_the_dashboard_sets_and_clears_her_model_and_her_sessions_follow
func TestAQueenModelPutRebuildsHerRunningSessionsProvider(t *testing.T) {
	home := queenTestHome(t)
	queenTestWriteProfile(t, home, "queen_growth", map[string]any{"name": "Growth", "title": "Head of Growth"})
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-x", "api_key": "sk-main"},
	})

	live := llm.NewHTTPProvider(llm.ProviderOptions{Model: "openai/queen-old", APIKey: "sk-old", APIBase: "https://old.example/v1"})
	slot := llm.NewProviderSlot(live, llm.HTTPProviderFactory)
	colonyLLM := llm.NewProviderSlot(llm.NewHTTPProvider(llm.ProviderOptions{Model: "openai/queen-old", APIKey: "sk-old"}), llm.HTTPProviderFactory)
	manager := NewSessionManager()
	manager.AddSession(&Session{
		ID: "g1", QueenName: "queen_growth", LLM: slot,
		Colony: &fakeWorkerSlotColony{colony: colonyLLM},
	})
	manager.AddSession(&Session{ID: "s1", QueenName: "queen_sales", LLM: "someone-else"})

	mux := queenTestMux(home, manager, nil)
	own := map[string]any{"provider": "openai", "model": "queen-new", "api_base": "https://new.example/v1", "api_key": "sk-new"}
	recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_growth/llm", `{"section":`+mustJSONString(t, own)+`}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["sessions_switched"] != float64(1) {
		t.Fatalf("sessions_switched = %#v, want 1", body["sessions_switched"])
	}
	rebuilt, ok := llm.ResolveCompleter(slot).(*llm.Provider)
	if !ok {
		t.Fatalf("session provider type = %T", llm.ResolveCompleter(slot))
	}
	if rebuilt.Model != "openai/queen-new" || rebuilt.APIKey != "sk-new" || rebuilt.APIBase != "https://new.example/v1" {
		t.Fatalf("her session still runs on %#v", rebuilt)
	}
	// A colony-bound session executes through the colony's overseer, whose
	// transport is separate in Go: it must follow her model too.
	colonyRebuilt, _ := llm.ResolveCompleter(colonyLLM).(*llm.Provider)
	if colonyRebuilt.Model != "openai/queen-new" || colonyRebuilt.APIBase != "https://new.example/v1" {
		t.Fatalf("her colony's overseer still runs on %#v", colonyRebuilt)
	}

	// Clearing her section puts her running sessions back on the main model.
	_, cleared := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_growth/llm", `{"section":null}`)
	if cleared["sessions_switched"] != float64(1) {
		t.Fatalf("cleared sessions_switched = %#v, want 1", cleared["sessions_switched"])
	}
	back, _ := llm.ResolveCompleter(slot).(*llm.Provider)
	if back.Model != "anthropic/claude-x" || back.APIKey != "sk-main" {
		t.Fatalf("after clearing she runs on %#v, want the main model", back)
	}
	if backColony, _ := llm.ResolveCompleter(colonyLLM).(*llm.Provider); backColony.Model != "anthropic/claude-x" {
		t.Fatalf("after clearing her colony runs on %#v, want the main model", backColony)
	}
}

// TestTheConfigSlotRoutesHotSwapTheMainSlotOnly pins the config-route
// behaviour against upstream's _write_role_section: a role=llm write swaps
// running sessions, while worker_llm (and vision_fallback) writes take effect
// for what is built next and leave running sessions alone.
// Parity: core/tests/test_routes_provider_library.py::test_apply_to_llm_slot_hot_swaps_sessions
// Parity: core/framework/server/tests/test_queen_models.py::test_changing_the_main_model_leaves_a_queen_with_her_own
func TestTheConfigSlotRoutesHotSwapTheMainSlotOnly(t *testing.T) {
	home := queenTestHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-x", "api_key": "sk-main"},
		"queen_llm": map[string]any{
			"queen_growth": map[string]any{"provider": "openai", "model": "queen-model", "api_key": "sk-queen"},
		},
	})

	plain := llm.NewProviderSlot(llm.NewHTTPProvider(llm.ProviderOptions{Model: "anthropic/claude-x", APIKey: "sk-main"}), llm.HTTPProviderFactory)
	queensOwn := llm.NewProviderSlot(llm.NewHTTPProvider(llm.ProviderOptions{Model: "openai/queen-model", APIKey: "sk-queen"}), llm.HTTPProviderFactory)
	workerSlot := llm.NewProviderSlot(llm.NewHTTPProvider(llm.ProviderOptions{Model: "groq/worker-old"}), llm.HTTPProviderFactory)
	colony := &fakeWorkerSlotColony{worker: workerSlot}

	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "s1", LLM: plain, Colony: colony})
	manager.AddSession(&Session{ID: "g1", QueenName: "queen_growth", LLM: queensOwn})

	app := NewApp(AppDeps{Manager: manager, Home: home, LiveSessions: manager.ListSessions})
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	section := map[string]any{"provider": "minimax", "model": "main-new", "api_base": "https://new.example/v1", "api_key": "sk-new"}
	status, body := putJSON(t, server.URL+"/api/config/llm-sections", map[string]any{"role": "llm", "section": section})
	// Two providers rotate, exactly as upstream's _hot_swap_sessions counts
	// them: the session's own and its colony's worker transport.
	if status != http.StatusOK || body["sessions_swapped"] != float64(2) {
		t.Fatalf("role=llm put = %d/%#v, want 200 and the session + colony swapped", status, body)
	}
	rebuilt, _ := llm.ResolveCompleter(plain).(*llm.Provider)
	if rebuilt.Model != "minimax/main-new" || rebuilt.APIBase != "https://new.example/v1" {
		t.Fatalf("main session provider = %#v", rebuilt)
	}
	if colonyCurrent, _ := llm.ResolveCompleter(colony.worker).(*llm.Provider); colonyCurrent.Model != "minimax/main-new" {
		t.Fatalf("colony worker transport = %#v, want the new main model", colonyCurrent)
	}
	if queensOwnCurrent, _ := llm.ResolveCompleter(queensOwn).(*llm.Provider); queensOwnCurrent.Model != "openai/queen-model" {
		t.Fatalf("the queen with her own model was rewritten: %#v", queensOwnCurrent)
	}

	// worker_llm: written, but no running session is touched (upstream swaps
	// only role=llm); the colony worker slot keeps its provider until the next
	// colony host is built.
	status, body = putJSON(t, server.URL+"/api/config/llm-sections", map[string]any{
		"role":    "worker_llm",
		"section": map[string]any{"provider": "groq", "model": "worker-new", "api_base": "https://worker.example/v1"},
	})
	if status != http.StatusOK || body["sessions_swapped"] != float64(0) {
		t.Fatalf("worker_llm put = %d/%#v, want 200 and no swapped sessions", status, body)
	}
	if current, _ := llm.ResolveCompleter(colony.worker).(*llm.Provider); current.Model != "minimax/main-new" {
		t.Fatalf("running worker slot was rewritten by a worker_llm write: %#v", current)
	}
	config := readFeatureConfig(t, home)
	if config["worker_llm"] == nil {
		t.Fatalf("worker_llm was not written: %#v", config)
	}
}

// fakeWorkerSlotColony is the session.colony seam the hot-swap walkers probe:
// it exposes the colony's own transport and its worker transport.
type fakeWorkerSlotColony struct {
	worker *llm.ProviderSlot
	colony *llm.ProviderSlot
}

func (f *fakeWorkerSlotColony) StreamID() string { return "colony_test" }

func (f *fakeWorkerSlotColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (f *fakeWorkerSlotColony) InjectInput(string, string) error { return nil }

func (f *fakeWorkerSlotColony) WorkerLLM() any { return f.worker }

func (f *fakeWorkerSlotColony) LLM() any { return f.colony }

func putJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	_ = json.NewDecoder(response.Body).Decode(&decoded)
	return response.StatusCode, decoded
}
