package agentloop

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

func tierParityPool() []tools.Tool {
	return []tools.Tool{
		{Name: "browser_interact", Description: "desc of browser_interact"},
		{Name: "browser_upload", Description: "desc of browser_upload"},
		{Name: "send_email", Description: "desc of send_email"},
		{Name: "task_update", Description: "desc of task_update"},
	}
}

func tierParityState() *tools.ToolTierState {
	state := &tools.ToolTierState{
		Pool:               tierParityPool(),
		AlwaysEnabledNames: map[string]struct{}{"browser_interact": {}},
		GateableNames: map[string]struct{}{
			"browser_interact": {},
			"browser_upload":   {},
			"send_email":       {},
		},
	}
	state.Rebuild()
	return state
}

func tierReminderTools(list []tools.Tool) []Tool {
	out := make([]Tool, 0, len(list))
	for _, tool := range list {
		out = append(out, Tool{Name: tool.Name, Description: tool.Description})
	}
	return out
}

func tierToolDefs(list []tools.Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(list))
	for _, tool := range list {
		out = append(out, llm.ToolDef{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return out
}

func tierWorkerContext(state *tools.ToolTierState) *ReminderAgentContext {
	return &ReminderAgentContext{
		IsQueenStream: false,
		SearchableToolsProvider: func() []Tool {
			return tierReminderTools(state.GetSearchableTools())
		},
		LoadedToolNamesProvider: func() []string {
			return append([]string(nil), state.LoadedToolNames...)
		},
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestWorkerReminder::test_applies_to_worker_with_provider
func TestWorkerTierReminderAppliesToWorkerWithProvider(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	worker := &ReminderAgentContext{IsQueenStream: false, SearchableToolsProvider: func() []Tool { return nil }}
	if !src.AppliesTo(worker) {
		t.Error("source skipped a tiered worker")
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestWorkerReminder::test_skips_worker_without_provider
func TestWorkerTierReminderSkipsWorkerWithoutProvider(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if src.AppliesTo(&ReminderAgentContext{IsQueenStream: false}) {
		t.Error("source bound an untiered worker")
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestWorkerReminder::test_still_applies_to_queen
func TestWorkerTierReminderStillAppliesToQueen(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if !src.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Error("source skipped the queen stream")
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestWorkerReminder::test_renders_worker_manifest
func TestWorkerTierReminderRendersWorkerManifest(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	state := tierParityState()
	ctx := tierWorkerContext(state)

	body, ok := src.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx})
	if !ok {
		t.Fatal("no manifest rendered at SESSION_START")
	}
	for _, name := range []string{"browser_upload", "send_email"} {
		if !strings.Contains(body, name) {
			t.Errorf("manifest missing %q: %q", name, body)
		}
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestWorkerReminder::test_loaded_tool_departs_silently
func TestWorkerTierReminderLoadedToolDepartsSilently(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	state := tierParityState()
	ctx := tierWorkerContext(state)

	if _, ok := src.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx}); !ok {
		t.Fatal("no manifest rendered at SESSION_START")
	}
	state.PromoteSearchedTools([]string{"send_email"})
	if body, ok := src.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx}); ok {
		t.Fatalf("loaded tool departure was announced: %q", body)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestDynamicRefreshSynthetics::test_worker_synthetics_survive_refresh
func TestDynamicRefreshWorkerSyntheticsSurvive(t *testing.T) {
	state := tierParityState()
	dyn := tierToolDefs(state.GetCurrentTools())
	live := append(append([]llm.ToolDef(nil), dyn...),
		llm.ToolDef{Name: "report_to_parent"},
		llm.ToolDef{Name: "search_tools"},
		llm.ToolDef{Name: "ask_user"},
	)
	ctx := &AgentContext{DynamicToolsProvider: func() []llm.ToolDef {
		return tierToolDefs(state.GetCurrentTools())
	}}

	refreshed := (&AgentLoop{}).RefreshDynamicTools(ctx, live)
	names := make([]string, 0, len(refreshed))
	seen := map[string]int{}
	for _, tool := range refreshed {
		names = append(names, tool.Name)
		seen[tool.Name]++
	}
	for _, want := range []string{"report_to_parent", "search_tools", "ask_user"} {
		if seen[want] == 0 {
			t.Errorf("synthetic %q vanished after refresh; names = %v", want, names)
		}
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("%q appears %d times, want 1", name, count)
		}
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestDynamicRefreshSynthetics::test_provider_supplied_search_tools_not_duplicated
func TestDynamicRefreshProviderSearchToolsNotDuplicated(t *testing.T) {
	state := &tools.ToolTierState{
		Pool:               []tools.Tool{{Name: "search_tools"}, {Name: "browser_interact"}},
		AlwaysEnabledNames: map[string]struct{}{},
		GateableNames:      map[string]struct{}{},
	}
	state.Rebuild()
	live := tierToolDefs(state.GetCurrentTools())
	ctx := &AgentContext{DynamicToolsProvider: func() []llm.ToolDef {
		return tierToolDefs(state.GetCurrentTools())
	}}

	refreshed := (&AgentLoop{}).RefreshDynamicTools(ctx, live)
	count := 0
	for _, tool := range refreshed {
		if tool.Name == "search_tools" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("search_tools appears %d times, want 1", count)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestPromptGatingUsesTier::test_gating_reads_eager_set_not_pool
func TestPromptGatingReadsEagerSetNotPool(t *testing.T) {
	state := &tools.ToolTierState{
		Pool: []tools.Tool{
			{Name: "browser_interact", Description: "desc of browser_interact"},
			{Name: "terminal_exec", Description: "desc of terminal_exec"},
		},
		AlwaysEnabledNames: map[string]struct{}{"terminal_exec": {}},
		GateableNames: map[string]struct{}{
			"browser_interact": {},
			"terminal_exec":    {},
		},
	}
	state.Rebuild()

	focus := "focus"
	ctx := &AgentContext{
		AvailableTools:      tierToolDefs(state.Pool),
		ToolTierState:       state,
		SkillsCatalogPrompt: "CATALOG",
		AgentSpec:           AgentSpec{SystemPrompt: &focus, AgentType: "event_loop"},
	}

	spec := BuildPromptSpec(ctx, PromptOverrides{})
	const marker = "Pre-Activated Skill: hive.browser-automation"
	if strings.Contains(spec.SkillsCatalogPrompt, marker) {
		t.Errorf("deferred tool pre-activated its skill: %q", spec.SkillsCatalogPrompt)
	}

	state.PromoteSearchedTools([]string{"browser_interact"})
	spec2 := BuildPromptSpec(ctx, PromptOverrides{})
	if !strings.Contains(spec2.SkillsCatalogPrompt, marker) {
		t.Errorf("promoted tool did not pre-activate its skill: %q", spec2.SkillsCatalogPrompt)
	}
}
