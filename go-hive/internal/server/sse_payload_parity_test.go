package server

// SSE payload parity pins for the sse-payload-parity lane: the session events
// feed's default type filter, its replay set, and the per-turn payload fields
// the dual-run probe (scripts/sse_payload_parity.py) found missing.

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// upstreamDefaultSessionEventTypes is routes_events.py::DEFAULT_EVENT_TYPES
// verbatim: the curated chat feed a session subscriber receives when it passes
// no ?types= filter. Keep both lists in sync; the upstream set is the contract.
var upstreamDefaultSessionEventTypes = []string{
	"session_snapshot",
	"client_output_delta",
	"client_input_requested",
	"client_input_received",
	"client_input_committed",
	"llm_text_delta",
	"llm_reasoning_delta",
	"client_reasoning",
	"tool_call_started",
	"tool_call_completed",
	"execution_started",
	"execution_completed",
	"execution_failed",
	"execution_paused",
	"payment_required",
	"node_loop_started",
	"node_loop_iteration",
	"node_loop_completed",
	"llm_turn_complete",
	"goal_progress",
	"node_internal_output",
	"node_stalled",
	"node_retry",
	"node_tool_doom_loop",
	"loop_state_changed",
	"context_compaction_started",
	"context_compacted",
	"context_usage_updated",
	"worker_colony_loaded",
	"colony_created",
	"colony_suggestion_requested",
	"credentials_required",
	"client_credential_form_requested",
	"skill_install_requested",
	"skill_install_decided",
	"subagent_report",
	"queen_phase_changed",
	"trigger_available",
	"trigger_activated",
	"trigger_deactivated",
	"trigger_fired",
	"trigger_removed",
	"trigger_updated",
	"task_created",
	"task_updated",
	"task_deleted",
	"task_list_reset",
	"session_forked",
	"session_title_updated",
}

// Parity: framework/server/routes_events.py::DEFAULT_EVENT_TYPES
func TestSessionEventsDefaultFilterMatchesUpstream(t *testing.T) {
	if len(defaultSessionEventTypes) != len(upstreamDefaultSessionEventTypes) {
		t.Fatalf("default filter has %d entries, upstream has %d", len(defaultSessionEventTypes), len(upstreamDefaultSessionEventTypes))
	}
	for _, name := range upstreamDefaultSessionEventTypes {
		if _, present := defaultSessionEventTypes[name]; !present {
			t.Fatalf("default filter missing upstream type %q", name)
		}
		// Every name must resolve to a real EventType, so a typo cannot turn
		// into a silently never-delivered event.
		if _, ok := eventbus.LookupEventType(name); !ok {
			t.Fatalf("default filter names unknown event type %q", name)
		}
	}
	// Unknown names are dropped from an explicit ?types= filter, and a filter
	// that resolves to nothing falls back to the default set at the call site
	// (upstream's `result or DEFAULT_EVENT_TYPES`).
	if got := parseEventTypeFilter("not_a_type,also_not"); len(got) != 0 {
		t.Fatalf("unknown-only filter = %v, want empty so the caller falls back", got)
	}
	if got := parseEventTypeFilter("client_output_delta,not_a_type"); len(got) != 1 {
		t.Fatalf("mixed filter = %v, want only the known name", got)
	}
}

// Parity: framework/server/routes_events.py::_REPLAY_TYPES
func TestSessionEventsReplaySetMatchesUpstream(t *testing.T) {
	upstream := map[eventbus.EventType]struct{}{
		eventbus.EventClientOutputDelta:    {},
		eventbus.EventExecutionStarted:     {},
		eventbus.EventExecutionCompleted:   {},
		eventbus.EventLLMTurnComplete:      {},
		eventbus.EventToolCallStarted:      {},
		eventbus.EventToolCallCompleted:    {},
		eventbus.EventClientInputRequested: {},
		eventbus.EventClientInputReceived:  {},
		eventbus.EventClientInputCommitted: {},
		eventbus.EventTriggerAvailable:     {},
		eventbus.EventTriggerActivated:     {},
		eventbus.EventTriggerDeactivated:   {},
		eventbus.EventTriggerFired:         {},
		eventbus.EventTriggerRemoved:       {},
		eventbus.EventTriggerUpdated:       {},
		eventbus.EventTaskCreated:          {},
		eventbus.EventTaskUpdated:          {},
		eventbus.EventTaskDeleted:          {},
		eventbus.EventTaskListReset:        {},
		eventbus.EventSessionForked:        {},
	}
	if len(sessionEventsReplayTypes) != len(upstream) {
		t.Fatalf("replay set has %d entries, upstream has %d", len(sessionEventsReplayTypes), len(upstream))
	}
	for eventType := range upstream {
		if _, present := sessionEventsReplayTypes[eventType]; !present {
			t.Fatalf("replay set missing upstream type %q", eventType)
		}
	}
	// Upstream never replays execution_failed (only started/completed); a
	// reconnect must not re-surface a past failure as a live state change.
	if _, present := sessionEventsReplayTypes[eventbus.EventExecutionFailed]; present {
		t.Fatal("replay set contains execution_failed, which upstream does not replay")
	}
}

// Parity: framework/server/routes_events.py::handle_events
func TestSessionEventsReplayHonoursTypeFilter(t *testing.T) {
	bus := eventbus.NewEventBus()
	delta := eventbus.NewAgentEvent(eventbus.EventClientOutputDelta, "queen")
	delta.Data = map[string]any{"content": "x", "snapshot": "x", "inner_turn": 0}
	bus.Publish(delta)
	turnComplete := eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, "queen")
	turnComplete.Data = map[string]any{"stop_reason": "end_turn"}
	bus.Publish(turnComplete)

	// A subscriber that asked for one type gets only that type replayed:
	// upstream intersects the replay set with the subscribed filter
	// (_REPLAY_TYPES & event_type_values).
	filtered := make(chan map[string]any, 8)
	replaySessionEvents(bus, filtered, map[string]struct{}{"client_output_delta": {}}, false, nil)
	if len(filtered) != 1 {
		t.Fatalf("filtered replay queued %d frames, want only the requested type", len(filtered))
	}
	if payload := <-filtered; payload["type"] != "client_output_delta" {
		t.Fatalf("filtered replay frame = %#v, want client_output_delta", payload["type"])
	}

	// Without an explicit filter the caller passes the default set; the whole
	// replayable subset follows, oldest first.
	unfiltered := make(chan map[string]any, 8)
	replaySessionEvents(bus, unfiltered, defaultSessionEventTypes, false, nil)
	if len(unfiltered) != 2 {
		t.Fatalf("unfiltered replay queued %d frames, want both replayable events", len(unfiltered))
	}
	if payload := <-unfiltered; payload["type"] != "client_output_delta" {
		t.Fatalf("replay order starts with %#v, want the oldest event first", payload["type"])
	}
	if payload := <-unfiltered; payload["type"] != "llm_turn_complete" {
		t.Fatalf("replay order continues with %#v, want llm_turn_complete", payload["type"])
	}
}

// Parity: framework/agent_loop/agent_loop.py::_publish_iteration
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
// Parity: framework/agent_loop/agent_loop.py::_drain_injection_queue
func TestChatTurnCarriesPhaseCommitAndContextUsage(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &scriptedChatLLM{},
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_abcdef12",
		Phase:     func() string { return "independent" },
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	recorder := recordChatEvents(bus)
	if err := runtime.DeliverClientInput("hello queen", "corr-sse-1"); err != nil {
		t.Fatalf("DeliverClientInput: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	// node_loop_iteration carries the queen phase on every iteration, as the
	// upstream loop's iteration_metadata_provider reports it.
	iterations := recorder.ofType(eventbus.EventNodeLoopIteration)
	if len(iterations) == 0 {
		t.Fatalf("no node_loop_iteration events; got %v", recorder.types())
	}
	last := iterations[len(iterations)-1]
	if last.Data["phase"] != "independent" {
		t.Fatalf("node_loop_iteration data = %#v, want phase=independent", last.Data)
	}

	// client_input_committed marks the conversation seq the user message
	// landed at and pairs with the received event's correlation id.
	committed := recorder.ofType(eventbus.EventClientInputCommitted)
	if len(committed) != 1 {
		t.Fatalf("client_input_committed count = %d, want 1 (%v)", len(committed), recorder.types())
	}
	if _, ok := committed[0].Data["seq"].(int); !ok {
		t.Fatalf("client_input_committed data = %#v, want an int seq", committed[0].Data)
	}
	if committed[0].CorrelationID == nil || *committed[0].CorrelationID != "corr-sse-1" {
		t.Fatalf("client_input_committed correlation_id = %v, want corr-sse-1", committed[0].CorrelationID)
	}

	// The iteration_start context readout follows the iteration on a normal
	// turn (the opening turn keeps the session-start block's single copy).
	usage := recorder.ofType(eventbus.EventContextUsageUpdated)
	if len(usage) == 0 {
		t.Fatalf("no context_usage_updated events; got %v", recorder.types())
	}
	lastUsage := usage[len(usage)-1]
	for _, key := range []string{
		"usage_ratio", "usage_pct", "message_count", "estimated_tokens",
		"max_context_tokens", "trigger", "breakdown",
	} {
		if _, present := lastUsage.Data[key]; !present {
			t.Fatalf("context_usage_updated missing %q: %#v", key, lastUsage.Data)
		}
	}
	if lastUsage.Data["trigger"] != "iteration_start" {
		t.Fatalf("context_usage_updated trigger = %#v, want iteration_start", lastUsage.Data["trigger"])
	}
	if lastUsage.ExecutionID != nil {
		t.Fatalf("context_usage_updated execution_id = %v, want null", *lastUsage.ExecutionID)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: tests/test_event_loop_node.py::test_client_input_requested_event_published
// Parity: tests/test_event_loop_node.py::test_queen_ask_user_with_real_tools
func TestAskUserTurnStopsAtTheQuestionWithoutAnotherModelCall(t *testing.T) {
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{
			ID:        "ask-1",
			Name:      "ask_user",
			Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
		}}},
		{Content: "never streamed"},
	}}
	runtime, recorder, _ := newQueenChatRuntime(t, model, "", "queen_technology")
	if err := runtime.Deliver("start the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	// Upstream's _run_turn_loop returns on user_input_requested: the ask turn
	// is one model call, and the second scripted completion is never consumed.
	if calls := len(model.requestSnapshot()); calls != 1 {
		t.Fatalf("model calls in the ask_user turn = %d, want 1; frames: %v", calls, recorder.types())
	}
	if got := len(recorder.ofType(eventbus.EventLLMTurnComplete)); got != 1 {
		t.Fatalf("llm_turn_complete count = %d, want 1 (no second iteration); frames: %v", got, recorder.types())
	}
	if got := len(recorder.ofType(eventbus.EventClientOutputDelta)); got != 0 {
		t.Fatalf("client_output_delta count = %d, want 0: the ask turn has no text answer; frames: %v", got, recorder.types())
	}
	if got := len(recorder.ofType(eventbus.EventNodeLoopIteration)); got != 1 {
		t.Fatalf("node_loop_iteration count = %d, want 1; frames: %v", got, recorder.types())
	}
	completed := recorder.ofType(eventbus.EventNodeLoopCompleted)
	if len(completed) != 1 || completed[0].Data["iterations"] != 1 {
		t.Fatalf("node_loop_completed = %#v, want one frame with iterations=1", completed)
	}
	// The question itself is the turn's park and is announced exactly once,
	// with its options, before the awaiting_user boundary.
	requested := recorder.ofType(eventbus.EventClientInputRequested)
	if len(requested) != 1 {
		t.Fatalf("client_input_requested count = %d, want 1; frames: %v", len(requested), recorder.types())
	}
	if requested[0].Data["park_reason"] != "ask_user" {
		t.Fatalf("client_input_requested park_reason = %#v, want ask_user", requested[0].Data["park_reason"])
	}
	if questions, ok := requested[0].Data["questions"].([]map[string]any); !ok || len(questions) != 1 {
		t.Fatalf("client_input_requested questions = %#v, want one question", requested[0].Data["questions"])
	}
}

// TestAskUserRequestIsAnnouncedAtTheTurnBoundary pins the emission point the
// sse-timing-judge probe found: upstream's ask_user turn does not announce the
// wait from the tool handler. It announces it in _await_user_input, after the
// tool batch (tool_call_completed) and after llm_turn_complete. The Go port
// must keep that order so a subscriber sees the question arrive with the park,
// not in the middle of the tool pair.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
// Parity: framework/agent_loop/agent_loop.py::_await_user_input
func TestAskUserRequestIsAnnouncedAtTheTurnBoundary(t *testing.T) {
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{
			ID:        "ask-1",
			Name:      "ask_user",
			Arguments: `{"questions":[{"id":"next","prompt":"Which next?","options":["Ship it","Wait"]}]}`,
		}}},
		{Content: "never streamed"},
	}}
	runtime, recorder, _ := newQueenChatRuntime(t, model, "", "queen_technology")
	if err := runtime.Deliver("start the work"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	index := map[string]int{}
	for position, event := range recorder.snapshot() {
		switch event.Type {
		case eventbus.EventToolCallStarted:
			index["tool_call_started"] = position
		case eventbus.EventToolCallCompleted:
			index["tool_call_completed"] = position
		case eventbus.EventLLMTurnComplete:
			index["llm_turn_complete"] = position
		case eventbus.EventNodeLoopCompleted:
			index["node_loop_completed"] = position
		case eventbus.EventClientInputRequested:
			index["client_input_requested"] = position
		}
	}
	wantOrder := []string{
		"tool_call_started",
		"tool_call_completed",
		"llm_turn_complete",
		"node_loop_completed",
		"client_input_requested",
	}
	last := -1
	for _, name := range wantOrder {
		position, present := index[name]
		if !present {
			t.Fatalf("missing %s; frames: %v", name, recorder.types())
		}
		if position <= last {
			t.Fatalf("%s at %d does not follow the previous frame (order: %v)", name, position, wantOrder)
		}
		last = position
	}
}

// TestImplicitJudgeVerdictFollowsThePreviousTurn pins the judge behaviour the
// probe found reference-only: the queen's permanent loop evaluates each turn
// when its park unblocks, and the skip_judge short-circuit still publishes the
// verdict (RETRY / implicit / empty feedback). The Go port emits the finished
// turn's verdict at the next turn start: after client_input_received and the
// new EXECUTING state, before that message's client_input_committed boundary.
//
// Parity: framework/agent_loop/agent_loop.py::execute (step 6i)
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn (skip_judge)
// Parity: framework/server/queen_orchestrator.py (queen spec sets skip_judge=True)
func TestImplicitJudgeVerdictFollowsThePreviousTurn(t *testing.T) {
	runtime, recorder, _ := newQueenChatRuntime(t, &queenChatLLM{}, "", "queen_technology")

	if err := runtime.Deliver("first turn"); err != nil {
		t.Fatalf("Deliver first: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	if got := len(recorder.ofType(eventbus.EventJudgeVerdict)); got != 0 {
		t.Fatalf("judge_verdict after the first turn = %d, want 0: upstream publishes it when the next turn starts", got)
	}

	if err := runtime.Deliver("second turn"); err != nil {
		t.Fatalf("Deliver second: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 2)

	verdicts := recorder.ofType(eventbus.EventJudgeVerdict)
	if len(verdicts) != 1 {
		t.Fatalf("judge_verdict count = %d, want exactly one for the finished turn; frames: %v", len(verdicts), recorder.types())
	}
	verdict := verdicts[0]
	if verdict.Data["action"] != "RETRY" || verdict.Data["feedback"] != "" {
		t.Fatalf("judge_verdict data = %#v, want RETRY with empty feedback (skip_judge short-circuit)", verdict.Data)
	}
	if verdict.Data["judge_type"] != "implicit" {
		t.Fatalf("judge_verdict judge_type = %#v, want implicit", verdict.Data["judge_type"])
	}
	if verdict.Data["iteration"] != 0 {
		t.Fatalf("judge_verdict iteration = %#v, want the finished turn's index 0", verdict.Data["iteration"])
	}

	// llm_turn_complete is published after the turn body, but it reports the
	// request that was actually sent — not the conversation that has since
	// absorbed the assistant reply.
	completes := recorder.ofType(eventbus.EventLLMTurnComplete)
	if len(completes) != 2 {
		t.Fatalf("llm_turn_complete count = %d, want 2; frames: %v", len(completes), recorder.types())
	}
	if completes[0].Data["message_count"] != 1 || completes[1].Data["message_count"] != 3 {
		t.Fatalf("llm_turn_complete message_counts = %#v/%#v, want the request sizes 1/3",
			completes[0].Data["message_count"], completes[1].Data["message_count"])
	}

	// Position: after the first turn's completion, and inside the second turn
	// between its execution envelope and its client_input_committed frame.
	positions := map[string][]int{}
	for position, event := range recorder.snapshot() {
		positions[string(event.Type)] = append(positions[string(event.Type)], position)
	}
	verdictPos := -1
	for position, event := range recorder.snapshot() {
		if event == verdict {
			verdictPos = position
			break
		}
	}
	if verdictPos < 0 {
		t.Fatal("judge_verdict not found in the recorded stream")
	}
	if completed := positions["execution_completed"]; len(completed) < 1 || verdictPos <= completed[0] {
		t.Fatalf("judge_verdict at %d must follow the first turn's execution_completed %v", verdictPos, completed)
	}
	secondStart := positions["execution_started"][1]
	secondCommit := positions["client_input_committed"][1]
	if verdictPos <= secondStart || verdictPos >= secondCommit {
		t.Fatalf("judge_verdict at %d, want after the second execution_started (%d) and before its client_input_committed (%d)",
			verdictPos, secondStart, secondCommit)
	}
}

// TestToolBatchPublishesPostToolContextReadouts pins the per-tool context
// granularity the sse-frames golden found missing: upstream publishes a
// context_usage_updated readout after every tool result lands in the
// conversation (trigger=post_tool_call) and one more at the batch boundary
// (trigger=post_tool_results), both after tool_call_completed and before the
// batch's llm_turn_complete. The readouts carry no execution id (upstream's
// publish_context_usage does not stamp one), and they must see the stored
// result: the per-tool readout's message_count counts the assistant tool-call
// message and the tool result that were just appended.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (publish_context_usage)
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
func TestToolBatchPublishesPostToolContextReadouts(t *testing.T) {
	model := &scriptedChatLLM{responses: []llm.Completion{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "tool-1",
				Name:      "echo_tool",
				Arguments: `{"value":"x"}`,
			}},
		},
		{Content: "final answer"},
	}}
	execute := func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
		if name != "echo_tool" {
			t.Fatalf("unexpected tool %q", name)
		}
		return "tool-ok", false, nil
	}
	tools := []llm.ToolDef{{Name: "echo_tool", Description: "echoes"}}
	runtime, bus := newBusChatRuntime(t, model, tools, execute)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("use the tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	// The batch's frames, in order: the completion is followed immediately by
	// the per-tool readout, then the batch readout, then llm_turn_complete.
	batch := []string{}
	usageByTrigger := map[string]*eventbus.AgentEvent{}
	for _, event := range recorder.snapshot() {
		switch event.Type {
		case eventbus.EventToolCallCompleted:
			batch = append(batch, "tool_call_completed")
		case eventbus.EventContextUsageUpdated:
			trigger, _ := event.Data["trigger"].(string)
			batch = append(batch, "context_usage_updated:"+trigger)
			if _, seen := usageByTrigger[trigger]; !seen {
				usageByTrigger[trigger] = event
			}
		case eventbus.EventLLMTurnComplete:
			batch = append(batch, "llm_turn_complete")
		}
	}
	wantBatch := []string{
		"tool_call_completed",
		"context_usage_updated:post_tool_call",
		"context_usage_updated:post_tool_results",
		"llm_turn_complete",
	}
	if joined := strings.Join(batch, ","); !strings.Contains(joined, strings.Join(wantBatch, ",")) {
		t.Fatalf("tool-batch frames = %v, want %v in order", batch, wantBatch)
	}

	for _, trigger := range []string{"post_tool_call", "post_tool_results"} {
		frame := usageByTrigger[trigger]
		if frame == nil {
			t.Fatalf("no context_usage_updated frame for trigger %q; frames: %v", trigger, batch)
		}
		if frame.ExecutionID != nil {
			t.Fatalf("context_usage_updated(%s) execution_id = %v, want null", trigger, *frame.ExecutionID)
		}
		if frame.Data["message_count"] != 3 {
			t.Fatalf("context_usage_updated(%s) message_count = %#v, want 3 (user + assistant call + tool result)", trigger, frame.Data["message_count"])
		}
	}
	// The batch boundary emits exactly one post_tool_results readout.
	postBatch := 0
	for _, name := range batch {
		if name == "context_usage_updated:post_tool_results" {
			postBatch++
		}
	}
	if postBatch != 1 {
		t.Fatalf("post_tool_results frames = %d, want 1; frames: %v", postBatch, batch)
	}
}
