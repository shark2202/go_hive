package server

// Cold-session opening events: a newly created queen session must carry the
// same five-event opening upstream's queen writes before any user turn, so
// /events/history, /snapshot and /stats all answer with data instead of zero.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

const startEventsSessionID = "session_20260101_000000_c0ffee01"

func readSessionEventFile(t *testing.T, path string) []map[string]any {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var events []map[string]any
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event := map[string]any{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse event line %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return events
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		value, _ := event["type"].(string)
		out = append(out, value)
	}
	return out
}

func eventString(t *testing.T, event map[string]any, key string) string {
	t.Helper()
	value, _ := event[key].(string)
	return value
}

func eventData(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	data, ok := event["data"].(map[string]any)
	if !ok {
		t.Fatalf("event %v has no data object", event["type"])
	}
	return data
}

func newStartEventsSession(t *testing.T) (*Session, *SessionManager, string) {
	t.Helper()
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	chat, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      root,
		Bus:       bus,
		SessionID: startEventsSessionID,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chat.Close() })

	profile := map[string]any{"name": "Steve Jobs", "title": "Head of Growth"}
	phase := NewQueenPhaseState(QueenPhaseIndependent, "queen_growth", profile, "", bus)
	// The loader stamps this before building the runtime; the test mirrors the
	// same order so the session carries a bound identity when it registers.
	phase.SetQueenIdentity("queen_growth", profile)

	session := NewSession(startEventsSessionID, bus)
	session.QueenDir = root
	session.QueenName = "queen_growth"
	session.PhaseState = phase
	session.Chat = chat
	return session, NewSessionManager(), root
}

// Parity: framework/server/queen_orchestrator.py::materialize_queen_identity
// Parity: framework/agent_loop/agent_loop.py::_emit_reminder_injected
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
func TestSessionAddWritesOpeningEvents(t *testing.T) {
	session, manager, root := newStartEventsSession(t)
	manager.AddSession(session)

	eventsPath := filepath.Join(root, "events.jsonl")
	events := readSessionEventFile(t, eventsPath)
	wantTypes := []string{
		"queen_identity_selected",
		"reminder_injected",
		"node_loop_started",
		"node_loop_iteration",
		"context_usage_updated",
	}
	if got := eventTypes(events); strings.Join(got, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("opening events = %v, want %v", got, wantTypes)
	}
	for index, event := range events {
		if seq, _ := event["seq"].(float64); int(seq) != index+1 {
			t.Fatalf("event %d seq = %#v, want %d", index, event["seq"], index+1)
		}
		if stream := eventString(t, event, "stream_id"); stream != chatStreamID {
			t.Fatalf("event %d stream_id = %q, want %q", index, stream, chatStreamID)
		}
	}

	identity := events[0]
	if node := identity["node_id"]; node != nil {
		t.Fatalf("queen_identity_selected node_id = %#v, want null", node)
	}
	if execution := identity["execution_id"]; execution != nil {
		t.Fatalf("queen_identity_selected execution_id = %#v, want null", execution)
	}
	identityData := eventData(t, identity)
	if identityData["queen_id"] != "queen_growth" || identityData["name"] != "Steve Jobs" || identityData["title"] != "Head of Growth" {
		t.Fatalf("queen_identity_selected data = %#v", identityData)
	}

	reminder := events[1]
	if eventString(t, reminder, "node_id") != chatNodeID || eventString(t, reminder, "execution_id") != startEventsSessionID {
		t.Fatalf("reminder_injected node/execution = %#v/%#v", reminder["node_id"], reminder["execution_id"])
	}
	reminderData := eventData(t, reminder)
	if reminderData["source"] != "point:session_start" || reminderData["detail"] != "session_start" {
		t.Fatalf("reminder_injected data = %#v", reminderData)
	}
	meta, _ := reminderData["meta"].(map[string]any)
	if meta["point"] != "session_start" {
		t.Fatalf("reminder_injected meta = %#v", reminderData["meta"])
	}

	loopStarted := events[2]
	if eventString(t, loopStarted, "execution_id") != startEventsSessionID {
		t.Fatalf("node_loop_started execution_id = %#v, want %q", loopStarted["execution_id"], startEventsSessionID)
	}
	if max, _ := eventData(t, loopStarted)["max_iterations"].(float64); int(max) <= 0 {
		t.Fatalf("node_loop_started max_iterations = %#v", eventData(t, loopStarted)["max_iterations"])
	}

	iterationData := eventData(t, events[3])
	if iterationData["iteration"] != float64(0) || iterationData["phase"] != QueenPhaseIndependent {
		t.Fatalf("node_loop_iteration data = %#v", iterationData)
	}

	usage := events[4]
	if execution := usage["execution_id"]; execution != nil {
		t.Fatalf("context_usage_updated execution_id = %#v, want null", execution)
	}
	usageData := eventData(t, usage)
	for _, key := range []string{"usage_ratio", "usage_pct", "message_count", "estimated_tokens", "max_context_tokens", "trigger", "breakdown"} {
		if _, present := usageData[key]; !present {
			t.Fatalf("context_usage_updated lacks %q: %#v", key, usageData)
		}
	}
	if usageData["trigger"] != "iteration_start" || usageData["message_count"] != float64(0) {
		t.Fatalf("context_usage_updated data = %#v", usageData)
	}
	if max, _ := usageData["max_context_tokens"].(float64); int(max) != 180000 {
		t.Fatalf("context_usage_updated max_context_tokens = %#v, want 180000", usageData["max_context_tokens"])
	}
	breakdown, _ := usageData["breakdown"].(map[string]any)
	for _, key := range []string{"conversation_chars", "system_chars", "tool_defs_chars", "image_blocks", "image_tokens"} {
		if _, present := breakdown[key]; !present {
			t.Fatalf("context_usage_updated breakdown lacks %q: %#v", key, breakdown)
		}
	}

	// The same events are what history/snapshot/stats read.
	snapshot := eventbus.ComputeSessionSnapshot(session.EventBus)
	if snapshot["snapshot_seq"] != 5 {
		t.Fatalf("snapshot_seq = %#v, want 5", snapshot["snapshot_seq"])
	}
	if snapshot["last_event_at"] == nil || snapshot["last_queen_event_at"] == nil {
		t.Fatalf("snapshot timestamps = %#v, want the opening block's time", snapshot)
	}
	stats := session.EventBus.GetStats()
	if total, _ := stats["total_events"].(int); total != 5 {
		t.Fatalf("total_events = %#v, want 5", stats["total_events"])
	}
	byType, _ := stats["events_by_type"].(map[string]int)
	for _, want := range wantTypes {
		if byType[want] != 1 {
			t.Fatalf("events_by_type[%s] = %d, want 1 (%v)", want, byType[want], byType)
		}
	}
}

// Parity: framework/server/session_manager.py::_start_queen
func TestSessionAddOpeningEventsRunOncePerSegment(t *testing.T) {
	session, manager, root := newStartEventsSession(t)
	manager.AddSession(session)
	manager.AddSession(session)
	eventsPath := filepath.Join(root, "events.jsonl")
	if got := len(readSessionEventFile(t, eventsPath)); got != 5 {
		t.Fatalf("events after repeated registration = %d, want 5", got)
	}

	// A second record over the same session tree (a resume rebuild) must not
	// append a second opening block to a segment that already carries events.
	reopened := NewSession(startEventsSessionID, eventbus.NewEventBus())
	reopened.QueenDir = root
	reopened.QueenName = "queen_growth"
	NewSessionManager().AddSession(reopened)
	if got := len(readSessionEventFile(t, eventsPath)); got != 5 {
		t.Fatalf("events after reopening the segment = %d, want 5", got)
	}
}

// Parity: framework/server/session_manager.py::_start_queen
func TestSessionAddOpeningEventsWithoutChatRuntime(t *testing.T) {
	root := t.TempDir()
	bus := eventbus.NewEventBus()
	session := NewSession(startEventsSessionID, bus)
	session.QueenDir = root
	session.QueenName = "queen_growth"
	session.PhaseState = NewQueenPhaseState(QueenPhaseIndependent, "queen_growth",
		map[string]any{"name": "Steve Jobs", "title": "Head of Growth"}, "", bus)

	NewSessionManager().AddSession(session)

	events := readSessionEventFile(t, filepath.Join(root, "events.jsonl"))
	if got := eventTypes(events); len(got) != 5 || got[4] != "context_usage_updated" {
		t.Fatalf("opening events without a runtime = %v, want the full five-event block", got)
	}
	usageData := eventData(t, events[4])
	if usageData["estimated_tokens"] != float64(0) || usageData["message_count"] != float64(0) {
		t.Fatalf("context_usage_updated without a runtime = %#v, want zeroed components", usageData)
	}
	if max, _ := usageData["max_context_tokens"].(float64); int(max) != 180000 {
		t.Fatalf("max_context_tokens = %#v, want the default window", usageData["max_context_tokens"])
	}
}

// Parity: framework/agent_loop/agent_loop.py::execute
func TestSessionOpeningEventsNotDuplicatedByFirstTurn(t *testing.T) {
	session, manager, root := newStartEventsSession(t)
	manager.AddSession(session)
	eventsPath := filepath.Join(root, "events.jsonl")

	if err := session.Chat.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var events []map[string]any
	for time.Now().Before(deadline) {
		events = readSessionEventFile(t, eventsPath)
		seen := false
		for _, event := range events {
			if event["type"] == "execution_completed" {
				seen = true
				break
			}
		}
		if seen {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	counts := map[string]int{}
	for _, event := range events {
		value, _ := event["type"].(string)
		counts[value]++
	}
	if counts["execution_completed"] != 1 {
		t.Fatalf("the first turn never completed; events = %v", eventTypes(events))
	}
	for _, opening := range []string{"queen_identity_selected", "reminder_injected", "context_usage_updated"} {
		if counts[opening] != 1 {
			t.Fatalf("%s count = %d, want exactly the opening one (%v)", opening, counts[opening], eventTypes(events))
		}
	}
	if got := eventTypes(events)[:5]; strings.Join(got, ",") != strings.Join([]string{
		"queen_identity_selected",
		"reminder_injected",
		"node_loop_started",
		"node_loop_iteration",
		"context_usage_updated",
	}, ",") {
		t.Fatalf("opening block changed after the first turn: %v", got)
	}
}
