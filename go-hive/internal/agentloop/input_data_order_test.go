package agentloop

// Regression tests for A9: ordered input_data in the initial user message.
//
// Python iterates ctx.input_data.items() and therefore preserves insertion
// order; a Go map cannot. AgentContext.InputDataOrder plus SetInputData carry
// the order, and BuildInitialMessage emits it before the deterministic sorted
// fallback.
//
// Parity: framework/agent_loop/agent_loop.py::_build_initial_message

import (
	"strings"
	"testing"
)

// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
func TestBuildInitialMessagePreservesInputDataOrder(t *testing.T) {
	ctx := &AgentContext{
		AgentSpec: NewAgentSpec("worker", "Worker", "do the task"),
		InputData: map[string]any{
			"task":    "research widgets",
			"binding": "colony-1",
			"zeta":    "last",
		},
		InputDataOrder: []string{"task", "binding"},
	}
	message := BuildInitialMessage(ctx)
	lines := strings.Split(message, "\n")
	if len(lines) != 3 {
		t.Fatalf("message lines = %d, want 3: %q", len(lines), message)
	}
	if lines[0] != "task: research widgets" {
		t.Fatalf("line 0 = %q, want the first inserted key (task)", lines[0])
	}
	if lines[1] != "binding: colony-1" {
		t.Fatalf("line 1 = %q, want the second inserted key (binding)", lines[1])
	}
	if lines[2] != "zeta: last" {
		t.Fatalf("line 2 = %q, want the remaining key in sorted order", lines[2])
	}
}

// Parity-exempt: Go map iteration is unordered; the sorted fallback is the
// deterministic port choice when no producer recorded order.
//
// Without order metadata the historical deterministic sorted order is kept, so
// a map-only caller never gets random output.
func TestBuildInitialMessageWithoutOrderFallsBackToSorted(t *testing.T) {
	ctx := &AgentContext{
		AgentSpec: NewAgentSpec("worker", "Worker", "d"),
		InputData: map[string]any{"beta": "2", "alpha": "1"},
	}
	if got := BuildInitialMessage(ctx); got != "alpha: 1\nbeta: 2" {
		t.Fatalf("message = %q, want sorted fallback", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
//
// SetInputData is the producer-facing helper: it writes the map and records the
// insertion order in one call, including duplicate-key override semantics.
func TestSetInputDataRecordsInsertionOrder(t *testing.T) {
	ctx := &AgentContext{}
	ctx.SetInputData(
		InputDataPair{Key: "task", Value: "first"},
		InputDataPair{Key: "binding", Value: "colony-1"},
		InputDataPair{Key: "task", Value: "overridden"},
	)
	if got := ctx.InputData["task"]; got != "overridden" {
		t.Fatalf("task = %#v, want the later value", got)
	}
	if len(ctx.InputDataOrder) != 2 ||
		ctx.InputDataOrder[0] != "task" || ctx.InputDataOrder[1] != "binding" {
		t.Fatalf("InputDataOrder = %#v, want [task binding]", ctx.InputDataOrder)
	}
	if got := BuildInitialMessage(ctx); !strings.HasPrefix(got, "task: overridden\nbinding: colony-1") {
		t.Fatalf("message = %q, want task before binding", got)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_build_initial_message
//
// Declared InputKeys are a fallback only: a key already emitted from input_data
// must not be duplicated, and nil values stay skipped like upstream.
func TestBuildInitialMessageUsesInputKeysAsFallbackOnly(t *testing.T) {
	ctx := &AgentContext{
		AgentSpec: func() AgentSpec {
			spec := NewAgentSpec("worker", "Worker", "d")
			spec.InputKeys = []string{"missing", "task"}
			return spec
		}(),
		InputData:      map[string]any{"task": "do it", "optional": nil},
		InputDataOrder: []string{"task"},
	}
	if got := BuildInitialMessage(ctx); got != "task: do it" {
		t.Fatalf("message = %q, want one task line and no nil optional", got)
	}
}
