package agentloop

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// probeRecorder is the loop-level counterpart of the Python tests' executor
// with probe_for_tool / kill_for_tool attributes: it counts how often the loop
// asked the shared connection whether it is alive, and how often it asked for
// the connection to be torn down.
type probeRecorder struct {
	verdict    string
	probeCalls atomic.Int32
	killCalls  atomic.Int32
}

func (p *probeRecorder) ProbeForTool(toolName string) string {
	p.probeCalls.Add(1)
	return p.verdict
}

func (p *probeRecorder) KillForTool(toolName string) error {
	p.killCalls.Add(1)
	return nil
}

// killFunc adapts a closure to tools.ToolKiller for loops wired without a
// prober.
type killFunc func(toolName string) error

func (f killFunc) KillForTool(toolName string) error { return f(toolName) }

// hungToolLoop builds a loop whose only tool never returns until released, with
// a 50 ms budget, and the probe/kill handles under test.
func hungToolLoop(t *testing.T, prober *probeRecorder) (*AgentLoop, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	cfg := DefaultLoopConfig()
	cfg.ToolCallTimeoutSeconds = 0.05
	cfg.ToolTimeoutOverrides = map[string]float64{}
	loop := NewAgentLoop(cfg, func(req ToolCallRequest) (ToolResult, error) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return ToolResult{Content: "late"}, nil
	})
	if prober != nil {
		loop.ToolProber = prober
		loop.ToolKiller = prober
	}
	return loop, release
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_alive_server_is_not_killed
func TestAliveToolServerKeepsItsConnection(t *testing.T) {
	prober := &probeRecorder{verdict: "alive"}
	loop, _ := hungToolLoop(t, prober)

	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "browser_evaluate", Input: map[string]any{}})

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if got := prober.probeCalls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := prober.killCalls.Load(); got != 0 {
		t.Fatalf("kill calls = %d, want 0 (alive server must not be force-disconnected)", got)
	}
	for _, needle := range []string{"timed out after", "TRANSPORT STATUS", "DO NOT kill", "NOT an application or browser crash"} {
		if !strings.Contains(result.Content, needle) {
			t.Fatalf("timeout message missing %q: %s", needle, result.Content)
		}
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_resetting_server_is_not_killed_again
func TestResettingToolServerIsNotKilledAgain(t *testing.T) {
	prober := &probeRecorder{verdict: "resetting"}
	loop, _ := hungToolLoop(t, prober)

	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "browser_evaluate", Input: map[string]any{}})

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if got := prober.killCalls.Load(); got != 0 {
		t.Fatalf("kill calls = %d, want 0 (an in-flight reset must not stack a teardown)", got)
	}
	for _, needle := range []string{"timed out after", "AUTOMATICALLY", "DO NOT kill"} {
		if !strings.Contains(result.Content, needle) {
			t.Fatalf("timeout message missing %q: %s", needle, result.Content)
		}
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_dead_server_is_killed
func TestDeadToolServerIsReclaimed(t *testing.T) {
	prober := &probeRecorder{verdict: "dead"}
	loop, _ := hungToolLoop(t, prober)

	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "browser_evaluate", Input: map[string]any{}})

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if got := prober.probeCalls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
	if got := prober.killCalls.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1 (a provably dead server must be torn down)", got)
	}
	if !strings.Contains(result.Content, "failed a liveness check") {
		t.Fatalf("dead-server message missing the liveness wording: %s", result.Content)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_unknown_probe_falls_back_to_legacy_kill
func TestUnknownProbeFallsBackToLegacyKill(t *testing.T) {
	prober := &probeRecorder{verdict: "unknown"}
	loop, _ := hungToolLoop(t, prober)

	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "mystery_tool", Input: map[string]any{}})

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if got := prober.killCalls.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1 (unknown probe keeps the legacy teardown)", got)
	}
}

// Parity: core/tests/test_tool_timeout_probe_policy.py::test_no_probe_support_falls_back_to_legacy_kill
func TestNoProbeSupportStillKillsThroughToolKiller(t *testing.T) {
	// A loop with a killer but no prober: the probe reads unknown, so the
	// teardown the killer provides still runs.
	cfg := DefaultLoopConfig()
	cfg.ToolCallTimeoutSeconds = 0.05
	cfg.ToolTimeoutOverrides = map[string]float64{}
	release := make(chan struct{})
	defer close(release)
	loop := NewAgentLoop(cfg, func(req ToolCallRequest) (ToolResult, error) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		return ToolResult{Content: "late"}, nil
	})
	var kills atomic.Int32
	loop.ToolKiller = killFunc(func(string) error {
		kills.Add(1)
		return nil
	})

	result := loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "mystery_tool", Input: map[string]any{}})

	if !result.IsError {
		t.Fatalf("hung call must surface as an error result, got %+v", result)
	}
	if got := kills.Load(); got != 1 {
		t.Fatalf("kill calls = %d, want 1", got)
	}
}

// Parity-exempt: the loop-wired timeout counter has no upstream counterpart
// (upstream only logs the timeout); it exists so the host can observe how often
// the budget fired.
func TestToolTimeoutBumpsTheLoopCounter(t *testing.T) {
	prober := &probeRecorder{verdict: "alive"}
	loop, _ := hungToolLoop(t, prober)

	loop.callExecutor(context.Background(),
		ToolCallRequest{ID: "tu_1", Name: "slow_tool", Input: map[string]any{}})

	if got := loop.Counter("tool_timeout"); got != 1 {
		t.Fatalf("tool_timeout counter = %d, want 1", got)
	}
}
