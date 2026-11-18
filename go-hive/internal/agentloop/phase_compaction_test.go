package agentloop

import (
	"reflect"
	"strings"
	"testing"
)

func testToolCall(id, name, args string) map[string]any {
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func strPtrTest(value string) *string { return &value }

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_defaults
func TestPhaseMetadataDefaults(t *testing.T) {
	msg := Message{Seq: 0, Role: "user", Content: "hello"}
	if msg.PhaseID != nil {
		t.Fatalf("phase_id default = %v, want nil", *msg.PhaseID)
	}
	if msg.IsTransitionMarker {
		t.Fatal("is_transition_marker should default false")
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_set_phase
func TestPhaseMetadataSetPhase(t *testing.T) {
	msg := Message{Seq: 0, Role: "user", Content: "hello", PhaseID: strPtrTest("research")}
	if msg.PhaseID == nil || *msg.PhaseID != "research" {
		t.Fatalf("phase_id = %v, want research", msg.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_transition_marker
func TestPhaseMetadataTransitionMarker(t *testing.T) {
	msg := Message{
		Seq:                0,
		Role:               "user",
		Content:            "PHASE TRANSITION",
		IsTransitionMarker: true,
		PhaseID:            strPtrTest("report"),
	}
	if !msg.IsTransitionMarker {
		t.Fatal("is_transition_marker should be true")
	}
	if msg.PhaseID == nil || *msg.PhaseID != "report" {
		t.Fatalf("phase_id = %v, want report", msg.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_storage_roundtrip
func TestPhaseMetadataStorageRoundtrip(t *testing.T) {
	msg := Message{
		Seq:                5,
		Role:               "user",
		Content:            "transition",
		PhaseID:            strPtrTest("review"),
		IsTransitionMarker: true,
	}
	d := msg.ToStorageDict()
	if d["phase_id"] != "review" {
		t.Fatalf("storage phase_id = %v", d["phase_id"])
	}
	if d["is_transition_marker"] != true {
		t.Fatalf("storage is_transition_marker = %v", d["is_transition_marker"])
	}

	restored := MessageFromStorageDict(d)
	if restored.PhaseID == nil || *restored.PhaseID != "review" {
		t.Fatalf("restored phase_id = %v", restored.PhaseID)
	}
	if !restored.IsTransitionMarker {
		t.Fatal("restored is_transition_marker should be true")
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_storage_roundtrip_no_phase
func TestPhaseMetadataStorageRoundtripNoPhase(t *testing.T) {
	msg := Message{Seq: 0, Role: "assistant", Content: "hello"}
	d := msg.ToStorageDict()
	if _, ok := d["phase_id"]; ok {
		t.Fatal("phase_id should be omitted")
	}
	if _, ok := d["is_transition_marker"]; ok {
		t.Fatal("is_transition_marker should be omitted")
	}

	restored := MessageFromStorageDict(d)
	if restored.PhaseID != nil {
		t.Fatalf("restored phase_id = %v, want nil", *restored.PhaseID)
	}
	if restored.IsTransitionMarker {
		t.Fatal("restored is_transition_marker should be false")
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseMetadata::test_to_llm_dict_no_metadata
func TestPhaseMetadataToLLMDictNoMetadata(t *testing.T) {
	msg := Message{
		Seq:                0,
		Role:               "user",
		Content:            "hello",
		PhaseID:            strPtrTest("research"),
		IsTransitionMarker: true,
	}
	d := msg.ToLLMDict()
	if _, ok := d["phase_id"]; ok {
		t.Fatal("phase_id should not reach the LLM dict")
	}
	if _, ok := d["is_transition_marker"]; ok {
		t.Fatal("is_transition_marker should not reach the LLM dict")
	}
	want := map[string]any{"role": "user", "content": "hello"}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("dict = %#v, want %#v", d, want)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseStamping::test_messages_stamped_with_phase
func TestPhaseStampingMessagesStampedWithPhase(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("research")

	msg1, err := conv.AddUserMessage("search for X", UserMessageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	msg2, err := conv.AddAssistantMessage("Found it.", nil, AssistantMessageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if msg1.PhaseID == nil || *msg1.PhaseID != "research" {
		t.Fatalf("msg1 phase = %v", msg1.PhaseID)
	}
	if msg2.PhaseID == nil || *msg2.PhaseID != "research" {
		t.Fatalf("msg2 phase = %v", msg2.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseStamping::test_phase_changes_stamp
func TestPhaseStampingPhaseChangesStamp(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("research")
	msg1, _ := conv.AddUserMessage("research msg", UserMessageOptions{})

	conv.SetCurrentPhase("report")
	msg2, _ := conv.AddUserMessage("report msg", UserMessageOptions{})

	if msg1.PhaseID == nil || *msg1.PhaseID != "research" {
		t.Fatalf("msg1 phase = %v", msg1.PhaseID)
	}
	if msg2.PhaseID == nil || *msg2.PhaseID != "report" {
		t.Fatalf("msg2 phase = %v", msg2.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseStamping::test_no_phase_no_stamp
func TestPhaseStampingNoPhaseNoStamp(t *testing.T) {
	conv := NewNodeConversation("test")
	msg, _ := conv.AddUserMessage("no phase", UserMessageOptions{})
	if msg.PhaseID != nil {
		t.Fatalf("phase = %v, want nil", *msg.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseStamping::test_transition_marker_flag
func TestPhaseStampingTransitionMarkerFlag(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("report")
	msg, _ := conv.AddUserMessage(
		"PHASE TRANSITION: Research → Report",
		UserMessageOptions{IsTransitionMarker: true},
	)
	if !msg.IsTransitionMarker {
		t.Fatal("is_transition_marker should be true")
	}
	if msg.PhaseID == nil || *msg.PhaseID != "report" {
		t.Fatalf("phase = %v", msg.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseStamping::test_tool_result_stamped
func TestPhaseStampingToolResultStamped(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("research")
	msg, _ := conv.AddToolResult("call_1", "tool output here", ToolResultOptions{})
	if msg.PhaseID == nil || *msg.PhaseID != "research" {
		t.Fatalf("phase = %v", msg.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseAwareCompaction::test_transition_marker_survives_compaction
func TestPhaseAwareCompactionTransitionMarkerSurvives(t *testing.T) {
	conv := NewNodeConversation("test")

	conv.SetCurrentPhase("research")
	_, _ = conv.AddAssistantMessage("calling tool", []map[string]any{
		testToolCall("call_1", "search", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("call_1", strings.Repeat("x", 20000), ToolResultOptions{
		SpilloverPath: strPtrTest("/data/search_1.txt"),
	})

	_, _ = conv.AddUserMessage("PHASE TRANSITION: Research → Report", UserMessageOptions{IsTransitionMarker: true})

	conv.SetCurrentPhase("report")
	_, _ = conv.AddAssistantMessage("calling another tool", []map[string]any{
		testToolCall("call_2", "save", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("call_2", strings.Repeat("y", 200), ToolResultOptions{})

	pruned, err := conv.PruneOldToolResults(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if pruned < 1 {
		t.Fatalf("pruned = %d, want >= 1", pruned)
	}

	markers := []Message{}
	for _, m := range conv.Messages() {
		if m.IsTransitionMarker {
			markers = append(markers, m)
		}
	}
	if len(markers) != 1 {
		t.Fatalf("markers = %d, want 1", len(markers))
	}
	if !strings.Contains(markers[0].Content, "PHASE TRANSITION") {
		t.Fatalf("marker content = %q", markers[0].Content)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseAwareCompaction::test_current_phase_protected
func TestPhaseAwareCompactionCurrentPhaseProtected(t *testing.T) {
	conv := NewNodeConversation("test")

	conv.SetCurrentPhase("research")
	_, _ = conv.AddAssistantMessage("tool call", []map[string]any{
		testToolCall("c1", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c1", strings.Repeat("old_data ", 5000), ToolResultOptions{
		SpilloverPath: strPtrTest("/data/s_old.txt"),
	})

	conv.SetCurrentPhase("report")
	_, _ = conv.AddAssistantMessage("tool call", []map[string]any{
		testToolCall("c2", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c2", strings.Repeat("current_data ", 5000), ToolResultOptions{})

	if _, err := conv.PruneOldToolResults(0, 100); err != nil {
		t.Fatal(err)
	}

	var oldTool, currentTool []Message
	for _, m := range conv.Messages() {
		if m.Role != RoleTool {
			continue
		}
		if m.PhaseID != nil && *m.PhaseID == "research" {
			oldTool = append(oldTool, m)
		}
		if m.PhaseID != nil && *m.PhaseID == "report" {
			currentTool = append(currentTool, m)
		}
	}
	if len(oldTool) != 1 || !strings.HasPrefix(oldTool[0].Content, "Pruned tool result") {
		t.Fatalf("old tool not pruned: %#v", oldTool)
	}
	if len(currentTool) != 1 || !strings.Contains(currentTool[0].Content, "current_data") {
		t.Fatalf("current tool altered: %#v", currentTool)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseAwareCompaction::test_no_phase_metadata_works_normally
func TestPhaseAwareCompactionNoPhaseMetadata(t *testing.T) {
	conv := NewNodeConversation("test")

	_, _ = conv.AddAssistantMessage("tool call", []map[string]any{
		testToolCall("c1", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c1", strings.Repeat("data ", 5000), ToolResultOptions{
		SpilloverPath: strPtrTest("/data/s1.txt"),
	})

	_, _ = conv.AddAssistantMessage("another tool call", []map[string]any{
		testToolCall("c2", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c2", strings.Repeat("more ", 100), ToolResultOptions{})

	pruned, err := conv.PruneOldToolResults(100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if pruned < 1 {
		t.Fatalf("pruned = %d, want >= 1", pruned)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseAwareCompaction::test_pruned_message_preserves_phase_metadata
func TestPhaseAwareCompactionPreservesPhaseMetadata(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("research")

	_, _ = conv.AddAssistantMessage("tool call", []map[string]any{
		testToolCall("c1", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c1", strings.Repeat("data ", 5000), ToolResultOptions{
		SpilloverPath: strPtrTest("/data/s1.txt"),
	})

	conv.SetCurrentPhase("report")
	_, _ = conv.AddAssistantMessage("recent", []map[string]any{
		testToolCall("c2", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c2", strings.Repeat("x", 200), ToolResultOptions{})

	if _, err := conv.PruneOldToolResults(0, 100); err != nil {
		t.Fatal(err)
	}

	var pruned *Message
	for _, m := range conv.Messages() {
		if strings.HasPrefix(m.Content, "Pruned") {
			msg := m
			pruned = &msg
			break
		}
	}
	if pruned == nil {
		t.Fatal("no pruned message found")
	}
	if pruned.PhaseID == nil || *pruned.PhaseID != "research" {
		t.Fatalf("pruned phase = %v", pruned.PhaseID)
	}
}

// Parity: core/tests/test_phase_compaction.py::TestPhaseAwareCompaction::test_pathless_result_not_pruned
func TestPhaseAwareCompactionPathlessResultNotPruned(t *testing.T) {
	conv := NewNodeConversation("test")
	conv.SetCurrentPhase("research")
	_, _ = conv.AddAssistantMessage("tool call", []map[string]any{
		testToolCall("c1", "s", "{}"),
	}, AssistantMessageOptions{})
	_, _ = conv.AddToolResult("c1", strings.Repeat("big_unrecoverable ", 5000), ToolResultOptions{})
	conv.SetCurrentPhase("report")

	pruned, err := conv.PruneOldToolResults(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 0 {
		t.Fatalf("pruned = %d, want 0", pruned)
	}
	for _, m := range conv.Messages() {
		if m.Role == RoleTool {
			if !strings.HasPrefix(m.Content, "big_unrecoverable") {
				t.Fatalf("tool content changed: %q", m.Content)
			}
			if strings.HasPrefix(m.Content, "Pruned") {
				t.Fatal("pathless result was pruned")
			}
		}
	}
}

// Parity: core/tests/test_agent_loop_prompting_cache.py::test_dynamic_suffix_empty_without_narrative
func TestPromptingCacheDynamicSuffixEmptyWithoutNarrative(t *testing.T) {
	suffix := BuildSystemPromptDynamicSuffix(PromptSpec{IdentityPrompt: "id"})
	if suffix != "" {
		t.Fatalf("suffix = %q, want empty", suffix)
	}
}

// Parity: core/tests/test_agent_loop_prompting_cache.py::test_dynamic_suffix_is_narrative_only
func TestPromptingCacheDynamicSuffixIsNarrativeOnly(t *testing.T) {
	suffix := BuildSystemPromptDynamicSuffix(PromptSpec{Narrative: "the narrative"})
	if suffix != "the narrative" {
		t.Fatalf("suffix = %q", suffix)
	}
	if strings.Contains(suffix, "Current date and time") {
		t.Fatalf("suffix carries a timestamp: %q", suffix)
	}
}

// Parity: core/tests/test_agent_loop_prompting_cache.py::test_static_prompt_carries_no_timestamp
func TestPromptingCacheStaticPromptCarriesNoTimestamp(t *testing.T) {
	static := BuildSystemPromptStatic(PromptSpec{IdentityPrompt: "id", FocusPrompt: "focus"})
	if strings.Contains(static, "Current date and time") {
		t.Fatalf("static carries a timestamp: %q", static)
	}
}
