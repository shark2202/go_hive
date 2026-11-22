package server

// Memory-notes push wiring: the chat runtime registers the "index" and "wait"
// reminder sources, fills their providers from the session's own memory
// folders, and keeps the "background" injection gated on mode.
//
// Parity: core/framework/server/queen_orchestrator.py::_memory_index
// Parity: core/framework/server/queen_orchestrator.py::_recall_for_message
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// Background mode starts the pick off the turn: nothing is injected until
	// the pick has landed and a later drain delivers it.
	if runtime.conv.MessageCount() != before {
		t.Fatalf("background mode injected before the pick landed: messages %d, want %d", runtime.conv.MessageCount(), before)
	}
	if !waitForChatRecallCache(t, runtime, 2*time.Second) {
		t.Fatal("background pick never landed in the cache")
	}
	runtime.injectRecall("What tea do I like?", "2026-11-22 10:00 CST")
	if runtime.conv.MessageCount() != before+1 {
		t.Fatalf("background mode did not deliver the cached pick: messages %d, want %d", runtime.conv.MessageCount(), before+1)
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input —
// the pick for a message runs while she answers; the cached block (the pick
// made for an earlier message) rides the next drain, so the answer for message
// N never waits on the pick for message N.
func TestChatRuntimeBackgroundRecallLandsWithTheNextMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "background"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{responses: []llm.Completion{
		{Content: `{"selected_memories": ["tea.md"]}`},
		{Content: `{"selected_memories": []}`},
	}}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	before := runtime.conv.MessageCount()
	runtime.injectRecall("What tea do I like?", "2026-11-22 10:00 CST")
	if runtime.conv.MessageCount() != before {
		t.Fatalf("the pick for the current message was injected into it: messages %d, want %d", runtime.conv.MessageCount(), before)
	}
	if !waitForChatRecallCache(t, runtime, 2*time.Second) {
		t.Fatal("background pick never landed in the cache")
	}
	if runtime.conv.MessageCount() != before {
		t.Fatalf("the landed pick was injected without a drain: messages %d, want %d", runtime.conv.MessageCount(), before)
	}

	runtime.injectRecall("And what do I drink?", "2026-11-22 10:02 CST")
	if runtime.conv.MessageCount() != before+1 {
		t.Fatalf("the cached pick did not ride the next message: messages %d, want %d", runtime.conv.MessageCount(), before+1)
	}
	delivered := chatLastMessageContent(t, runtime)
	for _, want := range []string{"<system-reminder>", agentloop.MemoryRecallHeader, "### tea.md", "Ava drinks pu-erh."} {
		if !strings.Contains(delivered, want) {
			t.Fatalf("delivered recall missing %q:\n%s", want, delivered)
		}
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input —
// the EventBus drops a handler past 15s, so the handler only starts the pick:
// injectRecall must return while the model call is still in flight.
func TestChatRuntimeBackgroundRecallNeverBlocksTheTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "background"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &gatedChatLLM{release: make(chan struct{})}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	done := make(chan struct{})
	go func() {
		runtime.injectRecall("What tea do I like?", "2026-11-22 10:00 CST")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("injectRecall blocked on the pick")
	}
	if runtime.conv.MessageCount() != 0 {
		t.Fatalf("nothing was cached yet, but %d messages were injected", runtime.conv.MessageCount())
	}
	close(model.release)
	if !waitForChatRecallCache(t, runtime, 2*time.Second) {
		t.Fatal("released pick never landed in the cache")
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_refresh_recall_cache —
// an empty message makes no pick call; the cached block is still delivered at
// the next drain.
func TestChatRuntimeBackgroundRecallEmptyMessageKeepsTheCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "background"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	model := &recordingChatLLM{}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	runtime.recallMu.Lock()
	runtime.recallCache = "--- Global Memories ---\n\n### tea.md\n\nAva drinks pu-erh.\n\n--- End Global Memories ---"
	runtime.recallMu.Unlock()
	calls := model.callCount()

	runtime.injectRecall("", "2026-11-22 10:00 CST")
	if runtime.conv.MessageCount() != 1 {
		t.Fatalf("empty message did not deliver the cache: messages %d, want 1", runtime.conv.MessageCount())
	}
	if model.callCount() != calls {
		t.Fatalf("empty message made a pick call: %d -> %d", calls, model.callCount())
	}
}

// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
// — an unchanged block is not injected again; a changed one is.
func TestChatRuntimeBackgroundRecallReinjectsOnlyOnChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_MEMORY_RECALL", "")
	writeChatRecallConfig(t, home, `{"memory_recall": {"mode": "background"}}`)
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "tea.md", "What Ava drinks", "Ava drinks pu-erh.")
	writeChatMemoryNote(t, filepath.Join(home, "memories", "global"), "coffee.md", "What Ava avoids", "Ava avoids coffee.")
	model := &recordingChatLLM{}
	runtime := newMemoryNotesChatRuntime(t, home, model)

	stamp := "2026-11-22 10:00 CST"
	runtime.recallMu.Lock()
	runtime.recallCache = "first block"
	runtime.recallMu.Unlock()
	runtime.injectRecall("", stamp)
	runtime.injectRecall("", stamp)
	if runtime.conv.MessageCount() != 1 {
		t.Fatalf("an unchanged block was re-injected: messages %d, want 1", runtime.conv.MessageCount())
	}

	runtime.recallMu.Lock()
	runtime.recallCache = "second block"
	runtime.recallMu.Unlock()
	runtime.injectRecall("", stamp)
	if runtime.conv.MessageCount() != 2 {
		t.Fatalf("a changed block was not injected: messages %d, want 2", runtime.conv.MessageCount())
	}
}

func waitForChatRecallCache(t *testing.T, runtime *ChatRuntime, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		runtime.recallMu.Lock()
		cached := runtime.recallCache
		runtime.recallMu.Unlock()
		if cached != "" {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func chatLastMessageContent(t *testing.T, runtime *ChatRuntime) string {
	t.Helper()
	messages := runtime.conv.Messages()
	if len(messages) == 0 {
		t.Fatal("conversation has no messages")
	}
	return messages[len(messages)-1].Content
}

// gatedChatLLM holds every completion until released or the runtime context
// ends — a pick that is still in flight while the turn has already moved on.
type gatedChatLLM struct {
	mu      sync.Mutex
	calls   int
	release chan struct{}
}

func (g *gatedChatLLM) ModelName() string { return "gated-chat" }

func (g *gatedChatLLM) AComplete(ctx context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	select {
	case <-g.release:
		return llm.Completion{Content: `{"selected_memories": ["tea.md"]}`, Model: "gated-chat"}, nil
	case <-ctx.Done():
		return llm.Completion{}, ctx.Err()
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
