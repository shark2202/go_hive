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

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
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
