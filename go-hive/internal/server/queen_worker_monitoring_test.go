package server

// Tests for get_worker_health_summary: the Python-verbatim definition, the
// colony-phase gate, and the read/auto-discovery behaviour over the on-disk
// worker run records.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::register_worker_monitoring_tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

// pythonWorkerHealthDescription is the Python Tool(description=...) text of
// framework/tools/worker_monitoring_tools.py, byte for byte.
const pythonWorkerHealthDescription = "Read the worker agent's execution logs and return a compact health snapshot. " +
	"Returns worker_agent_id and worker_graph_id (use these for ticket identity fields), " +
	"recent verdicts, step count, time since last step, and " +
	"a snippet of the most recent LLM output. " +
	"session_id is optional — omit it to auto-discover the most recent active session."

// pythonWorkerHealthSchema mirrors the Python Tool(parameters=...) declaration.
func pythonWorkerHealthSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "The worker's active session ID. Omit or pass 'auto' to auto-discover the most recent session.",
			},
			"last_n_steps": map[string]any{
				"type":        "integer",
				"description": "How many recent log steps to include (default 40)",
			},
		},
		"required": []any{},
	}
}

// healthTestBinding builds a binding whose session carries a worker storage
// root (the colony directory upstream binds as session.worker_path).
func healthTestBinding(t *testing.T, root string) *QueenLifecycleBinding {
	t.Helper()
	session := NewSession("session_lifecycle", eventbus.NewEventBus())
	session.WorkerPath = root
	return NewQueenLifecycleBinding(session, hivehome.Home{Root: t.TempDir()})
}

// writeHealthWorker creates one worker run directory.
func writeHealthWorker(t *testing.T, root, workerID string, state map[string]any) string {
	t.Helper()
	dir := filepath.Join(root, "workers", workerID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worker dir: %v", err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), encoded, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}
	return dir
}

// writeHealthPart persists one conversation part (the Go worker transcript).
func writeHealthPart(t *testing.T, workerDir string, seq int, role, content string) string {
	t.Helper()
	partsDir := filepath.Join(workerDir, "conversations", "parts")
	if err := os.MkdirAll(partsDir, 0o755); err != nil {
		t.Fatalf("mkdir parts: %v", err)
	}
	record := map[string]any{"seq": seq, "role": role, "content": content}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal part: %v", err)
	}
	path := filepath.Join(partsDir, padSix(seq)+".json")
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
	return path
}

// decodeHealthPayload parses a tool result into a generic map.
func decodeHealthPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON (%v): %s", err, content)
	}
	return payload
}

// TestQueenWorkerHealthSummaryDefinitionMatchesPythonSource pins the
// definition to the Python Tool(...) declaration.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::register_worker_monitoring_tools
func TestQueenWorkerHealthSummaryDefinitionMatchesPythonSource(t *testing.T) {
	def := QueenWorkerHealthSummaryToolDef()
	if def.Name != "get_worker_health_summary" {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Description != pythonWorkerHealthDescription {
		t.Fatalf("description is not the Python text (got %d bytes, want %d)",
			len(def.Description), len(pythonWorkerHealthDescription))
	}
	if !reflect.DeepEqual(def.Parameters, pythonWorkerHealthSchema()) {
		t.Fatalf("parameters are not the Python declaration:\ngot:  %#v\nwant: %#v", def.Parameters, pythonWorkerHealthSchema())
	}
	if !colonyBoundQueenTool(QueenWorkerHealthSummaryToolName) {
		t.Fatal("get_worker_health_summary must be colony-phase gated")
	}
}

// TestQueenWorkerHealthSummaryReadsWorkerRunRecord covers the Go colony
// layout: workers/<id>/{state.json,conversations/parts}.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::get_worker_health_summary
func TestQueenWorkerHealthSummaryReadsWorkerRunRecord(t *testing.T) {
	root := t.TempDir()
	dir := writeHealthWorker(t, root, "session_w1", map[string]any{
		"worker_id": "session_w1", "colony_id": "acme", "status": "running",
	})
	writeHealthPart(t, dir, 1, "user", "do the thing")
	writeHealthPart(t, dir, 2, "assistant", "starting the crawl")
	writeHealthPart(t, dir, 3, "tool", "some tool result")
	writeHealthPart(t, dir, 4, "assistant", "latest assistant output")

	binding := healthTestBinding(t, root)
	payload := decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{"session_id": "session_w1"}))

	wantAgent := filepath.Base(root)
	if payload["worker_agent_id"] != wantAgent || payload["worker_graph_id"] != wantAgent {
		t.Fatalf("worker identity = %v/%v, want the storage root name %q", payload["worker_agent_id"], payload["worker_graph_id"], wantAgent)
	}
	if payload["session_id"] != "session_w1" || payload["session_status"] != "running" {
		t.Fatalf("session = %v/%v", payload["session_id"], payload["session_status"])
	}
	if payload["total_steps"] != float64(2) {
		t.Fatalf("total_steps = %v, want 2 assistant turns", payload["total_steps"])
	}
	if payload["steps_since_last_accept"] != float64(0) {
		t.Fatalf("steps_since_last_accept = %v (no verdict log in this layout)", payload["steps_since_last_accept"])
	}
	if verdicts, ok := payload["recent_verdicts"].([]any); !ok || len(verdicts) != 0 {
		t.Fatalf("recent_verdicts = %#v, want an empty list", payload["recent_verdicts"])
	}
	if payload["evidence_snippet"] != "latest assistant output" {
		t.Fatalf("evidence_snippet = %q", payload["evidence_snippet"])
	}
	if payload["last_step_time_iso"] == nil {
		t.Fatal("last_step_time_iso must be set once a step exists")
	}
}

// TestQueenWorkerHealthSummaryReadsToolLogsLayout covers an upstream-shaped
// storage tree (sessions/<id>/logs/tool_logs.jsonl): verdicts, evidence and
// the stall clock come from the log file exactly like Python's.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::get_worker_health_summary
func TestQueenWorkerHealthSummaryReadsToolLogsLayout(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "session_up")
	logsDir := filepath.Join(sessionDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	state, _ := json.Marshal(map[string]any{"status": "completed"})
	if err := os.WriteFile(filepath.Join(sessionDir, "state.json"), state, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	steps := []map[string]any{
		{"step_index": 0, "verdict": "RETRY", "llm_text": "attempting"},
		{"step_index": 1, "verdict": "ACCEPT", "llm_text": "done"},
		{"step_index": 2, "verdict": "RETRY", "llm_text": strings.Repeat("x", 600)},
	}
	lines := []string{}
	for _, step := range steps {
		encoded, _ := json.Marshal(step)
		lines = append(lines, string(encoded))
	}
	logPath := filepath.Join(logsDir, "tool_logs.jsonl")
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write tool logs: %v", err)
	}
	// Age the log so the stall clock is exercised (Python: round(minutes, 1)).
	old := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	binding := healthTestBinding(t, root)
	payload := decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{"session_id": "session_up"}))

	if payload["session_status"] != "completed" {
		t.Fatalf("session_status = %v", payload["session_status"])
	}
	if payload["total_steps"] != float64(3) {
		t.Fatalf("total_steps = %v, want the tool-log line count", payload["total_steps"])
	}
	verdicts, _ := payload["recent_verdicts"].([]any)
	if len(verdicts) != 3 || verdicts[0] != "RETRY" || verdicts[1] != "ACCEPT" || verdicts[2] != "RETRY" {
		t.Fatalf("recent_verdicts = %#v", verdicts)
	}
	if payload["steps_since_last_accept"] != float64(1) {
		t.Fatalf("steps_since_last_accept = %v, want 1", payload["steps_since_last_accept"])
	}
	if payload["last_step_time_iso"] == nil || payload["stall_minutes"] == nil {
		t.Fatalf("timing fields = %v/%v", payload["last_step_time_iso"], payload["stall_minutes"])
	}
	evidence, _ := payload["evidence_snippet"].(string)
	if len([]rune(evidence)) != 500 || !strings.HasPrefix(evidence, "xxxx") {
		t.Fatalf("evidence_snippet must be the last llm_text truncated to 500 chars (got %d)", len([]rune(evidence)))
	}

	// last_n_steps limits the verdict window (last step only).
	limited := decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{"session_id": "session_up", "last_n_steps": 1}))
	if verdicts, _ := limited["recent_verdicts"].([]any); len(verdicts) != 1 || verdicts[0] != "RETRY" {
		t.Fatalf("last_n_steps=1 verdicts = %#v", limited["recent_verdicts"])
	}
}

// TestQueenWorkerHealthSummaryAutoDiscovery covers the omit/auto path: the
// queen's own session id wins when it exists, otherwise the most recent active
// session is picked (status priority before mtime).
//
// Parity: core/framework/tools/worker_monitoring_tools.py::get_worker_health_summary
func TestQueenWorkerHealthSummaryAutoDiscovery(t *testing.T) {
	root := t.TempDir()
	completed := writeHealthWorker(t, root, "session_done", map[string]any{"status": "completed"})
	writeHealthPart(t, completed, 1, "assistant", "older completed worker")
	stale := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filepath.Join(completed, "state.json"), stale, stale); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	running := writeHealthWorker(t, root, "session_run", map[string]any{"status": "running"})
	writeHealthPart(t, running, 1, "assistant", "active worker")

	binding := healthTestBinding(t, root)
	payload := decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{}))
	if payload["session_id"] != "session_run" {
		t.Fatalf("auto-discovery picked %v, want the running session", payload["session_id"])
	}

	// "auto" behaves like an omitted session id.
	payload = decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{"session_id": "auto"}))
	if payload["session_id"] != "session_run" {
		t.Fatalf("auto picked %v", payload["session_id"])
	}

	// The queen's own session id (upstream's default_session_id) wins when its
	// directory exists.
	defaultRoot := t.TempDir()
	defaultDir := writeHealthWorker(t, defaultRoot, "session_lifecycle", map[string]any{"status": "completed"})
	writeHealthPart(t, defaultDir, 1, "assistant", "the bound session")
	_ = writeHealthWorker(t, defaultRoot, "session_other", map[string]any{"status": "running"})
	defaultBinding := healthTestBinding(t, defaultRoot)
	payload = decodeHealthPayload(t, defaultBinding.getWorkerHealthSummary(map[string]any{}))
	if payload["session_id"] != "session_lifecycle" {
		t.Fatalf("default session preference lost: picked %v", payload["session_id"])
	}
}

// TestQueenWorkerHealthSummaryNoSessions covers the empty-storage envelope.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::get_worker_health_summary
func TestQueenWorkerHealthSummaryNoSessions(t *testing.T) {
	binding := healthTestBinding(t, t.TempDir())
	payload := decodeHealthPayload(t, binding.getWorkerHealthSummary(map[string]any{}))
	if payload["error"] != "No sessions found — worker has not started yet" {
		t.Fatalf("empty storage error = %v", payload["error"])
	}

	// No bound storage (no colony): the tool is hidden per turn, and a direct
	// call answers with the no-colony envelope.
	empty := NewQueenLifecycleBinding(NewSession("session_lifecycle", eventbus.NewEventBus()), hivehome.Home{Root: t.TempDir()})
	payload = decodeHealthPayload(t, empty.getWorkerHealthSummary(map[string]any{}))
	if payload["error"] != "No colony running." {
		t.Fatalf("unbound storage error = %v", payload["error"])
	}
}
