package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// scriptedChatLLM answers with a fixed script: each call consumes the next
// completion, then falls through to a plain text reply. err forces the call to
// fail, which drives the turn-failure path.
type scriptedChatLLM struct {
	mu        sync.Mutex
	responses []llm.Completion
	err       error
	calls     int
}

func (f *scriptedChatLLM) ModelName() string { return "fake-chat-model" }

func (f *scriptedChatLLM) AComplete(_ context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return llm.Completion{}, f.err
	}
	if len(f.responses) == 0 {
		return llm.Completion{Content: "done", Model: "fake-chat-model", StopReason: "end_turn"}, nil
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	if next.Model == "" {
		next.Model = "fake-chat-model"
	}
	if next.StopReason == "" {
		next.StopReason = "end_turn"
	}
	return next, nil
}

func (f *scriptedChatLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type chatEventRecorder struct {
	mu     sync.Mutex
	events []*eventbus.AgentEvent
}

func recordChatEvents(bus *eventbus.EventBus) *chatEventRecorder {
	recorder := &chatEventRecorder{}
	bus.Subscribe(eventbus.AllEventTypes, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		recorder.mu.Lock()
		recorder.events = append(recorder.events, event.Clone())
		recorder.mu.Unlock()
	})
	return recorder
}

func (r *chatEventRecorder) snapshot() []*eventbus.AgentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*eventbus.AgentEvent(nil), r.events...)
}

func (r *chatEventRecorder) ofType(want eventbus.EventType) []*eventbus.AgentEvent {
	var out []*eventbus.AgentEvent
	for _, event := range r.snapshot() {
		if event.Type == want {
			out = append(out, event)
		}
	}
	return out
}

func (r *chatEventRecorder) types() []string {
	out := make([]string, 0, len(r.snapshot()))
	for _, event := range r.snapshot() {
		out = append(out, string(event.Type))
	}
	return out
}

func waitForChatEvents(t *testing.T, recorder *chatEventRecorder, want eventbus.EventType, count int) []*eventbus.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if matches := recorder.ofType(want); len(matches) >= count {
			return matches
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d %s events; got %v", count, want, recorder.types())
	return nil
}

func newBusChatRuntime(t *testing.T, model llm.Completer, tools []llm.ToolDef, execute func(context.Context, string, map[string]any) (string, bool, error)) (*ChatRuntime, *eventbus.EventBus) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     tools,
		Execute:   execute,
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_abcdef10",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, bus
}

// Parity: tests/test_event_loop_node.py::test_lifecycle_events_published
// Parity: tests/test_event_loop_node.py::test_queen_stream_uses_client_output_delta
func TestChatRuntimePublishesTurnEvents(t *testing.T) {
	runtime, bus := newBusChatRuntime(t, &scriptedChatLLM{}, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	completed := waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
	executionID := ""
	if completed[0].ExecutionID != nil {
		executionID = *completed[0].ExecutionID
	}
	if executionID == "" {
		t.Fatalf("execution_completed carries no execution_id: %#v", completed[0])
	}

	// The dashboard leaves "Working…" only on the loop-lifecycle / execution
	// boundary events, so every one of them must be present and ordered. The
	// turn_done boundary announces client_input_requested before the awaiting
	// loop state, as upstream's _await_user_input does on every park.
	order := recorder.types()
	wantOrder := []string{
		"execution_started",
		"loop_state_changed",
		"node_loop_started",
		"node_loop_iteration",
		"client_output_delta",
		"llm_turn_complete",
		"node_loop_completed",
		"client_input_requested",
		"loop_state_changed",
		"execution_completed",
	}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("event order = %v, want %v", order, wantOrder)
	}

	started := recorder.ofType(eventbus.EventExecutionStarted)[0]
	input, _ := started.Data["input"].(map[string]any)
	if input["message"] != "hello queen" {
		t.Fatalf("execution_started data = %#v, want data.input.message to carry the delivered message", started.Data)
	}

	delta := recorder.ofType(eventbus.EventClientOutputDelta)[0]
	if delta.StreamID != "queen" || delta.NodeID == nil || *delta.NodeID != "queen" {
		t.Fatalf("client_output_delta stream/node = %q/%v, want queen/queen", delta.StreamID, delta.NodeID)
	}
	if delta.Data["content"] != "done" || delta.Data["snapshot"] != "done" {
		t.Fatalf("client_output_delta data = %#v, want the assistant text as content+snapshot", delta.Data)
	}
	if delta.Data["iteration"] != 1 {
		t.Fatalf("client_output_delta iteration = %#v, want 1", delta.Data["iteration"])
	}
	if recorder.ofType(eventbus.EventLLMTextDelta) != nil {
		t.Fatal("queen chat streams must publish client_output_delta, not llm_text_delta")
	}

	turnComplete := recorder.ofType(eventbus.EventLLMTurnComplete)[0]
	if turnComplete.Data["model"] != "fake-chat-model" || turnComplete.Data["stop_reason"] != "end_turn" {
		t.Fatalf("llm_turn_complete data = %#v", turnComplete.Data)
	}

	done := recorder.ofType(eventbus.EventExecutionCompleted)[0]
	output, _ := done.Data["output"].(map[string]any)
	if output["status"] != "completed" {
		t.Fatalf("execution_completed output = %#v", done.Data["output"])
	}

	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) != 2 {
		t.Fatalf("loop_state_changed count = %d, want executing then awaiting_user", len(states))
	}
	if states[0].Data["activity"] != "executing" {
		t.Fatalf("first loop state = %#v, want executing", states[0].Data)
	}
	if states[1].Data["activity"] != "awaiting_user" || states[1].Data["park_reason"] != "turn_done" {
		t.Fatalf("final loop state = %#v, want awaiting_user/turn_done", states[1].Data)
	}

	// The session snapshot must agree: after the turn the loop is parked on
	// the user (turn_done), not executing — a dashboard reload cannot re-seed
	// a stuck "Working…" spinner.
	snapshot := eventbus.ComputeSessionSnapshot(bus)
	if snapshot["is_executing"] != false {
		t.Fatalf("snapshot is_executing = %#v, want false", snapshot["is_executing"])
	}
	if snapshot["awaiting_input"] != true || snapshot["queen_busy_reason"] != "awaiting_input" {
		t.Fatalf("snapshot = %#v, want awaiting_input/turn_done idle state", snapshot)
	}
	if snapshot["pending_questions"] != nil {
		t.Fatalf("snapshot pending_questions = %#v, want none", snapshot["pending_questions"])
	}
}

// Parity: tests/test_event_loop_node.py::test_lifecycle_events_published
// Parity: tests/test_event_bus.py::test_emit_tool_call_started
// Parity: tests/test_event_bus.py::test_emit_llm_turn_complete
func TestChatRuntimePublishesToolCallEvents(t *testing.T) {
	model := &scriptedChatLLM{responses: []llm.Completion{
		{
			Content: "checking",
			ToolCalls: []llm.ToolCall{{
				ID:        "tool-1",
				Name:      "echo_tool",
				Arguments: `{"value":"x"}`,
			}},
		},
		{Content: "final answer"},
	}}
	execute := func(_ context.Context, name string, input map[string]any) (string, bool, error) {
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

	starts := recorder.ofType(eventbus.EventToolCallStarted)
	if len(starts) != 1 {
		t.Fatalf("tool_call_started count = %d, want 1 (%v)", len(starts), recorder.types())
	}
	start := starts[0]
	if start.Data["tool_use_id"] != "tool-1" || start.Data["tool_name"] != "echo_tool" {
		t.Fatalf("tool_call_started data = %#v", start.Data)
	}
	input, _ := start.Data["tool_input"].(map[string]any)
	if input["value"] != "x" {
		t.Fatalf("tool_call_started input = %#v", start.Data["tool_input"])
	}

	completions := recorder.ofType(eventbus.EventToolCallCompleted)
	if len(completions) != 1 {
		t.Fatalf("tool_call_completed count = %d, want 1 (%v)", len(completions), recorder.types())
	}
	completed := completions[0]
	if completed.Data["tool_use_id"] != "tool-1" || completed.Data["result"] != "tool-ok" {
		t.Fatalf("tool_call_completed data = %#v", completed.Data)
	}
	if completed.Data["is_error"] != false {
		t.Fatalf("tool_call_completed is_error = %#v, want false", completed.Data["is_error"])
	}

	// Start must precede completion, and both must share the turn's execution.
	if start.Seq >= completed.Seq {
		t.Fatalf("tool_call_started seq %d must precede completion seq %d", start.Seq, completed.Seq)
	}
	if start.ExecutionID == nil || completed.ExecutionID == nil || *start.ExecutionID != *completed.ExecutionID {
		t.Fatalf("tool events execution ids = %v/%v, want the same turn id", start.ExecutionID, completed.ExecutionID)
	}

	// Two LLM calls: the tool-picking turn and the answer turn.
	if got := len(recorder.ofType(eventbus.EventLLMTurnComplete)); got != 2 {
		t.Fatalf("llm_turn_complete count = %d, want 2", got)
	}
	var texts []string
	for _, delta := range recorder.ofType(eventbus.EventClientOutputDelta) {
		texts = append(texts, delta.Data["content"].(string))
	}
	if strings.Join(texts, "|") != "checking|final answer" {
		t.Fatalf("assistant texts = %v, want [checking, final answer]", texts)
	}
	if model.callCount() != 2 {
		t.Fatalf("LLM calls = %d, want 2", model.callCount())
	}
}

// Parity: tests/test_event_bus.py::test_emit_execution_failed
func TestChatRuntimePublishesFailedTurn(t *testing.T) {
	model := &scriptedChatLLM{err: errors.New("provider exploded")}
	runtime, bus := newBusChatRuntime(t, model, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("this will fail"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	failed := waitForChatEvents(t, recorder, eventbus.EventExecutionFailed, 1)
	if failed[0].Data["error"] != "provider exploded" {
		t.Fatalf("execution_failed error = %#v, want the provider error", failed[0].Data["error"])
	}
	if recorder.ofType(eventbus.EventExecutionCompleted) != nil {
		t.Fatal("a failed turn must not publish execution_completed")
	}
	states := recorder.ofType(eventbus.EventLoopStateChanged)
	if len(states) != 2 || states[1].Data["activity"] != "interrupted" || states[1].Data["park_reason"] != "llm_error" {
		t.Fatalf("loop states = %#v, want executing then interrupted/llm_error", states)
	}

	// LastError is the in-process mirror of the published error.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := runtime.LastError(); err != nil && strings.Contains(err.Error(), "provider exploded") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("LastError = %v, want the provider error", runtime.LastError())
}

// Parity: tests/test_event_bus.py::test_park_reason_captured_from_loop_state
// Parity-exempt: framework/server has no Python test for the Go chat session
// SSE stream + events/history wiring; this pins the upstream event names end
// to end against a live chat runtime.
func TestChatSessionEventsStreamAndHistory(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	sessionID := "session_20260101_000000_abcdef11"
	sessionDir := filepath.Join(home.QueensDir(), "default", "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	manager := NewSessionManager()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &scriptedChatLLM{},
		Root:      sessionDir,
		Bus:       bus,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	session := NewSession(sessionID, bus)
	session.Chat = runtime
	manager.AddSession(session)

	app := NewApp(AppDeps{
		Manager:      manager,
		LiveSessions: manager.ListSessions,
		Home:         home,
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	// Subscribe to the session SSE channel before the turn starts so the
	// frames under test are observed live, not replayed.
	response, err := http.Get(server.URL + "/api/sessions/" + sessionID + "/events?types=client_output_delta,execution_completed")
	if err != nil {
		t.Fatalf("GET events stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events stream status = %d, want 200", response.StatusCode)
	}
	waitForSubscriber(t, bus)
	reader := bufio.NewReader(response.Body)

	status, body := postChat(t, server.URL, sessionID, map[string]any{"message": "stream me"})
	if status != http.StatusOK || body["delivered"] != true {
		t.Fatalf("chat status = %d body = %#v, want delivered", status, body)
	}

	// First frame: the rehydrating session snapshot (idle before the turn),
	// then the text delta, then the terminal execution event that clears the
	// Working… state. Frames are data-only (upstream sends no event: name), so
	// the type rides the payload.
	snapshotName, data := readEventFrame(t, reader)
	if snapshotName != "" {
		t.Fatalf("first SSE event name = %q, want data-only frame", snapshotName)
	}
	var snapshotFrame map[string]any
	if err := json.Unmarshal([]byte(data), &snapshotFrame); err != nil {
		t.Fatalf("decode snapshot frame: %v", err)
	}
	if snapshotFrame["type"] != "session_snapshot" {
		t.Fatalf("first SSE frame type = %#v, want session_snapshot", snapshotFrame["type"])
	}
	snapshotData, _ := snapshotFrame["data"].(map[string]any)
	if snapshotData["is_executing"] != false {
		t.Fatalf("pre-turn snapshot = %#v, want is_executing false", snapshotData)
	}

	_, data = readEventFrame(t, reader)
	var delta map[string]any
	if err := json.Unmarshal([]byte(data), &delta); err != nil {
		t.Fatalf("decode delta frame: %v", err)
	}
	if delta["type"] != "client_output_delta" {
		t.Fatalf("second SSE frame type = %#v, want client_output_delta", delta["type"])
	}
	payload, _ := delta["data"].(map[string]any)
	if payload["content"] != "done" {
		t.Fatalf("delta payload = %#v, want the assistant text", payload)
	}

	_, data = readEventFrame(t, reader)
	var terminal map[string]any
	if err := json.Unmarshal([]byte(data), &terminal); err != nil {
		t.Fatalf("decode terminal frame: %v", err)
	}
	if terminal["type"] != "execution_completed" {
		t.Fatalf("third SSE frame type = %#v, want execution_completed", terminal["type"])
	}

	// /events/history reads the same events.jsonl the bus wrote during the
	// turn, so a dashboard reload replays the whole turn without a live bus.
	history := getEventsHistory(t, server.URL, sessionID)
	types := map[string]bool{}
	for _, event := range history {
		if eventType, ok := event["type"].(string); ok {
			types[eventType] = true
		}
	}
	for _, want := range []string{"execution_started", "node_loop_started", "client_output_delta", "llm_turn_complete", "execution_completed"} {
		if !types[want] {
			t.Fatalf("events/history missing %q; got %#v", want, types)
		}
	}
}

func getEventsHistory(t *testing.T, baseURL, sessionID string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []map[string]any
	for time.Now().Before(deadline) {
		response, err := http.Get(baseURL + "/api/sessions/" + sessionID + "/events/history?limit=100")
		if err != nil {
			t.Fatalf("GET events history: %v", err)
		}
		var decoded struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
			response.Body.Close()
			t.Fatalf("decode events history: %v", err)
		}
		response.Body.Close()
		last = decoded.Events
		if len(last) > 0 {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("events/history stayed empty; last = %#v", last)
	return nil
}
