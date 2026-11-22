package server

// The chat-app gate's runtime source: upstream wires
// ``chat_app_provider=lambda: session.channel``; the Go port reconstructs
// session.channel for queen-mail threads (a channel session when one runs) and
// carries it through the reminder bridge to the tasks source.
//
// Parity: core/framework/server/queen_orchestrator.py (chat_app_provider)
// Parity: core/framework/server/channels.py::ChannelHost._claim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// Parity: core/framework/server/queen_orchestrator.py (chat_app_provider reads
// session.channel at each render)
func TestChatRuntimeChatAppProviderFollowsTheSessionsChannel(t *testing.T) {
	channel := ""
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &namedMemoryLLM{model: "queen-model"},
		Root:      t.TempDir(),
		SessionID: "session_chat_app",
		Channel:   func() string { return channel },
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	provider, ok := runtime.agentCtx.ChatAppProvider.(func() any)
	if !ok || provider == nil {
		t.Fatalf("ChatAppProvider = %#v, want a callable provider", runtime.agentCtx.ChatAppProvider)
	}
	if got := provider(); got != nil {
		t.Fatalf("dashboard session reads as chat app %#v", got)
	}
	channel = "queens"
	if got := provider(); got != "queens" {
		t.Fatalf("chat app after the channel claimed the session = %#v, want queens", got)
	}
	channel = "  "
	if got := provider(); got != nil {
		t.Fatalf("blank channel reads as chat app %#v", got)
	}
}

// Parity: core/framework/server/queen_orchestrator.py (chat_app_provider)
func TestChatRuntimeWithoutAChannelSourceHasNoChatAppProvider(t *testing.T) {
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &namedMemoryLLM{model: "queen-model"},
		Root:      t.TempDir(),
		SessionID: "session_no_channel",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.agentCtx.ChatAppProvider != nil {
		t.Fatalf("ChatAppProvider = %#v, want nil without a channel source", runtime.agentCtx.ChatAppProvider)
	}
}

// Parity: core/framework/tasks/tests/test_reminders.py::test_task_source_does_not_push_the_panel_on_a_chat_app
func TestTaskReminderAdapterCarriesTheChatAppGate(t *testing.T) {
	chatAppTaskStore(t)
	adapter := &taskReminderSourceAdapter{inner: tasks.NewTaskReminderSource()}
	for i := 0; i < tasks.UntrackedTurns; i++ {
		adapter.ObserveTurn([]string{"browser_click"})
	}

	onChatApp := agentloop.ReminderContext{
		Point: tasks.ReminderStopPoint,
		AgentCtx: &agentloop.ReminderAgentContext{
			SessionID:       "s1",
			ChatAppProvider: func() any { return "queens" },
		},
	}
	if body, ok := adapter.Render(onChatApp); ok || body != "" {
		t.Fatalf("STOP on a chat app = %q, %v; want empty", body, ok)
	}

	onDashboard := agentloop.ReminderContext{
		Point: tasks.ReminderStopPoint,
		AgentCtx: &agentloop.ReminderAgentContext{
			SessionID:       "s1",
			ChatAppProvider: func() any { return nil },
		},
	}
	body, ok := adapter.Render(onDashboard)
	if !ok || !strings.Contains(body, "haven't created any tasks") {
		t.Fatalf("STOP on the dashboard = %q, %v; want the untracked drift", body, ok)
	}
}

// Parity: core/framework/server/queen_orchestrator.py::create_queen
// (session.channel restored from meta.json on resume)
func TestSessionChannelRoundTripsThroughMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"channel":"queens","title":"Ivy"}`), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if got := ReadSessionChannel(dir); got != "queens" {
		t.Fatalf("ReadSessionChannel = %q, want queens", got)
	}
	if got := ReadSessionChannel(t.TempDir()); got != "" {
		t.Fatalf("ReadSessionChannel without meta = %q, want empty", got)
	}
	if got := ReadSessionChannel(""); got != "" {
		t.Fatalf("ReadSessionChannel without a dir = %q, want empty", got)
	}

	session := NewSession("session_channel", nil)
	session.SetChannel("  queens ")
	if got := session.Channel(); got != "queens" {
		t.Fatalf("Channel = %q, want queens", got)
	}
	session.SetChannel("")
	if got := session.Channel(); got != "" {
		t.Fatalf("Channel after clearing = %q, want empty", got)
	}
}

func chatAppTaskStore(t *testing.T) *tasks.TaskStore {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HIVE_HOME", root)
	tasks.ResetSessionDirCache()
	previous := tasks.GetTaskStore()
	store := tasks.NewTaskStore(root)
	tasks.SetDefaultTaskStore(store)
	t.Cleanup(func() {
		tasks.SetDefaultTaskStore(previous)
		tasks.ResetSessionDirCache()
	})
	return store
}
