package agentloop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func toolCallWithName(id, name string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": "{}",
		},
	}
}

func assistantTurn(seq int, tcID, name string, args map[string]any) Message {
	arguments := "{}"
	if len(args) > 0 {
		encoded, err := marshalJSON(args)
		if err == nil {
			arguments = string(encoded)
		}
	}
	return Message{
		Seq:  seq,
		Role: RoleAssistant,
		ToolCalls: []map[string]any{{
			"id":   tcID,
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": arguments,
			},
		}},
	}
}

func newTestConversationWithMessages(msgs []Message) *NodeConversation {
	conv := NewNodeConversation("s")
	conv.messages = msgs
	conv.nextSeq = 0
	for _, m := range msgs {
		if m.Seq+1 > conv.nextSeq {
			conv.nextSeq = m.Seq + 1
		}
	}
	return conv
}

func addCompactableTurn(t *testing.T, conv *NodeConversation, id, toolName, content string, spillover *string, isError bool) {
	t.Helper()
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName(id, toolName)}, AssistantMessageOptions{}); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}
	if _, err := conv.AddToolResult(id, content, ToolResultOptions{IsError: isError, SpilloverPath: spillover}); err != nil {
		t.Fatalf("AddToolResult: %v", err)
	}
}

func toolMessages(conv *NodeConversation) []Message {
	out := []Message{}
	for _, m := range conv.messages {
		if m.Role == RoleTool {
			out = append(out, m)
		}
	}
	return out
}

func rolesOf(msgs []map[string]any) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i], _ = m["role"].(string)
	}
	return out
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_orphaned_tool_result_dropped
func TestRepairOrphanedToolCallsDropsOrphanResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "tool", "tool_call_id": "orphan_1", "content": "stale result"},
		{"role": "user", "content": "hello"},
		{"role": "assistant", "content": "hi"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if len(repaired) != 2 {
		t.Fatalf("len = %d, want 2", len(repaired))
	}
	if repaired[0]["role"] != "user" || repaired[1]["role"] != "assistant" {
		t.Fatalf("roles = %v", rolesOf(repaired))
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_valid_tool_pair_preserved
func TestRepairOrphanedToolCallsKeepsValidPair(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "search"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{toolCallWithName("tc_1", "search")}},
		{"role": "tool", "tool_call_id": "tc_1", "content": "results"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if len(repaired) != 3 {
		t.Fatalf("len = %d, want 3", len(repaired))
	}
	if repaired[2]["tool_call_id"] != "tc_1" {
		t.Fatalf("tool_call_id = %v", repaired[2]["tool_call_id"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_orphaned_tool_use_gets_stub
func TestRepairOrphanedToolCallsStubsMissingResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "user", "content": "search"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{toolCallWithName("tc_1", "search")}},
		{"role": "user", "content": "what happened?"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if len(repaired) != 4 {
		t.Fatalf("len = %d, want 4", len(repaired))
	}
	if repaired[2]["role"] != "tool" || repaired[2]["tool_call_id"] != "tc_1" {
		t.Fatalf("stub = %#v", repaired[2])
	}
	if !strings.Contains(strings.ToLower(repaired[2]["content"].(string)), "interrupted") {
		t.Fatalf("stub content = %q", repaired[2]["content"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_mixed_orphans
func TestRepairOrphanedToolCallsMixed(t *testing.T) {
	msgs := []map[string]any{
		{"role": "tool", "tool_call_id": "gone_1", "content": "old result"},
		{"role": "user", "content": "try again"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{toolCallWithName("tc_2", "fetch")}},
		{"role": "user", "content": "done?"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if got := rolesOf(repaired); !reflect.DeepEqual(got, []string{"user", "assistant", "tool", "user"}) {
		t.Fatalf("roles = %v", got)
	}
	if repaired[2]["tool_call_id"] != "tc_2" {
		t.Fatalf("tool_call_id = %v", repaired[2]["tool_call_id"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_interleaved_user_messages_hoist_real_result
func TestRepairOrphanedToolCallsHoistsInterleavedResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "assistant", "content": "Hollinden in.", "tool_calls": []map[string]any{toolCallWithName("tc_1", "tracker_sql")}},
		{"role": "user", "content": "[WORKER_REPORT] Hubcap"},
		{"role": "user", "content": "[WORKER_REPORT] Hivehouse"},
		{"role": "tool", "tool_call_id": "tc_1", "content": `{"success": true}`},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if got := rolesOf(repaired); !reflect.DeepEqual(got, []string{"assistant", "tool", "user", "user"}) {
		t.Fatalf("roles = %v", got)
	}
	if repaired[1]["tool_call_id"] != "tc_1" || repaired[1]["content"] != `{"success": true}` {
		t.Fatalf("hoisted result = %#v", repaired[1])
	}
	for _, m := range repaired {
		content, _ := m["content"].(string)
		if strings.Contains(strings.ToLower(content), "interrupted") {
			t.Fatalf("unexpected synthetic stub: %#v", m)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_injection_splitting_multiple_results
func TestRepairOrphanedToolCallsInjectionBetweenResults(t *testing.T) {
	msgs := []map[string]any{
		{
			"role":    "assistant",
			"content": "",
			"tool_calls": []map[string]any{
				toolCallWithName("tc_1", "a"),
				toolCallWithName("tc_2", "b"),
			},
		},
		{"role": "tool", "tool_call_id": "tc_1", "content": "r1"},
		{"role": "user", "content": "[WORKER_REPORT]"},
		{"role": "tool", "tool_call_id": "tc_2", "content": "r2"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if got := rolesOf(repaired); !reflect.DeepEqual(got, []string{"assistant", "tool", "tool", "user"}) {
		t.Fatalf("roles = %v", got)
	}
	if repaired[1]["content"] != "r1" || repaired[2]["content"] != "r2" {
		t.Fatalf("contents = %v / %v", repaired[1]["content"], repaired[2]["content"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestRepairOrphanedToolCalls::test_hoist_across_later_assistant_block
func TestRepairOrphanedToolCallsHoistAcrossLaterBlock(t *testing.T) {
	msgs := []map[string]any{
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{toolCallWithName("tc_1", "a")}},
		{"role": "user", "content": "injected"},
		{"role": "assistant", "content": "", "tool_calls": []map[string]any{toolCallWithName("tc_2", "b")}},
		{"role": "tool", "tool_call_id": "tc_1", "content": "r1"},
		{"role": "tool", "tool_call_id": "tc_2", "content": "r2"},
	}
	repaired := repairOrphanedToolCalls(msgs)
	if got := rolesOf(repaired); !reflect.DeepEqual(got, []string{"assistant", "tool", "user", "assistant", "tool"}) {
		t.Fatalf("roles = %v", got)
	}
	if repaired[1]["tool_call_id"] != "tc_1" || repaired[1]["content"] != "r1" {
		t.Fatalf("first result = %#v", repaired[1])
	}
	if repaired[4]["tool_call_id"] != "tc_2" || repaired[4]["content"] != "r2" {
		t.Fatalf("second result = %#v", repaired[4])
	}
}

// Parity: core/tests/test_node_conversation.py::TestFindCompletedToolCall::test_returns_match_when_prior_non_error_result_exists
func TestFindCompletedToolCallMatch(t *testing.T) {
	tcID := "tc_a"
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "browser_setup", map[string]any{}),
		{Seq: 2, Role: RoleTool, Content: "ok", ToolUseID: &tcID},
	})
	match := conv.FindCompletedToolCall("browser_setup", map[string]any{}, 3)
	if match == nil {
		t.Fatal("expected a match")
	}
	if match.Seq != 1 {
		t.Fatalf("seq = %d, want 1", match.Seq)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFindCompletedToolCall::test_ignores_error_result
func TestFindCompletedToolCallIgnoresError(t *testing.T) {
	tcID := "tc_a"
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "browser_navigate", map[string]any{"url": "x"}),
		{Seq: 2, Role: RoleTool, Content: "boom", ToolUseID: &tcID, IsError: true},
	})
	if match := conv.FindCompletedToolCall("browser_navigate", map[string]any{"url": "x"}, 3); match != nil {
		t.Fatalf("expected nil, got %#v", match)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFindCompletedToolCall::test_canonicalizes_json_args_regardless_of_key_order
func TestFindCompletedToolCallCanonicalizesArgs(t *testing.T) {
	tcID := "tc_a"
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "fetch", map[string]any{"b": 2, "a": 1}),
		{Seq: 2, Role: RoleTool, Content: "ok", ToolUseID: &tcID},
	})
	if match := conv.FindCompletedToolCall("fetch", map[string]any{"a": 1, "b": 2}, 3); match == nil {
		t.Fatal("expected a match with reordered keys")
	}
	if match := conv.FindCompletedToolCall("fetch", map[string]any{"a": 1, "b": 3}, 3); match != nil {
		t.Fatalf("expected nil for different args, got %#v", match)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFindCompletedToolCall::test_respects_within_last_turns_window
func TestFindCompletedToolCallWindow(t *testing.T) {
	tcID := "tc_a"
	msgs := []Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "browser_setup", map[string]any{}),
		{Seq: 2, Role: RoleTool, Content: "ok", ToolUseID: &tcID},
	}
	for i := 3; i < 7; i++ {
		msgs = append(msgs, Message{Seq: i, Role: RoleAssistant, Content: fmt.Sprintf("noise %d", i)})
	}
	conv := newTestConversationWithMessages(msgs)
	if match := conv.FindCompletedToolCall("browser_setup", map[string]any{}, 3); match != nil {
		t.Fatalf("expected nil within 3 turns, got %#v", match)
	}
	if match := conv.FindCompletedToolCall("browser_setup", map[string]any{}, 10); match == nil {
		t.Fatal("expected a match within 10 turns")
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_returns_zero_when_no_prior
func TestCountConsecutiveCompletedToolCallsNone(t *testing.T) {
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
	})
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{}, 3, false); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_counts_three_consecutive_matching_turns
func TestCountConsecutiveCompletedToolCallsThree(t *testing.T) {
	msgs := []Message{{Seq: 0, Role: RoleUser, Content: "go"}}
	for i := 0; i < 3; i++ {
		tcID := fmt.Sprintf("tc_%d", i)
		msgs = append(msgs, assistantTurn(1+i*2, tcID, "get_time", map[string]any{"tz": "UTC"}))
		id := tcID
		msgs = append(msgs, Message{Seq: 2 + i*2, Role: RoleTool, Content: "ok", ToolUseID: &id})
	}
	conv := newTestConversationWithMessages(msgs)
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{"tz": "UTC"}, 3, false); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_non_matching_turn_breaks_streak
func TestCountConsecutiveCompletedToolCallsNonMatchingBreaks(t *testing.T) {
	msgs := []Message{{Seq: 0, Role: RoleUser, Content: "go"}}
	appendPair := func(seq int, tcID, name string, args map[string]any) {
		msgs = append(msgs, assistantTurn(seq, tcID, name, args))
		id := tcID
		msgs = append(msgs, Message{Seq: seq + 1, Role: RoleTool, Content: "ok", ToolUseID: &id})
	}
	appendPair(1, "tc_a", "get_time", map[string]any{"tz": "UTC"})
	appendPair(3, "tc_b", "get_time", map[string]any{"tz": "UTC"})
	appendPair(5, "tc_c", "other_tool", map[string]any{})
	appendPair(7, "tc_d", "get_time", map[string]any{"tz": "UTC"})
	conv := newTestConversationWithMessages(msgs)
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{"tz": "UTC"}, 3, false); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_error_result_does_not_count
func TestCountConsecutiveCompletedToolCallsErrorIgnored(t *testing.T) {
	tcID := "tc_a"
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "get_time", map[string]any{}),
		{Seq: 2, Role: RoleTool, Content: "boom", ToolUseID: &tcID, IsError: true},
	})
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{}, 3, false); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_canonicalises_args
func TestCountConsecutiveCompletedToolCallsCanonicalArgs(t *testing.T) {
	tcID := "tc_a"
	conv := newTestConversationWithMessages([]Message{
		{Seq: 0, Role: RoleUser, Content: "go"},
		assistantTurn(1, "tc_a", "fetch", map[string]any{"b": 2, "a": 1}),
		{Seq: 2, Role: RoleTool, Content: "ok", ToolUseID: &tcID},
	})
	if got := conv.CountConsecutiveCompletedToolCalls("fetch", map[string]any{"a": 1, "b": 2}, 3, false); got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	if got := conv.CountConsecutiveCompletedToolCalls("fetch", map[string]any{"a": 1, "b": 3}, 3, false); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_text_only_turn_breaks_streak
func TestCountConsecutiveCompletedToolCallsTextOnlyBreaks(t *testing.T) {
	msgs := []Message{{Seq: 0, Role: RoleUser, Content: "go"}}
	appendPair := func(seq int, tcID string) {
		msgs = append(msgs, assistantTurn(seq, tcID, "get_time", map[string]any{}))
		id := tcID
		msgs = append(msgs, Message{Seq: seq + 1, Role: RoleTool, Content: "ok", ToolUseID: &id})
	}
	appendPair(1, "tc_a")
	appendPair(3, "tc_b")
	msgs = append(msgs, Message{Seq: 5, Role: RoleAssistant, Content: "just thinking"})
	conv := newTestConversationWithMessages(msgs)
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{}, 3, false); got != 0 {
		t.Fatalf("count = %d, want 0", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestCountConsecutiveCompletedToolCalls::test_respects_within_last_turns_window
func TestCountConsecutiveCompletedToolCallsWindow(t *testing.T) {
	msgs := []Message{{Seq: 0, Role: RoleUser, Content: "go"}}
	for i := 0; i < 5; i++ {
		tcID := fmt.Sprintf("tc_%d", i)
		msgs = append(msgs, assistantTurn(1+i*2, tcID, "get_time", map[string]any{}))
		id := tcID
		msgs = append(msgs, Message{Seq: 2 + i*2, Role: RoleTool, Content: "ok", ToolUseID: &id})
	}
	conv := newTestConversationWithMessages(msgs)
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{}, 3, false); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	if got := conv.CountConsecutiveCompletedToolCalls("get_time", map[string]any{}, 10, false); got != 5 {
		t.Fatalf("count = %d, want 5", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPartialCheckpoint::test_checkpoint_is_cleared_when_real_part_lands
func TestPartialCheckpointClearedOnRealPart(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "c"))
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "s", Store: store})
	if _, err := conv.AddUserMessage("hi", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := conv.CheckpointPartialAssistant("half-written...", nil); err != nil {
		t.Fatal(err)
	}
	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 1 {
		t.Fatalf("partials = %d, want 1", len(partials))
	}
	if partials[0]["content"] != "half-written..." {
		t.Fatalf("content = %v", partials[0]["content"])
	}
	if _, err := conv.AddAssistantMessage("fully written", nil, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	partials, err = store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("partials after commit = %d, want 0", len(partials))
	}
}

// Parity: core/tests/test_node_conversation.py::TestPartialCheckpoint::test_restore_surfaces_partial_as_truncated_message
func TestPartialCheckpointRestoreSurfacesTruncated(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "c"))
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "s", Store: store})
	if _, err := conv.AddUserMessage("hi", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	toolCalls := []map[string]any{toolCallWithName("tc_x", "browser_click")}
	if err := conv.CheckpointPartialAssistant("I was working on this when the stream died", toolCalls); err != nil {
		t.Fatal(err)
	}

	fresh, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("expected a restored conversation")
	}
	if got := messageRoles(fresh.messages); !reflect.DeepEqual(got, []string{"user", "assistant"}) {
		t.Fatalf("roles = %v", got)
	}
	last := fresh.messages[len(fresh.messages)-1]
	if !last.Truncated {
		t.Fatal("expected truncated flag")
	}
	if last.Content != "I was working on this when the stream died" {
		t.Fatalf("content = %q", last.Content)
	}
	if len(last.ToolCalls) == 0 {
		t.Fatal("expected tool_calls")
	}
	fn, _ := last.ToolCalls[0]["function"].(map[string]any)
	if fn["name"] != "browser_click" {
		t.Fatalf("tool call = %#v", last.ToolCalls[0])
	}
}

// Parity: core/tests/test_node_conversation.py::TestPartialCheckpoint::test_restore_cleans_stale_partials
func TestPartialCheckpointRestoreCleansStale(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "c"))
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "s", Store: store})
	if _, err := conv.AddUserMessage("hi", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("real", nil, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePartial(1, map[string]any{
		"seq": 1, "role": "assistant", "content": "stale", "truncated": true,
	}); err != nil {
		t.Fatal(err)
	}

	fresh, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("expected a restored conversation")
	}
	if got := messageContents(fresh.messages); !reflect.DeepEqual(got, []string{"hi", "real"}) {
		t.Fatalf("contents = %#v", got)
	}
	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("stale partials = %d, want 0", len(partials))
	}
}

func messageRoles(messages []Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Role
	}
	return out
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_over_keep_recent_clears_oldest_recoverably
func TestProactiveMicrocompactClearsOldest(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	keep := MicrocompactKeepRecent
	n := keep + 2
	for i := 0; i < n; i++ {
		payload := fmt.Sprintf("file_%d_contents: ", i) + strings.Repeat("x", 200)
		spill := fmt.Sprintf("/tmp/data/terminal_exec_%d.txt", i)
		addCompactableTurn(t, conv, fmt.Sprintf("call_%d", i), "terminal_exec", payload, &spill, false)
	}

	toolMsgs := toolMessages(conv)
	if len(toolMsgs) != n {
		t.Fatalf("tool msgs = %d, want %d", len(toolMsgs), n)
	}
	cleared := toolMsgs[:n-keep]
	kept := toolMsgs[n-keep:]
	for _, m := range cleared {
		if !strings.HasPrefix(m.Content, "Old tool result") {
			t.Fatalf("oldest not cleared: %q", m.Content[:minInt(len(m.Content), 80)])
		}
		if !strings.Contains(m.Content, "terminal_rg") || !strings.Contains(m.Content, "/tmp/data/terminal_exec_") {
			t.Fatalf("placeholder must cite a recovery path: %q", m.Content)
		}
	}
	for _, m := range kept {
		if !strings.HasPrefix(m.Content, "file_") {
			t.Fatalf("recent result should be intact: %q", m.Content[:minInt(len(m.Content), 80)])
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_pathless_results_are_never_cleared
func TestProactiveMicrocompactPathlessUntouched(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < MicrocompactKeepRecent+4; i++ {
		payload := fmt.Sprintf("result_%d ", i) + strings.Repeat("y", 200)
		addCompactableTurn(t, conv, fmt.Sprintf("call_%d", i), "terminal_exec", payload, nil, false)
	}
	for _, m := range toolMessages(conv) {
		if strings.HasPrefix(m.Content, "Old tool result") {
			t.Fatalf("path-less result must not be cleared: %q", m.Content[:minInt(len(m.Content), 80)])
		}
		if !strings.HasPrefix(m.Content, "result_") {
			t.Fatalf("content = %q", m.Content[:minInt(len(m.Content), 80)])
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_batch_within_keep_recent_all_survive
func TestProactiveMicrocompactBatchSurvives(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < MicrocompactKeepRecent; i++ {
		spill := fmt.Sprintf("/d/e_%d.txt", i)
		addCompactableTurn(t, conv, fmt.Sprintf("c%d", i), "terminal_exec", strings.Repeat("z", 200), &spill, false)
	}
	for _, m := range toolMessages(conv) {
		if strings.HasPrefix(m.Content, "Old tool result") {
			t.Fatalf("batch within keep count should survive: %q", m.Content[:minInt(len(m.Content), 80)])
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_metadata_spillover_path_preferred_over_text
func TestProactiveMicrocompactMetadataPathPreferred(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < MicrocompactKeepRecent+1; i++ {
		payload := fmt.Sprintf("payload_%d ", i) + strings.Repeat("x", 200)
		spill := fmt.Sprintf("/session/data/terminal_exec_%d.txt", i)
		addCompactableTurn(t, conv, fmt.Sprintf("c%d", i), "terminal_exec", payload, &spill, false)
	}
	oldest := toolMessages(conv)[0]
	if !strings.HasPrefix(oldest.Content, "Old tool result") {
		t.Fatalf("oldest not cleared: %q", oldest.Content)
	}
	if !strings.Contains(oldest.Content, "/session/data/terminal_exec_0.txt") {
		t.Fatalf("placeholder should cite metadata path: %q", oldest.Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_non_compactable_tool_does_not_trigger
func TestProactiveMicrocompactNonCompactable(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < 8; i++ {
		addCompactableTurn(t, conv, fmt.Sprintf("call_%d", i), "my_custom_irreversible_tool", strings.Repeat("x", 200), nil, false)
	}
	toolMsgs := toolMessages(conv)
	if len(toolMsgs) != 8 {
		t.Fatalf("tool msgs = %d, want 8", len(toolMsgs))
	}
	for _, m := range toolMsgs {
		if strings.HasPrefix(m.Content, "Old tool result") {
			t.Fatalf("non-compactable tool must not be cleared: %q", m.Content)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestProactiveMicrocompact::test_error_results_skip_microcompact
func TestProactiveMicrocompactSkipsErrors(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < 5; i++ {
		addCompactableTurn(t, conv, fmt.Sprintf("call_%d", i), "terminal_exec", strings.Repeat("ERR ", 50), nil, true)
	}
	for _, m := range toolMessages(conv) {
		if !m.IsError {
			t.Fatal("expected error result")
		}
		if strings.HasPrefix(m.Content, "Old tool result") {
			t.Fatalf("error result must not be cleared: %q", m.Content)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestSpilloverPathExtraction::test_new_format_at_path
func TestSpilloverPathExtractionNewFormat(t *testing.T) {
	content := "Old tool result (44,754 chars) at /tmp/data/web_scrape_1.txt. Use terminal_rg with a pattern against this path to recover specifics."
	got := ExtractSpilloverFilename(content)
	if got == nil || *got != "/tmp/data/web_scrape_1.txt" {
		t.Fatalf("got %v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestSpilloverPathExtraction::test_mid_saved_at_form
func TestSpilloverPathExtractionMidFormat(t *testing.T) {
	content := "Old tool result (44,754 chars) cleared from context. " +
		"Full data saved at: /tmp/data/web_scrape_1.txt\n" +
		"Read the complete data with read_file(path='/tmp/data/web_scrape_1.txt')."
	got := ExtractSpilloverFilename(content)
	if got == nil || *got != "/tmp/data/web_scrape_1.txt" {
		t.Fatalf("got %v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestSpilloverPathExtraction::test_legacy_bracketed_saved_to
func TestSpilloverPathExtractionLegacyFormat(t *testing.T) {
	content := "Some prose. [Saved to '/tmp/data/web_scrape_1.txt']"
	got := ExtractSpilloverFilename(content)
	if got == nil || *got != "/tmp/data/web_scrape_1.txt" {
		t.Fatalf("got %v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestSpilloverPathExtraction::test_no_spillover_returns_none
func TestSpilloverPathExtractionNoMatch(t *testing.T) {
	if got := ExtractSpilloverFilename("a small tool result"); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := ExtractSpilloverFilename("Old tool result (44,754 chars) cleared from context."); got != nil {
		t.Fatalf("got %v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMicrocompactRecoveryHint::test_placeholder_recommends_terminal_rg
func TestMicrocompactRecoveryHintUsesTerminalRg(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", MaxContextTokens: 10_000})
	for i := 0; i < MicrocompactKeepRecent+2; i++ {
		payload := fmt.Sprintf("file_%d_head_content ", i) + strings.Repeat("x", 200) +
			fmt.Sprintf("\nsaved at: /tmp/data/file_%d.txt", i)
		addCompactableTurn(t, conv, fmt.Sprintf("call_%d", i), "terminal_exec", payload, nil, false)
	}
	cleared := toolMessages(conv)[0]
	if !strings.Contains(cleared.Content, "terminal_rg") {
		t.Fatalf("expected terminal_rg recovery hint: %q", cleared.Content)
	}
	if strings.Contains(cleared.Content, "read_file") {
		t.Fatalf("should not push read_file: %q", cleared.Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessageFlags::test_is_system_reminder_roundtrip
func TestMessageFlagsSystemReminderRoundtrip(t *testing.T) {
	m := Message{Seq: 0, Role: RoleUser, Content: "reminder", IsSystemReminder: true}
	d := m.ToStorageDict()
	if d["is_system_reminder"] != true {
		t.Fatalf("storage dict = %#v", d)
	}
	r := MessageFromStorageDict(d)
	if !r.IsSystemReminder || r.Role != RoleUser {
		t.Fatalf("roundtrip = %#v", r)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessageFlags::test_is_system_reminder_loads_legacy_key
func TestMessageFlagsLegacyNudgeKey(t *testing.T) {
	r := MessageFromStorageDict(map[string]any{
		"seq": 0, "role": "user", "content": "x", "is_system_nudge": true,
	})
	if !r.IsSystemReminder {
		t.Fatal("expected legacy is_system_nudge to populate IsSystemReminder")
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessageFlags::test_truncated_roundtrip
func TestMessageFlagsTruncatedRoundtrip(t *testing.T) {
	m := Message{Seq: 0, Role: RoleAssistant, Content: "half", Truncated: true}
	d := m.ToStorageDict()
	if d["truncated"] != true {
		t.Fatalf("storage dict = %#v", d)
	}
	if r := MessageFromStorageDict(d); !r.Truncated {
		t.Fatal("expected truncated to survive the roundtrip")
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessageFlags::test_defaults_omit_flags_from_storage
func TestMessageFlagsDefaultsOmitted(t *testing.T) {
	d := Message{Seq: 0, Role: RoleUser, Content: "plain"}.ToStorageDict()
	if _, ok := d["is_system_reminder"]; ok {
		t.Fatalf("is_system_reminder should be omitted: %#v", d)
	}
	if _, ok := d["truncated"]; ok {
		t.Fatalf("truncated should be omitted: %#v", d)
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_small_message_untouched
func TestTruncateOversizedSmallUntouched(t *testing.T) {
	conv := NewNodeConversation("sys")
	conv.messages = append(conv.messages, Message{Seq: 0, Role: RoleUser, Content: "hello", IsClientInput: true})
	if clipped := TruncateOversizedUserMessages(conv); clipped != 0 {
		t.Fatalf("clipped = %d, want 0", clipped)
	}
	if conv.messages[0].Content != "hello" {
		t.Fatalf("content = %q", conv.messages[0].Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_oversized_user_message_clipped
func TestTruncateOversizedUserMessageClipped(t *testing.T) {
	conv := NewNodeConversation("sys")
	headMarker := strings.Repeat("USER_TYPED_THIS_PREFIX ", 50)
	tailMarker := "[Attachments saved to disk]\n- PDF: /path/to/doc.pdf"
	oversized := headMarker + strings.Repeat("FILLER", 100_000) + "\n" + tailMarker
	if len(oversized) <= UserMessageMaxChars {
		t.Fatalf("test fixture not oversized: %d", len(oversized))
	}
	conv.messages = append(conv.messages, Message{Seq: 0, Role: RoleUser, Content: oversized, IsClientInput: true})

	if clipped := TruncateOversizedUserMessages(conv); clipped != 1 {
		t.Fatalf("clipped = %d, want 1", clipped)
	}
	newContent := conv.messages[0].Content
	if len(newContent) >= UserMessageMaxChars {
		t.Fatalf("still oversized: %d", len(newContent))
	}
	if !strings.HasPrefix(newContent, headMarker) {
		t.Fatalf("head not preserved: %q", newContent[:minInt(len(newContent), 80)])
	}
	if !strings.HasSuffix(newContent, tailMarker) {
		t.Fatalf("tail not preserved")
	}
	if !strings.Contains(newContent, "<system-reminder>") || !strings.Contains(newContent, "chars truncated") {
		t.Fatalf("missing truncation notice: %q", newContent)
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_skips_system_reminder_messages
func TestTruncateOversizedSkipsSystemReminder(t *testing.T) {
	conv := NewNodeConversation("sys")
	huge := strings.Repeat("x", 500_000)
	conv.messages = append(conv.messages, Message{Seq: 0, Role: RoleUser, Content: huge, IsSystemReminder: true})
	if clipped := TruncateOversizedUserMessages(conv); clipped != 0 {
		t.Fatalf("clipped = %d, want 0", clipped)
	}
	if conv.messages[0].Content != huge {
		t.Fatal("system reminder should be untouched")
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_skips_skill_content_messages
func TestTruncateOversizedSkipsSkillContent(t *testing.T) {
	conv := NewNodeConversation("sys")
	hugeSkill := strings.Repeat("skill body ", 50_000)
	conv.messages = append(conv.messages, Message{Seq: 0, Role: RoleUser, Content: hugeSkill, IsSkillContent: true})
	if clipped := TruncateOversizedUserMessages(conv); clipped != 0 {
		t.Fatalf("clipped = %d, want 0", clipped)
	}
	if conv.messages[0].Content != hugeSkill {
		t.Fatal("skill content should be untouched")
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_skips_non_user_roles
func TestTruncateOversizedSkipsNonUserRoles(t *testing.T) {
	conv := NewNodeConversation("sys")
	huge := strings.Repeat("x", 500_000)
	toolID := "t1"
	conv.messages = append(conv.messages,
		Message{Seq: 0, Role: RoleAssistant, Content: huge},
		Message{Seq: 1, Role: RoleTool, Content: huge, ToolUseID: &toolID},
	)
	if clipped := TruncateOversizedUserMessages(conv); clipped != 0 {
		t.Fatalf("clipped = %d, want 0", clipped)
	}
	if conv.messages[0].Content != huge || conv.messages[1].Content != huge {
		t.Fatal("non-user messages should be untouched")
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_idempotent_on_already_clipped
func TestTruncateOversizedIdempotent(t *testing.T) {
	conv := NewNodeConversation("sys")
	conv.messages = append(conv.messages, Message{
		Seq: 0, Role: RoleUser, Content: strings.Repeat("x", 500_000), IsClientInput: true,
	})
	if first := TruncateOversizedUserMessages(conv); first != 1 {
		t.Fatalf("first = %d, want 1", first)
	}
	if second := TruncateOversizedUserMessages(conv); second != 0 {
		t.Fatalf("second = %d, want 0", second)
	}
}

// Parity: core/tests/test_node_conversation.py::TestTruncateOversizedUserMessages::test_calc_textbook_scenario
func TestTruncateOversizedCalcTextbook(t *testing.T) {
	conv := NewNodeConversation("sys")
	textbook := "[2026-05-21 16:40 PDT] teach me calculus 1\n\n" +
		"--- Attached file content ---\n" +
		strings.Repeat("[PDF page N] dense math content ", 50_000) +
		"\n\n[Attachments saved to disk]\n- PDF: data/attachments/X.pdf"
	conv.messages = append(conv.messages,
		Message{Seq: 0, Role: RoleUser, Content: textbook, IsClientInput: true},
		Message{Seq: 1, Role: RoleUser, Content: textbook, IsClientInput: true},
	)
	if clipped := TruncateOversizedUserMessages(conv); clipped != 2 {
		t.Fatalf("clipped = %d, want 2", clipped)
	}
	totalAfter := 0
	for _, m := range conv.messages {
		totalAfter += len(m.Content)
	}
	if totalAfter >= UserMessageMaxChars {
		t.Fatalf("total after = %d, want < %d", totalAfter, UserMessageMaxChars)
	}
	for _, m := range conv.messages {
		if !strings.Contains(m.Content, "teach me calculus 1") {
			t.Fatal("typed prompt not preserved")
		}
		if !strings.Contains(m.Content, "[Attachments saved to disk]") {
			t.Fatal("attachments hint not preserved")
		}
	}
}

var (
	hoistImgA = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,AAA"}}
	hoistImgB = map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/jpeg;base64,BBB"}}
)

func screenshotTurn(t *testing.T, model string) []map[string]any {
	t.Helper()
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("take a screenshot", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName("c1", "browser_screenshot")}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c1", `{"ok": true}`, ToolResultOptions{ImageContent: []map[string]any{hoistImgA}}); err != nil {
		t.Fatal(err)
	}
	return conv.ToLLMMessages(model)
}

func findToolMessage(msgs []map[string]any) map[string]any {
	for _, m := range msgs {
		if m["role"] == RoleTool {
			return m
		}
	}
	return nil
}

func contentBlockSlice(t *testing.T, content any) []map[string]any {
	t.Helper()
	blocks, ok := content.([]map[string]any)
	if !ok {
		t.Fatalf("content is %T, want []map[string]any", content)
	}
	return blocks
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_openai_model_receives_image_as_user_message
func TestHoistToolResultImagesOpenAIModel(t *testing.T) {
	msgs := screenshotTurn(t, "openai/gpt-6-astra")

	toolMsg := findToolMessage(msgs)
	if toolMsg == nil {
		t.Fatal("no tool message")
	}
	if toolMsg["content"] != `{"ok": true}` {
		t.Fatalf("tool content = %#v", toolMsg["content"])
	}

	hoisted := msgs[len(msgs)-1]
	if hoisted["role"] != RoleUser {
		t.Fatalf("last role = %v", hoisted["role"])
	}
	blocks := contentBlockSlice(t, hoisted["content"])
	if len(blocks) < 2 || !reflect.DeepEqual(blocks[1], hoistImgA) {
		t.Fatalf("hoisted blocks = %#v", blocks)
	}
	label, _ := blocks[0]["text"].(string)
	if !strings.Contains(label, "browser_screenshot") {
		t.Fatalf("label = %q", label)
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_anthropic_model_keeps_image_in_tool_result
func TestHoistToolResultImagesAnthropicModel(t *testing.T) {
	msgs := screenshotTurn(t, "anthropic/claude-opus-4-6")

	toolMsg := findToolMessage(msgs)
	blocks := contentBlockSlice(t, toolMsg["content"])
	if len(blocks) < 2 || !reflect.DeepEqual(blocks[1], hoistImgA) {
		t.Fatalf("tool blocks = %#v", blocks)
	}
	for _, m := range msgs {
		if m["role"] == RoleUser {
			if _, isList := m["content"].([]map[string]any); isList {
				t.Fatalf("unexpected user content blocks: %#v", m["content"])
			}
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_no_model_leaves_shape_untouched
func TestHoistToolResultImagesNoModel(t *testing.T) {
	msgs := screenshotTurn(t, "")
	toolMsg := findToolMessage(msgs)
	blocks := contentBlockSlice(t, toolMsg["content"])
	if len(blocks) < 2 || !reflect.DeepEqual(blocks[1], hoistImgA) {
		t.Fatalf("tool blocks = %#v", blocks)
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_parallel_tool_results_stay_contiguous
func TestHoistToolResultImagesParallelContiguous(t *testing.T) {
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("screenshot both tabs", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{
		toolCallWithName("c1", "browser_screenshot"),
		toolCallWithName("c2", "attach_file"),
	}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c1", "tab one", ToolResultOptions{ImageContent: []map[string]any{hoistImgA}}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c2", "tab two", ToolResultOptions{ImageContent: []map[string]any{hoistImgB}}); err != nil {
		t.Fatal(err)
	}

	msgs := conv.ToLLMMessages("openai/gpt-6-astra")
	if got := rolesOf(msgs); !reflect.DeepEqual(got, []string{"user", "assistant", "tool", "tool", "user"}) {
		t.Fatalf("roles = %v", got)
	}
	blocks := contentBlockSlice(t, msgs[len(msgs)-1]["content"])
	if !reflect.DeepEqual(blocks[1:], []map[string]any{hoistImgA, hoistImgB}) {
		t.Fatalf("hoisted images = %#v", blocks[1:])
	}
	label, _ := blocks[0]["text"].(string)
	if !strings.Contains(label, "browser_screenshot (1)") || !strings.Contains(label, "attach_file (1)") {
		t.Fatalf("label = %q", label)
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_hoisted_images_merge_with_a_following_user_message
func TestHoistToolResultImagesMergeFollowingUser(t *testing.T) {
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("look", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName("c1", "browser_screenshot")}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c1", "ok", ToolResultOptions{ImageContent: []map[string]any{hoistImgA}}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddUserMessage("what do you see?", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}

	msgs := conv.ToLLMMessages("openai/gpt-6-astra")
	if got := rolesOf(msgs); !reflect.DeepEqual(got, []string{"user", "assistant", "tool", "user"}) {
		t.Fatalf("roles = %v", got)
	}
	blocks := contentBlockSlice(t, msgs[len(msgs)-1]["content"])
	found := false
	for _, b := range blocks {
		if reflect.DeepEqual(b, hoistImgA) {
			found = true
		}
	}
	if !found {
		t.Fatalf("image not hoisted: %#v", blocks)
	}
	if blocks[len(blocks)-1]["text"] != "what do you see?" {
		t.Fatalf("last block = %#v", blocks[len(blocks)-1])
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_text_only_tool_results_are_untouched
func TestHoistToolResultImagesTextOnly(t *testing.T) {
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("run it", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName("c1", "terminal_exec")}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c1", "exit 0", ToolResultOptions{}); err != nil {
		t.Fatal(err)
	}
	openai := conv.ToLLMMessages("openai/gpt-6-astra")
	anthropic := conv.ToLLMMessages("anthropic/claude-opus-4-6")
	if !reflect.DeepEqual(openai, anthropic) {
		t.Fatalf("shapes differ:\nopenai=%#v\nanthropic=%#v", openai, anthropic)
	}
}

// Parity: core/tests/test_node_conversation.py::TestHoistToolResultImages::test_stored_image_content_survives_hoisting
func TestHoistToolResultImagesStoredContentSurvives(t *testing.T) {
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("look", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName("c1", "browser_screenshot")}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddToolResult("c1", "ok", ToolResultOptions{ImageContent: []map[string]any{hoistImgA}}); err != nil {
		t.Fatal(err)
	}

	conv.ToLLMMessages("openai/gpt-6-astra")

	last := conv.messages[len(conv.messages)-1]
	if !reflect.DeepEqual(last.ImageContent, []map[string]any{hoistImgA}) {
		t.Fatalf("stored image content = %#v", last.ImageContent)
	}
}

// Parity-exempt: ChatMessage.ContentParts is the Go transport seam; upstream
// hands the same blocks to LiteLLM, which has no Go-side test target.
func TestToChatMessagesPreservesImageBlocks(t *testing.T) {
	msgs := screenshotTurn(t, "anthropic/claude-opus-4-6")
	chats := toChatMessages(msgs)

	var toolChat *llm.ChatMessage
	for i := range chats {
		if chats[i].Role == RoleTool {
			toolChat = &chats[i]
		}
	}
	if toolChat == nil {
		t.Fatal("no tool message")
	}
	if len(toolChat.ContentParts) == 0 {
		t.Fatalf("tool ContentParts empty; image blocks were flattened away")
	}
	if !reflect.DeepEqual(toolChat.ContentParts[1], hoistImgA) {
		t.Fatalf("tool ContentParts = %#v", toolChat.ContentParts)
	}
	if toolChat.Content != `{"ok": true}` {
		t.Fatalf("tool text projection = %q", toolChat.Content)
	}
}

// Parity-exempt: the capability gate serializes blocks for text-only models,
// a Go-side replacement for upstream's caption subagent; upstream has no test
// for an explicit not-delivered placeholder.
func TestToChatMessagesGatesImageForTextOnlyModel(t *testing.T) {
	msgs := screenshotTurn(t, "deepseek-v4-pro")
	serialized, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "image_url") {
		t.Fatalf("image block survived for a text-only model: %s", serialized)
	}
	if !strings.Contains(string(serialized), "not delivered") {
		t.Fatalf("no explicit not-delivered note: %s", serialized)
	}
	chats := toChatMessages(msgs)
	for _, chat := range chats {
		if len(chat.ContentParts) == 0 {
			continue
		}
		for _, block := range chat.ContentParts {
			if block["type"] == "image_url" {
				t.Fatalf("gated chat still carries an image block: %#v", block)
			}
		}
	}
}

// Parity-exempt: PDF capability gating is Go-side; upstream relies on LiteLLM
// to remap the file block and has no unsupported-transport test.
func TestToChatMessagesGatesPDFForUnsupportedModel(t *testing.T) {
	conv := NewNodeConversation("sys")
	if _, err := conv.AddUserMessage("read the pdf", UserMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.AddAssistantMessage("", []map[string]any{toolCallWithName("c1", "attach_file")}, AssistantMessageOptions{}); err != nil {
		t.Fatal(err)
	}
	pdfBlock := map[string]any{
		"type": "file",
		"file": map[string]any{
			"file_data": "data:application/pdf;base64,JVBERi0=",
			"filename":  "doc.pdf",
		},
	}
	if _, err := conv.AddToolResult("c1", `{"attached": []}`, ToolResultOptions{ImageContent: []map[string]any{pdfBlock}}); err != nil {
		t.Fatal(err)
	}

	unsupported := conv.ToLLMMessages("ollama/llama3")
	serialized, err := json.Marshal(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "application/pdf") {
		t.Fatalf("pdf block survived for a model that cannot take it: %s", serialized)
	}
	if !strings.Contains(string(serialized), "attachment not delivered") {
		t.Fatalf("no explicit pdf not-delivered note: %s", serialized)
	}

	supported := conv.ToLLMMessages("openai/gpt-5.4")
	found := false
	for _, m := range supported {
		for _, block := range contentBlocks(m["content"]) {
			if block["type"] == "file" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("openai model lost the pdf block: %#v", supported)
	}
	chats := toChatMessages(supported)
	pdfParts := 0
	for _, chat := range chats {
		for _, block := range chat.ContentParts {
			if block["type"] == "file" {
				pdfParts++
			}
		}
	}
	if pdfParts != 1 {
		t.Fatalf("pdf ContentParts = %d, want 1", pdfParts)
	}
}
