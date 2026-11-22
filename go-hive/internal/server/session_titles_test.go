package server

// Parity: tests/test_session_titles.py

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type titleFakeLLM struct {
	mu    sync.Mutex
	calls []llm.CompletionRequest
	reply string
	err   error
}

func (f *titleFakeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.err != nil {
		return llm.Completion{}, f.err
	}
	return llm.Completion{Content: f.reply}, nil
}

func (f *titleFakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *titleFakeLLM) request(i int) llm.CompletionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[i]
}

func newTitleSessionDir(t *testing.T, firstMessage string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "session_20261017_090000_aaaa1111")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"queen_id": "queen_x"}`), 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
	line, err := json.Marshal(map[string]any{
		"type":      "client_input_received",
		"stream_id": "queen",
		"data":      map[string]any{"content": firstMessage},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	return dir
}

func finishTitleTurn(t *testing.T, bus *eventbus.EventBus, trigger *TitleTrigger, stopReason, reply string) {
	t.Helper()
	out := eventbus.NewAgentEvent(eventbus.EventClientOutputDelta, "queen")
	out.Data = map[string]any{"snapshot": reply}
	bus.Publish(out)

	done := eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, "queen")
	done.Data = map[string]any{"stop_reason": stopReason}
	bus.Publish(done)

	trigger.Wait()
}

func readTitleSource(t *testing.T, dir string) any {
	t.Helper()
	meta := readJSONObjectOr(filepath.Join(dir, "meta.json"), map[string]any{})
	return meta["title_source"]
}

// Parity: tests/test_session_titles.py::test_clean_title
func TestCleanTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "strips_quotes_and_trailing_period",
			raw:  `"Launch plan for Notebook."`,
			want: "Launch plan for Notebook",
		},
		{
			name: "drops_think_block_and_title_label",
			raw:  "<think>pondering</think>\nTitle: 周末旅行计划。",
			want: "周末旅行计划",
		},
		{
			name: "strips_markdown_and_takes_first_line",
			raw:  "## **Pricing test review**\nextra line",
			want: "Pricing test review",
		},
		{
			name: "cuts_overlong_titles_with_ellipsis",
			raw:  strings.Repeat("x", 80),
			want: strings.Repeat("x", 59) + "…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanTitle(tc.raw); got != tc.want {
				t.Errorf("CleanTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// Parity: tests/test_session_titles.py::test_a_finished_turn_titles_the_session_and_announces_it
func TestFinishedTurnTitlesSessionAndAnnouncesIt(t *testing.T) {
	bus := eventbus.NewEventBus()
	llmFake := &titleFakeLLM{reply: "Kyoto weekend plan."}
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")

	var mu sync.Mutex
	var announced []map[string]any
	bus.Subscribe([]eventbus.EventType{eventbus.EventSessionTitleUpdated}, func(event *eventbus.AgentEvent) {
		mu.Lock()
		announced = append(announced, event.Data)
		mu.Unlock()
	})

	trigger := SubscribeTitleTrigger(bus, dir, llmFake)
	defer trigger.Close()

	finishTitleTurn(t, bus, trigger, "stop", "Here is a two-day plan.")

	title, ok := ReadTitle(dir)
	if !ok || title != "Kyoto weekend plan" {
		t.Fatalf("ReadTitle = (%q, %v), want (\"Kyoto weekend plan\", true)", title, ok)
	}
	if source := readTitleSource(t, dir); source != "auto" {
		t.Errorf("title_source = %v, want auto", source)
	}

	mu.Lock()
	gotAnnounced := announced
	mu.Unlock()
	wantAnnounced := []map[string]any{{
		"session_id": filepath.Base(dir),
		"title":      "Kyoto weekend plan",
	}}
	if !reflect.DeepEqual(gotAnnounced, wantAnnounced) {
		t.Errorf("announced = %v, want %v", gotAnnounced, wantAnnounced)
	}

	if got := llmFake.callCount(); got != 1 {
		t.Fatalf("llm calls = %d, want 1", got)
	}
	req := llmFake.request(0)
	if len(req.Messages) != 1 {
		t.Fatalf("request messages = %d, want 1", len(req.Messages))
	}
	if content := req.Messages[0].Content; !strings.Contains(content, "Plan a weekend in Kyoto") {
		t.Errorf("namer prompt %q does not contain the first user message", content)
	}
	if !strings.Contains(req.Messages[0].Content, "Here is a two-day plan.") {
		t.Errorf("namer prompt %q does not contain the latest reply", req.Messages[0].Content)
	}
	if req.System != titleSystemPrompt {
		t.Errorf("namer system prompt = %q, want the upstream instruction", req.System)
	}
	if req.MaxTokens != TitleTokenBudget() {
		t.Errorf("namer max_tokens = %d, want %d", req.MaxTokens, TitleTokenBudget())
	}
}

// Parity: tests/test_session_titles.py::test_a_turn_that_only_calls_tools_does_not_title
func TestTurnThatOnlyCallsToolsDoesNotTitle(t *testing.T) {
	bus := eventbus.NewEventBus()
	llmFake := &titleFakeLLM{reply: "Kyoto weekend plan."}
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")

	trigger := SubscribeTitleTrigger(bus, dir, llmFake)
	defer trigger.Close()

	finishTitleTurn(t, bus, trigger, "tool_calls", "Here is a two-day plan.")

	if got := llmFake.callCount(); got != 0 {
		t.Errorf("llm calls = %d, want 0", got)
	}
	if title, ok := ReadTitle(dir); ok {
		t.Errorf("ReadTitle = (%q, %v), want no title", title, ok)
	}
}

// Parity: tests/test_session_titles.py::test_a_title_the_user_set_is_never_replaced
func TestTitleTheUserSetIsNeverReplaced(t *testing.T) {
	bus := eventbus.NewEventBus()
	llmFake := &titleFakeLLM{reply: "Kyoto weekend plan."}
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")

	if stored, err := WriteTitle(dir, "My Kyoto trip", "user"); err != nil || stored != "My Kyoto trip" {
		t.Fatalf("WriteTitle(user) = (%q, %v), want (\"My Kyoto trip\", nil)", stored, err)
	}

	trigger := SubscribeTitleTrigger(bus, dir, llmFake)
	defer trigger.Close()

	finishTitleTurn(t, bus, trigger, "stop", "Here is a two-day plan.")

	if got := llmFake.callCount(); got != 0 {
		t.Errorf("llm calls = %d, want 0 (a user title blocks naming)", got)
	}
	if title, ok := ReadTitle(dir); !ok || title != "My Kyoto trip" {
		t.Errorf("ReadTitle = (%q, %v), want (\"My Kyoto trip\", true)", title, ok)
	}
	if source := readTitleSource(t, dir); source != "user" {
		t.Errorf("title_source = %v, want user", source)
	}
}

// Parity: tests/test_session_titles.py::test_title_route_sets_and_clears_a_users_title
func TestWriteTitleSetsAndClearsTitle(t *testing.T) {
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")

	stored, err := WriteTitle(dir, "  Kyoto trip  ", "user")
	if err != nil {
		t.Fatalf("WriteTitle(set) error: %v", err)
	}
	if stored != "Kyoto trip" {
		t.Errorf("WriteTitle(set) = %q, want %q", stored, "Kyoto trip")
	}
	if title, ok := ReadTitle(dir); !ok || title != "Kyoto trip" {
		t.Errorf("ReadTitle = (%q, %v), want (\"Kyoto trip\", true)", title, ok)
	}

	stored, err = WriteTitle(dir, "", "user")
	if err != nil {
		t.Fatalf("WriteTitle(clear) error: %v", err)
	}
	if stored != "" {
		t.Errorf("WriteTitle(clear) = %q, want empty", stored)
	}
	if title, ok := ReadTitle(dir); ok {
		t.Errorf("ReadTitle after clear = (%q, %v), want no title", title, ok)
	}
	meta := readJSONObjectOr(filepath.Join(dir, "meta.json"), map[string]any{})
	if _, present := meta["title"]; present {
		t.Error("meta.json still has a title key after clearing")
	}
	if _, present := meta["title_source"]; present {
		t.Error("meta.json still has a title_source key after clearing")
	}
	if meta["queen_id"] != "queen_x" {
		t.Errorf("clearing dropped the other meta keys: %v", meta)
	}
}

// Parity-exempt: extra clean_title cases for Python's Unicode-aware \s and
func TestCleanTitlePythonWhitespaceAndLineBoundaries(t *testing.T) {
	if got, want := CleanTitle("\u00a0\u3000Title: 旅行\u3000计划 。\u00a0"), "旅行 计划"; got != want {
		t.Errorf("Unicode whitespace: CleanTitle = %q, want %q", got, want)
	}
	if got, want := CleanTitle("first\u2028second"), "first"; got != want {
		t.Errorf("U+2028 line separator: CleanTitle = %q, want %q", got, want)
	}
	if got, want := CleanTitle("a\u0085b"), "a"; got != want {
		t.Errorf("NEL line separator: CleanTitle = %q, want %q", got, want)
	}
	if got, want := CleanTitle("\r\n\r\n  Real title  \r\n"), "Real title"; got != want {
		t.Errorf("CRLF blank lines: CleanTitle = %q, want %q", got, want)
	}
}

// Parity-exempt: a failed naming attempt must not leave a title and must be
func TestFailedNamingLeavesNoTitleAndRetries(t *testing.T) {
	bus := eventbus.NewEventBus()
	llmFake := &titleFakeLLM{err: context.DeadlineExceeded}
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")

	trigger := SubscribeTitleTrigger(bus, dir, llmFake)
	defer trigger.Close()

	finishTitleTurn(t, bus, trigger, "stop", "Here is a two-day plan.")
	if title, ok := ReadTitle(dir); ok {
		t.Fatalf("ReadTitle after failure = (%q, %v), want no title", title, ok)
	}

	llmFake.mu.Lock()
	llmFake.err = nil
	llmFake.reply = "Kyoto weekend plan."
	llmFake.mu.Unlock()

	finishTitleTurn(t, bus, trigger, "stop", "Here is a two-day plan.")
	if title, ok := ReadTitle(dir); !ok || title != "Kyoto weekend plan" {
		t.Errorf("retry ReadTitle = (%q, %v), want (\"Kyoto weekend plan\", true)", title, ok)
	}
	if got := llmFake.callCount(); got != 2 {
		t.Errorf("llm calls = %d, want 2 (failure then retry)", got)
	}
}

// Parity-exempt: upstream's session_titles.generate_title sends
// get_aux_max_tokens() as the namer's max_tokens; no upstream test pins that
// the configured llm.aux_max_tokens reaches the namer, so this test covers the
// wiring directly (llm.SetAuxMaxTokens is what serve installs at startup).
func TestTitleBudgetFollowsConfiguredAuxMaxTokens(t *testing.T) {
	previous := llm.AuxMaxTokens()
	llm.SetAuxMaxTokens(1234)
	defer llm.SetAuxMaxTokens(previous)

	if got := TitleTokenBudget(); got != 1234 {
		t.Fatalf("TitleTokenBudget = %d, want the configured aux budget 1234", got)
	}
	fake := &titleFakeLLM{reply: "Kyoto weekend plan."}
	if _, err := GenerateTitle(context.Background(), fake, "Plan a weekend in Kyoto", "Here is a two-day plan."); err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if req := fake.request(0); req.MaxTokens != 1234 {
		t.Fatalf("namer max_tokens = %d, want 1234", req.MaxTokens)
	}
}

// TestTitleTriggerLLMTakesTheWorkerSlotWhenConfigured pins the model the
// auto-title trigger is handed. Upstream resolves `_title_llm` once when the
// trigger is subscribed — (session_manager.build_worker_llm() or session.llm)
// — so a configured worker_llm names sessions on the worker model, and without
// one the session's own model stands in. The choice is a value read at
// subscribe time: a later worker_llm write reaches only sessions started
// afterwards.
//
// Parity: tests/test_session_titles.py::test_a_finished_turn_titles_the_session_and_announces_it
// Parity: framework/server/queen_orchestrator.py::_title_llm
func TestTitleTriggerLLMTakesTheWorkerSlotWhenConfigured(t *testing.T) {
	home := newFeatureHome(t)
	sessionLLM := &titleFakeLLM{reply: "Kyoto weekend plan."}
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-main", "api_key": "main-key"},
	})

	// No worker_llm: build_worker_llm returns None and the session's own model
	// is what the trigger must name the session with.
	if got := TitleTriggerLLM(home, sessionLLM); got != llm.Completer(sessionLLM) {
		t.Fatalf("TitleTriggerLLM without worker_llm = %T, want the session's own completer", got)
	}
	bus := eventbus.NewEventBus()
	dir := newTitleSessionDir(t, "Plan a weekend in Kyoto")
	trigger := SubscribeTitleTrigger(bus, dir, TitleTriggerLLM(home, sessionLLM))
	finishTitleTurn(t, bus, trigger, "stop", "Here is a two-day plan.")
	trigger.Close()
	if title, ok := ReadTitle(dir); !ok || title != "Kyoto weekend plan" {
		t.Fatalf("ReadTitle = (%q, %v), want the fallback model to name the session", title, ok)
	}

	// worker_llm configured: the worker slot's provider, not the session's.
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-main", "api_key": "main-key"},
		"worker_llm": map[string]any{
			"provider": "groq",
			"model":    "llama-worker",
			"api_base": "https://worker.example/v1",
		},
	})
	worker := TitleTriggerLLM(home, sessionLLM)
	if worker == llm.Completer(sessionLLM) {
		t.Fatal("TitleTriggerLLM returned the session completer with worker_llm configured")
	}
	provider, ok := llm.ResolveCompleter(worker).(*llm.Provider)
	if !ok {
		t.Fatalf("worker title model type = %T, want *llm.Provider", llm.ResolveCompleter(worker))
	}
	if provider.ModelName() != "groq/llama-worker" || provider.APIBase != "https://worker.example/v1" {
		t.Fatalf("worker title model = %q @ %q, want groq/llama-worker @ the configured base", provider.ModelName(), provider.APIBase)
	}

	// Read once at subscribe time: a later worker_llm write resolves a new
	// provider for the NEXT session, while the one already built is unchanged.
	writeFeatureConfig(t, home, map[string]any{
		"llm":        map[string]any{"provider": "anthropic", "model": "claude-main", "api_key": "main-key"},
		"worker_llm": map[string]any{"provider": "groq", "model": "llama-worker-next"},
	})
	next, ok := llm.ResolveCompleter(TitleTriggerLLM(home, sessionLLM)).(*llm.Provider)
	if !ok || next.ModelName() != "groq/llama-worker-next" {
		t.Fatalf("next session's title model = %v, want the new worker slot", next)
	}
	if provider.ModelName() != "groq/llama-worker" {
		t.Fatalf("already-built title model moved to %q after the write", provider.ModelName())
	}
}
