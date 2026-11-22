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
