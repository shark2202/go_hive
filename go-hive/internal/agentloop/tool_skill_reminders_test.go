package agentloop

import (
	"strings"
	"testing"
)

func testTool(name string) Tool {
	return Tool{Name: name, Description: "desc of " + name}
}

func searchableAgent(tools []Tool, loaded []string) *ReminderAgentContext {
	return &ReminderAgentContext{
		IsQueenStream:           true,
		SearchableToolsProvider: ToolsProvider(tools...),
		LoadedToolNamesProvider: ToolNamesProvider(loaded...),
	}
}

func renderReminder(src ReminderSource, point ReminderPoint, agentCtx *ReminderAgentContext) (string, bool) {
	return src.Render(ReminderContext{Point: point, AgentCtx: agentCtx})
}

// Parity: tests/test_tool_skill_reminders.py::test_lists_searchable_tools_with_contract
func TestListsSearchableToolsWithContract(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	out, ok := renderReminder(src, USER_PROMPT_SUBMIT, searchableAgent(
		[]Tool{testTool("gmail_send"), testTool("notion_search")}, nil,
	))
	if !ok {
		t.Fatal("expected a reminder, got none")
	}
	for _, want := range []string{"search_tools", "select:<name>", "schemas are NOT loaded"} {
		if !strings.Contains(out, want) {
			t.Errorf("reminder missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "gmail_send") || !strings.Contains(out, "notion_search") {
		t.Errorf("reminder missing tool names:\n%s", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_queen_only
func TestSearchableToolsQueenOnly(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if src.AppliesTo(&ReminderAgentContext{IsQueenStream: false}) {
		t.Error("non-queen without a provider must not apply")
	}
	if !src.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Error("queen must always apply")
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_fires_at_session_start
func TestSearchableToolsFiresAtSessionStart(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if !hasReminderPoint(src.Points(), SESSION_START) {
		t.Fatal("SESSION_START must be a fire point")
	}
	out, ok := renderReminder(src, SESSION_START, searchableAgent([]Tool{testTool("gmail_send")}, nil))
	if !ok || !strings.Contains(out, "gmail_send") {
		t.Fatalf("SESSION_START expected a reminder with gmail_send, got ok=%v out=%q", ok, out)
	}
	if out, ok := renderReminder(src, POST_TOOL_USE, searchableAgent([]Tool{testTool("gmail_send")}, nil)); ok {
		t.Fatalf("POST_TOOL_USE expected silence, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_delta_added_then_silent
func TestSearchableToolsDeltaAddedThenSilent(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	attrs := searchableAgent([]Tool{testTool("gmail_send")}, nil)
	if _, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); !ok {
		t.Fatal("first render expected a reminder")
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("unchanged pool expected silence, got %q", out)
	}
	attrs2 := searchableAgent([]Tool{testTool("gmail_send"), testTool("calendar_create")}, nil)
	out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs2)
	if !ok {
		t.Fatal("new tool expected a reminder")
	}
	if !strings.Contains(out, "calendar_create") {
		t.Errorf("reminder missing new tool:\n%s", out)
	}
	if strings.Contains(out, "gmail_send") {
		t.Errorf("already-announced tool must not be repeated:\n%s", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_loaded_departure_is_silent
func TestSearchableToolsLoadedDepartureIsSilent(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if _, ok := renderReminder(src, USER_PROMPT_SUBMIT, searchableAgent([]Tool{testTool("gmail_send")}, nil)); !ok {
		t.Fatal("first render expected a reminder")
	}
	if out, ok := renderReminder(src, POST_TOOL_USE, searchableAgent(nil, []string{"gmail_send"})); ok {
		t.Fatalf("loaded departure must be silent, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_removed_departure_is_announced
func TestSearchableToolsRemovedDepartureIsAnnounced(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if _, ok := renderReminder(src, USER_PROMPT_SUBMIT, searchableAgent([]Tool{testTool("gmail_send")}, nil)); !ok {
		t.Fatal("first render expected a reminder")
	}
	out, ok := renderReminder(src, USER_PROMPT_SUBMIT, searchableAgent(nil, nil))
	if !ok {
		t.Fatal("removed departure expected a reminder")
	}
	if !strings.Contains(out, "no longer available") || !strings.Contains(out, "gmail_send") {
		t.Errorf("reminder missing removal notice:\n%s", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_empty_searchable_set_is_silent
func TestSearchableToolsEmptySetIsSilent(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, searchableAgent(nil, nil)); ok {
		t.Fatalf("empty pool expected silence, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_post_compact_reannounces_full
func TestSearchableToolsPostCompactReannouncesFull(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	attrs := searchableAgent([]Tool{testTool("gmail_send")}, nil)
	if _, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); !ok {
		t.Fatal("first render expected a reminder")
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("unchanged pool expected silence, got %q", out)
	}
	boundary, ok := renderReminder(src, POST_COMPACT, attrs)
	if !ok || !strings.Contains(boundary, "gmail_send") {
		t.Fatalf("POST_COMPACT expected full re-announcement, got ok=%v out=%q", ok, boundary)
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("post-compact baseline expected next turn silence, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_no_provider_is_silent
func TestSearchableToolsNoProviderIsSilent(t *testing.T) {
	src := NewSearchableToolsReminderSource()
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, &ReminderAgentContext{IsQueenStream: true}); ok {
		t.Fatalf("missing provider expected silence, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_emits_catalog_verbatim_then_on_change
func TestSkillsCatalogEmitsVerbatimThenOnChange(t *testing.T) {
	src := NewSkillsCatalogReminderSource()
	cat := "<available_skills>\n  <skill><name>pdf</name></skill>\n</available_skills>"
	attrs := &ReminderAgentContext{IsQueenStream: true, QueenSkillsCatalogProvider: CatalogProvider(cat)}
	out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs)
	if !ok || out != cat {
		t.Fatalf("expected verbatim catalog, got ok=%v out=%q", ok, out)
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("unchanged catalog expected silence, got %q", out)
	}
	cat2 := strings.Replace(cat, "pdf", "pdf</name></skill>\n  <skill><name>enrich", 1)
	attrs2 := &ReminderAgentContext{IsQueenStream: true, QueenSkillsCatalogProvider: CatalogProvider(cat2)}
	out, ok = renderReminder(src, POST_TOOL_USE, attrs2)
	if !ok || out != cat2 {
		t.Fatalf("expected changed catalog verbatim, got ok=%v out=%q", ok, out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_post_compact_reannounces_full_catalog
func TestSkillsCatalogPostCompactReannouncesFull(t *testing.T) {
	src := NewSkillsCatalogReminderSource()
	cat := "<available_skills>\n  <skill><name>pdf</name></skill>\n</available_skills>"
	attrs := &ReminderAgentContext{IsQueenStream: true, QueenSkillsCatalogProvider: CatalogProvider(cat)}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); !ok || out != cat {
		t.Fatalf("first render expected the catalog, got ok=%v out=%q", ok, out)
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("unchanged catalog expected silence, got %q", out)
	}
	if out, ok := renderReminder(src, POST_COMPACT, attrs); !ok || out != cat {
		t.Fatalf("POST_COMPACT expected full catalog, got ok=%v out=%q", ok, out)
	}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("post-compact baseline expected next turn silence, got %q", out)
	}
}

// Parity: tests/test_tool_skill_reminders.py::test_empty_catalog_silent_and_queen_only
func TestSkillsCatalogEmptySilentAndQueenOnly(t *testing.T) {
	src := NewSkillsCatalogReminderSource()
	if src.AppliesTo(&ReminderAgentContext{IsQueenStream: false}) {
		t.Error("non-queen must not apply")
	}
	attrs := &ReminderAgentContext{IsQueenStream: true, QueenSkillsCatalogProvider: CatalogProvider("")}
	if out, ok := renderReminder(src, USER_PROMPT_SUBMIT, attrs); ok {
		t.Fatalf("empty catalog expected silence, got %q", out)
	}
}
