package agentloop

// Cold-resume cursor persistence for the loop-owned state.
//
// A restarted loop must continue the previous run's cumulative tool-call count
// (so LoopConfig.ToolCallLifetimeBudget stays a true cap) and the turn-level
// doom fingerprint window, while never dropping the cursor fields another
// writer owns (next_seq, recent_responses, pending_input, park records).
//
// Parity: core/tests/test_event_loop_node.py::TestToolCallLifetimeBudget::test_resume_restores_cursor_and_tool_calls_used
// Parity: core/tests/test_event_loop_node.py::TestFingerprintToolCalls::test_basic_fingerprint
// Parity: core/tests/test_event_loop_node.py::TestIsToolDoomLoop::test_at_threshold_identical

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func readTestCursor(t *testing.T, store ConversationStore) map[string]any {
	t.Helper()
	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	if cursor == nil {
		t.Fatal("cursor is nil, want the persisted checkpoint")
	}
	return cursor
}

func toolCallResponse(id, name, arguments string) scriptedResponse {
	return scriptedResponse{toolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: arguments}}}
}

// TestCursorStateSurvivesColdRestart drives one real turn, rebuilds the loop
// over the same FileConversationStore, and proves the restored counter and
// fingerprint window participate in detection instead of merely reading back.
//
// Parity: core/tests/test_event_loop_node.py::TestToolCallLifetimeBudget::test_resume_restores_cursor_and_tool_calls_used
func TestCursorStateSurvivesColdRestart(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	seed := map[string]any{
		"next_seq":                 42,
		"recent_responses":         []any{"previous reply"},
		"pending_input":            map[string]any{"reason": "ask_user"},
		"park_reason":              "ask_user",
		"parked_at":                "2026-11-25T00:00:00Z",
		"tool_calls_used":          0,
		"recent_tool_fingerprints": []any{},
	}
	if err := store.WriteCursor(seed); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}

	firstExecutor := &replayRecorder{result: ToolResult{Content: "contents"}}
	firstCompleter := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("a0", "read_file", `{"path":"a.txt"}`),
	}}
	firstCtx := judgeTestContext(firstCompleter, nil, "read_file")
	firstCtx.StopAfterToolBatch = func() bool { return firstExecutor.count() >= 1 }
	firstLoop := NewAgentLoop(LoopConfig{
		MaxIterations:           10,
		StallDetectionThreshold: 999,
		ToolCallLifetimeBudget:  5,
	}, firstExecutor.execute)
	firstConv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "system", Store: store})
	if err := firstLoop.RestoreCursorState(firstConv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	// A fresh store has no counter/window; restoring must not invent one.
	if got := firstLoop.ToolCallsUsed(); got != 0 {
		t.Fatalf("fresh loop tool calls = %d, want 0", got)
	}
	// The restore itself must leave every other cursor field untouched.
	restoredCursor := readTestCursor(t, store)
	if got := intOr(restoredCursor["next_seq"], -1); got != 42 {
		t.Fatalf("next_seq after restore = %d, want 42", got)
	}
	if got := cursorRecentResponses(restoredCursor); !reflect.DeepEqual(got, []string{"previous reply"}) {
		t.Fatalf("recent_responses after restore = %#v", got)
	}
	pending, _ := restoredCursor["pending_input"].(map[string]any)
	if pending["reason"] != "ask_user" {
		t.Fatalf("pending_input after restore = %#v", restoredCursor["pending_input"])
	}

	result, err := firstLoop.ExecuteTurn(context.Background(), firstCtx, firstConv, nil)
	if err != nil {
		t.Fatalf("first ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("first turn = %+v, want success", result)
	}
	if result.ToolCallsUsed != 1 {
		t.Fatalf("first turn ToolCallsUsed = %d, want 1", result.ToolCallsUsed)
	}

	cursor := readTestCursor(t, store)
	if got := cursorInt(cursor["tool_calls_used"]); got != 1 {
		t.Fatalf("persisted tool_calls_used = %d, want 1", got)
	}
	wantFingerprints := [][]ToolFingerprint{{
		{Name: "read_file", Arguments: `{"path":"a.txt"}`},
	}}
	if got := cursorToolFingerprints(cursor["recent_tool_fingerprints"]); !reflect.DeepEqual(got, wantFingerprints) {
		t.Fatalf("persisted recent_tool_fingerprints = %#v, want %#v", got, wantFingerprints)
	}
	// The loop's own checkpoint may rewrite recent_responses/pending_input with
	// the same values; it must never drop the keys another writer owns.
	if _, present := cursor["pending_input"]; !present {
		t.Fatal("pending_input key was dropped by the loop checkpoint")
	}
	if _, present := cursor["park_reason"]; !present {
		t.Fatal("park_reason key was dropped by the loop checkpoint")
	}
	if _, present := cursor["parked_at"]; !present {
		t.Fatal("parked_at key was dropped by the loop checkpoint")
	}

	// Cold restart: a brand-new loop restores the checkpoint.
	secondExecutor := &replayRecorder{result: ToolResult{Content: "contents"}}
	secondCompleter := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("a1", "read_file", `{"path":"a.txt"}`),
		toolCallResponse("a2", "read_file", `{"path":"a.txt"}`),
	}}
	secondCtx := judgeTestContext(secondCompleter, nil, "read_file")
	secondCtx.StopAfterToolBatch = func() bool { return secondExecutor.count() >= 2 }
	secondLoop := NewAgentLoop(LoopConfig{
		MaxIterations:           10,
		StallDetectionThreshold: 999,
		ToolCallLifetimeBudget:  5,
	}, secondExecutor.execute)
	secondConv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if secondConv == nil {
		t.Fatal("RestoreConversation returned nil over a populated store")
	}
	if err := secondLoop.RestoreCursorState(secondConv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := secondLoop.ToolCallsUsed(); got != 1 {
		t.Fatalf("restored tool calls = %d, want 1", got)
	}
	if got := secondLoop.DoomWindow.Depth(); got != 1 {
		t.Fatalf("restored doom window depth = %d, want 1", got)
	}

	// The resumed turn appends the same batch twice. With the restored window
	// kept, the third identical turn in the window trips the guard; a cleared
	// window would need three more turns and never fire here.
	if _, err := secondLoop.ExecuteTurn(context.Background(), secondCtx, secondConv, nil); err != nil {
		t.Fatalf("second ExecuteTurn: %v", err)
	}
	if got := secondLoop.Counter("doom_loop_window_detected"); got != 1 {
		t.Fatalf("doom_loop_window_detected = %d, want 1 (restored history must count)", got)
	}
	if got := secondLoop.ToolCallsUsed(); got != 3 {
		t.Fatalf("tool calls after resumed turn = %d, want 3", got)
	}

	// The detection cleared the window (upstream's branch clear); the cleared
	// state must be persisted, not left stale.
	cursor = readTestCursor(t, store)
	if got := cursorInt(cursor["tool_calls_used"]); got != 3 {
		t.Fatalf("persisted tool_calls_used after resumed turn = %d, want 3", got)
	}
	if got := cursorToolFingerprints(cursor["recent_tool_fingerprints"]); len(got) != 0 {
		t.Fatalf("persisted recent_tool_fingerprints after clear = %#v, want empty", got)
	}

	thirdLoop := NewAgentLoop(LoopConfig{MaxIterations: 10}, nil)
	thirdConv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation (third): %v", err)
	}
	if err := thirdLoop.RestoreCursorState(thirdConv); err != nil {
		t.Fatalf("RestoreCursorState (third): %v", err)
	}
	if got := thirdLoop.ToolCallsUsed(); got != 3 {
		t.Fatalf("third loop tool calls = %d, want 3", got)
	}
	if got := thirdLoop.DoomWindow.Depth(); got != 0 {
		t.Fatalf("third loop doom window depth = %d, want 0 after clear", got)
	}
}

// TestRestoredToolCallCountCapsLifetimeBudget proves the restored counter is
// live state: with four calls already spent, a two-call batch may only run the
// first call and the second is deferred by the lifetime budget.
//
// Parity: core/tests/test_event_loop_node.py::TestToolCallLifetimeBudget::test_resume_restores_cursor_and_tool_calls_used
func TestRestoredToolCallCountCapsLifetimeBudget(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	if err := store.WriteCursor(map[string]any{
		"next_seq":                 2,
		"recent_responses":         []any{"earlier reply"},
		"tool_calls_used":          4,
		"recent_tool_fingerprints": []any{},
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}

	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "a", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "b", Name: "read_file", Arguments: `{"path":"b.txt"}`},
		}},
		{content: "wound down"},
	}}
	ctx := judgeTestContext(completer, nil, "read_file")
	ctx.StopAfterToolBatch = func() bool { return executor.count() >= 1 }
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:           10,
		StallDetectionThreshold: 999,
		ToolCallLifetimeBudget:  5,
	}, executor.execute)
	// RestoreConversation needs a persisted conversation, not only a cursor.
	seedConv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "system", Store: store})
	if _, err := seedConv.AddUserMessage("original task", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	conv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if conv == nil {
		t.Fatal("RestoreConversation returned nil")
	}
	if err := loop.RestoreCursorState(conv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := loop.ToolCallsUsed(); got != 4 {
		t.Fatalf("restored tool calls = %d, want 4", got)
	}

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := executor.count(); got != 1 {
		t.Fatalf("executed %d calls, want 1 (the second exceeds the lifetime budget)", got)
	}
	if got := loop.ToolCallsUsed(); got != 5 {
		t.Fatalf("tool calls = %d, want 5 (the restored 4 plus the admitted call)", got)
	}
	cursor := readTestCursor(t, store)
	if got := cursorInt(cursor["tool_calls_used"]); got != 5 {
		t.Fatalf("persisted tool_calls_used = %d, want 5", got)
	}
}

// iterationRecorder captures the 1-based iteration index the loop publishes
// before each model call (upstream's _publish_iteration).
type iterationRecorder struct {
	mu   sync.Mutex
	seen []int
}

func (r *iterationRecorder) IterationStarted(iteration int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, iteration)
}
func (r *iterationRecorder) LLMTurnComplete(llm.Completion, int) {}
func (r *iterationRecorder) AssistantText(string, string, int)   {}
func (r *iterationRecorder) ToolCallStarted(ToolCallRequest)     {}
func (r *iterationRecorder) ToolCallCompleted(ToolCallRequest, ToolResult) {
}

func (r *iterationRecorder) iterations() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.seen...)
}

// seedCursorConversation creates a stored conversation with one user message,
// so RestoreConversation returns non-nil (upstream's restore needs a transcript
// before it reads the cursor).
func seedCursorConversation(t *testing.T, store ConversationStore) *NodeConversation {
	t.Helper()
	conv := NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: "system", Store: store})
	if _, err := conv.AddUserMessage("original task", UserMessageOptions{}); err != nil {
		t.Fatalf("AddUserMessage: %v", err)
	}
	return conv
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
// (iteration/node_id/outputs) and ::restore (start_iteration = iteration + 1)
//
// The checkpoint carries upstream's JSON shape — iteration as a number, node_id
// as ctx.agent_id, outputs as an object — and a cold restart resumes at the
// next iteration index instead of refilling the counter.
func TestCursorCheckpointPersistsIterationAndNodeID(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	if err := store.WriteCursor(map[string]any{
		"next_seq":      7,
		"pending_input": map[string]any{"reason": "ask_user"},
		"park_reason":   "ask_user",
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("a0", "read_file", `{"path":"a.txt"}`),
		toolCallResponse("a1", "read_file", `{"path":"b.txt"}`),
		{content: "done"},
	}}
	executor := &replayRecorder{result: ToolResult{Content: "contents"}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "read_file")
	conv := seedCursorConversation(t, store)

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.InnerTurns != 3 {
		t.Fatalf("turn = %+v, want success over 3 inner turns", result)
	}

	cursor := readTestCursor(t, store)
	// encoding/json decodes the stored number as float64; a JSON number (not a
	// string) is the upstream-compatible shape.
	iteration, ok := cursor[iterationCursorKey].(float64)
	if !ok || int(iteration) != 2 {
		t.Fatalf("persisted iteration = %#v (%T), want the JSON number 2",
			cursor[iterationCursorKey], cursor[iterationCursorKey])
	}
	if got := cursor[nodeIDCursorKey]; got != "agent" {
		t.Fatalf("persisted node_id = %#v, want the context agent id", got)
	}
	outputs, ok := cursor[outputsCursorKey].(map[string]any)
	if !ok {
		t.Fatalf("persisted outputs = %#v (%T), want a JSON object", cursor[outputsCursorKey], cursor[outputsCursorKey])
	}
	if len(outputs) != 0 {
		t.Fatalf("persisted outputs = %#v, want {} for a run that set none", outputs)
	}
	// The loop checkpoint must not drop the fields other writers own.
	if _, present := cursor["pending_input"]; !present {
		t.Fatal("pending_input key was dropped by the loop checkpoint")
	}
	if cursor["park_reason"] != "ask_user" {
		t.Fatalf("park_reason = %#v, want the preserved park", cursor["park_reason"])
	}

	// Cold restart: the restored index is the last iteration reached, so the
	// next turn starts at 3 and publishes iteration 4 (1-based).
	restoredCompleter := &scriptedCompleter{scenarios: []scriptedResponse{{content: "resumed"}}}
	observer := &iterationRecorder{}
	restoredCtx := judgeTestContext(restoredCompleter, nil)
	restoredCtx.Observer = observer
	secondLoop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, nil)
	secondConv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if secondConv == nil {
		t.Fatal("RestoreConversation returned nil over a populated store")
	}
	if err := secondLoop.RestoreCursorState(secondConv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := secondLoop.CursorIteration(); got != 2 {
		t.Fatalf("restored iteration = %d, want 2", got)
	}
	if got := secondLoop.cursorStartIteration(); got != 3 {
		t.Fatalf("resumed start iteration = %d, want 3 (upstream's iteration+1)", got)
	}
	if got := secondLoop.RestoredNodeID(); got != "agent" {
		t.Fatalf("restored node_id = %q, want agent", got)
	}
	if got := secondLoop.RestoredOutputs(); len(got) != 0 {
		t.Fatalf("restored outputs = %#v, want an empty accumulator", got)
	}
	if _, err := secondLoop.ExecuteTurn(context.Background(), restoredCtx, secondConv, nil); err != nil {
		t.Fatalf("resumed ExecuteTurn: %v", err)
	}
	if got := observer.iterations(); !reflect.DeepEqual(got, []int{4}) {
		t.Fatalf("resumed iterations = %v, want [4] (index 3, published 1-based)", got)
	}
}

// Parity: framework/agent_loop/internals/types.py::OutputAccumulator.to_dict
// and ::restore (values come from the cursor's "outputs" object).
//
// A successful set_output lands in the checkpoint as a JSON object, and a cold
// restart restores it into the loop's accumulator snapshot.
func TestCursorCheckpointRoundTripsOutputs(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("o0", "set_output", `{"key":"draft","value":"hello"}`),
	}}
	executor := &replayRecorder{result: ToolResult{Content: "Output 'draft' set successfully."}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, executor.execute)
	ctx := judgeTestContext(completer, nil, "set_output")
	ctx.AgentSpec.OutputKeys = []string{"draft"}
	// End the turn after the batch so the implicit output-key gate does not
	// judge it; the checkpoint under test is the turn-boundary write.
	ctx.StopAfterToolBatch = func() bool { return true }
	conv := seedCursorConversation(t, store)

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	cursor := readTestCursor(t, store)
	outputs, ok := cursor[outputsCursorKey].(map[string]any)
	if !ok {
		t.Fatalf("persisted outputs = %#v (%T), want a JSON object", cursor[outputsCursorKey], cursor[outputsCursorKey])
	}
	if got := outputs["draft"]; got != "hello" {
		t.Fatalf("persisted outputs[draft] = %#v, want hello", got)
	}

	secondLoop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, nil)
	secondConv, err := RestoreConversation(store, nil, nil)
	if err != nil {
		t.Fatalf("RestoreConversation: %v", err)
	}
	if err := secondLoop.RestoreCursorState(secondConv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := secondLoop.RestoredOutputs(); !reflect.DeepEqual(got, map[string]any{"draft": "hello"}) {
		t.Fatalf("restored outputs = %#v, want the checkpointed accumulator", got)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
// (cursor.get("iteration", 0) + 1, cursor.get("outputs", {}), getattr default)
//
// A cursor written before these fields existed restores upstream's defaults
// instead of failing, and the next checkpoint upgrades it with the live values.
// A cursor without "iteration" carries no consumed-iteration information, so
// the port starts the counter fresh at 0 rather than shifting it by one; an
// explicit iteration value always resumes at value+1.
func TestCursorRestoreDefaultsMissingFields(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	if err := store.WriteCursor(map[string]any{
		"next_seq":    3,
		"park_reason": "turn_done",
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	conv := seedCursorConversation(t, store)

	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, nil)
	if err := loop.RestoreCursorState(conv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := loop.CursorIteration(); got != 0 {
		t.Fatalf("restored iteration = %d, want upstream's default 0", got)
	}
	if got := loop.cursorStartIteration(); got != 0 {
		t.Fatalf("resumed start iteration = %d, want 0 for a legacy cursor", got)
	}
	if got := loop.RestoredNodeID(); got != "" {
		t.Fatalf("restored node_id = %q, want the empty default", got)
	}
	outputs := loop.RestoredOutputs()
	if outputs == nil || len(outputs) != 0 {
		t.Fatalf("restored outputs = %#v, want a non-nil empty accumulator", outputs)
	}

	// The next turn's checkpoint upgrades the legacy cursor: it writes the
	// live iteration/node_id/outputs even though the seed carried none.
	observer := &iterationRecorder{}
	ctx := judgeTestContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}, nil)
	ctx.Observer = observer
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := observer.iterations(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("iterations = %v, want [1] (fresh start 0, published 1-based)", got)
	}
	cursor := readTestCursor(t, store)
	if got := cursorInt(cursor[iterationCursorKey]); got != 0 {
		t.Fatalf("persisted iteration = %d, want 0", got)
	}
	if got := cursor[nodeIDCursorKey]; got != "agent" {
		t.Fatalf("persisted node_id = %#v, want the live context id", got)
	}
	if _, ok := cursor[outputsCursorKey].(map[string]any); !ok {
		t.Fatalf("persisted outputs = %#v (%T), want a JSON object", cursor[outputsCursorKey], cursor[outputsCursorKey])
	}
	if cursor["park_reason"] != "turn_done" {
		t.Fatalf("park_reason = %#v, want the preserved seed park", cursor["park_reason"])
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
//
// A cursor written by an older process may carry legacy shapes: a numeric
// string for iteration, a non-object outputs value, and a non-string node_id.
// The restore tolerates all three (defaults, never an error) and preserves the
// unrelated fields it does not own.
func TestCursorRestoreToleratesLegacyShapes(t *testing.T) {
	store := NewFileConversationStore(t.TempDir())
	if err := store.WriteCursor(map[string]any{
		"next_seq":    9,
		"iteration":   "4",
		"outputs":     []any{"legacy"},
		"node_id":     42,
		"park_reason": "turn_done",
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	conv := seedCursorConversation(t, store)

	loop := NewAgentLoop(LoopConfig{MaxIterations: 10, StallDetectionThreshold: 999}, nil)
	if err := loop.RestoreCursorState(conv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := loop.CursorIteration(); got != 4 {
		t.Fatalf("restored iteration = %d, want the numeric string 4", got)
	}
	if got := loop.RestoredNodeID(); got != "" {
		t.Fatalf("restored node_id = %q, want the empty default for a non-string", got)
	}
	if got := loop.RestoredOutputs(); len(got) != 0 {
		t.Fatalf("restored outputs = %#v, want the empty default for a non-object", got)
	}

	observer := &iterationRecorder{}
	ctx := judgeTestContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}, nil)
	ctx.Observer = observer
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := observer.iterations(); !reflect.DeepEqual(got, []int{6}) {
		t.Fatalf("iterations = %v, want [6] (legacy iteration 4 resumes at index 5)", got)
	}
	cursor := readTestCursor(t, store)
	if cursor["park_reason"] != "turn_done" {
		t.Fatalf("park_reason = %#v, want the preserved legacy park", cursor["park_reason"])
	}

	// A double-encoded legacy outputs object is decoded, not dropped.
	encodedStore := NewFileConversationStore(t.TempDir())
	if err := encodedStore.WriteCursor(map[string]any{
		"outputs": `{"draft":"legacy"}`,
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	encodedConv := seedCursorConversation(t, encodedStore)
	encodedLoop := NewAgentLoop(LoopConfig{MaxIterations: 10}, nil)
	if err := encodedLoop.RestoreCursorState(encodedConv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	if got := encodedLoop.RestoredOutputs(); !reflect.DeepEqual(got, map[string]any{"draft": "legacy"}) {
		t.Fatalf("restored outputs = %#v, want the decoded legacy object", got)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
// (outputs = accumulator.to_dict())
//
// The conversation is the Go port's source of truth for set_output values, so
// its newer value wins; a restored value the transcript can no longer
// reconstruct is re-persisted instead of being dropped by the next checkpoint.
func TestCursorOutputsPreferTranscriptAndKeepRestoredFallback(t *testing.T) {
	// Phase 1: the transcript has no set_output pair, so the restored snapshot
	// is what the next checkpoint writes.
	store := NewFileConversationStore(t.TempDir())
	if err := store.WriteCursor(map[string]any{
		"outputs": map[string]any{"draft": "from-cursor"},
		"node_id": "worker-7",
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	conv := seedCursorConversation(t, store)
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, nil)
	if err := loop.RestoreCursorState(conv); err != nil {
		t.Fatalf("RestoreCursorState: %v", err)
	}
	ctx := judgeTestContext(&scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}, nil)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	cursor := readTestCursor(t, store)
	outputs, _ := cursor[outputsCursorKey].(map[string]any)
	if got := outputs["draft"]; got != "from-cursor" {
		t.Fatalf("persisted outputs[draft] = %#v, want the restored fallback", got)
	}
	if got := cursor[nodeIDCursorKey]; got != "agent" {
		t.Fatalf("persisted node_id = %#v, want the current run's node", got)
	}

	// Phase 2: a set_output the transcript still carries overrides the restored
	// value, so a resumed run never rewinds an output.
	overrideStore := NewFileConversationStore(t.TempDir())
	if err := overrideStore.WriteCursor(map[string]any{
		"outputs": map[string]any{"draft": "stale"},
	}); err != nil {
		t.Fatalf("WriteCursor: %v", err)
	}
	overrideConv := seedCursorConversation(t, overrideStore)
	overrideCompleter := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("o0", "set_output", `{"key":"draft","value":"fresh"}`),
	}}
	overrideExecutor := &replayRecorder{result: ToolResult{Content: "Output 'draft' set successfully."}}
	overrideLoop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, overrideExecutor.execute)
	overrideCtx := judgeTestContext(overrideCompleter, nil, "set_output")
	overrideCtx.AgentSpec.OutputKeys = []string{"draft"}
	overrideCtx.StopAfterToolBatch = func() bool { return true }
	if _, err := overrideLoop.ExecuteTurn(context.Background(), overrideCtx, overrideConv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	overrideCursor := readTestCursor(t, overrideStore)
	overrideOutputs, _ := overrideCursor[outputsCursorKey].(map[string]any)
	if got := overrideOutputs["draft"]; got != "fresh" {
		t.Fatalf("persisted outputs[draft] = %#v, want the transcript's fresh value", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (set_output branch)
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
// (outputs = accumulator.to_dict())
//
// Integration negative: the live loop rejects set_output before dispatch while
// the cursor checkpoint still records the declared key's value for resume. The
// two views must not leak into each other — the judge gate keeps reporting the
// key missing, a value a successful legacy pair already set wins over a later
// rejected call, and an undeclared key never reaches the cursor.
func TestCursorOutputsKeepRejectedSetOutputOutOfJudgeGate(t *testing.T) {
	// Phase 1: the rejection is stored as an error and the judge gate sees no
	// output, yet the checkpoint carries the value the call carried.
	store := NewFileConversationStore(t.TempDir())
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		toolCallResponse("o0", "set_output", `{"key":"draft","value":"hello"}`),
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "set_output")
	ctx.AgentSpec.OutputKeys = []string{"draft"}
	ctx.StopAfterToolBatch = func() bool { return true }
	conv := seedCursorConversation(t, store)

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	var rejected *Message
	for _, message := range conv.Messages() {
		if message.Role == RoleTool && message.ToolUseID != nil && *message.ToolUseID == "o0" {
			stored := message
			rejected = &stored
			break
		}
	}
	if rejected == nil || !rejected.IsError {
		t.Fatalf("stored set_output result = %#v, want the upstream rejection", rejected)
	}
	if got := conv.OutputAccumulator([]string{"draft"}); len(got) != 0 {
		t.Fatalf("judge accumulator = %#v, want the rejected key unset", got)
	}
	cursor := readTestCursor(t, store)
	outputs, _ := cursor[outputsCursorKey].(map[string]any)
	if got := outputs["draft"]; got != "hello" {
		t.Fatalf("persisted outputs[draft] = %#v, want the rejected call's value for resume", got)
	}

	// Phase 2: a successful legacy pair keeps its value when a later live call
	// is rejected, and an undeclared key stays out of the checkpoint.
	legacyStore := NewFileConversationStore(t.TempDir())
	legacyConv := seedCursorConversation(t, legacyStore)
	if _, err := legacyConv.AddAssistantMessage("", []map[string]any{{
		"id":   "old1",
		"type": "function",
		"function": map[string]any{
			"name":      "set_output",
			"arguments": `{"key":"draft","value":"kept"}`,
		},
	}}, AssistantMessageOptions{}); err != nil {
		t.Fatalf("AddAssistantMessage: %v", err)
	}
	if _, err := legacyConv.AddToolResult("old1", "Output 'draft' set successfully.", ToolResultOptions{}); err != nil {
		t.Fatalf("AddToolResult: %v", err)
	}
	overrideCompleter := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{
			{ID: "n1", Name: "set_output", Arguments: `{"key":"draft","value":"world"}`},
			{ID: "n2", Name: "set_output", Arguments: `{"key":"other","value":"nope"}`},
		}},
	}}
	overrideLoop := NewAgentLoop(LoopConfig{MaxIterations: 5, StallDetectionThreshold: 999}, successToolExecutor)
	overrideCtx := judgeTestContext(overrideCompleter, nil, "set_output")
	overrideCtx.AgentSpec.OutputKeys = []string{"draft"}
	overrideCtx.StopAfterToolBatch = func() bool { return true }
	if _, err := overrideLoop.ExecuteTurn(context.Background(), overrideCtx, legacyConv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	overrideCursor := readTestCursor(t, legacyStore)
	overrideOutputs, _ := overrideCursor[outputsCursorKey].(map[string]any)
	if got := overrideOutputs["draft"]; got != "kept" {
		t.Fatalf("persisted outputs[draft] = %#v, want the successful legacy value", got)
	}
	if _, present := overrideOutputs["other"]; present {
		t.Fatalf("persisted outputs = %#v, want the undeclared key dropped", overrideOutputs)
	}
}
