package agentloop

import (
	"errors"
	"strings"
	"testing"
)

func colonyCtx() *ReminderAgentContext {
	return &ReminderAgentContext{
		IsQueenStream:  true,
		AvailableTools: []Tool{{Name: "run_playbook"}, {Name: "write_skill"}},
	}
}

func renderNudge(src *ColonyParallelNudgeSource, ctx *ReminderAgentContext) (string, bool) {
	return src.Render(ReminderContext{Point: POST_TOOL_USE, AgentCtx: ctx})
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_no_fire_before_pilot_work
func TestColonyParallelNudgeNoFireBeforePilotWork(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"tracker_sql"})
	if body, ok := renderNudge(src, colonyCtx()); ok {
		t.Fatalf("nudge fired before pilot work: %q", body)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_no_fire_after_work_without_tracker_write
func TestColonyParallelNudgeNoFireAfterWorkWithoutTrackerWrite(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"browser_click"})
	if body, ok := renderNudge(src, colonyCtx()); ok {
		t.Fatalf("nudge fired without a tracker write: %q", body)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_fires_after_pilot_then_tracker_write
func TestColonyParallelNudgeFiresAfterPilotThenTrackerWrite(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"browser_click", "web_scrape"})
	src.ObserveTurn([]string{"tracker_upsert"})

	body, ok := renderNudge(src, colonyCtx())
	if !ok {
		t.Fatal("nudge did not fire after pilot + tracker write")
	}
	if !strings.Contains(body, "run_playbook") || !strings.Contains(body, "pilot") {
		t.Errorf("body = %q, want run_playbook and pilot", body)
	}
	if body != colonyParallelNudgeBody {
		t.Errorf("body differs from the reference text:\n got: %q\nwant: %q", body, colonyParallelNudgeBody)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_silent_once_fanned_out
func TestColonyParallelNudgeSilentOnceFannedOut(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"browser_click"})
	src.ObserveTurn([]string{"run_playbook"})
	src.ObserveTurn([]string{"tracker_upsert"})
	if body, ok := renderNudge(src, colonyCtx()); ok {
		t.Fatalf("nudge fired after run_playbook: %q", body)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_silent_outside_colony_phase
func TestColonyParallelNudgeSilentOutsideColonyPhase(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"browser_click"})
	src.ObserveTurn([]string{"tracker_upsert"})
	independent := &ReminderAgentContext{
		IsQueenStream:  true,
		AvailableTools: []Tool{{Name: "suggest_colony"}},
	}
	if body, ok := renderNudge(src, independent); ok {
		t.Fatalf("nudge fired outside the colony phase: %q", body)
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_applies_to_queen_only
func TestColonyParallelNudgeAppliesToQueenOnly(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	if !src.AppliesTo(&ReminderAgentContext{IsQueenStream: true}) {
		t.Error("source did not apply to the queen stream")
	}
	if src.AppliesTo(&ReminderAgentContext{IsQueenStream: false}) {
		t.Error("source applied to a worker stream")
	}
}

// Parity: core/tests/test_colony_parallel_nudge.py::test_cooldown_blocks_repeat
func TestColonyParallelNudgeCooldownBlocksRepeat(t *testing.T) {
	src := NewColonyParallelNudgeSourceWithPolicy(4, 10)
	src.ObserveTurn([]string{"browser_click"})
	src.ObserveTurn([]string{"tracker_upsert"})
	if _, ok := renderNudge(src, colonyCtx()); !ok {
		t.Fatal("nudge did not fire the first time")
	}
	src.ObserveTurn([]string{"tracker_upsert"})
	if body, ok := renderNudge(src, colonyCtx()); ok {
		t.Fatalf("cooldown did not block the repeat: %q", body)
	}
}

// Parity-exempt: the reference resolves the phase through the dynamic provider
func TestColonyParallelNudgeDynamicProviderWinsAndFallsBack(t *testing.T) {
	src := NewColonyParallelNudgeSource()
	src.ObserveTurn([]string{"browser_click"})
	src.ObserveTurn([]string{"tracker_upsert"})

	live := &ReminderAgentContext{
		IsQueenStream:        true,
		AvailableTools:       []Tool{{Name: "run_playbook"}},
		DynamicToolsProvider: func() []Tool { return []Tool{{Name: "suggest_colony"}} },
	}
	if body, ok := renderNudge(src, live); ok {
		t.Fatalf("static snapshot overrode the live provider: %q", body)
	}

	failing := &ReminderAgentContext{
		IsQueenStream:  true,
		AvailableTools: []Tool{{Name: "run_worker"}},
		DynamicToolsProvider: func() []Tool {
			panic(errors.New("provider down"))
		},
	}
	if _, ok := renderNudge(src, failing); !ok {
		t.Fatal("panicking provider did not fall back to available tools")
	}
}

// Parity-exempt: the per-session cap silences the nudge even when every other
func TestColonyParallelNudgeCapSilences(t *testing.T) {
	src := NewColonyParallelNudgeSourceWithPolicy(1, 1)
	src.ObserveTurn([]string{"browser_click"})
	src.ObserveTurn([]string{"tracker_upsert"})
	if _, ok := renderNudge(src, colonyCtx()); !ok {
		t.Fatal("nudge did not fire the first time")
	}
	src.ObserveTurn([]string{"tracker_upsert"})
	if body, ok := renderNudge(src, colonyCtx()); ok {
		t.Fatalf("cap did not silence the nudge: %q", body)
	}
}
