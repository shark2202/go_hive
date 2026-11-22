package agentloop

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// streamStallResult builds the typed watchdog verdict the streaming completer
// returns when a stream trips (internal/server/chat_stream.go).
func streamStallResult(reason string) error {
	return &llm.StreamStalledError{Reason: reason, ElapsedSeconds: 121, LimitSeconds: 120}
}

// Parity-exempt: upstream drives this path from a live stream watchdog
// (agent_loop.py::_run_turn_loop) whose async harness has no Go equivalent;
// the assertions pin the same recovery contract — collect at STREAM_STALLED,
// inject the wrapped nudge, re-stream the same inner turn.
func TestStreamStallNudgeRecoversTheTurn(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{err: streamStallResult("inactive")},
		{content: "recovered"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(3, true))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "recovered" {
		t.Fatalf("turn did not recover: %+v", result)
	}
	if completer.callCount() != 2 {
		t.Fatalf("model calls = %d, want the stalled call plus one retry", completer.callCount())
	}
	retry := allText(completer.request(1).Messages)
	for _, want := range []string{
		"<system-reminder>",
		"previous stream stalled",
		"stream went silent after producing events",
		"121s",
	} {
		if !strings.Contains(retry, want) {
			t.Fatalf("retry call missing %q:\n%s", want, retry)
		}
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
	// The nudge re-streams the same inner turn, so the turn counts one
	// iteration, not two.
	if result.InnerTurns != 1 {
		t.Fatalf("InnerTurns = %d, want 1 (the nudge retries the same inner turn)", result.InnerTurns)
	}
}

// Parity: tests/test_stream_stall.py::test_per_turn_cap_then_reset
// (the cap's loop-level consequence: once spent, the failure surfaces).
func TestStreamStallNudgeCapSurfacesTheError(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{err: streamStallResult("ttft")},
		{err: streamStallResult("ttft")},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(1, true))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	_, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("ExecuteTurn error = %v, want the stream-stalled error after the cap", err)
	}
	if completer.callCount() != 2 {
		t.Fatalf("model calls = %d, want the capped nudge then the surfaced error", completer.callCount())
	}
	if got := loop.Counter("stream_stall_nudged"); got != 1 {
		t.Fatalf("stream_stall_nudged = %d, want 1", got)
	}
}

// Parity: tests/test_stream_stall.py::test_disabled_returns_none
// (disabled source → the loop keeps its ordinary failure path).
func TestStreamStallDisabledSourceSurfacesTheError(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{err: streamStallResult("inactive")},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(3, false))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err == nil {
		t.Fatal("a disabled stream-stall source must not absorb the stall")
	}
	if got := loop.Counter("stream_stall_nudged"); got != 0 {
		t.Fatalf("stream_stall_nudged = %d, want 0", got)
	}
}

// Parity-exempt: upstream resets the source at the top of every
// _run_turn_loop; this pins the Go loop's per-turn reset via the hub.
func TestStreamStallCapResetsPerTurn(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{err: streamStallResult("inactive")},
		{content: "first turn done"},
		{err: streamStallResult("inactive")},
		{content: "second turn done"},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 4}, nil)
	loop.Hub.Register(NewStreamStallSource(1, true))
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	for turn := 1; turn <= 2; turn++ {
		result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
		if err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
		if !result.Success {
			t.Fatalf("turn %d did not recover: %+v", turn, result)
		}
	}
	if got := loop.Counter("stream_stall_nudged"); got != 2 {
		t.Fatalf("stream_stall_nudged = %d, want one nudge in each turn (cap must reset per turn)", got)
	}
}

type resetCountingSource struct {
	mu      sync.Mutex
	resets  int
	applies bool
}

func (s *resetCountingSource) Name() string                         { return "reset-counting" }
func (s *resetCountingSource) Points() []ReminderPoint              { return nil }
func (s *resetCountingSource) AppliesTo(*ReminderAgentContext) bool { return s.applies }
func (s *resetCountingSource) Render(ReminderContext) (string, bool) {
	return "", false
}
func (s *resetCountingSource) ResetTurn() {
	s.mu.Lock()
	s.resets++
	s.mu.Unlock()
}

func (s *resetCountingSource) resetCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resets
}

// Parity-exempt: ReminderHub.ResetTurn is the Go seam for upstream's direct
// `self._stream_stall_source.reset_turn()` call; pins that it reaches consulted
// sources and skips ones Bind filtered out.
func TestHubResetTurnReachesConsultedSourcesOnly(t *testing.T) {
	hub := NewReminderHub()
	kept := &resetCountingSource{applies: true}
	dropped := &resetCountingSource{applies: false}
	hub.Register(kept)
	hub.Register(dropped)

	hub.ResetTurn()
	if kept.resetCount() != 1 || dropped.resetCount() != 1 {
		t.Fatalf("pre-bind reset counts = %d/%d, want 1/1 (no bind yet: every source is consulted)",
			kept.resetCount(), dropped.resetCount())
	}

	hub.Bind(&ReminderAgentContext{})
	hub.ResetTurn()
	if kept.resetCount() != 2 {
		t.Fatalf("kept reset count = %d, want 2", kept.resetCount())
	}
	if dropped.resetCount() != 1 {
		t.Fatalf("dropped source was reset after Bind filtered it out (count = %d)", dropped.resetCount())
	}
}
