package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// TestChatRuntimeWritesThreeLevelRuntimeLogs pins the production wiring: a
// session-shaped root (<storage>/sessions/<session_id>) gets the three-level
// runtime logs written under logs/ while the session runs, and Close writes
// the L1 summary.
//
// Parity-exempt: no upstream unit test pins the server-side RuntimeLogStore
// wiring (the Python coverage lives in core/tests/test_runtime_logger.py for
// the store/logger itself); this test pins the Go host wiring.
func TestChatRuntimeWritesThreeLevelRuntimeLogs(t *testing.T) {
	storage := t.TempDir()
	sessionID := "session_20260101_000000_logs001"
	sessionDir := filepath.Join(storage, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.NewEventBus()
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		{Content: "done"},
	}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     []llm.ToolDef{{Name: "echo", Parameters: map[string]any{"type": "object"}}},
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      sessionDir,
		Bus:       bus,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	recorder := recordChatEvents(bus)
	if err := runtime.Deliver("say hi with the echo tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	logsDir := filepath.Join(sessionDir, "logs")
	stepsRaw, err := os.ReadFile(filepath.Join(logsDir, "tool_logs.jsonl"))
	if err != nil {
		t.Fatalf("tool_logs.jsonl missing: %v", err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(stepsRaw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("tool_logs lines = %d, want 1", len(lines))
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &step); err != nil {
		t.Fatalf("step unmarshal: %v", err)
	}
	if step["node_id"] != "queen" || step["verdict"] != "RETRY" {
		t.Fatalf("step = %v", step)
	}
	calls, _ := step["tool_calls"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["tool_name"] != "echo" {
		t.Fatalf("tool_calls = %v", step["tool_calls"])
	}

	// Close writes the L1 summary (status follows the last turn: success).
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	summaryRaw, err := os.ReadFile(filepath.Join(logsDir, "summary.json"))
	if err != nil {
		t.Fatalf("summary.json missing after Close: %v", err)
	}
	var summary map[string]any
	if err := json.Unmarshal(summaryRaw, &summary); err != nil {
		t.Fatalf("summary unmarshal: %v", err)
	}
	if summary["run_id"] != sessionID || summary["status"] != "success" {
		t.Fatalf("summary = %v", summary)
	}
	if summary["total_nodes_executed"].(float64) != 0 {
		t.Fatalf("summary = %v (no node completed before the clean turn end)", summary)
	}
}

// TestChatRuntimeSkipsRuntimeLogsForAdHocRoots guards the wiring boundary:
// a root that is not <storage>/sessions/<id> (unit tests, ad-hoc dirs) must
// stay unwired — no runtime_logs tree is created next to it.
//
// Parity-exempt: boundary of the Go host wiring (upstream always has a
// storage root; this port refuses roots that are not session-shaped).
func TestChatRuntimeSkipsRuntimeLogsForAdHocRoots(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &queenChatLLM{},
		Root:      root,
		SessionID: "session_20260101_000000_adhoc01",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	if runtime.runtimeLogger != nil {
		t.Fatalf("runtime logger wired for ad-hoc root %s", root)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "runtime_logs")); !os.IsNotExist(err) {
		t.Fatalf("runtime_logs tree created next to ad-hoc root (err=%v)", err)
	}
}

// testTempDir isolates HIVE_HOME for a runtime-log test and returns it as the
// storage root. Env-reading paths outside ChatRuntimeDeps.HomeRoot (the LLM
// debug log, the compaction debug sink) must never touch the developer's
// ~/.hive. The server package has no shared helper; this mirrors internal/cli's
// testTempDir.
func testTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HIVE_HOME", dir)
	return dir
}

// newTraceLogChatRuntime builds the production session shape (a session
// directory under <storage>/sessions/<id>, an event bus and a scripted tool
// turn) and returns the runtime plus an event recorder.
func newTraceLogChatRuntime(t *testing.T, storage, sessionID string) (*ChatRuntime, *chatEventRecorder) {
	t.Helper()
	sessionDir := filepath.Join(storage, "sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	bus := eventbus.NewEventBus()
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}}},
		{Content: "done"},
	}}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Tools:     []llm.ToolDef{{Name: "echo", Parameters: map[string]any{"type": "object"}}},
		Execute:   func(context.Context, string, map[string]any) (string, bool, error) { return "ok", false, nil },
		Root:      sessionDir,
		Bus:       bus,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recordChatEvents(bus)
}

// runtimeLogObject reads a JSON object from summary.json or the single record
// of a one-line JSONL file.
func runtimeLogObject(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.HasSuffix(path, ".jsonl") {
		lines := []string{}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) != 1 {
			t.Fatalf("%s lines = %d, want 1", path, len(lines))
		}
		raw = []byte(lines[0])
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return object
}

// requireTraceHex32 pins the OTel/W3C shape runtimelogs.NewTraceContext
// produces.
func requireTraceHex32(t *testing.T, field, value string) {
	t.Helper()
	if len(value) != 32 {
		t.Fatalf("%s = %q, want 32 hex chars", field, value)
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("%s = %q, want lowercase hex", field, value)
		}
	}
}

// TestChatRuntimeRuntimeLogsCarryTraceContext pins the production trace
// wiring: the session runtime binds a trace/execution pair to its logger, so
// the L3 step and the L1 summary the Runtime logs drawer reads carry non-empty
// ids.
//
// Parity-exempt: upstream reads the ids from the observability ContextVar,
// which execution_manager.py sets at start_run; this host binds the same pair
// to the session logger. The record shapes are pinned by
// core/tests/test_runtime_logger.py::test_trace_context_populated_in_l1_l2_l3.
func TestChatRuntimeRuntimeLogsCarryTraceContext(t *testing.T) {
	storage := testTempDir(t)
	sessionID := "session_20260101_000000_trace001"
	runtime, recorder := newTraceLogChatRuntime(t, storage, sessionID)
	if err := runtime.Deliver("say hi with the echo tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	step := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionID, "logs", "tool_logs.jsonl"))
	traceID, _ := step["trace_id"].(string)
	executionID, _ := step["execution_id"].(string)
	requireTraceHex32(t, "step trace_id", traceID)
	requireTraceHex32(t, "step execution_id", executionID)
	if traceID == executionID {
		t.Fatalf("trace_id and execution_id are the same value: %q", traceID)
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	summary := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionID, "logs", "summary.json"))
	if summary["trace_id"] != traceID || summary["execution_id"] != executionID {
		t.Fatalf("summary trace = %v/%v, want %s/%s",
			summary["trace_id"], summary["execution_id"], traceID, executionID)
	}
}

// TestChatRuntimeTraceContextDoesNotLeakAcrossSessions pins the isolation
// property of the production wiring: two sessions created and run in the same
// process stamp their own trace ids, never the other session's.
//
// Parity-exempt: upstream gets the isolation from the per-execution
// ContextVar; the Go host gets it from the per-session RuntimeLogger binding.
func TestChatRuntimeTraceContextDoesNotLeakAcrossSessions(t *testing.T) {
	storage := testTempDir(t)
	sessionA := "session_20260101_000000_trace0a"
	sessionB := "session_20260101_000000_trace0b"
	runtimeA, recorderA := newTraceLogChatRuntime(t, storage, sessionA)
	runtimeB, recorderB := newTraceLogChatRuntime(t, storage, sessionB)
	// Deliver before waiting so both session workers are in flight together.
	if err := runtimeA.Deliver("say hi with the echo tool"); err != nil {
		t.Fatalf("Deliver A: %v", err)
	}
	if err := runtimeB.Deliver("say hi with the echo tool"); err != nil {
		t.Fatalf("Deliver B: %v", err)
	}
	waitForChatEvents(t, recorderA, eventbus.EventExecutionCompleted, 1)
	waitForChatEvents(t, recorderB, eventbus.EventExecutionCompleted, 1)

	stepA := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionA, "logs", "tool_logs.jsonl"))
	stepB := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionB, "logs", "tool_logs.jsonl"))
	traceA, _ := stepA["trace_id"].(string)
	execA, _ := stepA["execution_id"].(string)
	traceB, _ := stepB["trace_id"].(string)
	execB, _ := stepB["execution_id"].(string)
	requireTraceHex32(t, "session A trace_id", traceA)
	requireTraceHex32(t, "session A execution_id", execA)
	requireTraceHex32(t, "session B trace_id", traceB)
	requireTraceHex32(t, "session B execution_id", execB)
	if traceA == traceB || execA == execB {
		t.Fatalf("sessions share a trace pair: A=%s/%s B=%s/%s", traceA, execA, traceB, execB)
	}
	if stepA["trace_id"] == traceB || stepA["execution_id"] == execB {
		t.Fatalf("session A record carries session B's trace: %v/%v", stepA["trace_id"], stepA["execution_id"])
	}
	if stepB["trace_id"] == traceA || stepB["execution_id"] == execA {
		t.Fatalf("session B record carries session A's trace: %v/%v", stepB["trace_id"], stepB["execution_id"])
	}

	if err := runtimeA.Close(); err != nil {
		t.Fatalf("Close A: %v", err)
	}
	if err := runtimeB.Close(); err != nil {
		t.Fatalf("Close B: %v", err)
	}
	summaryA := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionA, "logs", "summary.json"))
	summaryB := runtimeLogObject(t, filepath.Join(storage, "sessions", sessionB, "logs", "summary.json"))
	if summaryA["trace_id"] != traceA || summaryA["execution_id"] != execA {
		t.Fatalf("session A summary trace = %v/%v, want %s/%s",
			summaryA["trace_id"], summaryA["execution_id"], traceA, execA)
	}
	if summaryB["trace_id"] != traceB || summaryB["execution_id"] != execB {
		t.Fatalf("session B summary trace = %v/%v, want %s/%s",
			summaryB["trace_id"], summaryB["execution_id"], traceB, execB)
	}
}
