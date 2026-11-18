package eventbus

// Parity: tests/test_event_type_extension.py

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// Parity: tests/test_event_type_extension.py::WP2_EVENT_TYPES
var wp2EventTypes = map[EventType]string{
	EventNodeLoopStarted:      "node_loop_started",
	EventNodeLoopIteration:    "node_loop_iteration",
	EventNodeLoopCompleted:    "node_loop_completed",
	EventLLMTextDelta:         "llm_text_delta",
	EventLLMReasoningDelta:    "llm_reasoning_delta",
	EventToolCallStarted:      "tool_call_started",
	EventToolCallCompleted:    "tool_call_completed",
	EventClientOutputDelta:    "client_output_delta",
	EventClientInputRequested: "client_input_requested",
	EventNodeInternalOutput:   "node_internal_output",
	EventNodeInputBlocked:     "node_input_blocked",
	EventNodeStalled:          "node_stalled",
}

// Parity: tests/test_event_type_extension.py::ORIGINAL_EVENT_TYPES
var originalEventTypes = map[EventType]string{
	EventExecutionStarted:    "execution_started",
	EventExecutionCompleted:  "execution_completed",
	EventExecutionFailed:     "execution_failed",
	EventExecutionPaused:     "execution_paused",
	EventExecutionResumed:    "execution_resumed",
	EventStateChanged:        "state_changed",
	EventStateConflict:       "state_conflict",
	EventGoalProgress:        "goal_progress",
	EventGoalAchieved:        "goal_achieved",
	EventConstraintViolation: "constraint_violation",
	EventStreamStarted:       "stream_started",
	EventStreamStopped:       "stream_stopped",
	EventCustom:              "custom",
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_new_member_value
func TestWP2NewMemberValue(t *testing.T) {
	for member, expected := range wp2EventTypes {
		if member.String() != expected {
			t.Errorf("%s value = %q, want %q", member, member.String(), expected)
		}
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_all_12_new_members_exist
func TestWP2All12NewMembersExist(t *testing.T) {
	if len(wp2EventTypes) != 12 {
		t.Fatalf("expected 12 WP-2 members, got %d", len(wp2EventTypes))
	}
	for member := range wp2EventTypes {
		if !member.Valid() {
			t.Errorf("%s is not a valid EventType", member)
		}
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_new_member_string_values_are_unique
func TestWP2NewMemberValuesUnique(t *testing.T) {
	seen := map[string]EventType{}
	for member, value := range wp2EventTypes {
		if prev, ok := seen[value]; ok {
			t.Errorf("duplicate value %q shared by %s and %s", value, prev, member)
		}
		seen[value] = member
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_no_collision_with_original_members
func TestWP2NoCollisionWithOriginalMembers(t *testing.T) {
	for member, value := range wp2EventTypes {
		for origMember, origValue := range originalEventTypes {
			if value == origValue {
				t.Errorf("WP-2 member %s collides with original %s on %q", member, origMember, value)
			}
		}
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_original_members_unchanged
func TestWP2OriginalMembersUnchanged(t *testing.T) {
	for member, expected := range originalEventTypes {
		if member.String() != expected {
			t.Errorf("%s value = %q, want %q", member, member.String(), expected)
		}
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_event_type_is_str_enum
func TestWP2EventTypeIsStrEnum(t *testing.T) {
	if string(EventNodeLoopStarted) != "node_loop_started" {
		t.Errorf("node_loop_started mismatch: %q", string(EventNodeLoopStarted))
	}
	if EventLLMTextDelta.String() != "llm_text_delta" {
		t.Errorf("llm_text_delta mismatch: %q", EventLLMTextDelta.String())
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_event_type_accessible_by_name
func TestWP2EventTypeAccessibleByName(t *testing.T) {
	if got, ok := EventTypeByName("NODE_LOOP_STARTED"); !ok || got != EventNodeLoopStarted {
		t.Errorf("EventTypeByName(NODE_LOOP_STARTED) = %v, %v", got, ok)
	}
	if got, ok := EventTypeByName("TOOL_CALL_COMPLETED"); !ok || got != EventToolCallCompleted {
		t.Errorf("EventTypeByName(TOOL_CALL_COMPLETED) = %v, %v", got, ok)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2EventTypeEnumMembers::test_event_type_accessible_by_value
func TestWP2EventTypeAccessibleByValue(t *testing.T) {
	if got, ok := LookupEventType("node_loop_started"); !ok || got != EventNodeLoopStarted {
		t.Errorf("LookupEventType(node_loop_started) = %v, %v", got, ok)
	}
	if got, ok := LookupEventType("tool_call_completed"); !ok || got != EventToolCallCompleted {
		t.Errorf("LookupEventType(tool_call_completed) = %v, %v", got, ok)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2AgentEventNodeId::test_node_id_defaults_to_none
func TestWP2NodeIDDefaultsToNil(t *testing.T) {
	event := NewAgentEvent(EventExecutionStarted, "stream-1")
	if event.NodeID != nil {
		t.Errorf("node_id = %v, want nil", *event.NodeID)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2AgentEventNodeId::test_node_id_can_be_set
func TestWP2NodeIDCanBeSet(t *testing.T) {
	event := NewAgentEvent(EventLLMTextDelta, "stream-1")
	event.NodeID = StrPtr("email_composer")
	if event.NodeID == nil || *event.NodeID != "email_composer" {
		t.Errorf("node_id = %v", event.NodeID)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2AgentEventNodeId::test_node_id_in_to_dict
func TestWP2NodeIDInToDict(t *testing.T) {
	event := NewAgentEvent(EventToolCallStarted, "stream-1")
	event.NodeID = StrPtr("search_node")
	d := event.ToDict()
	if d["node_id"] != "search_node" {
		t.Errorf("node_id in dict = %v", d["node_id"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2AgentEventNodeId::test_node_id_none_in_to_dict
func TestWP2NodeIDNilInToDict(t *testing.T) {
	event := NewAgentEvent(EventExecutionStarted, "stream-1")
	d := event.ToDict()
	value, present := d["node_id"]
	if !present {
		t.Fatal("node_id key missing from ToDict")
	}
	if value != nil {
		t.Errorf("node_id = %v, want nil", value)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2SubscriptionFilterNode::test_filter_node_defaults_to_none
func TestWP2FilterNodeDefaultsToNil(t *testing.T) {
	sub := Subscription{
		ID:         "sub_1",
		EventTypes: map[EventType]struct{}{EventLLMTextDelta: {}},
		Handler:    func(*AgentEvent) {},
	}
	if sub.FilterNode != nil {
		t.Errorf("filter_node = %v, want nil", *sub.FilterNode)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2SubscriptionFilterNode::test_filter_node_can_be_set
func TestWP2FilterNodeCanBeSet(t *testing.T) {
	sub := Subscription{
		ID:         "sub_1",
		EventTypes: map[EventType]struct{}{EventLLMTextDelta: {}},
		Handler:    func(*AgentEvent) {},
	}
	WithFilterNode("email_composer")(&sub)
	if sub.FilterNode == nil || *sub.FilterNode != "email_composer" {
		t.Errorf("filter_node = %v", sub.FilterNode)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_filter_node_receives_matching_events
func TestWP2FilterNodeReceivesMatching(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventLLMTextDelta}, rec.addEvent, WithFilterNode("node-A"))

	evt := NewAgentEvent(EventLLMTextDelta, "stream-1")
	evt.NodeID = StrPtr("node-A")
	evt.Data = map[string]any{"content": "hello"}
	bus.Publish(evt)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].NodeID == nil || *got[0].NodeID != "node-A" {
		t.Errorf("node_id = %v", got[0].NodeID)
	}
	if got[0].Data["content"] != "hello" {
		t.Errorf("content = %v", got[0].Data["content"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_filter_node_rejects_non_matching_events
func TestWP2FilterNodeRejectsNonMatching(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventLLMTextDelta}, rec.addEvent, WithFilterNode("node-B"))

	evt := NewAgentEvent(EventLLMTextDelta, "stream-1")
	evt.NodeID = StrPtr("node-A")
	bus.Publish(evt)

	if got := rec.eventSnapshot(); len(got) != 0 {
		t.Fatalf("received %d events, want 0", len(got))
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_no_filter_node_receives_all_events
func TestWP2NoFilterNodeReceivesAll(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventLLMTextDelta}, rec.addEvent)

	for _, nodeID := range []*string{StrPtr("node-A"), StrPtr("node-B"), nil} {
		evt := NewAgentEvent(EventLLMTextDelta, "stream-1")
		evt.NodeID = nodeID
		bus.Publish(evt)
	}

	if got := rec.eventSnapshot(); len(got) != 3 {
		t.Fatalf("received %d events, want 3", len(got))
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_interleaved_nodes_separated_by_filter
func TestWP2InterleavedNodesSeparated(t *testing.T) {
	bus := NewEventBus()
	recA := &recorder{}
	recB := &recorder{}
	bus.Subscribe([]EventType{EventLLMTextDelta}, recA.addEvent, WithFilterNode("email_sender"))
	bus.Subscribe([]EventType{EventLLMTextDelta}, recB.addEvent, WithFilterNode("inbox_scanner"))

	publish := func(nodeID, content string) {
		evt := NewAgentEvent(EventLLMTextDelta, "webhook")
		evt.NodeID = StrPtr(nodeID)
		evt.Data = map[string]any{"content": content}
		bus.Publish(evt)
	}
	publish("email_sender", "Dear Jo")
	publish("inbox_scanner", "RE: Meeting conf")
	publish("email_sender", "hn, Thank you for")
	publish("inbox_scanner", "irmed for Thursday")

	a := recA.eventSnapshot()
	b := recB.eventSnapshot()
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("node counts = %d, %d, want 2, 2", len(a), len(b))
	}
	if a[0].Data["content"] != "Dear Jo" || a[1].Data["content"] != "hn, Thank you for" {
		t.Errorf("node A contents = %v, %v", a[0].Data["content"], a[1].Data["content"])
	}
	if b[0].Data["content"] != "RE: Meeting conf" || b[1].Data["content"] != "irmed for Thursday" {
		t.Errorf("node B contents = %v, %v", b[0].Data["content"], b[1].Data["content"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_filter_node_combined_with_filter_stream
func TestWP2FilterNodeCombinedWithFilterStream(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe(
		[]EventType{EventToolCallStarted},
		rec.addEvent,
		WithFilterStream("webhook"),
		WithFilterNode("search_node"),
	)

	matching := NewAgentEvent(EventToolCallStarted, "webhook")
	matching.NodeID = StrPtr("search_node")
	bus.Publish(matching)
	wrongStream := NewAgentEvent(EventToolCallStarted, "api")
	wrongStream.NodeID = StrPtr("search_node")
	bus.Publish(wrongStream)
	wrongNode := NewAgentEvent(EventToolCallStarted, "webhook")
	wrongNode.NodeID = StrPtr("other_node")
	bus.Publish(wrongNode)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].StreamID != "webhook" || got[0].NodeID == nil || *got[0].NodeID != "search_node" {
		t.Errorf("stream/node = %q / %v", got[0].StreamID, got[0].NodeID)
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_wait_for_with_node_id
func TestWP2WaitForWithNodeID(t *testing.T) {
	bus := NewEventBus()
	go func() {
		time.Sleep(10 * time.Millisecond)
		evt := NewAgentEvent(EventNodeLoopCompleted, "stream-1")
		evt.NodeID = StrPtr("target_node")
		evt.Data = map[string]any{"iterations": 3}
		bus.Publish(evt)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event := bus.WaitFor(ctx, EventNodeLoopCompleted, WithFilterNode("target_node"))
	if event == nil {
		t.Fatal("WaitFor returned nil")
	}
	if event.NodeID == nil || *event.NodeID != "target_node" {
		t.Errorf("node_id = %v", event.NodeID)
	}
	if event.Data["iterations"] != 3 {
		t.Errorf("iterations = %v", event.Data["iterations"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2NodeLevelRouting::test_wait_for_ignores_wrong_node
func TestWP2WaitForIgnoresWrongNode(t *testing.T) {
	bus := NewEventBus()
	go func() {
		time.Sleep(10 * time.Millisecond)
		wrong := NewAgentEvent(EventNodeLoopCompleted, "stream-1")
		wrong.NodeID = StrPtr("wrong_node")
		bus.Publish(wrong)
		time.Sleep(10 * time.Millisecond)
		right := NewAgentEvent(EventNodeLoopCompleted, "stream-1")
		right.NodeID = StrPtr("target_node")
		right.Data = map[string]any{"iterations": 5}
		bus.Publish(right)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event := bus.WaitFor(ctx, EventNodeLoopCompleted, WithFilterNode("target_node"))
	if event == nil {
		t.Fatal("WaitFor returned nil")
	}
	if event.NodeID == nil || *event.NodeID != "target_node" {
		t.Errorf("node_id = %v", event.NodeID)
	}
	if event.Data["iterations"] != 5 {
		t.Errorf("iterations = %v", event.Data["iterations"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_node_loop_started
func TestWP2EmitNodeLoopStarted(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventNodeLoopStarted}, rec.addEvent)
	bus.EmitNodeLoopStarted("s1", "n1", nil, IntPtr(10))

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].NodeID == nil || *got[0].NodeID != "n1" {
		t.Errorf("node_id = %v", got[0].NodeID)
	}
	if got[0].Data["max_iterations"] != 10 {
		t.Errorf("max_iterations = %v", got[0].Data["max_iterations"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_node_loop_iteration
func TestWP2EmitNodeLoopIteration(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventNodeLoopIteration}, rec.addEvent)
	bus.EmitNodeLoopIteration("s1", "n1", 3, nil, nil)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["iteration"] != 3 {
		t.Errorf("iteration = %v", got[0].Data["iteration"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_node_loop_completed
func TestWP2EmitNodeLoopCompleted(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventNodeLoopCompleted}, rec.addEvent)
	bus.EmitNodeLoopCompleted("s1", "n1", 5, nil)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["iterations"] != 5 {
		t.Errorf("iterations = %v", got[0].Data["iterations"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_llm_text_delta
func TestWP2EmitLLMTextDelta(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventLLMTextDelta}, rec.addEvent)
	bus.EmitLLMTextDelta("s1", "n1", "hello", "hello world", nil, 0)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["content"] != "hello" {
		t.Errorf("content = %v", got[0].Data["content"])
	}
	if got[0].Data["snapshot"] != "hello world" {
		t.Errorf("snapshot = %v", got[0].Data["snapshot"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_tool_call_started
func TestWP2EmitToolCallStarted(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventToolCallStarted}, rec.addEvent)
	bus.EmitToolCallStarted("s1", "n1", "call_1", "web_search", map[string]any{"query": "test"}, nil)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["tool_name"] != "web_search" {
		t.Errorf("tool_name = %v", got[0].Data["tool_name"])
	}
	if !reflect.DeepEqual(got[0].Data["tool_input"], map[string]any{"query": "test"}) {
		t.Errorf("tool_input = %v", got[0].Data["tool_input"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_tool_call_completed
func TestWP2EmitToolCallCompleted(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventToolCallCompleted}, rec.addEvent)
	bus.EmitToolCallCompleted("s1", "n1", "call_1", "web_search", "3 results found", false, nil)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["result"] != "3 results found" {
		t.Errorf("result = %v", got[0].Data["result"])
	}
	if got[0].Data["is_error"] != false {
		t.Errorf("is_error = %v", got[0].Data["is_error"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_client_output_delta
func TestWP2EmitClientOutputDelta(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventClientOutputDelta}, rec.addEvent)
	bus.EmitClientOutputDelta("s1", "n1", "chunk", "full chunk", nil, nil, 0)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["content"] != "chunk" {
		t.Errorf("content = %v", got[0].Data["content"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_emit_node_stalled
func TestWP2EmitNodeStalled(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe([]EventType{EventNodeStalled}, rec.addEvent)
	bus.EmitNodeStalled("s1", "n1", "no progress after 10 iterations", nil)

	got := rec.eventSnapshot()
	if len(got) != 1 {
		t.Fatalf("received %d events, want 1", len(got))
	}
	if got[0].Data["reason"] != "no progress after 10 iterations" {
		t.Errorf("reason = %v", got[0].Data["reason"])
	}
}

// Parity: tests/test_event_type_extension.py::TestWP2ConveniencePublishers::test_convenience_publishers_set_node_id
func TestWP2ConveniencePublishersSetNodeID(t *testing.T) {
	bus := NewEventBus()
	rec := &recorder{}
	bus.Subscribe(
		[]EventType{EventLLMTextDelta, EventToolCallStarted},
		rec.addEvent,
		WithFilterNode("my_node"),
	)

	bus.EmitLLMTextDelta("s1", "my_node", "hi", "hi", nil, 0)
	bus.EmitToolCallStarted("s1", "my_node", "c1", "calc", nil, nil)
	bus.EmitLLMTextDelta("s1", "other_node", "bye", "bye", nil, 0)

	got := rec.eventSnapshot()
	if len(got) != 2 {
		t.Fatalf("received %d events, want 2", len(got))
	}
	for _, e := range got {
		if e.NodeID == nil || *e.NodeID != "my_node" {
			t.Errorf("node_id = %v", e.NodeID)
		}
	}
}
