package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeSummarySessionDir(t *testing.T) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "session_x")
	if err := os.MkdirAll(filepath.Join(d, "conversations", "parts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return d
}

func writeSummaryPart(t *testing.T, sessionDir string, seq int, data map[string]any) {
	t.Helper()
	partsDir := filepath.Join(sessionDir, "conversations", "parts")
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(partsDir, fmt.Sprintf("%010d.json", seq))
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
}

// Parity: tests/test_session_summary.py::test_is_client_facing
func TestIsClientFacing(t *testing.T) {
	cases := []struct {
		name string
		part map[string]any
		want bool
	}{
		{"user", map[string]any{"role": "user", "content": "hi"}, true},
		{"assistant text", map[string]any{"role": "assistant", "content": "ok"}, true},
		{"tool", map[string]any{"role": "tool", "content": "x"}, false},
		{"assistant tool call", map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "1"}}}, false},
		{"transition marker", map[string]any{"is_transition_marker": true}, false},
	}
	for _, tc := range cases {
		if got := IsClientFacing(tc.part); got != tc.want {
			t.Errorf("%s: IsClientFacing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Parity: tests/test_session_summary.py::test_rebuild_empty_session
func TestRebuildEmptySession(t *testing.T) {
	d := makeSummarySessionDir(t)
	summary := RebuildSummary(d)
	if summary == nil {
		t.Fatal("RebuildSummary returned nil")
	}
	if summary.MessageCount != 0 {
		t.Fatalf("message_count = %d, want 0", summary.MessageCount)
	}
	if summary.LastMessage != nil {
		t.Fatalf("last_message = %v, want nil", *summary.LastMessage)
	}
	if summary.LastActiveAt != 0.0 {
		t.Fatalf("last_active_at = %v, want 0", summary.LastActiveAt)
	}
	if _, err := os.Stat(filepath.Join(d, "summary.json")); err != nil {
		t.Fatalf("summary.json not persisted: %v", err)
	}
}

// Parity: tests/test_session_summary.py::test_rebuild_counts_only_client_facing
func TestRebuildCountsOnlyClientFacing(t *testing.T) {
	d := makeSummarySessionDir(t)
	writeSummaryPart(t, d, 0, map[string]any{"role": "user", "content": "hello", "seq": 0, "created_at": 1.0})
	writeSummaryPart(t, d, 1, map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "1"}}, "seq": 1, "created_at": 2.0})
	writeSummaryPart(t, d, 2, map[string]any{"role": "tool", "content": "result", "seq": 2, "created_at": 3.0})
	writeSummaryPart(t, d, 3, map[string]any{"role": "assistant", "content": "answer", "seq": 3, "created_at": 4.0})

	summary := RebuildSummary(d)
	if summary.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", summary.MessageCount)
	}
	if summary.LastMessage == nil || *summary.LastMessage != "answer" {
		t.Fatalf("last_message = %v, want answer", summary.LastMessage)
	}
	if summary.LastActiveAt != 4.0 {
		t.Fatalf("last_active_at = %v, want 4", summary.LastActiveAt)
	}
	if summary.LastPartSeq == nil || *summary.LastPartSeq != 3 {
		t.Fatalf("last_part_seq = %v, want 3", summary.LastPartSeq)
	}
}

// Parity: tests/test_session_summary.py::test_rebuild_truncates_long_message
func TestRebuildTruncatesLongMessage(t *testing.T) {
	d := makeSummarySessionDir(t)
	long := ""
	for i := 0; i < 500; i++ {
		long += "a"
	}
	writeSummaryPart(t, d, 0, map[string]any{"role": "assistant", "content": long, "seq": 0, "created_at": 1.0})
	summary := RebuildSummary(d)
	if summary.LastMessage == nil {
		t.Fatal("last_message is nil")
	}
	if got := len([]rune(*summary.LastMessage)); got != 120 {
		t.Fatalf("last_message length = %d, want 120", got)
	}
}

// Parity: tests/test_session_summary.py::test_rebuild_handles_anthropic_content_blocks
func TestRebuildHandlesAnthropicContentBlocks(t *testing.T) {
	d := makeSummarySessionDir(t)
	writeSummaryPart(t, d, 0, map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "block one"},
			map[string]any{"type": "text", "text": "block two"},
		},
		"seq":        0,
		"created_at": 1.0,
	})
	summary := RebuildSummary(d)
	if summary.LastMessage == nil || *summary.LastMessage != "block one block two" {
		t.Fatalf("last_message = %v, want %q", summary.LastMessage, "block one block two")
	}
}

// Parity: tests/test_session_summary.py::test_update_summary_increments
func TestUpdateSummaryIncrements(t *testing.T) {
	d := makeSummarySessionDir(t)
	UpdateSummary(d, map[string]any{"role": "user", "content": "hi", "seq": 0, "created_at": 1.0})
	s := ReadSummary(d)
	if s == nil {
		t.Fatal("summary is nil after first update")
	}
	if s.MessageCount != 1 {
		t.Fatalf("message_count = %d, want 1", s.MessageCount)
	}
	if s.LastActiveAt != 1.0 {
		t.Fatalf("last_active_at = %v, want 1", s.LastActiveAt)
	}

	UpdateSummary(d, map[string]any{"role": "assistant", "content": "ok", "seq": 1, "created_at": 2.0})
	s = ReadSummary(d)
	if s.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", s.MessageCount)
	}
	if s.LastMessage == nil || *s.LastMessage != "ok" {
		t.Fatalf("last_message = %v, want ok", s.LastMessage)
	}
	if s.LastActiveAt != 2.0 {
		t.Fatalf("last_active_at = %v, want 2", s.LastActiveAt)
	}
}

// Parity: tests/test_session_summary.py::test_update_summary_skips_non_client_parts
func TestUpdateSummarySkipsNonClientParts(t *testing.T) {
	d := makeSummarySessionDir(t)
	UpdateSummary(d, map[string]any{"role": "tool", "content": "x", "seq": 0, "created_at": 1.0})
	UpdateSummary(d, map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "x"}}, "seq": 1, "created_at": 2.0})
	if s := ReadSummary(d); s != nil {
		t.Fatalf("summary created for non-client parts: %+v", s)
	}
}

// Parity: tests/test_session_summary.py::test_update_summary_user_message_keeps_prior_assistant_snippet
func TestUpdateSummaryUserMessageKeepsPriorAssistantSnippet(t *testing.T) {
	d := makeSummarySessionDir(t)
	UpdateSummary(d, map[string]any{"role": "assistant", "content": "answer", "seq": 0, "created_at": 1.0})
	UpdateSummary(d, map[string]any{"role": "user", "content": "next q", "seq": 1, "created_at": 2.0})
	s := ReadSummary(d)
	if s.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", s.MessageCount)
	}
	if s.LastMessage == nil || *s.LastMessage != "answer" {
		t.Fatalf("last_message = %v, want answer", s.LastMessage)
	}
	if s.LastActiveAt != 2.0 {
		t.Fatalf("last_active_at = %v, want 2", s.LastActiveAt)
	}
}

// Parity: tests/test_session_summary.py::test_is_stale_when_missing
func TestIsStaleWhenMissing(t *testing.T) {
	d := makeSummarySessionDir(t)
	if !IsStale(d) {
		t.Fatal("missing summary reported fresh")
	}
}

// Parity: tests/test_session_summary.py::test_is_stale_after_part_write
func TestIsStaleAfterPartWrite(t *testing.T) {
	d := makeSummarySessionDir(t)
	UpdateSummary(d, map[string]any{"role": "assistant", "content": "x", "seq": 0, "created_at": 1.0})
	if IsStale(d) {
		t.Fatal("summary reported stale immediately after update")
	}

	time.Sleep(50 * time.Millisecond)
	writeSummaryPart(t, d, 1, map[string]any{"role": "assistant", "content": "y", "seq": 1})
	if !IsStale(d) {
		t.Fatal("summary reported fresh after a newer part was written")
	}
}

// Parity: tests/test_session_summary.py::test_rebuild_picks_up_node_based_layout
func TestRebuildPicksUpNodeBasedLayout(t *testing.T) {
	d := filepath.Join(t.TempDir(), "sess")
	for _, node := range []string{"node_a", "node_b"} {
		if err := os.MkdirAll(filepath.Join(d, "conversations", node, "parts"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	p1 := filepath.Join(d, "conversations", "node_a", "parts", "0000000000.json")
	if err := os.WriteFile(p1, []byte(`{"role":"user","content":"hi","seq":0,"created_at":1.0}`), 0o644); err != nil {
		t.Fatalf("write p1: %v", err)
	}
	p2 := filepath.Join(d, "conversations", "node_b", "parts", "0000000001.json")
	if err := os.WriteFile(p2, []byte(`{"role":"assistant","content":"yo","seq":1,"created_at":2.0}`), 0o644); err != nil {
		t.Fatalf("write p2: %v", err)
	}

	summary := RebuildSummary(d)
	if summary.MessageCount != 2 {
		t.Fatalf("message_count = %d, want 2", summary.MessageCount)
	}
	if summary.LastMessage == nil || *summary.LastMessage != "yo" {
		t.Fatalf("last_message = %v, want yo", summary.LastMessage)
	}
}
