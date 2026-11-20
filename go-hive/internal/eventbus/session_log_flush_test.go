package eventbus

// Parity-exempt: Go-side flush boundary. Upstream's overseer is a queen and
// publishes llm_turn_complete, which flushes buffered delta snapshots. This
// port's overseer emitter deliberately omits per-call usage, so the execution
// boundary is the only reliable flush point for its reply.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: Go-side flush boundary. Upstream's overseer publishes
// llm_turn_complete; this port's emitter drops it, so execution_completed is
// the flush point that keeps the mirrored reply in events.jsonl replay.
func TestSessionLogFlushesSnapshotsOnExecutionCompleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	bus := NewEventBus()
	if err := bus.SetSessionLog(path, 0); err != nil {
		t.Fatalf("SetSessionLog: %v", err)
	}
	t.Cleanup(bus.CloseSessionLog)

	executionID := "overseer_turn_1"
	nodeID := "queen"
	delta := NewAgentEvent(EventClientOutputDelta, "queen")
	delta.NodeID = &nodeID
	delta.ExecutionID = &executionID
	delta.Data = map[string]any{"snapshot": "the north field is clear", "content": "the north field is clear", "iteration": 1}
	bus.Publish(delta)

	// Deltas are buffered, not written one frame at a time.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log before completion: %v", err)
	}
	if strings.Contains(string(data), "the north field is clear") {
		t.Fatal("streaming delta was written before its turn boundary")
	}

	completed := NewAgentEvent(EventExecutionCompleted, "queen")
	completed.NodeID = &nodeID
	completed.ExecutionID = &executionID
	completed.Data = map[string]any{}
	bus.Publish(completed)

	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log after completion: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("session log lines = %d (%q), want the flushed delta then completion", len(lines), string(data))
	}
	if !strings.Contains(lines[0], "the north field is clear") || !strings.Contains(lines[0], `"stream_id":"queen"`) {
		t.Fatalf("flushed line = %q, want the mirrored reply on the queen stream", lines[0])
	}
	if !strings.Contains(lines[1], string(EventExecutionCompleted)) {
		t.Fatalf("last line = %q, want execution_completed", lines[1])
	}
}
