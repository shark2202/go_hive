package agentloop

// Parity: core/tests/test_stream_stall.py.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func streamStallCtx(reason string, elapsed float64) ReminderContext {
	return ReminderContext{
		Point:    STREAM_STALLED,
		AgentCtx: &ReminderAgentContext{},
		Signals:  &LoopSignals{StallReason: reason, StallElapsed: elapsed},
	}
}

type streamStallLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func captureStreamStallLogs(t *testing.T) *streamStallLogCapture {
	t.Helper()
	capture := &streamStallLogCapture{}
	previous := streamStallLogf
	streamStallLogf = func(format string, args ...any) {
		capture.mu.Lock()
		capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
		capture.mu.Unlock()
	}
	t.Cleanup(func() { streamStallLogf = previous })
	return capture
}

func (c *streamStallLogCapture) count(needle string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	found := 0
	for _, line := range c.lines {
		if strings.Contains(line, needle) {
			found++
		}
	}
	return found
}

// Parity: core/tests/test_stream_stall.py::test_render_returns_nudge_for_inactive_stall
func TestStreamStallRenderInactiveStall(t *testing.T) {
	src := NewStreamStallSource(3, true)
	out, ok := src.RenderReminder(streamStallCtx("inactive", 121.0))
	if !ok || out == nil {
		t.Fatal("expected a reminder for an inactive stall")
	}
	if out.Source != "stream_stall" {
		t.Errorf("source = %q, want stream_stall", out.Source)
	}
	if out.Meta["reason"] != "inactive" {
		t.Errorf("meta reason = %v, want inactive", out.Meta["reason"])
	}
	if out.Meta["nudge_count"] != 1 {
		t.Errorf("meta nudge_count = %v, want 1", out.Meta["nudge_count"])
	}
	if out.Meta["cap"] != 3 {
		t.Errorf("meta cap = %v, want 3", out.Meta["cap"])
	}
	for _, needle := range []string{
		"previous stream stalled",
		"stream went silent after producing events",
		"121s",
	} {
		if !strings.Contains(out.Body, needle) {
			t.Errorf("body missing %q: %s", needle, out.Body)
		}
	}
}

// Parity: core/tests/test_stream_stall.py::test_render_ttft_reason_label
func TestStreamStallTTFTReasonLabel(t *testing.T) {
	src := NewStreamStallSource(3, true)
	out, ok := src.RenderReminder(streamStallCtx("ttft", 600.0))
	if !ok || out == nil {
		t.Fatal("expected a reminder for a ttft stall")
	}
	if !strings.Contains(out.Body, "no tokens before TTFT budget") {
		t.Errorf("body missing TTFT label: %s", out.Body)
	}
}

// Parity: core/tests/test_stream_stall.py::test_per_turn_cap_then_reset
func TestStreamStallPerTurnCapThenReset(t *testing.T) {
	src := NewStreamStallSource(2, true)
	for i := 0; i < 2; i++ {
		if _, ok := src.RenderReminder(streamStallCtx("inactive", 120.0)); !ok {
			t.Fatalf("render %d = none, want a reminder", i+1)
		}
	}
	if _, ok := src.RenderReminder(streamStallCtx("inactive", 120.0)); ok {
		t.Fatal("third render fired past the per-turn cap")
	}
	src.ResetTurn()
	out, ok := src.RenderReminder(streamStallCtx("inactive", 120.0))
	if !ok || out == nil {
		t.Fatal("expected a reminder after ResetTurn")
	}
	if out.Meta["nudge_count"] != 1 {
		t.Errorf("meta nudge_count after reset = %v, want 1", out.Meta["nudge_count"])
	}
}

// Parity: core/tests/test_stream_stall.py::test_disabled_returns_none
func TestStreamStallDisabled(t *testing.T) {
	src := NewStreamStallSource(3, false)
	if _, ok := src.RenderReminder(streamStallCtx("inactive", 120.0)); ok {
		t.Fatal("disabled source produced a reminder")
	}
}

// Parity: core/tests/test_stream_stall.py::test_zero_cap_returns_none
func TestStreamStallZeroCap(t *testing.T) {
	src := NewStreamStallSource(0, true)
	if _, ok := src.RenderReminder(streamStallCtx("inactive", 120.0)); ok {
		t.Fatal("zero-cap source produced a reminder")
	}
}

// Parity: core/tests/test_stream_stall.py::test_no_stall_reason_returns_none
func TestStreamStallNoReason(t *testing.T) {
	src := NewStreamStallSource(3, true)
	if _, ok := src.RenderReminder(streamStallCtx("", 120.0)); ok {
		t.Fatal("a stall with no reason produced a reminder")
	}
}

// Parity: core/tests/test_stream_stall.py::test_points
func TestStreamStallPoints(t *testing.T) {
	points := NewStreamStallSource(3, true).Points()
	if len(points) != 1 || points[0] != STREAM_STALLED {
		t.Fatalf("points = %v, want [%s]", points, STREAM_STALLED)
	}
}

// Parity-exempt: asserts meta.elapsed and the production log line, which
func TestStreamStallMetaElapsedAndLog(t *testing.T) {
	capture := captureStreamStallLogs(t)
	src := NewStreamStallSource(3, true)
	out, ok := src.RenderReminder(streamStallCtx("inactive", 121.0))
	if !ok || out == nil {
		t.Fatal("expected a reminder")
	}
	if out.Meta["elapsed"] != 121.0 {
		t.Errorf("meta elapsed = %v, want 121", out.Meta["elapsed"])
	}
	if got := capture.count("[stream_stall] producing nudge (count=1/3, reason=inactive, elapsed=121s)"); got != 1 {
		t.Errorf("log line count = %d, want 1", got)
	}
}
