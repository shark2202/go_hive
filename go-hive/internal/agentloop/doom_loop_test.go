package agentloop

import (
	"context"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity-exempt: upstream detects stalls with a judge (framework/server/
// stall_check.py) and has no fingerprint rule; this locks the deterministic
// half — argument order must not disguise a repeated call.
func TestToolCallFingerprintIgnoresArgumentOrder(t *testing.T) {
	a := ToolCallFingerprint("read_file", map[string]any{"path": "a.txt", "limit": 10})
	b := ToolCallFingerprint("read_file", map[string]any{"limit": 10, "path": "a.txt"})
	if a != b {
		t.Fatalf("fingerprint depends on map order:\n%s\n%s", a, b)
	}
	if a == ToolCallFingerprint("read_file", map[string]any{"path": "a.txt", "limit": 11}) {
		t.Fatal("different arguments fingerprint the same")
	}
	if a == ToolCallFingerprint("write_file", map[string]any{"path": "a.txt", "limit": 10}) {
		t.Fatal("different tools fingerprint the same")
	}
}

// Parity-exempt: one reminder per streak, at the threshold, never on the calls
// before it — a second reminder for the same repetition would be noise.
func TestDoomLoopDetectorWarnsOncePerStreak(t *testing.T) {
	detector := NewDoomLoopDetector()
	call := map[string]any{"path": "a.txt"}
	for i := 1; i < DefaultDoomLoopThreshold; i++ {
		if warning := detector.Observe("read_file", call); warning != "" {
			t.Fatalf("call %d warned before the threshold: %s", i, warning)
		}
	}
	warning := detector.Observe("read_file", call)
	if warning == "" {
		t.Fatal("no warning at the threshold")
	}
	if !strings.Contains(warning, "`read_file`") || !strings.Contains(warning, "3 times") {
		t.Fatalf("warning does not name the call and the streak: %s", warning)
	}
	if again := detector.Observe("read_file", call); again != "" {
		t.Fatalf("second warning in the same streak: %s", again)
	}
	if got := detector.Warnings(); got != 1 {
		t.Fatalf("Warnings() = %d, want 1", got)
	}
	if !detector.Active() {
		t.Fatal("Active() = false inside the repeated streak")
	}

	// A different call breaks the streak; going back to the first call starts
	// a fresh one and can warn again.
	if warning := detector.Observe("read_file", map[string]any{"path": "b.txt"}); warning != "" {
		t.Fatalf("a changed call warned: %s", warning)
	}
	if got := detector.Streak(); got != 1 {
		t.Fatalf("Streak() = %d after a changed call, want 1", got)
	}
	if detector.Active() {
		t.Fatal("Active() = true after the streak broke")
	}
	for i := 1; i < DefaultDoomLoopThreshold; i++ {
		detector.Observe("read_file", call)
	}
	if warning := detector.Observe("read_file", call); warning == "" {
		t.Fatal("a fresh streak did not warn")
	}
	if got := detector.Warnings(); got != 2 {
		t.Fatalf("Warnings() = %d, want 2", got)
	}
}

// Parity-exempt: Reset is the per-turn boundary; the streak must not survive
// into the next user message.
func TestDoomLoopDetectorReset(t *testing.T) {
	detector := NewDoomLoopDetector()
	for i := 0; i < DefaultDoomLoopThreshold; i++ {
		detector.Observe("read_file", map[string]any{"path": "a.txt"})
	}
	if !detector.Active() {
		t.Fatal("detector is not active after a full streak")
	}
	detector.Reset()
	if detector.Active() || detector.Streak() != 0 {
		t.Fatalf("Reset left streak=%d active=%v", detector.Streak(), detector.Active())
	}
	if detector.Warnings() != 1 {
		t.Fatalf("Reset cleared the warning count: %d", detector.Warnings())
	}
	// The threshold is configurable for callers that want a looser rule.
	strict := &DoomLoopDetector{Threshold: 2}
	strict.Observe("read_file", map[string]any{"path": "a.txt"})
	if warning := strict.Observe("read_file", map[string]any{"path": "a.txt"}); warning == "" {
		t.Fatal("Threshold=2 did not warn on the second identical call")
	}
}

// Parity-exempt: the loop must notice the repetition and keep going. The model
// here answers, then repeats one identical call past the threshold, then
// finishes — the turn succeeds and the reminder rides on the tool result.
func TestLoopWarnsOnRepeatedToolCallWithoutStopping(t *testing.T) {
	call := llm.ToolCall{ID: "call_1", Name: "read_file", Arguments: `{"path":"status.txt"}`}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "let me look", toolCalls: []llm.ToolCall{call}},
		{content: "again", toolCalls: []llm.ToolCall{llm.ToolCall{ID: "call_2", Name: "read_file", Arguments: `{"path":"status.txt"}`}}},
		{content: "again", toolCalls: []llm.ToolCall{llm.ToolCall{ID: "call_3", Name: "read_file", Arguments: `{"path":"status.txt"}`}}},
		{content: "all done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 6}, successToolExecutor)
	ctx := judgeTestContext(completer, nil, "read_file")
	result, err := loop.ExecuteTurn(context.Background(), ctx, NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if got := loop.DoomLoop.Warnings(); got != 1 {
		t.Fatalf("DoomLoop.Warnings() = %d, want 1", got)
	}
	if got := loop.Stats()["doom_loop_warned"]; got != 1 {
		t.Fatalf("stats[doom_loop_warned] = %d, want 1", got)
	}

	warned := 0
	for _, record := range result.ToolResults {
		if strings.Contains(record.Content, "identical arguments") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("reminder appeared on %d tool results, want exactly 1", warned)
	}
	// The loop ran to the model's own answer: no hard stop, no error.
	if result.FinalText != "all done" {
		t.Fatalf("FinalText = %q, want the model's final answer", result.FinalText)
	}
}
