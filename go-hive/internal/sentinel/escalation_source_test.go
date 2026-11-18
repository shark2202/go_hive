package sentinel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
)

type sourceOptions struct {
	verdict         string
	errored         bool
	openTasks       []string
	goal            *string
	ui              bool
	forbidClassify  bool
	runningWorkers  []map[string]any
	enabled         bool
	disabled        bool
	perColonyBudget *float64
}

type sourceHarness struct {
	source       *EscalationSource
	escalatedRef *[]map[string]any
	clock        *fakeClock
	store        *Store
}

func (h *sourceHarness) escalated() []map[string]any { return *h.escalatedRef }

func sourceGoal(value string) *string { return &value }

func newSourceHarness(t *testing.T, opts sourceOptions) *sourceHarness {
	t.Helper()
	store := newTestStore(t, map[string]any{
		"classify_after_seconds":     100.0,
		"max_nudges_before_escalate": 3,
		"escalate_when_ui_attached":  false,
	})
	config := map[string]any{
		"sentinel_enabled": !opts.disabled,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "1"},
		"allowlist":        []any{},
	}
	if opts.perColonyBudget != nil {
		config["classify_after_seconds"] = *opts.perColonyBudget
	}
	writeColonyConfig(t, store, "c1", config)

	clock := newFakeClock()
	escalated := []map[string]any{}
	tokens := fixedSecretTokens(t, store)

	provider := func() (ParkContext, error) {
		return ParkContext{
			ParkReason:        "turn_done",
			Goal:              opts.goal,
			OpenTasks:         opts.openTasks,
			LastAssistantText: "Done 200. Shall I continue?",
			RunningWorkers:    opts.runningWorkers,
		}, nil
	}
	classify := func(_ context.Context, _ ParkContext, _ llm.Completer) ClassifierVerdict {
		if opts.forbidClassify {
			t.Fatal("classifier must not be called on this path")
		}
		verdict := opts.verdict
		if verdict == "" {
			verdict = VerdictContinue
		}
		return ClassifierVerdict{Verdict: verdict, Errored: opts.errored}
	}
	source := NewEscalationSource(EscalationSourceOptions{
		Store:               store,
		Tokens:              tokens,
		ParkContextProvider: provider,
		OnEscalate: func(payload map[string]any) bool {
			escalated = append(escalated, payload)
			return true
		},
		HasAttachedUI: func(string) bool { return opts.ui },
		ClassifyFn:    classify,
		Now:           clock.Now,
	})
	return &sourceHarness{source: source, escalatedRef: &escalated, clock: clock, store: store}
}

func reminderContext(reason agentloop.ParkReason, idle float64, awaiting, userStopped bool, colony, session string, queen bool) agentloop.ReminderContext {
	parkReason := reason
	return agentloop.ReminderContext{
		Point: agentloop.IDLE_TICK,
		AgentCtx: &agentloop.ReminderAgentContext{
			IsQueenStream: queen,
			ColonyID:      colony,
			SessionID:     session,
		},
		Signals: &agentloop.LoopSignals{
			IdleSeconds:   idle,
			AwaitingInput: awaiting,
			ParkReason:    &parkReason,
			UserStopped:   userStopped,
		},
	}
}

func defaultReminderContext() agentloop.ReminderContext {
	return reminderContext(agentloop.ParkReasonTurnDone, 200.0, true, false, "c1", "s1", true)
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_applies_to_colony_queen
func TestAppliesToColonyQueen(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if !harness.source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: true, ColonyID: "c1"}) {
		t.Fatal("colony queen should be owned")
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_applies_to_skips_worker
func TestAppliesToSkipsWorker(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if harness.source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: false, ColonyID: "c1"}) {
		t.Fatal("worker stream should be skipped")
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_applies_to_skips_dm_queen
func TestAppliesToSkipsDMQueen(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if harness.source.AppliesTo(&agentloop.ReminderAgentContext{IsQueenStream: true}) {
		t.Fatal("DM queen (no colony) should be skipped")
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_nudge_returns_reminder
func TestNudgeReturnsReminder(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if !ok || reminder == nil {
		t.Fatal("continue verdict should nudge")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("nudge should not escalate: %#v", harness.escalated())
	}
	if harness.source.nudgeCount != 1 {
		t.Fatalf("nudge_count = %d, want 1", harness.source.nudgeCount)
	}
	if reminder.Source != "sentinel_escalation" {
		t.Fatalf("source = %q", reminder.Source)
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_nudge_surfaces_running_workers
func TestNudgeSurfacesRunningWorkers(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{
		verdict:   VerdictContinue,
		openTasks: []string{"t1"},
		goal:      sourceGoal("enrich 1000 leads"),
		runningWorkers: []map[string]any{{
			"worker_id":       "w1",
			"status":          "running",
			"task":            "scrape influencer A",
			"elapsed_seconds": 750,
		}},
	})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if !ok || reminder == nil {
		t.Fatal("expected a nudge")
	}
	for _, want := range []string{"w1", "still running", "12m", "WORKER_REPORT", "wait"} {
		if !strings.Contains(strings.ToLower(reminder.Body), strings.ToLower(want)) {
			t.Fatalf("nudge body missing %q: %s", want, reminder.Body)
		}
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_nudge_omits_worker_note_when_none
func TestNudgeOmitsWorkerNoteWhenNone(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if !ok || reminder == nil {
		t.Fatal("expected a nudge")
	}
	if strings.Contains(strings.ToLower(reminder.Body), "still running") {
		t.Fatalf("nudge should omit the worker note: %s", reminder.Body)
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_escalate_side_effect_and_none
func TestEscalateSideEffectAndNone(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("escalation should stay parked")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("escalations = %d, want 1", len(harness.escalated()))
	}
	payload := harness.escalated()[0]
	if payload["colony_id"] != "c1" || payload["session_id"] != "s1" {
		t.Fatalf("payload = %#v", payload)
	}
	if PyStr(payload["correlation_token"]) == "" {
		t.Fatal("payload must carry a correlation token")
	}
	if !strings.Contains(strings.ToLower(PyStr(payload["question_text"])), "continue") {
		t.Fatalf("question_text = %q", payload["question_text"])
	}
	if payload["kind"] != "blocker" {
		t.Fatalf("kind = %v, want blocker", payload["kind"])
	}
	if !harness.source.held {
		t.Fatal("a blocker parks the source until resume")
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_escalate_suppressed_when_ui_attached
func TestEscalateSuppressedWhenUIAttached(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads"), ui: true})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("suppressed report should stay parked")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_per_colony_budget_overrides_global
func TestPerColonyBudgetOverridesGlobal(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{
		verdict:         VerdictNeedsHuman,
		openTasks:       []string{"t1"},
		goal:            sourceGoal("enrich 1000 leads"),
		perColonyBudget: floatPtr(500.0),
	})
	reminder, ok := harness.source.RenderReminder(reminderContext(agentloop.ParkReasonTurnDone, 200.0, true, false, "c1", "s1", true))
	if ok || reminder != nil {
		t.Fatal("under the per-colony budget sentinel should wait")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
	if harness.source.nudgeCount != 0 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_broken_park_escalates_without_classifier
func TestBrokenParkEscalatesWithoutClassifier(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{forbidClassify: true, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(reminderContext(agentloop.ParkReasonLLMError, 200.0, true, false, "c1", "s1", true))
	if ok || reminder != nil {
		t.Fatal("broken park should escalate, not nudge")
	}
	if len(harness.escalated()) != 1 || harness.escalated()[0]["kind"] != "blocker" {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_nudge_budget_escalates_as_heartbeat
func TestNudgeBudgetEscalatesAsHeartbeat(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{forbidClassify: true, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	harness.source.nudgeCount = 3 // max_nudges_before_escalate == 3
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("spent nudge budget should not nudge")
	}
	if len(harness.escalated()) != 1 || harness.escalated()[0]["kind"] != "heartbeat" {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_no_goal_and_no_tasks_does_nothing
func TestNoGoalAndNoTasksDoesNothing(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{}, goal: nil})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("nothing to act on should stay parked")
	}
	if len(harness.escalated()) != 0 || harness.source.nudgeCount != 0 {
		t.Fatalf("escalated=%#v nudge=%d", harness.escalated(), harness.source.nudgeCount)
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_goal_without_open_tasks_still_acts
func TestGoalWithoutOpenTasksStillActs(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("escalate path stays parked")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_classifier_error_does_not_nudge
func TestClassifierErrorDoesNotNudge(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, errored: true, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("errored classifier must not nudge")
	}
	if len(harness.escalated()) != 0 || harness.source.nudgeCount != 0 || harness.source.held {
		t.Fatalf("escalated=%#v nudge=%d held=%t", harness.escalated(), harness.source.nudgeCount, harness.source.held)
	}
	// Still errored next window → still a no-op, never a stuck escalation.
	harness.clock.Advance(200 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("second errored window must stay a no-op")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_loop_breaker_stops_nudging_done_queen
func TestLoopBreakerStopsNudgingDoneQueen(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("first nudge should be allowed")
	}
	if harness.source.nudgeCount != 1 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
	harness.clock.Advance(200 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("loop breaker should suppress the second nudge")
	}
	if len(harness.escalated()) != 1 || harness.escalated()[0]["kind"] != "heartbeat" {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
	if !harness.source.held {
		t.Fatal("heartbeat should hold the source")
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_loop_breaker_does_not_fire_with_open_tasks
func TestLoopBreakerDoesNotFireWithOpenTasks(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("first nudge expected")
	}
	harness.clock.Advance(200 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("with open tasks repeated nudges remain valid")
	}
	if harness.source.nudgeCount != 2 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_done_reports_completion
func TestDoneReportsCompletion(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictDone, openTasks: []string{}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("done should not nudge")
	}
	if len(harness.escalated()) != 1 || harness.escalated()[0]["kind"] != "done" {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_disabled_per_colony
func TestDisabledPerColony(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads"), disabled: true})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("disabled colony should stay parked")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_under_budget_does_nothing
func TestUnderBudgetDoesNothing(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(reminderContext(agentloop.ParkReasonTurnDone, 10.0, true, false, "c1", "s1", true)); ok || reminder != nil {
		t.Fatal("idle 10s is under the 100s budget")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_sacred_parks_skipped
func TestSacredParksSkipped(t *testing.T) {
	reasons := []agentloop.ParkReason{
		agentloop.ParkReasonUserStopped,
		agentloop.ParkReasonColonySuggestion,
		agentloop.ParkReasonAwaitingQueen,
	}
	for _, reason := range reasons {
		harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
		if reminder, ok := harness.source.RenderReminder(reminderContext(reason, 200.0, true, false, "c1", "s1", true)); ok || reminder != nil {
			t.Fatalf("reason %s should be skipped", reason)
		}
		if len(harness.escalated()) != 0 {
			t.Fatalf("reason %s escalated: %#v", reason, harness.escalated())
		}
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_user_stopped_flag_skipped
func TestUserStoppedFlagSkipped(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(reminderContext(agentloop.ParkReasonTurnDone, 200.0, true, true, "c1", "s1", true)); ok || reminder != nil {
		t.Fatal("user_stopped should skip")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_escalate_dedups_until_reset
func TestEscalateDedupsUntilReset(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictNeedsHuman, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("expected escalation")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("escalations = %d", len(harness.escalated()))
	}
	harness.clock.Advance(1000 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("held source should not re-report")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("escalations after window = %d", len(harness.escalated()))
	}
	harness.source.Reset()
	harness.clock.Advance(1000 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("expected escalation after reset")
	}
	if len(harness.escalated()) != 2 {
		t.Fatalf("escalations after reset = %d", len(harness.escalated()))
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_rate_limited_within_window
func TestRateLimitedWithinWindow(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("first nudge expected")
	}
	if harness.source.nudgeCount != 1 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
	harness.clock.Advance(50 * time.Second) // within the 100s window
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); ok || reminder != nil {
		t.Fatal("inside the window sentinel must not re-evaluate")
	}
	if harness.source.nudgeCount != 1 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
	harness.clock.Advance(100 * time.Second) // past the window
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("past the window a nudge is due")
	}
	if harness.source.nudgeCount != 2 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
}

// Parity: core/framework/sentinel/escalation_source.py, core/tests/test_sentinel_escalation_source.py::test_escalates_after_max_nudges
func TestEscalatesAfterMaxNudges(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	for index := 0; index < 3; index++ {
		if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
			t.Fatalf("nudge %d expected", index+1)
		}
		harness.clock.Advance(200 * time.Second)
	}
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if ok || reminder != nil {
		t.Fatal("after the nudge budget sentinel should escalate, not nudge")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("escalations = %#v", harness.escalated())
	}
}

// Parity: core/framework/sentinel/escalation_source.py::EscalationSource
func TestTickIntervalClampedToFiveToSixtySeconds(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{openTasks: []string{"t1"}, goal: sourceGoal("g")})
	// store budget is 100s → 50s.
	if interval, ok := harness.source.TickInterval(); !ok || interval != 50 {
		t.Fatalf("interval = %v, ok=%t", interval, ok)
	}
	harness.store.GlobalConfig = func() map[string]any {
		return map[string]any{"sentinel": map[string]any{"classify_after_seconds": 1.0}}
	}
	if interval, _ := harness.source.TickInterval(); interval != 5 {
		t.Fatalf("tiny budget interval = %v, want floor 5", interval)
	}
	harness.store.GlobalConfig = func() map[string]any {
		return map[string]any{"sentinel": map[string]any{"classify_after_seconds": 1000.0}}
	}
	if interval, _ := harness.source.TickInterval(); interval != 60 {
		t.Fatalf("huge budget interval = %v, want cap 60", interval)
	}
}

// Parity-exempt: escalation_source.py::EscalationSource._report has no Python
// test for the channel split; progress is Inbox-feed-only, yet it still nudges.
func TestProgressReportIsHiveOnlyButStillNudges(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	reminder, ok := harness.source.RenderReminder(defaultReminderContext())
	if !ok || reminder == nil {
		t.Fatal("progress should nudge")
	}
	if len(harness.escalated()) != 0 {
		t.Fatalf("progress must not go to telegram: %#v", harness.escalated())
	}

	hive := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	writeColonyConfig(t, hive.store, "c1", map[string]any{
		"sentinel_enabled": true,
		"channel":          "hive",
		"target":           map[string]any{},
		"allowlist":        []any{},
	})
	if reminder, ok := hive.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("hive progress should also nudge")
	}
	if len(hive.escalated()) != 1 || hive.escalated()[0]["kind"] != "progress" {
		t.Fatalf("hive escalations = %#v", hive.escalated())
	}
}

// Parity-exempt: escalation_source.py::EscalationSource._report dedup has no
// Python test for identical consecutive progress reports.
func TestIdenticalConsecutiveReportsCollapse(t *testing.T) {
	harness := newSourceHarness(t, sourceOptions{verdict: VerdictContinue, openTasks: []string{"t1"}, goal: sourceGoal("enrich 1000 leads")})
	writeColonyConfig(t, harness.store, "c1", map[string]any{
		"sentinel_enabled": true,
		"channel":          "hive",
		"target":           map[string]any{},
		"allowlist":        []any{},
	})
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("first progress nudge expected")
	}
	harness.clock.Advance(200 * time.Second)
	if reminder, ok := harness.source.RenderReminder(defaultReminderContext()); !ok || reminder == nil {
		t.Fatal("second progress nudge expected")
	}
	if len(harness.escalated()) != 1 {
		t.Fatalf("identical reports should collapse: %#v", harness.escalated())
	}
	if harness.source.nudgeCount != 2 {
		t.Fatalf("nudge_count = %d", harness.source.nudgeCount)
	}
}
