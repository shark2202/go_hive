package agentloop

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func idleTickCtx(signals *LoopSignals) ReminderContext {
	return ReminderContext{
		Point:    IDLE_TICK,
		AgentCtx: &ReminderAgentContext{},
		Signals:  signals,
	}
}

func idleTickCtxWithTasks(signals *LoopSignals, provider func() []IdleTask) ReminderContext {
	return ReminderContext{
		Point: IDLE_TICK,
		AgentCtx: &ReminderAgentContext{
			SessionID:         "session-1",
			OpenTasksProvider: provider,
		},
		Signals: signals,
	}
}

func reasonPtr(reason ParkReason) *ParkReason { return &reason }

type idleLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func captureIdleNudgeLogs(t *testing.T) *idleLogCapture {
	t.Helper()
	capture := &idleLogCapture{}
	previous := idleNudgeLogf
	idleNudgeLogf = func(format string, args ...any) {
		capture.mu.Lock()
		capture.lines = append(capture.lines, fmt.Sprintf(format, args...))
		capture.mu.Unlock()
	}
	t.Cleanup(func() { idleNudgeLogf = previous })
	return capture
}

func (c *idleLogCapture) count(needle string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, line := range c.lines {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// Parity: core/tests/test_idle_nudge.py::test_park_reason_activity_mapping
func TestIdleNudgeParkReasonActivityMapping(t *testing.T) {
	cases := []struct {
		reason   ParkReason
		expected LoopActivity
	}{
		{ParkReasonAskUser, LoopActivityAwaitingUser},
		{ParkReasonColonySuggestion, LoopActivityAwaitingUser},
		{ParkReasonTurnDone, LoopActivityAwaitingUser},
		{ParkReasonAwaitingQueen, LoopActivityAwaitingUser},
		{ParkReasonUserStopped, LoopActivityInterrupted},
		{ParkReasonLLMError, LoopActivityInterrupted},
		{ParkReasonEmptyResponses, LoopActivityInterrupted},
		{ParkReasonDoomLoop, LoopActivityInterrupted},
		{ParkReasonUnknown, LoopActivityInterrupted},
	}
	for _, tc := range cases {
		if got := tc.reason.Activity(); got != tc.expected {
			t.Errorf("%s.Activity() = %s, want %s", tc.reason, got, tc.expected)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_park_reason_activity_is_total
func TestIdleNudgeParkReasonActivityIsTotal(t *testing.T) {
	for _, reason := range AllParkReasons {
		got := reason.Activity()
		if got != LoopActivityAwaitingUser && got != LoopActivityInterrupted {
			t.Errorf("%s.Activity() = %q, outside the two-value set", reason, got)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_park_reason_is_silent_park
func TestIdleNudgeParkReasonIsSilentPark(t *testing.T) {
	cases := []struct {
		reason ParkReason
		silent bool
	}{
		{ParkReasonTurnDone, true},
		{ParkReasonUserStopped, true},
		{ParkReasonAskUser, false},
		{ParkReasonColonySuggestion, false},
		{ParkReasonAwaitingQueen, false},
		{ParkReasonLLMError, false},
		{ParkReasonEmptyResponses, false},
		{ParkReasonDoomLoop, false},
		{ParkReasonUnknown, false},
	}
	for _, tc := range cases {
		if got := tc.reason.IsSilentPark(); got != tc.silent {
			t.Errorf("%s.IsSilentPark() = %v, want %v", tc.reason, got, tc.silent)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_park_reason_is_silent_park_disjoint_from_external_wait
func TestIdleNudgeParkReasonIsSilentParkDisjointFromExternalWait(t *testing.T) {
	for _, reason := range AllParkReasons {
		if reason.IsSilentPark() && reason.IsExternalWait() {
			t.Errorf("%s cannot be both silent and external_wait", reason)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_fires_between_turns
func TestIdleNudgeRenderFiresBetweenTurns(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 12.0}))
	if !ok || out == nil {
		t.Fatal("expected a reminder between turns")
	}
	if out.Source != "idle_nudge" {
		t.Errorf("Source = %q, want idle_nudge", out.Source)
	}
	if got := out.Meta["substate"]; got != "between_turns" {
		t.Errorf("substate = %v, want between_turns", got)
	}
	if got := out.Meta["nudge_count"]; got != 1 {
		t.Errorf("nudge_count = %v, want 1", got)
	}
	if got := out.Meta["cap"]; got != 3 {
		t.Errorf("cap = %v, want 3", got)
	}
	if !strings.Contains(out.Body, "between_turns") {
		t.Errorf("body missing between_turns: %q", out.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_slow_ttft_substate
func TestIdleNudgeRenderSlowTTFTSubstate(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 12.0, StreamActive: true}))
	if !ok || out == nil {
		t.Fatal("expected a slow-TTFT reminder")
	}
	if got := out.Meta["substate"]; got != "slow_ttft" {
		t.Errorf("substate = %v, want slow_ttft", got)
	}
	if !strings.Contains(out.Body, "slow_ttft") {
		t.Errorf("body missing slow_ttft: %q", out.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_active_stream_with_events
func TestIdleNudgeRenderSkipsActiveStreamWithEvents(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:    12.0,
		StreamActive:   true,
		FirstEventSeen: true,
	})); ok || out != nil {
		t.Fatalf("active stream with events should not nudge, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_when_awaiting_input
func TestIdleNudgeRenderSkipsWhenAwaitingInput(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
	})); ok || out != nil {
		t.Fatalf("awaiting input without an awaiting budget should not nudge, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_fires_parked_no_question
func TestIdleNudgeRenderFiresParkedNoQuestion(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   12.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
	}))
	if !ok || out == nil {
		t.Fatal("expected a questionless-park reminder")
	}
	if got := out.Meta["substate"]; got != "parked_no_question" {
		t.Errorf("substate = %v, want parked_no_question", got)
	}
	if !strings.Contains(out.Body, "no question") {
		t.Errorf("body missing no question: %q", out.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_awaiting_with_question
func TestIdleNudgeRenderSkipsAwaitingWithQuestion(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonAskUser),
	})); ok || out != nil {
		t.Fatalf("ask_user park should not nudge, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_external_wait_parks
func TestIdleNudgeRenderSkipsExternalWaitParks(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	for _, reason := range []ParkReason{
		ParkReasonAskUser,
		ParkReasonColonySuggestion,
		ParkReasonAwaitingQueen,
	} {
		if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
			IdleSeconds:   999.0,
			AwaitingInput: true,
			ParkReason:    reasonPtr(reason),
		})); ok || out != nil {
			t.Errorf("%s should suppress the nudge, got %+v", reason, out)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_fires_parked_broken
func TestIdleNudgeRenderFiresParkedBroken(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 120.0,
		BrokenBudgetSeconds:   5.0,
	})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonLLMError),
	}))
	if !ok || out == nil {
		t.Fatal("expected a broken-park reminder")
	}
	if got := out.Meta["substate"]; got != "parked_broken" {
		t.Errorf("substate = %v, want parked_broken", got)
	}
	if got := out.Meta["cap"]; got != 3 {
		t.Errorf("cap = %v, want 3", got)
	}
	if !strings.Contains(out.Body, "llm_error") {
		t.Errorf("body missing llm_error: %q", out.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_parked_broken_body_is_reason_aware
func TestIdleNudgeParkedBrokenBodyIsReasonAware(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 120.0,
		BrokenBudgetSeconds:   5.0,
	})
	doom, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonDoomLoop),
	}))
	if !ok || doom == nil {
		t.Fatal("expected a doom-loop reminder")
	}
	body := strings.ToLower(doom.Body)
	if !strings.Contains(body, "different approach") {
		t.Errorf("doom body missing 'different approach': %q", doom.Body)
	}
	if !strings.Contains(body, "do not repeat") {
		t.Errorf("doom body missing 'do not repeat': %q", doom.Body)
	}
	if strings.Contains(body, "re-attempt") {
		t.Errorf("doom body must not re-attempt the blocked call: %q", doom.Body)
	}

	src.Reset()
	llm, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonLLMError),
	}))
	if !ok || llm == nil {
		t.Fatal("expected an llm-error reminder after reset")
	}
	if !strings.Contains(llm.Body, "re-attempt the work that failed") {
		t.Errorf("llm-error body missing the re-attempt wording: %q", llm.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_broken_park_keys_off_broken_budget
func TestIdleNudgeBrokenParkKeysOffBrokenBudget(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 120.0,
		BrokenBudgetSeconds:   5.0,
	})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonDoomLoop),
	}))
	if !ok || out == nil {
		t.Fatal("expected the broken budget to fire at idle 8s")
	}
	if got := out.Meta["substate"]; got != "parked_broken" {
		t.Errorf("substate = %v, want parked_broken", got)
	}
	src2 := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 120.0,
	})
	if out, ok := src2.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonDoomLoop),
	})); ok || out != nil {
		t.Fatalf("broken park without a broken budget should be inert, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_when_user_stopped
func TestIdleNudgeRenderSkipsWhenUserStopped(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
		UserStopped:   true,
	})); ok || out != nil {
		t.Fatalf("user-stopped park should not nudge, got %+v", out)
	}
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
	}))
	if !ok || out == nil {
		t.Fatal("identical park without user_stopped should fire")
	}
	if got := out.Meta["substate"]; got != "parked_no_question" {
		t.Errorf("substate = %v, want parked_no_question", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_awaiting_park_uses_its_own_budget
func TestIdleNudgeAwaitingParkUsesItsOwnBudget(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
	}))
	if !ok || out == nil {
		t.Fatal("expected the awaiting budget to fire at idle 8s")
	}
	if got := out.Meta["substate"]; got != "parked_no_question" {
		t.Errorf("substate = %v, want parked_no_question", got)
	}
	src2 := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 120.0, MaxNudges: 3})
	if out, ok := src2.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   8.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
	})); ok || out != nil {
		t.Fatalf("no awaiting budget should leave the questionless park inert, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_when_park_is_turn_done
func TestIdleNudgeRenderSkipsWhenParkIsTurnDone(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
		IdleSeconds:   999.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonTurnDone),
	})); ok || out != nil {
		t.Fatalf("turn_done park should not nudge, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_still_fires_for_broken_parks
func TestIdleNudgeRenderStillFiresForBrokenParks(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 120.0,
		BrokenBudgetSeconds:   5.0,
	})
	for _, broken := range []ParkReason{
		ParkReasonLLMError,
		ParkReasonDoomLoop,
		ParkReasonEmptyResponses,
	} {
		out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
			IdleSeconds:   10.0,
			AwaitingInput: true,
			ParkReason:    reasonPtr(broken),
		}))
		if !ok || out == nil {
			t.Fatalf("broken park %s should still nudge", broken)
		}
		if got := out.Meta["substate"]; got != "parked_broken" {
			t.Errorf("%s substate = %v, want parked_broken", broken, got)
		}
		src.Reset()
	}
}

// Parity: core/tests/test_idle_nudge.py::test_silent_park_gate_logs_once_across_ticks
func TestIdleNudgeSilentParkGateLogsOnceAcrossTicks(t *testing.T) {
	logs := captureIdleNudgeLogs(t)
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	for _, idle := range []float64{450.0, 495.0, 505.0, 600.0} {
		if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{
			IdleSeconds:   idle,
			AwaitingInput: true,
			ParkReason:    reasonPtr(ParkReasonTurnDone),
		})); ok || out != nil {
			t.Fatalf("turn_done park should stay suppressed, got %+v", out)
		}
	}
	if got := logs.count("gate=silent_park"); got != 1 {
		t.Fatalf("expected one silent_park line, got %d", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_gate_relogs_after_user_message
func TestIdleNudgeGateRelogsAfterUserMessage(t *testing.T) {
	logs := captureIdleNudgeLogs(t)
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	render := func(idle float64) {
		_, _ = src.RenderReminder(idleTickCtx(&LoopSignals{
			IdleSeconds:   idle,
			AwaitingInput: true,
			ParkReason:    reasonPtr(ParkReasonTurnDone),
		}))
	}
	render(450.0)
	render(495.0)
	src.Reset()
	render(540.0)
	if got := logs.count("gate=silent_park"); got != 2 {
		t.Fatalf("expected one line per cycle, got %d", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_skips_under_budget
func TestIdleNudgeRenderSkipsUnderBudget(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 2.0})); ok || out != nil {
		t.Fatalf("idle below budget should not nudge, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_self_rate_limits
func TestIdleNudgeRenderSelfRateLimits(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 3})
	first, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 12.0}))
	if !ok || first == nil {
		t.Fatal("first render should fire")
	}
	if second, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 13.0})); ok || second != nil {
		t.Fatalf("second render inside the window should be suppressed, got %+v", second)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_render_respects_cap
func TestIdleNudgeRenderRespectsCap(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 2})
	fired := 0
	for i := 0; i < 5; i++ {
		if _, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 99.0})); ok {
			fired++
		}
		src.lastEvalAt = time.Time{}
	}
	if fired != 2 {
		t.Errorf("fired = %d, want 2", fired)
	}
	if got := src.nudges["between_turns"]; got != 2 {
		t.Errorf("nudges[between_turns] = %d, want 2", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_parked_open_tasks_variant
func TestIdleNudgeParkedOpenTasksVariant(t *testing.T) {
	provider := func() []IdleTask {
		return []IdleTask{
			{ID: "7", Subject: "ship the thing"},
			{ID: "8", Subject: "write docs"},
		}
	}
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	out, ok := src.RenderReminder(idleTickCtxWithTasks(&LoopSignals{
		IdleSeconds:   12.0,
		AwaitingInput: true,
		ParkReason:    reasonPtr(ParkReasonUnknown),
	}, provider))
	if !ok || out == nil {
		t.Fatal("expected a parked_open_tasks reminder")
	}
	if got := out.Meta["substate"]; got != "parked_open_tasks" {
		t.Errorf("substate = %v, want parked_open_tasks", got)
	}
	if got := out.Meta["cap"]; got != 3 {
		t.Errorf("cap = %v, want 3", got)
	}
	if !strings.Contains(out.Body, `#7 "ship the thing"`) {
		t.Errorf("body missing task 7: %q", out.Body)
	}
	if !strings.Contains(out.Body, `#8 "write docs"`) {
		t.Errorf("body missing task 8: %q", out.Body)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_parked_open_tasks_respects_full_cap
func TestIdleNudgeParkedOpenTasksRespectsFullCap(t *testing.T) {
	provider := func() []IdleTask { return []IdleTask{{ID: "1", Subject: "task"}} }
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	fired := 0
	for i := 0; i < 6; i++ {
		if _, ok := src.RenderReminder(idleTickCtxWithTasks(&LoopSignals{
			IdleSeconds:   99.0,
			AwaitingInput: true,
			ParkReason:    reasonPtr(ParkReasonUnknown),
		}, provider)); ok {
			fired++
		}
		src.lastEvalAt = time.Time{}
	}
	if fired != 3 {
		t.Errorf("fired = %d, want 3", fired)
	}
	if got := src.nudges["parked_open_tasks"]; got != 3 {
		t.Errorf("nudges[parked_open_tasks] = %d, want 3", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_capped_park_does_not_rescan_every_tick
func TestIdleNudgeCappedParkDoesNotRescanEveryTick(t *testing.T) {
	calls := 0
	provider := func() []IdleTask {
		calls++
		return []IdleTask{{ID: "1", Subject: "task"}}
	}
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             1,
		AwaitingBudgetSeconds: 120.0,
	})
	tick := func() bool {
		_, ok := src.RenderReminder(idleTickCtxWithTasks(&LoopSignals{
			IdleSeconds:   999.0,
			AwaitingInput: true,
			ParkReason:    reasonPtr(ParkReasonUnknown),
		}, provider))
		return ok
	}

	if !tick() {
		t.Fatal("first tick should fire")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	src.lastEvalAt = time.Time{}
	for i := 0; i < 10; i++ {
		if tick() {
			t.Fatal("capped park should not fire again")
		}
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_parked_no_question_fires_only_once
func TestIdleNudgeParkedNoQuestionFiresOnlyOnce(t *testing.T) {
	provider := func() []IdleTask { return nil }
	src := NewIdleNudgeSource(IdleNudgeConfig{
		BudgetSeconds:         120.0,
		MaxNudges:             3,
		AwaitingBudgetSeconds: 5.0,
	})
	fired := 0
	for i := 0; i < 5; i++ {
		if _, ok := src.RenderReminder(idleTickCtxWithTasks(&LoopSignals{
			IdleSeconds:   99.0,
			AwaitingInput: true,
			ParkReason:    reasonPtr(ParkReasonUnknown),
		}, provider)); ok {
			fired++
		}
		src.lastEvalAt = time.Time{}
	}
	if fired != 1 {
		t.Errorf("fired = %d, want 1", fired)
	}
	if got := src.nudges["parked_no_question"]; got != 1 {
		t.Errorf("nudges[parked_no_question] = %d, want 1", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_reset_rearms_caps
func TestIdleNudgeResetRearmsCaps(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 5.0, MaxNudges: 2})
	for i := 0; i < 3; i++ {
		_, _ = src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 99.0}))
		src.lastEvalAt = time.Time{}
	}
	if got := src.nudges["between_turns"]; got != 2 {
		t.Fatalf("nudges[between_turns] = %d, want 2", got)
	}
	src.Reset()
	if len(src.nudges) != 0 {
		t.Fatalf("Reset left nudges: %v", src.nudges)
	}
	out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 99.0}))
	if !ok || out == nil {
		t.Fatal("expected a reminder after reset")
	}
	if got := out.Meta["nudge_count"]; got != 1 {
		t.Errorf("nudge_count = %v, want 1", got)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_disabled_when_budget_zero
func TestIdleNudgeDisabledWhenBudgetZero(t *testing.T) {
	src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 0.0, MaxNudges: 3})
	if interval, ok := src.TickInterval(); ok || interval != 0 {
		t.Errorf("TickInterval() = (%v, %v), want (0, false)", interval, ok)
	}
	if out, ok := src.RenderReminder(idleTickCtx(&LoopSignals{IdleSeconds: 999.0})); ok || out != nil {
		t.Fatalf("zero budget should disable the source, got %+v", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_tick_interval_scales_with_budget
func TestIdleNudgeTickIntervalScalesWithBudget(t *testing.T) {
	cases := []struct {
		budget float64
		want   float64
	}{
		{120.0, 5.0},
		{4.0, 2.0},
		{0.05, 0.025},
	}
	for _, tc := range cases {
		src := NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: tc.budget, MaxNudges: 3})
		got, ok := src.TickInterval()
		if !ok {
			t.Fatalf("budget %.2f: TickInterval reported disabled", tc.budget)
		}
		if got != tc.want {
			t.Errorf("budget %.2f: TickInterval = %v, want %v", tc.budget, got, tc.want)
		}
	}
}

// Parity: core/tests/test_idle_nudge.py::test_hub_ticker_parks_idle_nudge
func TestIdleNudgeHubTickerParksIdleNudge(t *testing.T) {
	hub := NewReminderHub()
	hub.Register(NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 0.05, MaxNudges: 2}))
	var mu sync.Mutex
	woken := 0

	hub.Start(&ReminderAgentContext{},
		func() *LoopSignals { return &LoopSignals{IdleSeconds: 10.0} },
		func() {
			mu.Lock()
			woken++
			mu.Unlock()
		},
	)
	time.Sleep(300 * time.Millisecond)
	hub.Stop()

	mu.Lock()
	got := woken
	mu.Unlock()
	if got == 0 {
		t.Fatal("ticker never parked a reminder / called wake")
	}
}

// Parity: core/tests/test_idle_nudge.py::test_hub_take_pending_hands_off_and_clears
func TestIdleNudgeHubTakePendingHandsOffAndClears(t *testing.T) {
	hub := NewReminderHub()
	hub.Register(NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 0.05, MaxNudges: 3}))

	hub.Start(&ReminderAgentContext{},
		func() *LoopSignals { return &LoopSignals{IdleSeconds: 10.0} },
		nil,
	)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hub.PendingLen() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	hub.stopTicker()

	first := hub.TakePending()
	if len(first) < 1 {
		t.Fatal("expected at least one parked reminder")
	}
	for _, reminder := range first {
		if reminder.Source != "idle_nudge" {
			t.Errorf("Source = %q, want idle_nudge", reminder.Source)
		}
	}
	if second := hub.TakePending(); len(second) != 0 {
		t.Fatalf("second drain = %v, want empty", second)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_hub_start_noop_without_temporal_sources
func TestIdleNudgeHubStartNoopWithoutTemporalSources(t *testing.T) {
	hub := NewReminderHub()
	hub.Start(&ReminderAgentContext{}, nil, nil)
	if hub.HasTicker() {
		t.Fatal("ticker started with no temporal sources")
	}
	hub.Stop()
}

// Parity: core/tests/test_idle_nudge.py::test_hub_ticker_skips_while_awaiting_input
func TestIdleNudgeHubTickerSkipsWhileAwaitingInput(t *testing.T) {
	hub := NewReminderHub()
	hub.Register(NewIdleNudgeSource(IdleNudgeConfig{BudgetSeconds: 0.05, MaxNudges: 3}))
	hub.Start(&ReminderAgentContext{},
		func() *LoopSignals { return &LoopSignals{IdleSeconds: 999.0, AwaitingInput: true} },
		nil,
	)
	time.Sleep(300 * time.Millisecond)
	pending := hub.TakePending()
	hub.Stop()
	if len(pending) != 0 {
		t.Fatalf("awaiting-input ticker parked %d reminders, want none", len(pending))
	}
}

// Parity: core/tests/test_idle_nudge.py::test_hub_post_then_take_pending_round_trip
func TestIdleNudgeHubPostThenTakePendingRoundTrip(t *testing.T) {
	hub := NewReminderHub()
	r := Reminder{Source: "tool_budget", Body: "deferred 3 calls"}
	hub.Post(r)
	out := hub.TakePending()
	if len(out) != 1 || out[0].Source != r.Source || out[0].Body != r.Body {
		t.Fatalf("TakePending() = %v, want [%v]", out, r)
	}
	if second := hub.TakePending(); len(second) != 0 {
		t.Fatalf("second drain = %v, want empty", second)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_hub_post_coexists_with_ticker_buffer
func TestIdleNudgeHubPostCoexistsWithTickerBuffer(t *testing.T) {
	hub := NewReminderHub()
	hub.Post(Reminder{Source: "tool_budget", Body: "a"})
	hub.Post(Reminder{Source: "tool_budget", Body: "b"})
	out := hub.TakePending()
	if len(out) != 2 || out[0].Body != "a" || out[1].Body != "b" {
		t.Fatalf("bodies = %v, want [a b]", out)
	}
}

// Parity: core/tests/test_idle_nudge.py::test_default_config_values
func TestIdleNudgeDefaultConfigValues(t *testing.T) {
	cfg := DefaultLoopConfig()
	if cfg.SessionIdleNudgeSeconds != 120.0 {
		t.Errorf("SessionIdleNudgeSeconds = %v, want 120", cfg.SessionIdleNudgeSeconds)
	}
	if cfg.SessionIdleNudgeMaxPerSession != 3 {
		t.Errorf("SessionIdleNudgeMaxPerSession = %v, want 3", cfg.SessionIdleNudgeMaxPerSession)
	}
	if cfg.SessionIdleNudgeAwaitingSeconds != 120.0 {
		t.Errorf("SessionIdleNudgeAwaitingSeconds = %v, want 120", cfg.SessionIdleNudgeAwaitingSeconds)
	}
	if cfg.SessionIdleNudgeBrokenSeconds != 30.0 {
		t.Errorf("SessionIdleNudgeBrokenSeconds = %v, want 30", cfg.SessionIdleNudgeBrokenSeconds)
	}
}
