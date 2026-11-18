package llm

import (
	"reflect"
	"sort"
	"testing"
)

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_default_construction
func TestEventDefaultConstruction(t *testing.T) {
	events := []StreamEvent{
		TextDeltaEvent{},
		TextEndEvent{},
		ToolCallEvent{},
		ToolResultEvent{},
		ReasoningStartEvent{},
		ReasoningDeltaEvent{},
		FinishEvent{},
		StreamErrorEvent{},
	}
	for _, event := range events {
		if event.StreamEventType() == "" {
			t.Errorf("%T has empty type", event)
		}
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_text_delta_defaults
func TestTextDeltaDefaults(t *testing.T) {
	e := TextDeltaEvent{}
	if e.StreamEventType() != "text_delta" || e.Content != "" || e.Snapshot != "" {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_text_end_defaults
func TestTextEndDefaults(t *testing.T) {
	e := TextEndEvent{}
	if e.StreamEventType() != "text_end" || e.FullText != "" {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_tool_call_defaults
func TestToolCallDefaults(t *testing.T) {
	e := ToolCallEvent{}
	if e.StreamEventType() != "tool_call" || e.ToolUseID != "" || e.ToolName != "" || e.ToolInput != nil {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_tool_result_defaults
func TestToolResultDefaults(t *testing.T) {
	e := ToolResultEvent{}
	if e.StreamEventType() != "tool_result" || e.ToolUseID != "" || e.Content != "" || e.IsError {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_reasoning_start_defaults
func TestReasoningStartDefaults(t *testing.T) {
	if got := (ReasoningStartEvent{}).StreamEventType(); got != "reasoning_start" {
		t.Errorf("type = %q", got)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_reasoning_delta_defaults
func TestReasoningDeltaDefaults(t *testing.T) {
	e := ReasoningDeltaEvent{}
	if e.StreamEventType() != "reasoning_delta" || e.Content != "" {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_finish_defaults
func TestFinishDefaults(t *testing.T) {
	e := FinishEvent{}
	if e.StreamEventType() != "finish" || e.StopReason != "" || e.InputTokens != 0 || e.OutputTokens != 0 || e.Model != "" {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventDefaults::test_stream_error_defaults
func TestStreamErrorDefaults(t *testing.T) {
	e := StreamErrorEvent{}
	if e.StreamEventType() != "error" || e.Error != "" || e.Recoverable {
		t.Errorf("unexpected defaults: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_text_delta_with_values
func TestTextDeltaWithValues(t *testing.T) {
	e := TextDeltaEvent{Content: "hello", Snapshot: "hello world"}
	if e.Content != "hello" || e.Snapshot != "hello world" {
		t.Errorf("unexpected values: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_text_end_with_values
func TestTextEndWithValues(t *testing.T) {
	e := TextEndEvent{FullText: "the complete response"}
	if e.FullText != "the complete response" {
		t.Errorf("full_text = %q", e.FullText)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_tool_call_with_values
func TestToolCallWithValues(t *testing.T) {
	input := map[string]any{"query": "python", "num_results": 5}
	e := ToolCallEvent{ToolUseID: "call_abc123", ToolName: "web_search", ToolInput: input}
	if e.ToolUseID != "call_abc123" || e.ToolName != "web_search" || !reflect.DeepEqual(e.ToolInput, input) {
		t.Errorf("unexpected values: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_tool_result_with_values
func TestToolResultWithValues(t *testing.T) {
	e := ToolResultEvent{ToolUseID: "call_abc123", Content: "search results here", IsError: false}
	if e.ToolUseID != "call_abc123" || e.Content != "search results here" || e.IsError {
		t.Errorf("unexpected values: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_tool_result_error
func TestToolResultError(t *testing.T) {
	e := ToolResultEvent{ToolUseID: "call_fail", Content: "timeout", IsError: true}
	if !e.IsError {
		t.Error("is_error should be true")
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_reasoning_delta_with_content
func TestReasoningDeltaWithContent(t *testing.T) {
	e := ReasoningDeltaEvent{Content: "Let me think about this..."}
	if e.Content != "Let me think about this..." {
		t.Errorf("content = %q", e.Content)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_finish_with_values
func TestFinishWithValues(t *testing.T) {
	e := FinishEvent{StopReason: "end_turn", InputTokens: 150, OutputTokens: 300, Model: "claude-haiku-4-5"}
	if e.StopReason != "end_turn" || e.InputTokens != 150 || e.OutputTokens != 300 || e.Model != "claude-haiku-4-5" {
		t.Errorf("unexpected values: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_stream_error_with_values
func TestStreamErrorWithValues(t *testing.T) {
	e := StreamErrorEvent{Error: "rate limit exceeded", Recoverable: true}
	if e.Error != "rate limit exceeded" || !e.Recoverable {
		t.Errorf("unexpected values: %+v", e)
	}
}

// Parity: core/tests/test_stream_events.py::TestTypeLiterals::test_type_value
func TestTypeValue(t *testing.T) {
	cases := []struct {
		event StreamEvent
		want  string
	}{
		{TextDeltaEvent{}, "text_delta"},
		{TextEndEvent{}, "text_end"},
		{ToolCallEvent{}, "tool_call"},
		{ToolResultEvent{}, "tool_result"},
		{ReasoningStartEvent{}, "reasoning_start"},
		{ReasoningDeltaEvent{}, "reasoning_delta"},
		{FinishEvent{}, "finish"},
		{StreamErrorEvent{}, "error"},
	}
	for _, c := range cases {
		if got := c.event.StreamEventType(); got != c.want {
			t.Errorf("%T type = %q, want %q", c.event, got, c.want)
		}
	}
}

// Parity: core/tests/test_stream_events.py::TestTypeLiterals::test_all_types_unique
func TestAllTypesUnique(t *testing.T) {
	types := AllStreamEventTypes()
	seen := map[string]bool{}
	for _, typ := range types {
		if seen[typ] {
			t.Errorf("duplicate type value %q in %v", typ, types)
		}
		seen[typ] = true
	}
}

// Parity: core/tests/test_stream_events.py::TestEventSerialization::test_text_delta_asdict
func TestTextDeltaAsdict(t *testing.T) {
	got, err := StreamEventToMap(TextDeltaEvent{Content: "chunk", Snapshot: "full chunk"})
	if err != nil {
		t.Fatalf("to map: %v", err)
	}
	want := map[string]any{"type": "text_delta", "content": "chunk", "snapshot": "full chunk"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventSerialization::test_tool_call_asdict
func TestToolCallAsdict(t *testing.T) {
	got, err := StreamEventToMap(ToolCallEvent{
		ToolUseID: "id_1",
		ToolName:  "calc",
		ToolInput: map[string]any{"expression": "2+2"},
	})
	if err != nil {
		t.Fatalf("to map: %v", err)
	}
	if got["tool_name"] != "calc" {
		t.Errorf("tool_name = %v", got["tool_name"])
	}
	if !reflect.DeepEqual(got["tool_input"], map[string]any{"expression": "2+2"}) {
		t.Errorf("tool_input = %v", got["tool_input"])
	}
}

// Parity: core/tests/test_stream_events.py::TestEventSerialization::test_finish_asdict
func TestFinishAsdict(t *testing.T) {
	got, err := StreamEventToMap(FinishEvent{StopReason: "stop", InputTokens: 10, OutputTokens: 20, Model: "gpt-4"})
	if err != nil {
		t.Fatalf("to map: %v", err)
	}
	want := map[string]any{
		"type":                  "finish",
		"stop_reason":           "stop",
		"input_tokens":          10,
		"output_tokens":         20,
		"cached_tokens":         0,
		"cache_creation_tokens": 0,
		"cost_usd":              0.0,
		"credits":               nil,
		"model":                 "gpt-4",
		"thinking_blocks":       []any{},
		"reasoning_content":     "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventSerialization::test_asdict_contains_type
func TestAsdictContainsType(t *testing.T) {
	events := []StreamEvent{
		TextDeltaEvent{}, TextEndEvent{}, ToolCallEvent{}, ToolResultEvent{},
		ReasoningStartEvent{}, ReasoningDeltaEvent{}, FinishEvent{}, StreamErrorEvent{},
	}
	for _, event := range events {
		got, err := StreamEventToMap(event)
		if err != nil {
			t.Fatalf("%T: %v", event, err)
		}
		if _, ok := got["type"]; !ok {
			t.Errorf("%T map lacks type", event)
		}
	}
}

// Parity: core/tests/test_stream_events.py::TestEventSerialization::test_asdict_keys_match_fields
func TestAsdictKeysMatchFields(t *testing.T) {
	cases := []struct {
		event  StreamEvent
		fields []string
	}{
		{TextDeltaEvent{}, []string{"type", "content", "snapshot"}},
		{TextEndEvent{}, []string{"type", "full_text"}},
		{ToolCallEvent{}, []string{"type", "tool_use_id", "tool_name", "tool_input"}},
		{ToolResultEvent{}, []string{"type", "tool_use_id", "content", "is_error"}},
		{ReasoningStartEvent{}, []string{"type"}},
		{ReasoningDeltaEvent{}, []string{"type", "content"}},
		{FinishEvent{}, []string{
			"type", "stop_reason", "input_tokens", "output_tokens", "cached_tokens",
			"cache_creation_tokens", "cost_usd", "credits", "model", "thinking_blocks",
			"reasoning_content",
		}},
		{StreamErrorEvent{}, []string{"type", "error", "recoverable", "error_type", "upstream_status"}},
	}
	for _, c := range cases {
		got, err := StreamEventToMap(c.event)
		if err != nil {
			t.Fatalf("%T: %v", c.event, err)
		}
		keys := make([]string, 0, len(got))
		for key := range got {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		want := append([]string(nil), c.fields...)
		sort.Strings(want)
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("%T keys = %v, want %v", c.event, keys, want)
		}
	}
}

// Parity: core/tests/test_stream_events.py::TestStreamEventUnion::test_union_has_exactly_expected_members
func TestUnionHasExactlyExpectedMembers(t *testing.T) {
	want := []string{
		"text_delta", "text_end", "tool_call", "tool_result",
		"reasoning_start", "reasoning_delta", "finish", "error",
	}
	if got := AllStreamEventTypes(); !reflect.DeepEqual(got, want) {
		t.Errorf("types = %v, want %v", got, want)
	}
}

// Parity: core/tests/test_stream_events.py::TestEventEquality::test_equal_events
func TestEqualEvents(t *testing.T) {
	a := TextDeltaEvent{Content: "hi", Snapshot: "hi"}
	b := TextDeltaEvent{Content: "hi", Snapshot: "hi"}
	if a != b {
		t.Error("equal events should compare equal")
	}
}

// Parity: core/tests/test_stream_events.py::TestEventEquality::test_unequal_events
func TestUnequalEvents(t *testing.T) {
	a := TextDeltaEvent{Content: "hi"}
	b := TextDeltaEvent{Content: "bye"}
	if a == b {
		t.Error("unequal events should not compare equal")
	}
}

// Parity: core/tests/test_stream_events.py::TestEventEquality::test_hashable
func TestHashable(t *testing.T) {
	e := TextEndEvent{FullText: "hi"}
	set := map[TextEndEvent]bool{e: true}
	if !set[e] {
		t.Error("value event should be usable as a map key")
	}
}

// Parity-exempt: Python's StreamEvent is a PEP 604 union whose members are only
func TestJsonShapeForUnknownEventType(t *testing.T) {
	if _, err := StreamEventToMap(nil); err == nil {
		t.Fatal("expected error for nil event")
	}
}
