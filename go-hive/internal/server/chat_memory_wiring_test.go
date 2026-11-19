package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

func writePastConversationSession(t *testing.T, homeRoot, queen, session string, turns [][2]string) string {
	t.Helper()
	dir := filepath.Join(homeRoot, "queens", queen, "sessions", session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var builder strings.Builder
	for i, turn := range turns {
		user, _ := json.Marshal(map[string]any{
			"type": "client_input_received",
			"data": map[string]any{"content": turn[0]},
		})
		builder.Write(user)
		builder.WriteByte('\n')
		if turn[1] != "" {
			assistant, _ := json.Marshal(map[string]any{
				"type": "client_output_delta",
				"data": map[string]any{"snapshot": turn[1], "iteration": i, "inner_turn": 0},
			})
			builder.Write(assistant)
			builder.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("WriteFile events.jsonl: %v", err)
	}
	return dir
}

func pastRecallPart(parts []map[string]any) string {
	for _, part := range parts {
		if part["role"] != "user" {
			continue
		}
		content, _ := part["content"].(string)
		if strings.Contains(content, "Excerpts from earlier conversations") {
			return content
		}
	}
	return ""
}

// Parity: core/tests/test_memory_timeline_recall.py::test_reminder_passes_the_user_message_to_the_provider
func TestChatRuntimeInjectsPastConversationExcerpts(t *testing.T) {
	homeRoot := t.TempDir()
	pastSession := "session_20230522_211800_aaaa0001"
	writePastConversationSession(t, homeRoot, "queen_ava", pastSession, [][2]string{
		{"I've been listening to audiobooks during my daily commute, which takes 45 minutes each way.", "That's a great use of the time."},
	})
	liveRoot := filepath.Join(homeRoot, "queens", "queen_ava", "sessions", "session_20260101_100000_live0001")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll live: %v", err)
	}

	model := &fakeChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
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
	block := pastRecallPart(parts)
	if block == "" {
		t.Fatalf("parts = %#v, want an injected past-conversation excerpt", parts)
	}
	for _, want := range []string{
		"<system-reminder>",
		"supersedes earlier excerpt reminders",
		"2023-05-22 (Mon)",
		pastSession,
		"45 minutes each way",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("excerpt block missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "live0001") {
		t.Fatalf("excerpt block leaked the live session: %q", block)
	}
}

// Parity-exempt: upstream spills in AgentLoop._truncate_tool_result; no Python test drives the queen runtime end to end for a spilled tool result.
func TestChatRuntimeSpillsLargeToolResults(t *testing.T) {
	root := t.TempDir()
	payload := strings.Repeat("y", 40000)
	model := &spillChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM: model,
		Tools: []llm.ToolDef{{
			Name:        "big_tool",
			Description: "returns a large payload",
			Parameters:  map[string]any{"type": "object"},
		}},
		Execute: func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
			if name == "big_tool" {
				return payload, false, nil
			}
			return "ok", false, nil
		},
		Root: root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("run the big tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && model.callCount() < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if model.callCount() < 2 {
		t.Fatalf("model calls = %d, want the follow-up turn after the tool result", model.callCount())
	}

	entries, err := os.ReadDir(filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("ReadDir spillover: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("spillover files = %v, want exactly one", entries)
	}
	spilled := filepath.Join(root, "data", entries[0].Name())
	data, err := os.ReadFile(spilled)
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("spill file carries %d bytes, want %d", len(data), len(payload))
	}
	second := model.lastRequest()
	found := false
	for _, message := range second.Messages {
		if strings.Contains(message.Content, "Full result at: "+spilled) {
			found = true
		}
	}
	if !found {
		t.Fatalf("model never saw the spill path %q: %+v", spilled, second.Messages)
	}
}

type spillChatLLM struct {
	mu    sync.Mutex
	calls []llm.CompletionRequest
}

func (m *spillChatLLM) ModelName() string { return "spill-fake" }

func (m *spillChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	index := len(m.calls) - 1
	m.mu.Unlock()
	if index == 0 {
		return llm.Completion{
			ToolCalls:  []llm.ToolCall{{ID: "c1", Name: "big_tool", Arguments: `{}`}},
			StopReason: "tool_calls",
			Model:      "spill-fake",
		}, nil
	}
	return llm.Completion{Content: "done", StopReason: "stop", Model: "spill-fake"}, nil
}

func (m *spillChatLLM) lastRequest() llm.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[len(m.calls)-1]
}

func (m *spillChatLLM) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func writeTimelineUserTurn(t *testing.T, sessionDir, message string) {
	t.Helper()
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	line, _ := json.Marshal(map[string]any{
		"type": "client_input_received",
		"data": map[string]any{"content": message},
	})
	if err := os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("WriteFile events.jsonl: %v", err)
	}
}

type timelineChatLLM struct {
	mu            sync.Mutex
	timelineCalls int
}

func (m *timelineChatLLM) ModelName() string { return "timeline-fake" }

func (m *timelineChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if req.System == memory.TimelineSystemPrompt {
		m.mu.Lock()
		m.timelineCalls++
		m.mu.Unlock()
		return llm.Completion{
			Content: `{"items":[{"session":"S1","turn":1,"kind":"event",` +
				`"text":"The user adopted a dog named Rex.","start":"2026-01-01","end":"2026-01-01",` +
				`"aliases":["dog","adoption"]}]}`,
			Model: "timeline-fake",
		}, nil
	}
	return llm.Completion{Content: "Noted.", Model: "timeline-fake"}, nil
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// Parity: core/tests/test_memory_timeline_recall.py::test_update_extracts_only_new_messages
func TestChatRuntimeWritesTimelineAfterTurn(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "1")
	homeRoot := t.TempDir()
	session := "session_20260101_090000_aaaa0001"
	liveRoot := filepath.Join(homeRoot, "queens", "queen_tl", "sessions", session)
	writeTimelineUserTurn(t, liveRoot, "I adopted a dog named Rex today.")

	model := &timelineChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      liveRoot,
		Bus:       eventbus.NewEventBus(),
		SessionID: session,
		QueenName: "queen_tl",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if err := runtime.Deliver("I adopted a dog named Rex today."); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	waitForFile(t, filepath.Join(liveRoot, memory.TimelineFile))

	// The timeline writer runs after the turn, so file existence can lead the
	// flushed content; poll the reader briefly instead of racing one read.
	var result *memory.SearchTimelineResult
	var toolErr *memory.ToolError
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, toolErr = memory.SearchTimeline(homeRoot, memory.ScopeRef{
			Scope: memory.SearchScopeQueens,
			Owner: "queen_tl",
		}, memory.SearchTimelineOptions{Pattern: "Rex"})
		if toolErr == nil && result != nil && result.Total > 0 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = runtime.Close() // stop the writer before t.TempDir cleanup removes files
	if toolErr != nil {
		t.Fatalf("SearchTimeline: %v", toolErr)
	}
	if result == nil {
		t.Fatalf("SearchTimeline: no result")
	}
	if result.Total != 1 {
		t.Fatalf("timeline items = %d, want 1: %+v", result.Total, result.Items)
	}
	item := result.Items[0]
	if item.Session != session {
		t.Fatalf("item session = %q, want %q", item.Session, session)
	}
	if !strings.Contains(item.Text, "Rex") {
		t.Fatalf("item text = %q, want the extracted dog fact", item.Text)
	}
}

// Parity: core/tests/test_memory_timeline_recall.py::test_backfill_reuses_cached_extractions
func TestChatRuntimeStartupBackfillsTimelines(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "1")
	homeRoot := t.TempDir()
	older := "session_20251231_080000_bbbb0001"
	olderDir := filepath.Join(homeRoot, "queens", "queen_tl", "sessions", older)
	writeTimelineUserTurn(t, olderDir, "We adopted a cat named Miso.")
	liveRoot := filepath.Join(homeRoot, "queens", "queen_tl", "sessions", "session_20260101_090000_bbbb0002")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll live: %v", err)
	}

	model := &timelineChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      liveRoot,
		Bus:       eventbus.NewEventBus(),
		SessionID: "session_20260101_090000_bbbb0002",
		QueenName: "queen_tl",
		HomeRoot:  homeRoot,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	waitForFile(t, filepath.Join(olderDir, memory.TimelineFile))
	result, toolErr := memory.SearchTimeline(homeRoot, memory.ScopeRef{
		Scope: memory.SearchScopeQueens,
		Owner: "queen_tl",
	}, memory.SearchTimelineOptions{Pattern: "Rex"})
	if toolErr != nil {
		t.Fatalf("SearchTimeline: %v", toolErr)
	}
	if result.Total != 1 || result.Items[0].Session != older {
		t.Fatalf("backfilled items = %+v, want the older session", result.Items)
	}
}
