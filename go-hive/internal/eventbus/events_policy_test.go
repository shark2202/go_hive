package eventbus

// Parity: tests/test_events_policy.py

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readJSONLLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			out = append(out, line)
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Parity: tests/test_events_policy.py::test_meta_types_are_all_real_event_types
func TestMetaTypesAreAllRealEventTypes(t *testing.T) {
	for name := range WorkerMetaTypes {
		evtType, ok := LookupEventType(name)
		if !ok {
			t.Errorf("WORKER_META_TYPES contains non-EventType value: %q", name)
			continue
		}
		if !evtType.Valid() {
			t.Errorf("WORKER_META_TYPES value %q is not a valid EventType", name)
		}
	}
}

// Parity: tests/test_events_policy.py::test_payment_required_is_never_worker_local
func TestPaymentRequiredIsNeverWorkerLocal(t *testing.T) {
	if IsWorkerLocal("worker:abc", EventPaymentRequired.String()) {
		t.Fatal("payment_required must never be worker-local")
	}
}

// Parity: tests/test_events_policy.py::test_bubble_can_be_rendered_from_meta_alone
func TestBubbleCanBeRenderedFromMetaAlone(t *testing.T) {
	for _, evtType := range []EventType{
		EventExecutionStarted,
		EventTaskUpdated,
		EventSubagentReport,
		EventExecutionCompleted,
		EventExecutionFailed,
	} {
		if IsWorkerLocal("worker:abc", evtType.String()) {
			t.Errorf("%s must not be worker-local", evtType)
		}
	}
}

// Parity: tests/test_events_policy.py::test_chatter_of_a_fanout_worker_is_worker_local
func TestChatterOfFanoutWorkerIsWorkerLocal(t *testing.T) {
	for _, evtType := range []EventType{
		EventLLMTextDelta,
		EventToolCallStarted,
		EventToolCallCompleted,
		EventNodeLoopIteration,
	} {
		if !IsWorkerLocal("worker:abc", evtType.String()) {
			t.Errorf("%s must be worker-local", evtType)
		}
	}
}

// Parity: tests/test_events_policy.py::test_single_spawn_worker_is_never_gated
func TestSingleSpawnWorkerIsNeverGated(t *testing.T) {
	for _, evtType := range []EventType{
		EventLLMTextDelta,
		EventToolCallStarted,
		EventNodeLoopIteration,
	} {
		if IsWorkerLocal("worker", evtType.String()) {
			t.Errorf("bare worker stream must not gate %s", evtType)
		}
	}
}

// Parity: tests/test_events_policy.py::test_queen_events_are_never_worker_local
func TestQueenEventsAreNeverWorkerLocal(t *testing.T) {
	if IsWorkerStream("queen") {
		t.Error("queen must not be a worker stream")
	}
	if IsWorkerLocal("queen", EventLLMTextDelta.String()) {
		t.Error("queen llm_text_delta must not be worker-local")
	}
	if IsWorkerLocal("", EventLLMTextDelta.String()) {
		t.Error("empty stream llm_text_delta must not be worker-local")
	}
}

// Parity: tests/test_events_policy.py::test_router_splits_chatter_from_queen_but_keeps_meta
func TestRouterSplitsChatterFromQueenButKeepsMeta(t *testing.T) {
	dir := t.TempDir()
	queenPath := filepath.Join(dir, "queen", "events.jsonl")
	workerPath := filepath.Join(dir, "workers", "w1", "events.jsonl")

	bus := NewEventBus()
	if err := bus.SetSessionLog(queenPath, 0); err != nil {
		t.Fatalf("set session log: %v", err)
	}
	bus.SetWorkerLogResolver(func(streamID string) (string, bool) {
		if streamID == "worker:w1" {
			return workerPath, true
		}
		return "", false
	})

	pub := func(eventType EventType, streamID string, data map[string]any) {
		evt := NewAgentEvent(eventType, streamID)
		evt.NodeID = StrPtr("n")
		evt.ExecutionID = StrPtr("e")
		evt.Data = data
		bus.Publish(evt)
	}

	pub(EventLLMTextDelta, "queen", map[string]any{"iteration": 1, "text": "hi"})
	pub(EventLLMTurnComplete, "queen", map[string]any{"iteration": 1})
	pub(EventExecutionStarted, "worker:w1", map[string]any{"iteration": 1})
	pub(EventLLMTextDelta, "worker:w1", map[string]any{"iteration": 1, "text": "working"})
	pub(EventToolCallStarted, "worker:w1", map[string]any{"iteration": 1})
	pub(EventLLMTurnComplete, "worker:w1", map[string]any{"iteration": 1})
	pub(EventSubagentReport, "worker:w1", map[string]any{"iteration": 1, "status": "ok"})
	bus.CloseSessionLog()

	queenEvents := readJSONLLines(t, queenPath)
	workerEvents := readJSONLLines(t, workerPath)

	queenTypes := map[string]bool{}
	for _, e := range queenEvents {
		if typ, _ := e["type"].(string); typ != "" {
			queenTypes[typ] = true
		}
	}
	workerTypes := map[string]bool{}
	for _, e := range workerEvents {
		if typ, _ := e["type"].(string); typ != "" {
			workerTypes[typ] = true
		}
	}

	if !queenTypes[EventExecutionStarted.String()] {
		t.Fatalf("queen log missing execution_started: %v", queenTypes)
	}
	if !queenTypes[EventSubagentReport.String()] {
		t.Fatalf("queen log missing subagent_report: %v", queenTypes)
	}
	for _, e := range queenEvents {
		typ, _ := e["type"].(string)
		streamID, _ := e["stream_id"].(string)
		if (typ == EventLLMTextDelta.String() || typ == EventToolCallStarted.String()) &&
			len(streamID) >= 6 && streamID[:6] == "worker" {
			t.Fatalf("worker chatter leaked into the queen's log: %v", queenTypes)
		}
	}
	queenOwnProse := false
	for _, e := range queenEvents {
		typ, _ := e["type"].(string)
		streamID, _ := e["stream_id"].(string)
		if typ == EventLLMTextDelta.String() && streamID == "queen" {
			queenOwnProse = true
		}
	}
	if !queenOwnProse {
		t.Fatal("queen's own llm_text_delta missing from its log")
	}

	for _, want := range []EventType{
		EventLLMTextDelta,
		EventToolCallStarted,
		EventExecutionStarted,
		EventSubagentReport,
	} {
		if !workerTypes[want.String()] {
			t.Errorf("worker log missing %s: %v", want, workerTypes)
		}
	}
}

// Parity: tests/test_events_policy.py::test_worker_log_handle_closed_on_report
func TestWorkerLogHandleClosedOnReport(t *testing.T) {
	dir := t.TempDir()
	bus := NewEventBus()
	if err := bus.SetSessionLog(filepath.Join(dir, "queen", "events.jsonl"), 0); err != nil {
		t.Fatalf("set session log: %v", err)
	}
	bus.SetWorkerLogResolver(func(streamID string) (string, bool) {
		return filepath.Join(dir, replaceColon(streamID), "e.jsonl"), true
	})

	pub := func(eventType EventType, streamID string) {
		evt := NewAgentEvent(eventType, streamID)
		evt.NodeID = StrPtr("n")
		evt.ExecutionID = StrPtr("e")
		bus.Publish(evt)
	}

	pub(EventToolCallStarted, "worker:w1")
	bus.logMu.Lock()
	_, present := bus.workerLogs["worker:w1"]
	bus.logMu.Unlock()
	if !present {
		t.Fatal("worker log handle not opened for worker:w1")
	}

	pub(EventSubagentReport, "worker:w1")
	bus.logMu.Lock()
	_, present = bus.workerLogs["worker:w1"]
	bus.logMu.Unlock()
	if present {
		t.Fatal("worker log handle leaked past subagent_report")
	}
	bus.CloseSessionLog()
}

func replaceColon(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, '_')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// Parity: tests/test_events_policy.py::test_routing_disabled_by_default_is_legacy_behaviour
func TestRoutingDisabledByDefaultIsLegacyBehaviour(t *testing.T) {
	dir := t.TempDir()
	queenPath := filepath.Join(dir, "events.jsonl")
	bus := NewEventBus()
	if err := bus.SetSessionLog(queenPath, 0); err != nil {
		t.Fatalf("set session log: %v", err)
	}

	evt := NewAgentEvent(EventToolCallStarted, "worker:w1")
	evt.NodeID = StrPtr("n")
	evt.ExecutionID = StrPtr("e")
	bus.Publish(evt)
	bus.CloseSessionLog()

	got := readJSONLLines(t, queenPath)
	if len(got) != 1 {
		t.Fatalf("expected 1 event in queen log, got %d", len(got))
	}
	if typ, _ := got[0]["type"].(string); typ != EventToolCallStarted.String() {
		t.Fatalf("expected tool_call_started, got %v", got[0]["type"])
	}
}

func policyEvt(streamID, eventType string) map[string]any {
	return map[string]any{"stream_id": streamID, "type": eventType}
}

// Parity: tests/test_events_policy.py::test_watch_opt_in_gates_only_the_chatter_of_unwatched_workers
func TestWatchOptInGatesOnlyChatterOfUnwatchedWorkers(t *testing.T) {
	watchAll, watched := ParseWatch("worker:w1")
	if watchAll {
		t.Fatal("watch_all should be false")
	}
	if _, ok := watched["worker:w1"]; !ok || len(watched) != 1 {
		t.Fatalf("watched = %v", watched)
	}

	sup := func(streamID, eventType string) bool {
		return IsSuppressedForClient(policyEvt(streamID, eventType), watchAll, watched)
	}

	if sup("worker:w1", "llm_text_delta") {
		t.Error("watched worker's llm_text_delta should flow")
	}
	if sup("worker:w1", "tool_call_started") {
		t.Error("watched worker's tool_call_started should flow")
	}
	if !sup("worker:w2", "llm_text_delta") {
		t.Error("unwatched worker's llm_text_delta should be suppressed")
	}
	if !sup("worker:w2", "tool_call_started") {
		t.Error("unwatched worker's tool_call_started should be suppressed")
	}
	for _, typ := range []string{"execution_started", "subagent_report", "payment_required"} {
		if sup("worker:w2", typ) {
			t.Errorf("worker:w2 %s must not be suppressed", typ)
		}
	}
	if sup("queen", "llm_text_delta") {
		t.Error("queen llm_text_delta must not be suppressed")
	}
}

// Parity: tests/test_events_policy.py::test_watch_defaults_to_no_chatter_at_all
func TestWatchDefaultsToNoChatterAtAll(t *testing.T) {
	watchAll, watched := ParseWatch("")
	if IsSuppressedForClient(policyEvt("worker:w1", "llm_text_delta"), watchAll, watched) != true {
		t.Error("default feed should suppress worker chatter")
	}
	if IsSuppressedForClient(policyEvt("worker:w1", "subagent_report"), watchAll, watched) {
		t.Error("default feed should still carry subagent_report")
	}
}

// Parity: tests/test_events_policy.py::test_watch_star_is_the_debug_escape_hatch
func TestWatchStarIsDebugEscapeHatch(t *testing.T) {
	watchAll, watched := ParseWatch("*")
	if !watchAll {
		t.Fatal("watch_all should be true")
	}
	if IsSuppressedForClient(policyEvt("worker:w9", "llm_text_delta"), watchAll, watched) {
		t.Error("watch=* must not suppress anything")
	}
}
