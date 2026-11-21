package server

// Flat lazy tool surface over the live chat runtime.
//
// These tests pin the Python Tool Library parity contract end to end:
//   - the first model request advertises exactly the 24 always-on names, in
//     the captured upstream order;
//   - the deferred layer is announced by name only (searchable-tools
//     reminder) and absent from tools[] until search_tools loads it;
//   - a search_tools load reaches the NEXT model request;
//   - a loaded tool executes through the ordinary executor path;
//   - a colony binding restores the colony-phase eager surface.
//
// Parity: framework/tools/tool_tiers.py (eager/searchable split)
// Parity: framework/agents/queen/queen_tools_defaults.py::always_enabled_tool_names
// Parity: framework/agent_loop/agent_loop.py::_refresh_dynamic_tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// lazyAlwaysOn mirrors the captured Python first-request order (the same list
// internal/cli pins against the frozen manifest).
var lazyAlwaysOn = []string{
	"search_tools", "write_skill", "task_create", "task_update", "task_list", "task_get",
	"suggest_colony", "pdf_read", "attach_file", "get_current_time", "image_generate",
	"get_account_info", "terminal_exec", "terminal_job_logs", "terminal_job_manage",
	"terminal_rg", "terminal_glob", "terminal_output_get", "search_messages",
	"browser_setup", "collect_result", "ask_user", "credentials", "sentinel_setup",
}

func lazyTestDef(name string) llm.ToolDef {
	return llm.ToolDef{
		Name:        name,
		Description: name + " test tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// lazySourceDefs is the pre-colony turn surface: the 24 always-on names plus
// the 11 lifecycle tools that carry no colony-phase gate.
func lazySourceDefs() []llm.ToolDef {
	names := append([]string(nil), lazyAlwaysOn...)
	names = append(names,
		"get_playbook_status", "list_browser_profiles", "list_playbook_runs",
		"list_triggers", "list_worker_questions", "remove_trigger", "reply_to_worker",
		"run_playbook", "set_trigger", "stop_playbook", "update_worker_profile",
	)
	out := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		out = append(out, lazyTestDef(name))
	}
	return out
}

// lazyColonyDefs are the four names the runtime filters out until a colony is
// bound (server.ChatRuntime.toolsForTurn).
func lazyColonyDefs() []llm.ToolDef {
	return []llm.ToolDef{
		lazyTestDef("get_worker_status"),
		lazyTestDef("inject_message"),
		lazyTestDef("run_worker"),
		lazyTestDef("stop_worker"),
	}
}

// lazyDeferredDefs is the loadable layer of the runtime tests: the four
// colony-gated names plus two flat directory tools.
func lazyDeferredDefs() []llm.ToolDef {
	out := lazyColonyDefs()
	out = append(out, lazyTestDef("chart_render"), lazyTestDef("read_file"))
	return out
}

func toolNamesOf(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

func hasToolName(defs []llm.ToolDef, want string) bool {
	for _, def := range defs {
		if def.Name == want {
			return true
		}
	}
	return false
}

func newLazyChatRuntime(t *testing.T, model llm.Completer, execute func(context.Context, string, map[string]any) (string, bool, error)) (*ChatRuntime, *chatEventRecorder) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:               model,
		Tools:             append(lazySourceDefs(), lazyColonyDefs()...),
		AlwaysOnToolNames: lazyAlwaysOn,
		DeferredTools:     lazyDeferredDefs(),
		Execute:           execute,
		Root:              t.TempDir(),
		Bus:               bus,
		SessionID:         "session_20260101_000000_lazy0001",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recordChatEvents(bus)
}

// TestLazyChatFirstRequestAdvertisesPythonEagerOrder is the primary parity
// assertion: a default session's first LLM request carries exactly the 24
// Python always-on names, in the captured order, and none of the deferred
// layer. The deferred names are announced in the searchable-tools reminder
// instead.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._refresh_dynamic_tools
func TestLazyChatFirstRequestAdvertisesPythonEagerOrder(t *testing.T) {
	model := &queenChatLLM{}
	runtime, recorder := newLazyChatRuntime(t, model, func(context.Context, string, map[string]any) (string, bool, error) {
		return "ok", false, nil
	})
	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("model never received a completion request")
	}
	first := toolNamesOf(requests[0].Tools)
	if len(first) != 24 {
		t.Fatalf("first request tools = %d %v, want 24 in Python order", len(first), first)
	}
	if strings.Join(first, ",") != strings.Join(lazyAlwaysOn, ",") {
		t.Fatalf("first request tool order mismatch:\n got %v\nwant %v", first, lazyAlwaysOn)
	}
	for _, deferred := range []string{"run_playbook", "stop_worker", "chart_render", "read_file"} {
		if hasToolName(requests[0].Tools, deferred) {
			t.Fatalf("deferred tool %q leaked into the first request", deferred)
		}
	}

	// The deferred names travel as the searchable-tools reminder (Python's
	// manifest announcement) so the model can discover and load them.
	var announced strings.Builder
	for _, message := range requests[0].Messages {
		if strings.Contains(message.Content, "schemas are NOT loaded") {
			announced.WriteString(message.Content)
		}
	}
	body := announced.String()
	if body == "" {
		t.Fatal("first request carries no searchable-tools announcement")
	}
	for _, name := range []string{"run_playbook", "chart_render", "read_file"} {
		if !strings.Contains(body, name) {
			t.Fatalf("searchable-tools announcement is missing %q: %s", name, body)
		}
	}
}

// TestLazyChatSearchLoadsDeferredToolForNextStep drives one scripted turn:
// search_tools(select:chart_render) → the next request advertises
// chart_render → the model calls it → the executor runs it.
//
// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_select_promotes_and_reports
func TestLazyChatSearchLoadsDeferredToolForNextStep(t *testing.T) {
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_tools", Arguments: `{"query":"select:chart_render"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "chart_render", Arguments: `{}`}}},
		{Content: "drawn"},
	}}
	var mu sync.Mutex
	var executed []string
	runtime, recorder := newLazyChatRuntime(t, model, func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
		mu.Lock()
		executed = append(executed, name)
		mu.Unlock()
		return `{"status":"rendered"}`, false, nil
	})
	if err := runtime.Deliver("draw a chart"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) < 2 {
		t.Fatalf("model requests = %d, want at least 2 (search_tools call + follow-up)", len(requests))
	}
	if hasToolName(requests[0].Tools, "chart_render") {
		t.Fatal("chart_render appeared before search_tools loaded it")
	}
	if !hasToolName(requests[1].Tools, "chart_render") {
		t.Fatalf("chart_render missing from the request after search_tools: %v", toolNamesOf(requests[1].Tools))
	}
	// The eager prefix is unchanged; the loaded tool appends behind it.
	second := toolNamesOf(requests[1].Tools)
	if strings.Join(second[:len(lazyAlwaysOn)], ",") != strings.Join(lazyAlwaysOn, ",") {
		t.Fatalf("loaded request disturbed the always-on order: %v", second)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 1 || executed[0] != "chart_render" {
		t.Fatalf("executed tools = %v, want [chart_render] (loaded tool must really run)", executed)
	}

	// The search_tools result is in the transcript the model sees next.
	var searchResult string
	for _, message := range requests[1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "chart_render") {
			searchResult = message.Content
		}
	}
	if !strings.Contains(searchResult, `"loaded"`) {
		t.Fatalf("search_tools result not carried into the follow-up request: %q", searchResult)
	}
}

// TestChatToolTierColonyBindingRestoresEagerSurface checks the colony phase:
// before a colony binds the tier advertises the always-on set only; a binding
// makes the colony-phase names eager again (upstream's phase list), while the
// flat directory layer stays searchable.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_current_tools
func TestChatToolTierColonyBindingRestoresEagerSurface(t *testing.T) {
	bound := false
	full := append(lazySourceDefs(), lazyColonyDefs()...)
	source := func() []llm.ToolDef {
		if bound {
			return full
		}
		return lazySourceDefs()
	}
	tier := newChatToolTier(t.TempDir(), source, lazyAlwaysOn, lazyDeferredDefs(), func() bool { return bound })

	first := toolNamesOf(tier.toolDefs())
	if len(first) != len(lazyAlwaysOn) {
		t.Fatalf("independent eager surface = %d %v, want %d", len(first), first, len(lazyAlwaysOn))
	}

	bound = true
	after := toolNamesOf(tier.toolDefs())
	if len(after) != len(full) {
		t.Fatalf("colony-phase eager surface = %d, want %d (%v)", len(after), len(full), after)
	}
	for _, name := range []string{"run_playbook", "run_worker", "get_worker_status"} {
		if !hasToolName(tier.toolDefs(), name) {
			t.Fatalf("colony-phase surface is missing %q", name)
		}
	}
	searchable := make([]string, 0)
	for _, tool := range tier.searchableTools() {
		searchable = append(searchable, tool.Name)
	}
	if strings.Join(searchable, ",") != "chart_render,read_file" {
		t.Fatalf("searchable directory layer in colony phase = %v, want [chart_render read_file]", searchable)
	}
}
