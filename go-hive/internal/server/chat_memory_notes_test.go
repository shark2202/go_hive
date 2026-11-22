package server

// Memory-notes push wiring: the chat runtime registers the "index" and "wait"
// reminder sources, fills their providers from the session's own memory
// folders, and keeps the "background" injection gated on mode.
//
// Parity: core/framework/server/queen_orchestrator.py::_memory_index
// Parity: core/framework/server/queen_orchestrator.py::_recall_for_message
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
)

func writeChatMemoryNote(t *testing.T, dir, filename, description, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	content := "---\nname: " + filename[:len(filename)-3] + "\ntype: preference\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", filename, err)
	}
}

func writeChatRecallConfig(t *testing.T, home, document string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), []byte(document), 0o644); err != nil {
		t.Fatalf("WriteFile configuration.json: %v", err)
	}
}

func newMemoryNotesChatRuntime(t *testing.T, home string, model llm.Completer) *ChatRuntime {
	t.Helper()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      t.TempDir(),
		HomeRoot:  home,
		SessionID: "memory-notes-chat",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

// Parity: core/tests/test_memory_reminders.py::test_index_is_shown_in_full_at_the_start
// Parity: framework/agent_loop/memory_reminders.py::MemoryIndexReminderSource
func TestChatRuntimeIndexModeListsNotesWithoutAModelCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	registered := runtime.hub.RegisteredSourceNames()
	active := runtime.hub.ActiveSourceNames()
	for _, name := range []string{"memory_index", "memory_recall"} {
		if !containsString(registered, name) || !containsString(active, name) {
			t.Fatalf("source %s missing: registered=%v active=%v", name, registered, active)
		}
	}

	block := runtime.fireReminderPoint(agentloop.SESSION_START, "Hi", nil)
	if !strings.Contains(block, agentloop.MemoryIndexHeader) {
		t.Fatalf("session start block = %q, want the index header", block)
	}
	if !strings.Contains(block, "[preference] tea.md: What Ava drinks") {
		t.Fatalf("block = %q, want the note's manifest line", block)
	}
	if !strings.Contains(block, "Shared with every queen") {
		t.Fatalf("block = %q, want the shared scope label", block)
	}
	if model.callCount() != 0 {
		t.Fatalf("index mode made %d model calls, want 0", model.callCount())
	}
}

// Parity: core/tests/test_memory_reminders.py::test_wait_shows_the_pick_for_the_users_message
func TestChatRuntimeWaitModeShowsThePickBeforeTheAnswer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "wait", "wait_seconds": 5}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{responses: []llm.Completion{{Content: `{"selected_memories": ["tea.md"]}`}}}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	block := runtime.fireReminderPoint(agentloop.USER_PROMPT_SUBMIT, "What tea do I like?", nil)
	if !strings.Contains(block, agentloop.MemoryRecallHeader) {
		t.Fatalf("block = %q, want the recall header", block)
	}
	if !strings.Contains(block, "### tea.md") || !strings.Contains(block, "Ava drinks pu-erh.") {
		t.Fatalf("block = %q, want the picked note", block)
	}
	if model.callCount() != 1 {
		t.Fatalf("wait mode made %d model calls, want 1", model.callCount())
	}
	request := chatAllText(model.request(0).Messages)
	if !strings.Contains(request, "What tea do I like?") || !strings.Contains(request, "Available memories") {
		t.Fatalf("the pick did not see the message and manifest:\n%s", request)
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
// (background is the injection path; index/wait are reminder sources, so the
// injection must stay silent while either is set).
func TestChatRuntimeRecallInjectionIsBackgroundOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "index"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{responses: []llm.Completion{{Content: `{"selected_memories": ["tea.md"]}`}}}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	before := runtime.conv.MessageCount()
	runtime.injectRecall("What tea do I like?", "2026-11-22 10:00 CST")
	if runtime.conv.MessageCount() != before || model.callCount() != 0 {
		t.Fatalf("index mode injected recall: messages %d->%d, model calls %d", before, runtime.conv.MessageCount(), model.callCount())
	}

	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "background"}}`)
	runtime.injectRecall("What tea do I like?", "2026-11-22 10:00 CST")
	if runtime.conv.MessageCount() != before+1 {
		t.Fatalf("background mode did not inject: messages %d, want %d", runtime.conv.MessageCount(), before+1)
	}
	if model.callCount() != 1 {
		t.Fatalf("background mode made %d model calls, want 1", model.callCount())
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
