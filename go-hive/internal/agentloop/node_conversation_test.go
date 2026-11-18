package agentloop

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sampleToolCalls() []map[string]any {
	return []map[string]any{
		{
			"id":   "call_1",
			"type": "function",
			"function": map[string]any{
				"name":      "get_weather",
				"arguments": `{"city":"SF"}`,
			},
		},
	}
}

func testIntPtr(v int) *int { return &v }

func testFloatPtr(v float64) *float64 { return &v }

func mustAddUser(t *testing.T, conv *NodeConversation, content string, opts UserMessageOptions) {
	t.Helper()
	if _, err := conv.AddUserMessage(content, opts); err != nil {
		t.Fatalf("AddUserMessage(%q): %v", content, err)
	}
}

func mustAddAssistant(t *testing.T, conv *NodeConversation, content string, toolCalls []map[string]any, opts AssistantMessageOptions) {
	t.Helper()
	if _, err := conv.AddAssistantMessage(content, toolCalls, opts); err != nil {
		t.Fatalf("AddAssistantMessage(%q): %v", content, err)
	}
}

func mustAddTool(t *testing.T, conv *NodeConversation, toolUseID, content string, opts ToolResultOptions) {
	t.Helper()
	if _, err := conv.AddToolResult(toolUseID, content, opts); err != nil {
		t.Fatalf("AddToolResult(%q): %v", toolUseID, err)
	}
}

func messageContents(messages []Message) []string {
	out := make([]string, len(messages))
	for i, m := range messages {
		out[i] = m.Content
	}
	return out
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_user_and_assistant_to_llm_dict
func TestMessageUserAndAssistantToLLMDict(t *testing.T) {
	got := Message{Seq: 0, Role: RoleUser, Content: "hi"}.ToLLMDict()
	want := map[string]any{"role": "user", "content": "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user dict = %#v, want %#v", got, want)
	}

	got = Message{Seq: 0, Role: RoleAssistant, Content: "hello"}.ToLLMDict()
	want = map[string]any{"role": "assistant", "content": "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assistant dict = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_assistant_to_llm_dict_with_tools
func TestMessageAssistantToLLMDictWithTools(t *testing.T) {
	calls := sampleToolCalls()
	dict := Message{Seq: 0, Role: RoleAssistant, Content: "", ToolCalls: calls}.ToLLMDict()
	if dict["role"] != "assistant" {
		t.Fatalf("role = %v, want assistant", dict["role"])
	}
	if !reflect.DeepEqual(dict["tool_calls"], calls) {
		t.Fatalf("tool_calls = %#v, want %#v", dict["tool_calls"], calls)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_tool_to_llm_dict
func TestMessageToolToLLMDict(t *testing.T) {
	got := Message{Seq: 0, Role: RoleTool, Content: "sunny", ToolUseID: strPtrTest("call_1")}.ToLLMDict()
	want := map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "sunny"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool dict = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_tool_error_to_llm_dict
func TestMessageToolErrorToLLMDict(t *testing.T) {
	dict := Message{Seq: 0, Role: RoleTool, Content: "not found", ToolUseID: strPtrTest("call_1"), IsError: true}.ToLLMDict()
	if dict["content"] != "ERROR: not found" {
		t.Fatalf("content = %v, want ERROR: not found", dict["content"])
	}
	if dict["tool_call_id"] != "call_1" {
		t.Fatalf("tool_call_id = %v, want call_1", dict["tool_call_id"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_storage_roundtrip
func TestMessageStorageRoundtrip(t *testing.T) {
	original := Message{Seq: 5, Role: RoleAssistant, Content: "ok", ToolCalls: sampleToolCalls()}
	restored := MessageFromStorageDict(original.ToStorageDict())
	if restored.Seq != original.Seq || restored.Role != original.Role || restored.Content != original.Content {
		t.Fatalf("restored = %#v, want seq/role/content matching %#v", restored, original)
	}
	if !reflect.DeepEqual(restored.ToolCalls, original.ToolCalls) {
		t.Fatalf("tool_calls = %#v, want %#v", restored.ToolCalls, original.ToolCalls)
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_storage_dict_edge_cases
func TestMessageStorageDictEdgeCases(t *testing.T) {
	original := Message{Seq: 1, Role: RoleTool, Content: "fail", ToolUseID: strPtrTest("c1"), IsError: true}
	dict := original.ToStorageDict()
	if dict["is_error"] != true {
		t.Fatalf("is_error = %v, want true", dict["is_error"])
	}
	if !MessageFromStorageDict(dict).IsError {
		t.Fatal("restored is_error should be true")
	}

	plain := Message{Seq: 0, Role: RoleUser, Content: "hi"}.ToStorageDict()
	for _, key := range []string{"tool_use_id", "tool_calls", "is_error"} {
		if _, ok := plain[key]; ok {
			t.Fatalf("%q should be omitted from storage dict, got %#v", key, plain)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_thinking_blocks_echoed_back_in_llm_dict
func TestMessageThinkingBlocksEchoedBack(t *testing.T) {
	blocks := []map[string]any{{"type": "thinking", "thinking": "weighing options", "signature": "SIG-XYZ"}}

	dict := Message{Seq: 0, Role: RoleAssistant, Content: "answer", ThinkingBlocks: blocks}.ToLLMDict()
	if !reflect.DeepEqual(dict["thinking_blocks"], blocks) {
		t.Fatalf("thinking_blocks = %#v, want %#v", dict["thinking_blocks"], blocks)
	}

	withTools := Message{Seq: 1, Role: RoleAssistant, Content: "", ToolCalls: sampleToolCalls(), ThinkingBlocks: blocks}
	if !reflect.DeepEqual(withTools.ToLLMDict()["thinking_blocks"], blocks) {
		t.Fatal("thinking_blocks should survive alongside tool calls")
	}

	if _, ok := (Message{Seq: 2, Role: RoleAssistant, Content: "hi"}).ToLLMDict()["thinking_blocks"]; ok {
		t.Fatal("thinking_blocks should be absent for non-reasoning turns")
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_thinking_blocks_survive_storage_roundtrip
func TestMessageThinkingBlocksStorageRoundtrip(t *testing.T) {
	blocks := []map[string]any{{"type": "thinking", "thinking": "step one", "signature": "SIG-1"}}
	original := Message{Seq: 7, Role: RoleAssistant, Content: "ok", ThinkingBlocks: blocks}
	restored := MessageFromStorageDict(original.ToStorageDict())
	if !reflect.DeepEqual(restored.ThinkingBlocks, blocks) {
		t.Fatalf("thinking_blocks = %#v, want %#v", restored.ThinkingBlocks, blocks)
	}

	if _, ok := (Message{Seq: 8, Role: RoleAssistant, Content: "x"}).ToStorageDict()["thinking_blocks"]; ok {
		t.Fatal("thinking_blocks should be omitted when absent")
	}
}

// Parity: core/tests/test_node_conversation.py::TestMessage::test_reasoning_content_echoed_back_and_stored
func TestMessageReasoningContentEchoedAndStored(t *testing.T) {
	original := Message{
		Seq: 0, Role: RoleAssistant, Content: "", ToolCalls: sampleToolCalls(),
		ReasoningContent: strPtrTest("check SF first"),
	}
	if original.ToLLMDict()["reasoning_content"] != "check SF first" {
		t.Fatalf("reasoning_content = %v, want check SF first", original.ToLLMDict()["reasoning_content"])
	}
	restored := MessageFromStorageDict(original.ToStorageDict())
	if restored.ReasoningContent == nil || *restored.ReasoningContent != "check SF first" {
		t.Fatalf("restored reasoning_content = %v, want check SF first", restored.ReasoningContent)
	}

	plain := Message{Seq: 1, Role: RoleAssistant, Content: "hi"}
	if _, ok := plain.ToLLMDict()["reasoning_content"]; ok {
		t.Fatal("reasoning_content should be absent from llm dict")
	}
	if _, ok := plain.ToStorageDict()["reasoning_content"]; ok {
		t.Fatal("reasoning_content should be absent from storage dict")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_multi_turn_build_and_export
func TestNodeConversationMultiTurnBuildAndExport(t *testing.T) {
	conv := NewNodeConversation("You are helpful.")
	mustAddUser(t, conv, "hello", UserMessageOptions{})
	mustAddAssistant(t, conv, "hi there", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "weather?", UserMessageOptions{})
	mustAddAssistant(t, conv, "", sampleToolCalls(), AssistantMessageOptions{})
	mustAddTool(t, conv, "call_1", "sunny", ToolResultOptions{})
	mustAddAssistant(t, conv, "It's sunny!", nil, AssistantMessageOptions{})

	if conv.TurnCount() != 2 {
		t.Fatalf("turn_count = %d, want 2", conv.TurnCount())
	}
	if conv.MessageCount() != 6 {
		t.Fatalf("message_count = %d, want 6", conv.MessageCount())
	}
	llm := conv.ToLLMMessages("")
	if len(llm) != 6 {
		t.Fatalf("llm length = %d, want 6", len(llm))
	}
	if llm[0]["role"] != "user" {
		t.Fatalf("llm[0] role = %v, want user", llm[0]["role"])
	}
	if !reflect.DeepEqual(llm[3]["tool_calls"], sampleToolCalls()) {
		t.Fatalf("llm[3] tool_calls = %#v, want %#v", llm[3]["tool_calls"], sampleToolCalls())
	}

	summary := conv.ExportSummary()
	if !strings.Contains(summary, "turns: 2") {
		t.Fatalf("summary missing 'turns: 2': %q", summary)
	}
	if !strings.Contains(summary, "messages: 6") {
		t.Fatalf("summary missing 'messages: 6': %q", summary)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_reasoning_goes_back_only_within_the_turn
func TestNodeConversationReasoningOnlyWithinTurn(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "weather?", UserMessageOptions{})
	mustAddAssistant(t, conv, "", sampleToolCalls(), AssistantMessageOptions{ReasoningContent: strPtrTest("look up SF")})
	mustAddTool(t, conv, "call_1", "sunny", ToolResultOptions{})
	mustAddAssistant(t, conv, "It's sunny!", nil, AssistantMessageOptions{ReasoningContent: strPtrTest("sunny means no coat")})

	llm := conv.ToLLMMessages("")
	reasoning := []string{}
	for _, m := range llm {
		if m["role"] == RoleAssistant {
			if value, ok := m["reasoning_content"].(string); ok {
				reasoning = append(reasoning, value)
			}
		}
	}
	if !reflect.DeepEqual(reasoning, []string{"look up SF", "sunny means no coat"}) {
		t.Fatalf("reasoning = %#v, want [look up SF, sunny means no coat]", reasoning)
	}

	mustAddUser(t, conv, "and tomorrow?", UserMessageOptions{})
	for _, m := range conv.ToLLMMessages("") {
		if _, ok := m["reasoning_content"]; ok {
			t.Fatalf("reasoning_content should be dropped after a new user turn: %#v", m)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_system_prompt_excluded_from_messages
func TestNodeConversationSystemPromptExcluded(t *testing.T) {
	conv := NewNodeConversation("secret")
	mustAddUser(t, conv, "hi", UserMessageOptions{})
	llm := conv.ToLLMMessages("")
	if len(llm) != 1 {
		t.Fatalf("llm length = %d, want 1", len(llm))
	}
	for _, m := range llm {
		if strings.Contains(fmt.Sprint(m), "secret") {
			t.Fatalf("system prompt leaked into messages: %#v", m)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_turn_and_seq_counting
func TestNodeConversationTurnAndSeqCounting(t *testing.T) {
	conv := NewNodeConversation("")
	if conv.TurnCount() != 0 || conv.NextSeq() != 0 {
		t.Fatalf("initial turn_count=%d next_seq=%d, want 0/0", conv.TurnCount(), conv.NextSeq())
	}
	mustAddUser(t, conv, "a", UserMessageOptions{})
	if conv.TurnCount() != 1 || conv.NextSeq() != 1 {
		t.Fatalf("after user: turn_count=%d next_seq=%d, want 1/1", conv.TurnCount(), conv.NextSeq())
	}
	mustAddAssistant(t, conv, "b", nil, AssistantMessageOptions{})
	if conv.TurnCount() != 1 || conv.NextSeq() != 2 {
		t.Fatalf("after assistant: turn_count=%d next_seq=%d, want 1/2", conv.TurnCount(), conv.NextSeq())
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_token_estimation
func TestNodeConversationTokenEstimation(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, strings.Repeat("a", 400), UserMessageOptions{})
	if got := conv.EstimateTokens(); got != 400/3 {
		t.Fatalf("estimate_tokens = %d, want %d", got, 400/3)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_update_token_count_overrides_estimate
func TestNodeConversationUpdateTokenCountOverridesEstimate(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, strings.Repeat("a", 400), UserMessageOptions{})
	if got := conv.EstimateTokens(); got != 400/3 {
		t.Fatalf("char-based estimate = %d, want %d", got, 400/3)
	}
	conv.UpdateTokenCount(500)
	if got := conv.EstimateTokens(); got != 500 {
		t.Fatalf("estimate after update = %d, want 500", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_update_token_count_reflects_last_call_not_sum
func TestNodeConversationUpdateTokenCountReflectsLastCall(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{MaxContextTokens: 180_000})

	conv.UpdateTokenCount(40_000)
	if got := conv.EstimateTokens(); got != 40_000 {
		t.Fatalf("estimate = %d, want 40000", got)
	}
	if conv.UsageRatio() >= 1.0 {
		t.Fatalf("usage_ratio = %f, want < 1.0", conv.UsageRatio())
	}

	conv.UpdateTokenCount(50_000)
	if got := conv.EstimateTokens(); got != 50_000 {
		t.Fatalf("estimate = %d, want 50000 (not summed)", got)
	}

	for _, size := range []int{60_000, 70_000, 80_000, 90_000} {
		conv.UpdateTokenCount(size)
	}
	if got := conv.EstimateTokens(); got != 90_000 {
		t.Fatalf("estimate = %d, want 90000", got)
	}
	if diff := conv.UsageRatio() - 90_000.0/180_000.0; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("usage_ratio = %f, want %f", conv.UsageRatio(), 90_000.0/180_000.0)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_resets_token_count
func TestNodeConversationCompactResetsTokenCount(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, strings.Repeat("a", 400), UserMessageOptions{})
	conv.UpdateTokenCount(500)
	if got := conv.EstimateTokens(); got != 500 {
		t.Fatalf("estimate = %d, want 500", got)
	}
	if err := conv.Compact("summary", CompactOptions{KeepRecent: testIntPtr(0)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := conv.EstimateTokens(); got != len("summary")/3 {
		t.Fatalf("estimate after compact = %d, want %d", got, len("summary")/3)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_clear_resets_token_count
func TestNodeConversationClearResetsTokenCount(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "hello", UserMessageOptions{})
	conv.UpdateTokenCount(1000)
	if got := conv.EstimateTokens(); got != 1000 {
		t.Fatalf("estimate = %d, want 1000", got)
	}
	if err := conv.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got := conv.EstimateTokens(); got != 0 {
		t.Fatalf("estimate after clear = %d, want 0", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_usage_ratio
func TestNodeConversationUsageRatio(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{MaxContextTokens: 1000})
	mustAddUser(t, conv, strings.Repeat("a", 400), UserMessageOptions{})
	want := float64(400/3) / 1000.0
	if diff := conv.UsageRatio() - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("usage_ratio = %f, want %f", conv.UsageRatio(), want)
	}
	conv.UpdateTokenCount(800)
	if diff := conv.UsageRatio() - 0.8; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("usage_ratio = %f, want 0.8", conv.UsageRatio())
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_usage_ratio_zero_budget
func TestNodeConversationUsageRatioZeroBudget(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{MaxContextTokens: 0, UnlimitedContext: true})
	mustAddUser(t, conv, strings.Repeat("a", 400), UserMessageOptions{})
	if conv.UsageRatio() != 0.0 {
		t.Fatalf("usage_ratio = %f, want 0.0", conv.UsageRatio())
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_needs_compaction_with_actual_tokens
func TestNodeConversationNeedsCompactionWithActualTokens(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens: 1000, CompactionThreshold: 0.8,
	})
	mustAddUser(t, conv, strings.Repeat("a", 100), UserMessageOptions{})
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be false")
	}
	conv.UpdateTokenCount(850)
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true at 850 tokens")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_needs_compaction
func TestNodeConversationNeedsCompaction(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens: 100, CompactionThreshold: 0.8,
	})
	mustAddUser(t, conv, strings.Repeat("x", 320), UserMessageOptions{})
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_needs_compaction_uses_buffer_when_set
func TestNodeConversationNeedsCompactionUsesBuffer(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens:       1000,
		CompactionThreshold:    0.9,
		CompactionBufferTokens: testIntPtr(300),
	})
	conv.UpdateTokenCount(650)
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be false below the 700 budget")
	}
	conv.UpdateTokenCount(700)
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true at the 700 budget")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compaction_warning_fires_before_hard_trigger
func TestNodeConversationCompactionWarningFiresEarly(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens:              1000,
		CompactionBufferTokens:        testIntPtr(200),
		CompactionWarningBufferTokens: testIntPtr(400),
	})
	conv.UpdateTokenCount(500)
	if conv.CompactionWarning() {
		t.Fatal("warning should be false at 500")
	}
	if conv.NeedsCompaction() {
		t.Fatal("compaction should be false at 500")
	}

	conv.UpdateTokenCount(650)
	if !conv.CompactionWarning() {
		t.Fatal("warning should be true at 650")
	}
	if conv.NeedsCompaction() {
		t.Fatal("compaction should be false at 650")
	}

	conv.UpdateTokenCount(820)
	if !conv.CompactionWarning() {
		t.Fatal("warning should be true at 820")
	}
	if !conv.NeedsCompaction() {
		t.Fatal("compaction should be true at 820")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_needs_compaction_uses_hybrid_buffer
func TestNodeConversationNeedsCompactionHybridBuffer(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens:       1000,
		CompactionBufferTokens: testIntPtr(200),
		CompactionBufferRatio:  testFloatPtr(0.1),
	})
	conv.UpdateTokenCount(650)
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be false at 650")
	}
	conv.UpdateTokenCount(700)
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true at 700")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_needs_compaction_ratio_only
func TestNodeConversationNeedsCompactionRatioOnly(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens:      1000,
		CompactionBufferRatio: testFloatPtr(0.25),
	})
	conv.UpdateTokenCount(740)
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be false at 740")
	}
	conv.UpdateTokenCount(760)
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true at 760")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_legacy_threshold_rule_still_works_without_buffer
func TestNodeConversationLegacyThresholdRule(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		MaxContextTokens: 1000, CompactionThreshold: 0.75,
	})
	conv.UpdateTokenCount(700)
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be false at 700")
	}
	conv.UpdateTokenCount(800)
	if !conv.NeedsCompaction() {
		t.Fatal("needs_compaction should be true at 800")
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_replaces_with_summary
func TestNodeConversationCompactReplacesWithSummary(t *testing.T) {
	empty := NewNodeConversation("")
	if err := empty.Compact("summary", CompactOptions{}); err != nil {
		t.Fatalf("Compact on empty: %v", err)
	}
	if empty.TurnCount() != 0 {
		t.Fatalf("turn_count = %d, want 0", empty.TurnCount())
	}

	conv := NewNodeConversation("")
	mustAddUser(t, conv, "one", UserMessageOptions{})
	mustAddAssistant(t, conv, "two", nil, AssistantMessageOptions{})
	seqBefore := conv.NextSeq()

	if err := conv.Compact("summary of conversation", CompactOptions{KeepRecent: testIntPtr(0)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if conv.TurnCount() != 1 {
		t.Fatalf("turn_count = %d, want 1", conv.TurnCount())
	}
	if conv.MessageCount() != 1 {
		t.Fatalf("message_count = %d, want 1", conv.MessageCount())
	}
	if conv.messages[0].Content != "summary of conversation" {
		t.Fatalf("summary content = %q", conv.messages[0].Content)
	}
	if conv.messages[0].Role != RoleUser {
		t.Fatalf("summary role = %q, want user", conv.messages[0].Role)
	}
	if conv.messages[0].Seq != seqBefore {
		t.Fatalf("summary seq = %d, want %d", conv.messages[0].Seq, seqBefore)
	}
	if conv.NextSeq() != seqBefore+1 {
		t.Fatalf("next_seq = %d, want %d", conv.NextSeq(), seqBefore+1)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_keep_recent_default
func TestNodeConversationCompactKeepRecentDefault(t *testing.T) {
	conv := NewNodeConversation("")
	for _, pair := range [][2]string{{"m1", "m2"}, {"m3", "m4"}, {"m5", "m6"}} {
		mustAddUser(t, conv, pair[0], UserMessageOptions{})
		mustAddAssistant(t, conv, pair[1], nil, AssistantMessageOptions{})
	}
	if err := conv.Compact("summary of early conversation", CompactOptions{}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if conv.MessageCount() != 3 {
		t.Fatalf("message_count = %d, want 3", conv.MessageCount())
	}
	contents := messageContents(conv.messages)
	want := []string{"summary of early conversation", "m5", "m6"}
	if !reflect.DeepEqual(contents, want) {
		t.Fatalf("contents = %#v, want %#v", contents, want)
	}
	if conv.messages[0].Role != RoleUser {
		t.Fatalf("summary role = %q, want user", conv.messages[0].Role)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_keep_recent_clamped
func TestNodeConversationCompactKeepRecentClamped(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "a", UserMessageOptions{})
	mustAddAssistant(t, conv, "b", nil, AssistantMessageOptions{})
	if err := conv.Compact("summary", CompactOptions{KeepRecent: testIntPtr(5)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if conv.MessageCount() != 2 {
		t.Fatalf("message_count = %d, want 2", conv.MessageCount())
	}
	contents := messageContents(conv.messages)
	if !reflect.DeepEqual(contents, []string{"summary", "b"}) {
		t.Fatalf("contents = %#v, want [summary b]", contents)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_preserves_output_keys
func TestNodeConversationCompactPreservesOutputKeys(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"score", "status"}})
	mustAddUser(t, conv, "process this", UserMessageOptions{})
	mustAddAssistant(t, conv, "score: 87", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "status = complete", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "next question", UserMessageOptions{})

	if err := conv.Compact("conversation summary", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	summary := conv.messages[0].Content
	for _, want := range []string{"PRESERVED VALUES", "score: 87", "status: complete", "CONVERSATION SUMMARY:", "conversation summary"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %q", want, summary)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_seq_arithmetic_with_keep_recent
func TestNodeConversationCompactSeqArithmetic(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "m1", UserMessageOptions{})
	mustAddAssistant(t, conv, "m2", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "m3", UserMessageOptions{})
	mustAddAssistant(t, conv, "m4", nil, AssistantMessageOptions{})

	if err := conv.Compact("summary", CompactOptions{KeepRecent: testIntPtr(2)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	wantSeqs := []int{1, 2, 3}
	gotSeqs := make([]int, len(conv.messages))
	for i, m := range conv.messages {
		gotSeqs[i] = m.Seq
	}
	if !reflect.DeepEqual(gotSeqs, wantSeqs) {
		t.Fatalf("seqs = %#v, want %#v", gotSeqs, wantSeqs)
	}
	if conv.NextSeq() != 4 {
		t.Fatalf("next_seq = %d, want 4", conv.NextSeq())
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_preserves_client_input_messages
func TestNodeConversationCompactPreservesClientInput(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "original task: chart ISRG", UserMessageOptions{IsClientInput: true})
	mustAddAssistant(t, conv, "starting work", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "[system nudge — continue]", UserMessageOptions{})
	mustAddAssistant(t, conv, "continuing", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "also check OSCR", UserMessageOptions{IsClientInput: true})
	mustAddAssistant(t, conv, "acknowledged", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "most recent", nil, AssistantMessageOptions{})

	if err := conv.Compact("LLM summary text", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	roles := make([]string, len(conv.messages))
	for i, m := range conv.messages {
		roles[i] = m.Role
	}
	if !reflect.DeepEqual(roles, []string{"user", "user", "user", "assistant"}) {
		t.Fatalf("roles = %#v, want [user user user assistant]", roles)
	}
	contents := messageContents(conv.messages)
	want := []string{"original task: chart ISRG", "also check OSCR", "LLM summary text", "most recent"}
	if !reflect.DeepEqual(contents, want) {
		t.Fatalf("contents = %#v, want %#v", contents, want)
	}
	for _, c := range contents {
		if strings.Contains(c, "[system nudge") {
			t.Fatalf("system nudge survived compaction: %q", c)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_client_input_preserved_across_chained_compactions
func TestNodeConversationCompactClientInputAcrossChained(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "ORIGINAL", UserMessageOptions{IsClientInput: true})
	for i := 0; i < 6; i++ {
		mustAddAssistant(t, conv, fmt.Sprintf("work %d", i), nil, AssistantMessageOptions{})
	}
	if err := conv.Compact("first summary", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	if got := messageContents(conv.messages); !reflect.DeepEqual(got, []string{"ORIGINAL", "first summary", "work 5"}) {
		t.Fatalf("after first compact = %#v", got)
	}

	for i := 0; i < 6; i++ {
		mustAddAssistant(t, conv, fmt.Sprintf("more %d", i), nil, AssistantMessageOptions{})
	}
	if err := conv.Compact("second summary", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("second Compact: %v", err)
	}
	contents := messageContents(conv.messages)
	for _, c := range contents {
		if c == "first summary" {
			t.Fatalf("first summary was re-preserved: %#v", contents)
		}
	}
	if !reflect.DeepEqual(contents, []string{"ORIGINAL", "second summary", "more 5"}) {
		t.Fatalf("after second compact = %#v", contents)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_client_input_collision_at_boundary
func TestNodeConversationCompactClientInputCollision(t *testing.T) {
	conv := NewNodeConversation("")
	mustAddUser(t, conv, "first prompt", UserMessageOptions{IsClientInput: true})
	mustAddAssistant(t, conv, "response", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "boundary prompt", UserMessageOptions{IsClientInput: true})
	mustAddAssistant(t, conv, "most recent", nil, AssistantMessageOptions{})

	if err := conv.Compact("SUMMARY", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := messageContents(conv.messages); !reflect.DeepEqual(got, []string{"first prompt", "SUMMARY", "most recent"}) {
		t.Fatalf("contents = %#v, want [first prompt SUMMARY most recent]", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_compact_client_input_persisted_to_store
func TestNodeConversationCompactClientInputPersisted(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: store})
	mustAddUser(t, conv, "keep me", UserMessageOptions{IsClientInput: true})
	mustAddAssistant(t, conv, "work 1", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "work 2", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "work 3", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "most recent", nil, AssistantMessageOptions{})

	if err := conv.Compact("summary", CompactOptions{KeepRecent: testIntPtr(1)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got := messageContents(conv.messages); !reflect.DeepEqual(got, []string{"keep me", "summary", "most recent"}) {
		t.Fatalf("in-memory contents = %#v", got)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	contents := make([]string, len(parts))
	roles := make([]string, len(parts))
	for i, p := range parts {
		contents[i] = stringOr(p["content"], "")
		roles[i] = stringOr(p["role"], "")
	}
	if !reflect.DeepEqual(contents, []string{"keep me", "summary", "most recent"}) {
		t.Fatalf("stored contents = %#v", contents)
	}
	if !reflect.DeepEqual(roles, []string{"user", "user", "assistant"}) {
		t.Fatalf("stored roles = %#v", roles)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_clear
func TestNodeConversationClear(t *testing.T) {
	conv := NewNodeConversation("keep me")
	mustAddUser(t, conv, "a", UserMessageOptions{})
	mustAddUser(t, conv, "b", UserMessageOptions{})
	seqBefore := conv.NextSeq()
	if err := conv.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if conv.TurnCount() != 0 {
		t.Fatalf("turn_count = %d, want 0", conv.TurnCount())
	}
	if conv.SystemPrompt() != "keep me" {
		t.Fatalf("system_prompt = %q, want keep me", conv.SystemPrompt())
	}
	if conv.NextSeq() != seqBefore {
		t.Fatalf("next_seq = %d, want %d", conv.NextSeq(), seqBefore)
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_export_summary
func TestNodeConversationExportSummary(t *testing.T) {
	conv := NewNodeConversation("Be helpful")
	mustAddUser(t, conv, "q1", UserMessageOptions{})
	mustAddAssistant(t, conv, "a1", nil, AssistantMessageOptions{})
	summary := conv.ExportSummary()
	for _, want := range []string{"[STATS]", "turns: 1", "messages: 2", "[CONFIG]", "Be helpful", "[RECENT_MESSAGES]", "[user]", "[assistant]"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q: %q", want, summary)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestNodeConversation::test_export_summary_output_keys
func TestNodeConversationExportSummaryOutputKeys(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt: "test",
		OutputKeys:   []string{"confirmed_meetings", "lead_score"},
	})
	mustAddUser(t, conv, "hi", UserMessageOptions{})
	if got := conv.ExportSummary(); !strings.Contains(got, "output_keys: confirmed_meetings, lead_score") {
		t.Fatalf("summary missing output_keys line: %q", got)
	}

	conv2 := NewNodeConversation("test")
	mustAddUser(t, conv2, "hi", UserMessageOptions{})
	if got := conv2.ExportSummary(); strings.Contains(got, "output_keys") {
		t.Fatalf("summary should not mention output_keys: %q", got)
	}
}

func extractProtected(t *testing.T, conv *NodeConversation) map[string]string {
	t.Helper()
	return conv.extractProtectedValues(conv.messages)
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_colon_format
func TestExtractProtectedValuesColonFormat(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"score"}})
	mustAddAssistant(t, conv, "The score: 87", nil, AssistantMessageOptions{})
	got := extractProtected(t, conv)
	if !reflect.DeepEqual(got, map[string]string{"score": "87"}) {
		t.Fatalf("extracted = %#v, want score=87", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_json_format
func TestExtractProtectedValuesJSONFormat(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"meetings"}})
	mustAddAssistant(t, conv, `{"meetings": ["standup", "retro"]}`, nil, AssistantMessageOptions{})
	got := extractProtected(t, conv)
	if !reflect.DeepEqual(got, map[string]string{"meetings": `["standup", "retro"]`}) {
		t.Fatalf("extracted = %#v, want Python json.dumps spacing", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_equals_format
func TestExtractProtectedValuesEqualsFormat(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"status"}})
	mustAddAssistant(t, conv, "status = done", nil, AssistantMessageOptions{})
	got := extractProtected(t, conv)
	if !reflect.DeepEqual(got, map[string]string{"status": "done"}) {
		t.Fatalf("extracted = %#v, want status=done", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_most_recent_wins
func TestExtractProtectedValuesMostRecentWins(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"score"}})
	mustAddAssistant(t, conv, "score: 50", nil, AssistantMessageOptions{})
	mustAddAssistant(t, conv, "score: 99", nil, AssistantMessageOptions{})
	got := extractProtected(t, conv)
	if !reflect.DeepEqual(got, map[string]string{"score": "99"}) {
		t.Fatalf("extracted = %#v, want score=99", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_embedded_json
func TestExtractProtectedValuesEmbeddedJSON(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"lead_score"}})
	mustAddAssistant(t, conv, `Based on my analysis, here are the results: {"lead_score": 87, "status": "hot"}`, nil, AssistantMessageOptions{})
	got := extractProtected(t, conv)
	if !reflect.DeepEqual(got, map[string]string{"lead_score": "87"}) {
		t.Fatalf("extracted = %#v, want lead_score=87", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractProtectedValues::test_extract_no_match_cases
func TestExtractProtectedValuesNoMatch(t *testing.T) {
	conv := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"score"}})
	mustAddUser(t, conv, "score: 42", UserMessageOptions{})
	if got := extractProtected(t, conv); len(got) != 0 {
		t.Fatalf("user messages should not be scanned: %#v", got)
	}

	conv2 := NewNodeConversation("")
	mustAddAssistant(t, conv2, "score: 42", nil, AssistantMessageOptions{})
	if got := extractProtected(t, conv2); len(got) != 0 {
		t.Fatalf("no output_keys should extract nothing: %#v", got)
	}

	conv3 := NewNodeConversationWithOptions(ConversationOptions{OutputKeys: []string{"missing_key"}})
	mustAddAssistant(t, conv3, "nothing relevant here", nil, AssistantMessageOptions{})
	if got := extractProtected(t, conv3); len(got) != 0 {
		t.Fatalf("missing key should extract nothing: %#v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_write_through_each_add
func TestPersistenceWriteThroughEachAdd(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: store})
	mustAddUser(t, conv, "a", UserMessageOptions{})
	mustAddAssistant(t, conv, "b", nil, AssistantMessageOptions{})

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0]["content"] != "a" || parts[1]["content"] != "b" {
		t.Fatalf("part contents = %#v", parts)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_meta_and_cursor_persistence
func TestPersistenceMetaAndCursor(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sys", Store: store})
	if store.meta != nil {
		t.Fatal("meta should be nil before the first add")
	}
	mustAddUser(t, conv, "trigger", UserMessageOptions{})
	if store.meta == nil {
		t.Fatal("meta should be written on first add")
	}
	if store.meta["system_prompt"] != "sys" {
		t.Fatalf("meta system_prompt = %v, want sys", store.meta["system_prompt"])
	}
	if !reflect.DeepEqual(store.cursor, map[string]any{"next_seq": 1}) {
		t.Fatalf("cursor = %#v, want next_seq=1", store.cursor)
	}
	mustAddUser(t, conv, "b", UserMessageOptions{})
	if !reflect.DeepEqual(store.cursor, map[string]any{"next_seq": 2}) {
		t.Fatalf("cursor = %#v, want next_seq=2", store.cursor)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_restore_from_store
func TestPersistenceRestoreFromStore(t *testing.T) {
	store := NewMemoryConversationStore()
	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored != nil {
		t.Fatalf("restore from empty store = %#v, want nil", restored)
	}

	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "hello", MaxContextTokens: 500, Store: store})
	mustAddUser(t, conv, "u1", UserMessageOptions{})
	mustAddAssistant(t, conv, "a1", nil, AssistantMessageOptions{})

	restored, err = RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.SystemPrompt() != "hello" {
		t.Fatalf("system_prompt = %q, want hello", restored.SystemPrompt())
	}
	if restored.TurnCount() != 1 || restored.MessageCount() != 2 {
		t.Fatalf("turn_count=%d message_count=%d, want 1/2", restored.TurnCount(), restored.MessageCount())
	}
	if restored.NextSeq() != 2 {
		t.Fatalf("next_seq = %d, want 2", restored.NextSeq())
	}
	if restored.messages[0].Content != "u1" {
		t.Fatalf("first content = %q, want u1", restored.messages[0].Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_restore_filters_by_run_id_for_crash_recovery
func TestPersistenceRestoreFiltersByRunID(t *testing.T) {
	store := NewMemoryConversationStore()
	if err := store.WriteMeta(map[string]any{"system_prompt": "hello"}); err != nil {
		t.Fatal(err)
	}
	for seq, part := range []map[string]any{
		{"seq": 0, "role": "user", "content": "legacy"},
		{"seq": 1, "role": "user", "content": "run-a", "run_id": "run-a"},
		{"seq": 2, "role": "assistant", "content": "run-b", "run_id": "run-b"},
	} {
		if err := store.WritePart(seq, part); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 3}); err != nil {
		t.Fatal(err)
	}

	restored, err := RestoreConversation(store, nil, strPtrTest("run-a"))
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if got := messageContents(restored.messages); !reflect.DeepEqual(got, []string{"run-a"}) {
		t.Fatalf("contents = %#v, want [run-a]", got)
	}
	if restored.NextSeq() != 3 {
		t.Fatalf("next_seq = %d, want 3", restored.NextSeq())
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_restore_phase_filter_falls_back_for_legacy_unphased_parts
func TestPersistenceRestorePhaseLegacyFallback(t *testing.T) {
	store := NewMemoryConversationStore()
	if err := store.WriteMeta(map[string]any{"system_prompt": "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "role": "assistant", "content": "restored"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		t.Fatal(err)
	}

	restored, err := RestoreConversation(store, strPtrTest("queen"), nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if got := messageContents(restored.messages); !reflect.DeepEqual(got, []string{"restored"}) {
		t.Fatalf("contents = %#v, want [restored]", got)
	}
	if restored.NextSeq() != 1 {
		t.Fatalf("next_seq = %d, want 1", restored.NextSeq())
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_restore_phase_filter_does_not_fall_back_for_mismatched_phased_parts
func TestPersistenceRestorePhaseMismatchExcluded(t *testing.T) {
	store := NewMemoryConversationStore()
	if err := store.WriteMeta(map[string]any{"system_prompt": "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(0, map[string]any{
		"seq": 0, "role": "assistant", "content": "node-a only", "phase_id": "node-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		t.Fatal(err)
	}

	restored, err := RestoreConversation(store, strPtrTest("queen"), nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.MessageCount() != 0 {
		t.Fatalf("message_count = %d, want 0", restored.MessageCount())
	}
	if restored.NextSeq() != 1 {
		t.Fatalf("next_seq = %d, want 1", restored.NextSeq())
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_clear_deletes_all_parts
func TestPersistenceClearDeletesAllParts(t *testing.T) {
	store := NewMemoryConversationStore()
	convA := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "hello", Store: store, RunID: strPtrTest("run-a")})
	convB := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "hello", Store: store, RunID: strPtrTest("run-b")})

	mustAddUser(t, convA, "a1", UserMessageOptions{})
	mustAddUser(t, convB, "b1", UserMessageOptions{})

	if err := convA.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if len(restored.messages) != 0 {
		t.Fatalf("messages = %#v, want empty", messageContents(restored.messages))
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_restore_preserves_tool_messages
func TestPersistenceRestorePreservesToolMessages(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: store})
	mustAddAssistant(t, conv, "", sampleToolCalls(), AssistantMessageOptions{})
	mustAddTool(t, conv, "call_1", "result", ToolResultOptions{IsError: true})

	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	msgs := restored.messages
	if !reflect.DeepEqual(msgs[0].ToolCalls, sampleToolCalls()) {
		t.Fatalf("tool_calls = %#v", msgs[0].ToolCalls)
	}
	if msgs[1].ToolUseID == nil || *msgs[1].ToolUseID != "call_1" {
		t.Fatalf("tool_use_id = %v, want call_1", msgs[1].ToolUseID)
	}
	if !msgs[1].IsError {
		t.Fatal("is_error should be true")
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_compact_deletes_old_parts
func TestPersistenceCompactDeletesOldParts(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: store})
	mustAddUser(t, conv, "a", UserMessageOptions{})
	mustAddUser(t, conv, "b", UserMessageOptions{})
	if len(store.parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(store.parts))
	}
	if err := conv.Compact("summary", CompactOptions{KeepRecent: testIntPtr(0)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(store.parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(store.parts))
	}
	for _, part := range store.parts {
		if part["content"] != "summary" {
			t.Fatalf("remaining part content = %v, want summary", part["content"])
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_compact_then_restore
func TestPersistenceCompactThenRestore(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "sp", Store: store})
	mustAddUser(t, conv, "m1", UserMessageOptions{})
	mustAddAssistant(t, conv, "m2", nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "m3", UserMessageOptions{})
	mustAddAssistant(t, conv, "m4", nil, AssistantMessageOptions{})

	if err := conv.Compact("early summary", CompactOptions{KeepRecent: testIntPtr(2)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.MessageCount() != 3 {
		t.Fatalf("message_count = %d, want 3", restored.MessageCount())
	}
	if got := messageContents(restored.messages); !reflect.DeepEqual(got, []string{"early summary", "m3", "m4"}) {
		t.Fatalf("contents = %#v", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestPersistence::test_clear_deletes_store_parts
func TestPersistenceClearDeletesStoreParts(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: store})
	mustAddUser(t, conv, "a", UserMessageOptions{})
	mustAddUser(t, conv, "b", UserMessageOptions{})
	if err := conv.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(store.parts) != 0 {
		t.Fatalf("parts = %d, want 0", len(store.parts))
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_meta_and_cursor_crud
func TestFileConversationStoreMetaAndCursorCRUD(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	meta, err := store.ReadMeta()
	if err != nil || meta != nil {
		t.Fatalf("empty meta = %#v err=%v, want nil", meta, err)
	}
	if err := store.WriteMeta(map[string]any{"system_prompt": "hi"}); err != nil {
		t.Fatal(err)
	}
	meta, err = store.ReadMeta()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta, map[string]any{"system_prompt": "hi"}) {
		t.Fatalf("meta = %#v", meta)
	}

	if err := store.WriteCursor(map[string]any{"next_seq": 5}); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cursor, map[string]any{"next_seq": float64(5)}) {
		t.Fatalf("cursor = %#v, want next_seq 5", cursor)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_write_and_read_parts_in_order
func TestFileConversationStorePartsInOrder(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	_ = store.WritePart(2, map[string]any{"seq": 2, "content": "second"})
	_ = store.WritePart(0, map[string]any{"seq": 0, "content": "first"})
	_ = store.WritePart(1, map[string]any{"seq": 1, "content": "middle"})
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int, len(parts))
	for i, p := range parts {
		seqs[i] = intOr(p["seq"], -1)
	}
	if !reflect.DeepEqual(seqs, []int{0, 1, 2}) {
		t.Fatalf("seqs = %#v, want [0 1 2]", seqs)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_delete_parts_before
func TestFileConversationStoreDeletePartsBefore(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	for i := 0; i < 5; i++ {
		if err := store.WritePart(i, map[string]any{"seq": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeletePartsBefore(3); err != nil {
		t.Fatal(err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int, len(parts))
	for i, p := range parts {
		seqs[i] = intOr(p["seq"], -1)
	}
	if !reflect.DeepEqual(seqs, []int{3, 4}) {
		t.Fatalf("seqs = %#v, want [3 4]", seqs)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_idempotent_write_part
func TestFileConversationStoreIdempotentWrite(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WritePart(0, map[string]any{"seq": 0, "v": 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "v": 2}); err != nil {
		t.Fatal(err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if intOr(parts[0]["v"], -1) != 2 {
		t.Fatalf("v = %v, want 2", parts[0]["v"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_integration_with_node_conversation
func TestFileConversationStoreIntegration(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "test", Store: store})
	mustAddUser(t, conv, "u1", UserMessageOptions{})
	mustAddAssistant(t, conv, "a1", sampleToolCalls(), AssistantMessageOptions{})
	mustAddTool(t, conv, "call_1", "r1", ToolResultOptions{IsError: true})

	restored, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.SystemPrompt() != "test" {
		t.Fatalf("system_prompt = %q, want test", restored.SystemPrompt())
	}
	if restored.TurnCount() != 1 || restored.MessageCount() != 3 || restored.NextSeq() != 3 {
		t.Fatalf("turn=%d messages=%d next=%d, want 1/3/3", restored.TurnCount(), restored.MessageCount(), restored.NextSeq())
	}
	msgs := restored.messages
	if msgs[0].Content != "u1" {
		t.Fatalf("msgs[0] = %q, want u1", msgs[0].Content)
	}
	if !reflect.DeepEqual(msgs[1].ToolCalls, sampleToolCalls()) {
		t.Fatalf("tool_calls = %#v", msgs[1].ToolCalls)
	}
	if !msgs[2].IsError {
		t.Fatal("msgs[2].is_error should be true")
	}
	llm := restored.ToLLMMessages("")
	if llm[2]["content"] != "ERROR: r1" {
		t.Fatalf("llm[2] content = %v, want ERROR: r1", llm[2]["content"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_corrupt_part_skipped_on_read
func TestFileConversationStoreCorruptPartSkipped(t *testing.T) {
	base := filepath.Join(t.TempDir(), "conv")
	store := NewFileConversationStore(base)
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(1, map[string]any{"seq": 1, "content": "good"}); err != nil {
		t.Fatal(err)
	}
	corruptPath := filepath.Join(base, "parts", "0000000000.json")
	if err := os.WriteFile(corruptPath, []byte("{truncated"), 0o644); err != nil {
		t.Fatal(err)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if intOr(parts[0]["seq"], -1) != 1 {
		t.Fatalf("remaining seq = %v, want 1", parts[0]["seq"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_directory_structure
func TestFileConversationStoreDirectoryStructure(t *testing.T) {
	base := filepath.Join(t.TempDir(), "conv")
	store := NewFileConversationStore(base)
	if err := store.WriteMeta(map[string]any{"system_prompt": "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "first"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePart(1, map[string]any{"seq": 1, "content": "second"}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(base, "meta.json"),
		filepath.Join(base, "cursor.json"),
		filepath.Join(base, "parts", "0000000000.json"),
		filepath.Join(base, "parts", "0000000001.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_partials_separate_from_parts
func TestFileConversationStorePartialsSeparate(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	if err := store.WritePart(0, map[string]any{"seq": 0, "content": "real"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WritePartial(1, map[string]any{"seq": 1, "content": "inflight", "truncated": true}); err != nil {
		t.Fatal(err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || intOr(parts[0]["seq"], -1) != 0 {
		t.Fatalf("parts = %#v, want only seq 0", parts)
	}
	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 1 || partials[0]["content"] != "inflight" {
		t.Fatalf("partials = %#v", partials)
	}
	partial, err := store.ReadPartial(1)
	if err != nil {
		t.Fatal(err)
	}
	if partial == nil || partial["content"] != "inflight" {
		t.Fatalf("read_partial(1) = %#v", partial)
	}
	missing, err := store.ReadPartial(99)
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("read_partial(99) = %#v, want nil", missing)
	}
	if err := store.ClearPartial(1); err != nil {
		t.Fatal(err)
	}
	partials, err = store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	if len(partials) != 0 {
		t.Fatalf("partials after clear = %#v, want empty", partials)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFileConversationStore::test_partials_dir_does_not_break_parts_glob
func TestFileConversationStorePartialsDoNotBreakDeletion(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "conv"))
	for i := 0; i < 3; i++ {
		if err := store.WritePart(i, map[string]any{"seq": i}); err != nil {
			t.Fatal(err)
		}
		if err := store.WritePartial(i+100, map[string]any{"seq": i + 100}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeletePartsBefore(2); err != nil {
		t.Fatal(err)
	}
	parts, err := store.ReadParts()
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || intOr(parts[0]["seq"], -1) != 2 {
		t.Fatalf("parts = %#v, want [2]", parts)
	}
	partials, err := store.ReadAllPartials()
	if err != nil {
		t.Fatal(err)
	}
	seqs := make([]int, len(partials))
	for i, p := range partials {
		seqs[i] = intOr(p["seq"], -1)
	}
	if !reflect.DeepEqual(seqs, []int{100, 101, 102}) {
		t.Fatalf("partial seqs = %#v, want [100 101 102]", seqs)
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_multi_turn_agent_conversation
func TestConversationIntegrationMultiTurn(t *testing.T) {
	base := filepath.Join(t.TempDir(), "agent_conv")
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:     "You are a helpful travel agent.",
		MaxContextTokens: 16000,
		Store:            NewFileConversationStore(base),
	})

	mustAddUser(t, conv, "Find me flights from NYC to London next Friday.", UserMessageOptions{})
	mustAddAssistant(t, conv, "Let me search for flights.", []map[string]any{
		{
			"id":   "call_flight_1",
			"type": "function",
			"function": map[string]any{
				"name":      "search_flights",
				"arguments": `{"origin":"JFK","destination":"LHR","date":"2025-06-13"}`,
			},
		},
	}, AssistantMessageOptions{})
	mustAddTool(t, conv, "call_flight_1", `{"flights":[{"airline":"BA","price":450,"departure":"08:00"},{"airline":"AA","price":520,"departure":"14:30"}]}`, ToolResultOptions{})
	mustAddAssistant(t, conv,
		"I found 2 flights:\n1. British Airways at $450, departing 08:00\n2. American Airlines at $520, departing 14:30\nWhich one would you like?",
		nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "Book the British Airways one.", UserMessageOptions{})
	mustAddAssistant(t, conv, "Booking the BA flight now.", []map[string]any{
		{
			"id":   "call_book_1",
			"type": "function",
			"function": map[string]any{
				"name":      "book_flight",
				"arguments": `{"flight_id":"BA-JFK-LHR-0800","passenger":"user"}`,
			},
		},
	}, AssistantMessageOptions{})
	mustAddTool(t, conv, "call_book_1", `{"confirmation":"BA-12345","status":"confirmed"}`, ToolResultOptions{})
	mustAddAssistant(t, conv, "Your flight is booked! Confirmation: BA-12345.", nil, AssistantMessageOptions{})

	if conv.TurnCount() != 2 || conv.MessageCount() != 8 || conv.NextSeq() != 8 {
		t.Fatalf("turn=%d messages=%d next=%d, want 2/8/8", conv.TurnCount(), conv.MessageCount(), conv.NextSeq())
	}

	restored, err := RestoreConversation(NewFileConversationStore(base), nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.SystemPrompt() != "You are a helpful travel agent." {
		t.Fatalf("system_prompt = %q", restored.SystemPrompt())
	}
	if restored.TurnCount() != 2 || restored.MessageCount() != 8 || restored.NextSeq() != 8 {
		t.Fatalf("restored turn=%d messages=%d next=%d, want 2/8/8", restored.TurnCount(), restored.MessageCount(), restored.NextSeq())
	}

	msgs := restored.messages
	if msgs[0].Role != RoleUser || !strings.Contains(msgs[0].Content, "NYC to London") {
		t.Fatalf("msgs[0] = %#v", msgs[0])
	}
	if msgs[1].Role != RoleAssistant || len(msgs[1].ToolCalls) == 0 || msgs[1].ToolCalls[0]["id"] != "call_flight_1" {
		t.Fatalf("msgs[1] = %#v", msgs[1])
	}
	if msgs[2].Role != RoleTool || msgs[2].ToolUseID == nil || *msgs[2].ToolUseID != "call_flight_1" || !strings.Contains(msgs[2].Content, "BA") {
		t.Fatalf("msgs[2] = %#v", msgs[2])
	}
	if msgs[7].Content != "Your flight is booked! Confirmation: BA-12345." {
		t.Fatalf("msgs[7] = %q", msgs[7].Content)
	}

	llm := restored.ToLLMMessages("")
	if !reflect.DeepEqual(llm[0], map[string]any{"role": "user", "content": msgs[0].Content}) {
		t.Fatalf("llm[0] = %#v", llm[0])
	}
	if llm[2]["role"] != RoleTool || llm[2]["tool_call_id"] != "call_flight_1" {
		t.Fatalf("llm[2] = %#v", llm[2])
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_compaction_and_restore_preserves_continuity
func TestConversationIntegrationCompactionContinuity(t *testing.T) {
	base := filepath.Join(t.TempDir(), "compact_conv")
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt: "research assistant",
		Store:        NewFileConversationStore(base),
	})

	for i := 0; i < 5; i++ {
		mustAddUser(t, conv, fmt.Sprintf("question %d", i), UserMessageOptions{})
		mustAddAssistant(t, conv, fmt.Sprintf("answer %d", i), nil, AssistantMessageOptions{})
	}
	if conv.MessageCount() != 10 || conv.NextSeq() != 10 {
		t.Fatalf("message_count=%d next_seq=%d, want 10/10", conv.MessageCount(), conv.NextSeq())
	}

	if err := conv.Compact("Summary of questions 0-3 and their answers.", CompactOptions{KeepRecent: testIntPtr(2)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if conv.MessageCount() != 3 {
		t.Fatalf("message_count = %d, want 3", conv.MessageCount())
	}
	if got := messageContents(conv.messages); !reflect.DeepEqual(got, []string{"Summary of questions 0-3 and their answers.", "question 4", "answer 4"}) {
		t.Fatalf("contents = %#v", got)
	}

	mustAddUser(t, conv, "question 5", UserMessageOptions{})
	mustAddAssistant(t, conv, "answer 5", nil, AssistantMessageOptions{})
	if conv.NextSeq() != 12 {
		t.Fatalf("next_seq = %d, want 12", conv.NextSeq())
	}

	entries, err := os.ReadDir(filepath.Join(base, "parts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var seq int
		if _, err := fmt.Sscanf(entry.Name(), "%d.json", &seq); err != nil {
			t.Fatalf("unparseable part name %q: %v", entry.Name(), err)
		}
		if seq < 7 {
			t.Fatalf("stale part seq %d found", seq)
		}
	}

	restored, err := RestoreConversation(NewFileConversationStore(base), nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.NextSeq() != 12 || restored.MessageCount() != 5 {
		t.Fatalf("restored next_seq=%d message_count=%d, want 12/5", restored.NextSeq(), restored.MessageCount())
	}
	if !strings.Contains(restored.messages[0].Content, "Summary of questions 0-3") {
		t.Fatalf("restored summary = %q", restored.messages[0].Content)
	}
	if restored.messages[len(restored.messages)-1].Content != "answer 5" {
		t.Fatalf("restored last = %q, want answer 5", restored.messages[len(restored.messages)-1].Content)
	}
	for i := 1; i < len(restored.messages); i++ {
		if restored.messages[i].Seq < restored.messages[i-1].Seq {
			t.Fatalf("seqs not monotonic: %#v", restored.messages)
		}
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_output_key_preservation_through_compact_and_restore
func TestConversationIntegrationOutputKeysSurviveRestore(t *testing.T) {
	base := filepath.Join(t.TempDir(), "output_key_conv")
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt: "classifier",
		OutputKeys:   []string{"classification", "confidence"},
		Store:        NewFileConversationStore(base),
	})

	mustAddUser(t, conv, "Classify this email: 'You won a prize!'", UserMessageOptions{})
	mustAddAssistant(t, conv, `{"classification": "spam", "confidence": "0.97"}`, nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "What about: 'Meeting at 3pm'", UserMessageOptions{})
	mustAddAssistant(t, conv, `{"classification": "ham", "confidence": "0.99"}`, nil, AssistantMessageOptions{})
	mustAddUser(t, conv, "And: 'Buy cheap meds now'", UserMessageOptions{})
	mustAddAssistant(t, conv, `{"classification": "spam", "confidence": "0.95"}`, nil, AssistantMessageOptions{})

	if err := conv.Compact("Classified 3 emails.", CompactOptions{KeepRecent: testIntPtr(2)}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	summary := conv.messages[0].Content
	if !strings.Contains(summary, "PRESERVED VALUES") {
		t.Fatalf("summary missing PRESERVED VALUES: %q", summary)
	}
	if !strings.Contains(summary, "ham") && !strings.Contains(summary, "spam") {
		t.Fatalf("summary missing classification value: %q", summary)
	}

	restored, err := RestoreConversation(NewFileConversationStore(base), nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if !strings.Contains(restored.messages[0].Content, "PRESERVED VALUES") {
		t.Fatalf("restored summary missing PRESERVED VALUES: %q", restored.messages[0].Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_tool_error_roundtrip
func TestConversationIntegrationToolErrorRoundtrip(t *testing.T) {
	base := filepath.Join(t.TempDir(), "error_conv")
	conv := NewNodeConversationWithOptions(ConversationOptions{Store: NewFileConversationStore(base)})

	mustAddUser(t, conv, "Calculate 1/0", UserMessageOptions{})
	mustAddAssistant(t, conv, "Let me calculate that.", []map[string]any{
		{
			"id":   "call_calc",
			"type": "function",
			"function": map[string]any{
				"name":      "calculator",
				"arguments": `{"expr":"1/0"}`,
			},
		},
	}, AssistantMessageOptions{})
	mustAddTool(t, conv, "call_calc", "ZeroDivisionError: division by zero", ToolResultOptions{IsError: true})
	mustAddAssistant(t, conv, "The calculation failed: division by zero is undefined.", nil, AssistantMessageOptions{})

	restored, err := RestoreConversation(NewFileConversationStore(base), nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	toolMsg := restored.messages[2]
	if toolMsg.Role != RoleTool || !toolMsg.IsError || toolMsg.ToolUseID == nil || *toolMsg.ToolUseID != "call_calc" {
		t.Fatalf("tool message = %#v", toolMsg)
	}
	dict := toolMsg.ToLLMDict()
	content, _ := dict["content"].(string)
	if !strings.HasPrefix(content, "ERROR: ") || !strings.Contains(content, "ZeroDivisionError") {
		t.Fatalf("llm content = %q", content)
	}
	if dict["tool_call_id"] != "call_calc" {
		t.Fatalf("tool_call_id = %v", dict["tool_call_id"])
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_concurrent_conversations_isolated
func TestConversationIntegrationConcurrentIsolated(t *testing.T) {
	tmp := t.TempDir()
	convA := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "Agent A", Store: NewFileConversationStore(filepath.Join(tmp, "conv_a"))})
	convB := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "Agent B", Store: NewFileConversationStore(filepath.Join(tmp, "conv_b"))})

	mustAddUser(t, convA, "Hello from A", UserMessageOptions{})
	mustAddUser(t, convB, "Hello from B", UserMessageOptions{})
	mustAddAssistant(t, convA, "Response A", nil, AssistantMessageOptions{})
	mustAddAssistant(t, convB, "Response B", nil, AssistantMessageOptions{})
	mustAddUser(t, convB, "Follow-up B", UserMessageOptions{})

	restoredA, err := RestoreConversation(NewFileConversationStore(filepath.Join(tmp, "conv_a")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	restoredB, err := RestoreConversation(NewFileConversationStore(filepath.Join(tmp, "conv_b")), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restoredA.SystemPrompt() != "Agent A" || restoredB.SystemPrompt() != "Agent B" {
		t.Fatalf("system prompts = %q / %q", restoredA.SystemPrompt(), restoredB.SystemPrompt())
	}
	if restoredA.MessageCount() != 2 || restoredB.MessageCount() != 3 {
		t.Fatalf("message counts = %d / %d, want 2/3", restoredA.MessageCount(), restoredB.MessageCount())
	}
	if restoredA.messages[0].Content != "Hello from A" {
		t.Fatalf("A first = %q", restoredA.messages[0].Content)
	}
	if restoredB.messages[2].Content != "Follow-up B" {
		t.Fatalf("B last = %q", restoredB.messages[2].Content)
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_destroy_removes_all_files
func TestConversationIntegrationDestroy(t *testing.T) {
	base := filepath.Join(t.TempDir(), "doomed_conv")
	store := NewFileConversationStore(base)
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "temp", Store: store})
	mustAddUser(t, conv, "ephemeral", UserMessageOptions{})
	mustAddAssistant(t, conv, "gone soon", nil, AssistantMessageOptions{})

	for _, path := range []string{base, filepath.Join(base, "meta.json"), filepath.Join(base, "parts")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if err := store.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("base still exists after destroy: %v", err)
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_restore_empty_store_returns_none
func TestConversationIntegrationRestoreEmpty(t *testing.T) {
	store := NewFileConversationStore(filepath.Join(t.TempDir(), "empty"))
	result, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if result != nil {
		t.Fatalf("restore empty = %#v, want nil", result)
	}
}

// Parity: core/tests/test_node_conversation.py::TestConversationIntegration::test_clear_then_continue_then_restore
func TestConversationIntegrationClearContinueRestore(t *testing.T) {
	base := filepath.Join(t.TempDir(), "clear_conv")
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "s", Store: NewFileConversationStore(base)})

	mustAddUser(t, conv, "old msg 0", UserMessageOptions{})
	mustAddAssistant(t, conv, "old msg 1", nil, AssistantMessageOptions{})
	if conv.NextSeq() != 2 {
		t.Fatalf("next_seq = %d, want 2", conv.NextSeq())
	}

	if err := conv.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if conv.MessageCount() != 0 || conv.NextSeq() != 2 {
		t.Fatalf("after clear message_count=%d next_seq=%d, want 0/2", conv.MessageCount(), conv.NextSeq())
	}

	mustAddUser(t, conv, "new msg", UserMessageOptions{})
	mustAddAssistant(t, conv, "new response", nil, AssistantMessageOptions{})
	if conv.NextSeq() != 4 {
		t.Fatalf("next_seq = %d, want 4", conv.NextSeq())
	}
	if conv.messages[0].Seq != 2 || conv.messages[1].Seq != 3 {
		t.Fatalf("seqs = %d/%d, want 2/3", conv.messages[0].Seq, conv.messages[1].Seq)
	}

	restored, err := RestoreConversation(NewFileConversationStore(base), nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if restored == nil {
		t.Fatal("restored conversation is nil")
	}
	if restored.MessageCount() != 2 || restored.NextSeq() != 4 {
		t.Fatalf("restored message_count=%d next_seq=%d, want 2/4", restored.MessageCount(), restored.NextSeq())
	}
	if restored.messages[0].Content != "new msg" || restored.messages[0].Seq != 2 {
		t.Fatalf("restored first = %#v", restored.messages[0])
	}
}

func makeHistoryToolCall(callID, name string, args map[string]any) map[string]any {
	encoded, _ := marshalJSON(args)
	return map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": string(encoded),
		},
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractToolCallHistory::test_basic_extraction
func TestExtractToolCallHistoryBasic(t *testing.T) {
	msgs := []Message{
		{Seq: 0, Role: RoleAssistant, Content: "", ToolCalls: []map[string]any{
			makeHistoryToolCall("c1", "web_search", map[string]any{"query": "python async"}),
		}},
		{Seq: 1, Role: RoleTool, Content: "results", ToolUseID: strPtrTest("c1")},
		{Seq: 2, Role: RoleAssistant, Content: "", ToolCalls: []map[string]any{
			makeHistoryToolCall("c2", "terminal_exec", map[string]any{"command": "cat /tmp/output.txt"}),
		}},
		{Seq: 3, Role: RoleTool, Content: "contents", ToolUseID: strPtrTest("c2")},
	}
	result := ExtractToolCallHistory(msgs, 30)
	if !strings.Contains(result, "web_search (1x)") {
		t.Fatalf("missing web_search (1x): %q", result)
	}
	if !strings.Contains(result, "terminal_exec (1x)") {
		t.Fatalf("missing terminal_exec (1x): %q", result)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractToolCallHistory::test_errors_included
func TestExtractToolCallHistoryErrors(t *testing.T) {
	msgs := []Message{
		{Seq: 0, Role: RoleTool, Content: "Connection refused", IsError: true, ToolUseID: strPtrTest("c1")},
	}
	result := ExtractToolCallHistory(msgs, 30)
	if !strings.Contains(result, "ERRORS") {
		t.Fatalf("missing ERRORS section: %q", result)
	}
	if !strings.Contains(result, "Connection refused") {
		t.Fatalf("missing error text: %q", result)
	}
}

// Parity: core/tests/test_node_conversation.py::TestExtractToolCallHistory::test_empty_messages
func TestExtractToolCallHistoryEmpty(t *testing.T) {
	if got := ExtractToolCallHistory([]Message{}, 30); got != "" {
		t.Fatalf("empty history = %q, want empty", got)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFormatMessagesForSummary::test_user_assistant_messages
func TestFormatMessagesForSummaryUserAssistant(t *testing.T) {
	result := FormatMessagesForSummary([]Message{
		{Seq: 0, Role: RoleUser, Content: "Hello world"},
		{Seq: 1, Role: RoleAssistant, Content: "Hi there"},
	})
	if !strings.Contains(result, "[user]: Hello world") {
		t.Fatalf("missing user line: %q", result)
	}
	if !strings.Contains(result, "[assistant]: Hi there") {
		t.Fatalf("missing assistant line: %q", result)
	}
}

// Parity: core/tests/test_node_conversation.py::TestFormatMessagesForSummary::test_tool_result_truncated
func TestFormatMessagesForSummaryToolTruncated(t *testing.T) {
	result := FormatMessagesForSummary([]Message{
		{Seq: 0, Role: RoleTool, Content: strings.Repeat("x", 1000), ToolUseID: strPtrTest("c1")},
	})
	if !strings.Contains(result, "[tool result]:") {
		t.Fatalf("missing tool result marker: %q", result)
	}
	if !strings.Contains(result, "...") {
		t.Fatalf("missing truncation marker: %q", result)
	}
	if len(result) >= 600 {
		t.Fatalf("result length = %d, want < 600", len(result))
	}
}

// Parity: core/tests/test_node_conversation.py::TestFormatMessagesForSummary::test_assistant_with_tool_calls
func TestFormatMessagesForSummaryAssistantWithToolCalls(t *testing.T) {
	result := FormatMessagesForSummary([]Message{
		{Seq: 0, Role: RoleAssistant, Content: "Searching", ToolCalls: []map[string]any{
			makeHistoryToolCall("c1", "web_search", map[string]any{"query": "test"}),
		}},
	})
	if !strings.Contains(result, "web_search") {
		t.Fatalf("missing tool name: %q", result)
	}
	if !strings.Contains(result, "[assistant (calls:") {
		t.Fatalf("missing assistant calls marker: %q", result)
	}
}
