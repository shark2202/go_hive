package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// Parity: core/framework/server/queen_orchestrator.py:1719 (past_conversation_recall_provider wired on the queen session)
func TestChatHubRegistersPastConversationsReminder(t *testing.T) {
	homeRoot := t.TempDir()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if !containsString(runtime.hub.RegisteredSourceNames(), "past_conversations") {
		t.Fatalf("registered sources = %v, want past_conversations", runtime.hub.RegisteredSourceNames())
	}
	if !containsString(runtime.hub.ActiveSourceNames(), "past_conversations") {
		t.Fatalf("active sources = %v, want the wired past_conversations source", runtime.hub.ActiveSourceNames())
	}
	if runtime.agentCtx.PastConversationRecallProvider == nil {
		t.Fatal("the chat runtime did not wire the past-conversation recall provider")
	}
}

// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource.points
// (SESSION_START recalls the first message before it joins an empty conversation).
func TestChatRuntimePastConversationRecallAtSessionStart(t *testing.T) {
	homeRoot := t.TempDir()
	pastSession := "session_20230522_211800_aaaa0001"
	writePastConversationSession(t, homeRoot, "queen_ava", pastSession, [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	liveRoot := filepath.Join(homeRoot, "queens", "queen_ava", "sessions", "session_20260101_100000_live0001")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll live: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      liveRoot,
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if runtime.conv.MessageCount() != 0 {
		t.Fatalf("conversation starts with %d messages, want empty", runtime.conv.MessageCount())
	}

	block := runtime.fireReminderPoint(agentloop.SESSION_START, "How long is my daily commute to work?", nil)
	for _, want := range []string{
		"<system-reminder>",
		"Excerpts from earlier conversations that may relate",
		"45 minutes each way",
		pastSession,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("session-start block missing %q: %q", want, block)
		}
	}
	messages := runtime.conv.Messages()
	if len(messages) != 1 || !messages[0].IsSystemReminder {
		t.Fatalf("conversation = %#v, want one system-reminder frame", messages)
	}
}

// Parity-exempt: Go fires SESSION_START and USER_PROMPT_SUBMIT on the same
// first turn; upstream renders past_conversations once because the initial
// message is not also queued as a pending client text. The runtime passes the
// first turn's text only to SESSION_START so the source still renders once.
func TestChatRuntimeInjectsPastConversationExcerptsOnceOnFirstTurn(t *testing.T) {
	homeRoot := t.TempDir()
	pastSession := "session_20230522_211800_aaaa0001"
	writePastConversationSession(t, homeRoot, "queen_ava", pastSession, [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	liveRoot := filepath.Join(homeRoot, "queens", "queen_ava", "sessions", "session_20260101_100000_live0001")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll live: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      liveRoot,
		QueenName: "queen_ava",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("How long is my daily commute to work?"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	parts := waitForAssistantParts(t, runtime, 1)
	count := 0
	for _, part := range parts {
		if part["role"] != "user" {
			continue
		}
		content, _ := part["content"].(string)
		if strings.Contains(content, "Excerpts from earlier conversations") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("past-conversation excerpts injected %d times on the first turn, want 1: %#v", count, parts)
	}
}
