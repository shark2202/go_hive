package agentloop

// Ported from the upstream suite for the memory-notes reminder sources
// (core/tests/test_memory_reminders.py), plus the no-extra-model-call
// invariants the probe verified against the reference.

import (
	"strings"
	"testing"
	"time"
)

func recallSettingsProvider(mode string, waitSeconds float64) func() MemoryRecallSettings {
	return func() MemoryRecallSettings {
		return MemoryRecallSettings{Mode: mode, WaitSeconds: waitSeconds}
	}
}

func indexSettingsProvider() func() MemoryRecallSettings {
	return recallSettingsProvider("index", 10)
}

func waitSettingsProvider(waitSeconds float64) func() MemoryRecallSettings {
	return recallSettingsProvider("wait", waitSeconds)
}

func memoryRecallCtx(provider func(string) string) *ReminderAgentContext {
	return &ReminderAgentContext{IsQueenStream: true, MemoryRecallProvider: provider}
}

func memoryIndexCtx(scopes func() []MemoryNoteScope) *ReminderAgentContext {
	return &ReminderAgentContext{IsQueenStream: true, MemoryIndexProvider: scopes}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_shows_the_pick_for_the_users_message
func TestMemoryRecallWaitShowsThePickForTheUsersMessage(t *testing.T) {
	seen := []string{}
	ctx := memoryRecallCtx(func(text string) string {
		seen = append(seen, text)
		return "### tea.md\n\nAva drinks pu-erh."
	})
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(10)

	source := NewMemoryRecallReminderSource()
	body, ok := source.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "What tea do I like?"})
	want := MemoryRecallHeader + "\n\n### tea.md\n\nAva drinks pu-erh."
	if !ok || body != want {
		t.Fatalf("body = %q, ok = %v, want %q", body, ok, want)
	}
	if len(seen) != 1 || seen[0] != "What tea do I like?" {
		t.Fatalf("provider saw %v, want the user's message", seen)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_shows_an_unchanged_pick_once_until_compaction
func TestMemoryRecallWaitShowsAnUnchangedPickOnceUntilCompaction(t *testing.T) {
	ctx := memoryRecallCtx(func(string) string { return "### tea.md\n\nAva drinks pu-erh." })
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(10)
	source := NewMemoryRecallReminderSource()

	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); !ok {
		t.Fatal("first pick must be shown")
	}
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "And for breakfast?"}); ok {
		t.Fatal("an unchanged pick must not be repeated")
	}
	if _, ok := source.Render(ReminderContext{Point: POST_COMPACT, AgentCtx: ctx}); ok {
		t.Fatal("post-compact must show nothing")
	}
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "And for breakfast?"}); !ok {
		t.Fatal("the same pick must be shown again after compaction")
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_gives_up_past_its_limit
func TestMemoryRecallWaitGivesUpPastItsLimit(t *testing.T) {
	release := make(chan struct{})
	ctx := memoryRecallCtx(func(string) string {
		<-release
		return "late"
	})
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(0.05)

	started := time.Now()
	_, ok := NewMemoryRecallReminderSource().Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"})
	elapsed := time.Since(started)
	close(release)
	if ok {
		t.Fatal("a pick past wait_seconds must be dropped")
	}
	if elapsed >= time.Second {
		t.Fatalf("render took %s; the wait must be capped", elapsed)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_applies_only_to_a_queen_with_a_pick
func TestMemoryRecallWaitAppliesOnlyToAQueenWithAPick(t *testing.T) {
	provider := func(string) string { return "" }
	source := NewMemoryRecallReminderSource()
	if !source.AppliesTo(memoryRecallCtx(provider)) {
		t.Fatal("a queen stream with a pick provider must apply")
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: false, MemoryRecallProvider: provider}) {
		t.Fatal("a non-queen stream must not apply")
	}
	if source.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("a queen without a pick provider must not apply")
	}

	ctx := memoryRecallCtx(provider)
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(10)
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); ok {
		t.Fatal("an empty pick must show nothing")
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_is_silent_while_another_mode_is_set_and_shows_the_pick_again_after
func TestMemoryRecallWaitSilentUnderAnotherModeShowsPickAgainAfter(t *testing.T) {
	ctx := memoryRecallCtx(func(string) string { return "### tea.md\n\nAva drinks pu-erh." })
	mode := "wait"
	ctx.MemoryRecallSettingsProvider = func() MemoryRecallSettings { return MemoryRecallSettings{Mode: mode, WaitSeconds: 10} }
	source := NewMemoryRecallReminderSource()

	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); !ok {
		t.Fatal("first pick must be shown")
	}
	mode = "index"
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); ok {
		t.Fatal("must be silent while index is set")
	}
	mode = "wait"
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); !ok {
		t.Fatal("the pick must show again when wait returns")
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_applies_only_to_a_queen_with_a_pick
// (upstream covers the empty pick; the probe also pinned the failed pick).
func TestMemoryRecallWaitSwallowsAFailedPick(t *testing.T) {
	ctx := memoryRecallCtx(func(string) string { panic("provider down") })
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(10)
	if _, ok := NewMemoryRecallReminderSource().Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "Hello"}); ok {
		t.Fatal("a panicking pick must not break the turn")
	}
}

// Parity: core/tests/test_memory_reminders.py::test_index_is_shown_in_full_at_the_start
func TestMemoryIndexIsShownInFullAtTheStart(t *testing.T) {
	scopes := func() []MemoryNoteScope {
		return []MemoryNoteScope{
			{Label: "Shared", Folder: "/tmp/shared", Entries: []MemoryNoteEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
			{Label: "Yours", Folder: "/tmp/own", Entries: []MemoryNoteEntry{{Name: "garden.md", Line: "[preference] garden.md: Ava's balcony garden"}}},
		}
	}
	ctx := memoryIndexCtx(scopes)
	ctx.MemoryRecallSettingsProvider = indexSettingsProvider()

	body, ok := NewMemoryIndexReminderSource().Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"})
	want := MemoryIndexHeader + "\n\n" +
		"## Shared (/tmp/shared)\n- [preference] tea.md: What Ava drinks\n\n" +
		"## Yours (/tmp/own)\n- [preference] garden.md: Ava's balcony garden"
	if !ok || body != want {
		t.Fatalf("body = %q, ok = %v, want %q", body, ok, want)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_index_then_shows_only_what_changed
func TestMemoryIndexThenShowsOnlyWhatChanged(t *testing.T) {
	current := []MemoryNoteScope{
		{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{
			{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"},
			{Name: "pets.md", Line: "[preference] pets.md: Ava's cat"},
		}},
		{Label: "Yours", Folder: "/q", Entries: []MemoryNoteEntry{
			{Name: "garden.md", Line: "[preference] garden.md: Ava's balcony garden"},
		}},
	}
	ctx := memoryIndexCtx(func() []MemoryNoteScope { return current })
	ctx.MemoryRecallSettingsProvider = indexSettingsProvider()
	source := NewMemoryIndexReminderSource()
	if _, ok := source.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"}); !ok {
		t.Fatal("the start listing must be shown")
	}
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "How are you?"}); ok {
		t.Fatal("an unchanged listing must not be repeated")
	}

	current = []MemoryNoteScope{
		{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{
			{Name: "work.md", Line: "[preference] work.md: Where Ava works"},
			{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks, morning and night"},
		}},
		{Label: "Yours", Folder: "/q", Entries: []MemoryNoteEntry{}},
	}
	body, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What should I drink?"})
	want := MemoryIndexChangedHeader + "\n\n" +
		"## Shared (/g)\n" +
		"New or updated:\n" +
		"- [preference] work.md: Where Ava works\n" +
		"- [preference] tea.md: What Ava drinks, morning and night\n" +
		"Removed:\n" +
		"- pets.md\n\n" +
		"## Yours (/q)\n" +
		"Removed:\n" +
		"- garden.md"
	if !ok || body != want {
		t.Fatalf("body = %q, ok = %v, want %q", body, ok, want)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_index_is_shown_in_full_again_after_compaction
func TestMemoryIndexIsShownInFullAgainAfterCompaction(t *testing.T) {
	ctx := memoryIndexCtx(func() []MemoryNoteScope {
		return []MemoryNoteScope{
			{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
		}
	})
	ctx.MemoryRecallSettingsProvider = indexSettingsProvider()
	source := NewMemoryIndexReminderSource()
	if _, ok := source.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"}); !ok {
		t.Fatal("the start listing must be shown")
	}
	body, ok := source.Render(ReminderContext{Point: POST_COMPACT, AgentCtx: ctx})
	if !ok || !strings.HasPrefix(body, MemoryIndexHeader) || !strings.Contains(body, "- [preference] tea.md: What Ava drinks") {
		t.Fatalf("post-compact body = %q, ok = %v", body, ok)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_index_says_nothing_without_notes
func TestMemoryIndexSaysNothingWithoutNotes(t *testing.T) {
	current := []MemoryNoteScope{}
	ctx := memoryIndexCtx(func() []MemoryNoteScope { return current })
	ctx.MemoryRecallSettingsProvider = indexSettingsProvider()
	source := NewMemoryIndexReminderSource()
	if _, ok := source.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"}); ok {
		t.Fatal("no notes must be silent")
	}

	current = []MemoryNoteScope{
		{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
	}
	body, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"})
	if !ok || !strings.HasPrefix(body, MemoryIndexHeader) {
		t.Fatalf("the first note must list in full: %q ok=%v", body, ok)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_index_is_silent_while_another_mode_is_set_and_lists_in_full_after
func TestMemoryIndexSilentUnderAnotherModeListsInFullAfter(t *testing.T) {
	ctx := memoryIndexCtx(func() []MemoryNoteScope {
		return []MemoryNoteScope{
			{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
		}
	})
	mode := "index"
	ctx.MemoryRecallSettingsProvider = func() MemoryRecallSettings { return MemoryRecallSettings{Mode: mode, WaitSeconds: 10} }
	source := NewMemoryIndexReminderSource()
	if _, ok := source.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"}); !ok {
		t.Fatal("the start listing must be shown")
	}
	mode = "wait"
	if _, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); ok {
		t.Fatal("must be silent while wait is set")
	}
	mode = "index"
	body, ok := source.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "And for breakfast?"})
	if !ok || !strings.HasPrefix(body, MemoryIndexHeader) {
		t.Fatalf("must list in full when index returns: %q ok=%v", body, ok)
	}
}

// Parity: framework/agent_loop/memory_reminders.py (the index source reads
// only the index provider: no model call in index mode).
func TestMemoryIndexMakesNoModelCall(t *testing.T) {
	pickCalls := 0
	indexCalls := 0
	ctx := &ReminderAgentContext{
		IsQueenStream: true,
		MemoryIndexProvider: func() []MemoryNoteScope {
			indexCalls++
			return []MemoryNoteScope{
				{Label: "Shared", Folder: "/g", Entries: []MemoryNoteEntry{{Name: "tea.md", Line: "[preference] tea.md: What Ava drinks"}}},
			}
		},
		MemoryRecallProvider: func(string) string {
			pickCalls++
			return "### tea.md\n\nAva drinks pu-erh."
		},
	}
	ctx.MemoryRecallSettingsProvider = indexSettingsProvider()

	indexSource, recallSource := NewMemoryIndexReminderSource(), NewMemoryRecallReminderSource()
	if _, ok := indexSource.Render(ReminderContext{Point: SESSION_START, AgentCtx: ctx, UserText: "Hi"}); !ok {
		t.Fatal("the index source must list the notes")
	}
	if _, ok := recallSource.Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "What tea do I like?"}); ok {
		t.Fatal("the wait source must be silent in index mode")
	}
	if pickCalls != 0 {
		t.Fatalf("pick provider called %d times in index mode, want 0", pickCalls)
	}
	if indexCalls == 0 {
		t.Fatal("the index provider must be read")
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_shows_the_pick_for_the_users_message
// (an empty message makes no pick call).
func TestMemoryRecallWaitSkipsEmptyMessages(t *testing.T) {
	calls := 0
	ctx := memoryRecallCtx(func(string) string {
		calls++
		return "### tea.md\n\nAva drinks pu-erh."
	})
	ctx.MemoryRecallSettingsProvider = waitSettingsProvider(10)
	if _, ok := NewMemoryRecallReminderSource().Render(ReminderContext{Point: USER_PROMPT_SUBMIT, AgentCtx: ctx, UserText: "   "}); ok {
		t.Fatal("an empty message must not pick")
	}
	if calls != 0 {
		t.Fatalf("pick provider called %d times for an empty message", calls)
	}
}
