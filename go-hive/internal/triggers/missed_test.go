package triggers

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func trigEntry(tid, ttype string, config map[string]any, enabled bool, lastFiredAt any) map[string]any {
	if config == nil {
		config = map[string]any{}
	}
	return map[string]any{
		"id":             tid,
		"name":           tid,
		"trigger_type":   ttype,
		"trigger_config": config,
		"task":           "do it",
		"enabled":        enabled,
		"last_fired_at":  lastFiredAt,
		"next_due_at":    nil,
	}
}

func strPtr(s string) *string { return &s }

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

func requireNextDue(t *testing.T, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("next_due_at = nil, want %q", want)
	}
	if *got != want {
		t.Fatalf("next_due_at = %q, want %q", *got, want)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_cron_counts_ticks_between_last_fired_and_now
func TestComputeMissedCronCountsTicksBetweenLastFiredAndNow(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"cron": "0 9 * * 1-5"}, true, "2026-05-18T09:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 10, 0)
	missed := ComputeMissed(triggers, now)
	if len(missed) != 1 {
		t.Fatalf("len(missed) = %d, want 1", len(missed))
	}
	if missed[0].Count != 3 {
		t.Fatalf("count = %d, want 3", missed[0].Count)
	}
	wantTicks := []string{
		"2026-05-19T09:00:00+00:00",
		"2026-05-20T09:00:00+00:00",
		"2026-05-21T09:00:00+00:00",
	}
	if !slices.Equal(missed[0].Ticks, wantTicks) {
		t.Fatalf("ticks = %v, want %v", missed[0].Ticks, wantTicks)
	}
	requireNextDue(t, missed[0].NextDueAt, "2026-05-22T09:00:00+00:00")
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_cron_zero_when_no_full_tick_elapsed
func TestComputeMissedCronZeroWhenNoFullTickElapsed(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"cron": "0 9 * * *"}, true, "2026-05-21T09:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 15, 0)
	missed := ComputeMissed(triggers, now)
	if len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_interval_floors_division
func TestComputeMissedIntervalFloorsDivision(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"interval_minutes": 60}, true, "2026-05-21T09:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 12, 25)
	missed := ComputeMissed(triggers, now)
	if len(missed) != 1 {
		t.Fatalf("len(missed) = %d, want 1", len(missed))
	}
	if missed[0].Count != 3 {
		t.Fatalf("count = %d, want 3", missed[0].Count)
	}
	requireNextDue(t, missed[0].NextDueAt, "2026-05-21T13:00:00+00:00")
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_interval_zero_when_under_one_step
func TestComputeMissedIntervalZeroWhenUnderOneStep(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"interval_minutes": 60}, true, "2026-05-21T09:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 9, 30)
	if missed := ComputeMissed(triggers, now); len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_skips_disabled_triggers
func TestComputeMissedSkipsDisabledTriggers(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"interval_minutes": 5}, false, "2026-05-01T00:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 0, 0)
	if missed := ComputeMissed(triggers, now); len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_skips_webhook_triggers
func TestComputeMissedSkipsWebhookTriggers(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "webhook", map[string]any{"path": "/hooks/x"}, true, "2026-05-01T00:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 0, 0)
	if missed := ComputeMissed(triggers, now); len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_skips_never_fired_triggers
func TestComputeMissedSkipsNeverFiredTriggers(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"interval_minutes": 5}, true, nil),
	}
	now := utc(2026, time.May, 21, 0, 0)
	if missed := ComputeMissed(triggers, now); len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_handles_invalid_cron_gracefully
func TestComputeMissedHandlesInvalidCronGracefully(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"cron": "not-a-cron"}, true, "2026-05-01T00:00:00+00:00"),
	}
	now := utc(2026, time.May, 21, 0, 0)
	if missed := ComputeMissed(triggers, now); len(missed) != 0 {
		t.Fatalf("missed = %+v, want empty", missed)
	}
}

// Parity: tests/test_missed_triggers.py::test_compute_missed_caps_reported_ticks_at_100
func TestComputeMissedCapsReportedTicksAt100(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"cron": "* * * * *"}, true, "2026-05-01T00:00:00+00:00"),
	}
	now := utc(2026, time.May, 11, 0, 0)
	missed := ComputeMissed(triggers, now)
	if len(missed) != 1 {
		t.Fatalf("len(missed) = %d, want 1", len(missed))
	}
	if missed[0].Count != 14400 {
		t.Fatalf("count = %d, want 14400", missed[0].Count)
	}
	if len(missed[0].Ticks) != 100 {
		t.Fatalf("len(ticks) = %d, want 100 (capped)", len(missed[0].Ticks))
	}
}

// Parity: tests/test_missed_triggers.py::test_missed_trigger_to_dict_shape
func TestMissedTriggerToDictShape(t *testing.T) {
	m := MissedTrigger{
		TriggerID:   "x",
		TriggerType: "timer",
		Count:       2,
		Ticks:       []string{"a", "b"},
		NextDueAt:   strPtr("c"),
	}
	want := map[string]any{
		"trigger_id":   "x",
		"trigger_type": "timer",
		"count":        2,
		"ticks":        []string{"a", "b"},
		"next_due_at":  strPtr("c"),
	}
	if got := m.ToDict(); !reflect.DeepEqual(got, want) {
		t.Fatalf("to_dict = %#v, want %#v", got, want)
	}
}

// Parity-exempt: the Go cron parser's supported-syntax matrix has no direct
func TestParseCronSupportedSyntax(t *testing.T) {
	base := utc(2026, time.May, 18, 9, 0)
	cases := []struct {
		expr  string
		match bool
	}{
		{"* * * * *", true},
		{"0 9 * * 1-5", true},
		{"0 9 * * 1", true},
		{"0 9 * * 0", false},
		{"0 9 * * 7", false},
		{"0 9 18 * *", true},
		{"0 9 */2 * *", false},
		{"*/15 * * * *", true},
		{"0 9 * * 1,3,5", true},
		{"0 9 * * 1-5/2", true},
		{"0 9 * * 1-5", true},
		{"0 9 * 5 *", true},
		// croniter extensions accepted by the parser (base is Monday,
		// 2026-05-18 09:00:00).
		{"5-1 * * * *", true},
		{"0 9 * * MON", true},
		{"0 9 ? * ?", true},
		{"0 9 * JAN-MAY *", true},
		{"0 9 * * 1#3", true},
		{"0 9 * * MON#3", true},
		{"0 9 * * 1#2", false},
		{"0 9 * * L5", false},
		{"0 9 L * *", false},
		{"0 9 * * 7", false},
		{"@daily", false},
	}
	for _, tc := range cases {
		sched, ok := parseCron(tc.expr)
		if !ok {
			t.Fatalf("parseCron(%q) failed", tc.expr)
		}
		if got := sched.matches(base); got != tc.match {
			t.Errorf("matches(%q, %s) = %v, want %v", tc.expr, base, got, tc.match)
		}
	}
	for _, bad := range []string{
		"", "* * * *", "not-a-cron", "60 * * * *", "* 24 * * *", "* * 0 * *",
		"* * * 13 *", "* * * * 8", "*/0 * * * *",
		// croniter 6.0.0 also rejects these: Quartz W, L offsets, bare DOW L,
		// out-of-range #n, numeric range with #n and spaced macros.
		"0 0 15W * *", "0 0 L-3 * *", "0 0 * * L", "0 0 * * 1#6",
		"0 0 * * 1-5#2", " @daily",
	} {
		if _, ok := parseCron(bad); ok {
			t.Errorf("parseCron(%q) = valid, want invalid", bad)
		}
	}
}

// Parity: core/tests/test_missed_triggers.py::test_compute_missed_cron_zero_when_no_full_tick_elapsed
func TestCronNextDueStrictlyAfterNow(t *testing.T) {
	since := utc(2026, time.May, 18, 9, 0)
	now := utc(2026, time.May, 18, 9, 0)
	count, ticks, next := missedForCron("0 9 * * *", since, now)
	if count != 0 || len(ticks) != 0 {
		t.Fatalf("count=%d ticks=%v, want none", count, ticks)
	}
	requireNextDue(t, next, "2026-05-19T09:00:00+00:00")
}

// Parity: croniter has no look-back horizon — a trigger dormant for more than
// five years still reports every missed tick (the old Go cap undercounted).
func TestComputeMissedCronCountsBeyondFiveYears(t *testing.T) {
	triggers := []map[string]any{
		trigEntry("t", "timer", map[string]any{"cron": "0 0 1 1 *"}, true, "2018-01-01T00:00:00+00:00"),
	}
	now := utc(2026, time.May, 1, 0, 0)
	missed := ComputeMissed(triggers, now)
	if len(missed) != 1 {
		t.Fatalf("len(missed) = %d, want 1", len(missed))
	}
	if missed[0].Count != 8 {
		t.Fatalf("count = %d, want 8 (2019..2026 Jan 1, not capped at 5 years)", missed[0].Count)
	}
	if len(missed[0].Ticks) != 8 || missed[0].Ticks[0] != "2019-01-01T00:00:00+00:00" {
		t.Fatalf("ticks = %v", missed[0].Ticks)
	}
	requireNextDue(t, missed[0].NextDueAt, "2027-01-01T00:00:00+00:00")
}

// Parity: croniter keeps searching past five years. Feb 29 skips the
// non-leap century year (2096 -> 2104 is an eight-year gap).
func TestCronNextMatchAcrossLeapCenturyGap(t *testing.T) {
	next, ok := NextCronMatch("0 0 29 2 *", utc(2097, time.March, 1, 0, 0))
	if !ok {
		t.Fatal("NextCronMatch returned no match; the search must outlive the old 5-year horizon")
	}
	if got := FormatISO(next); got != "2104-02-29T00:00:00+00:00" {
		t.Fatalf("next = %s, want 2104-02-29", got)
	}

	count, _, nextDue := missedForCron("0 0 29 2 *", utc(2097, time.March, 1, 0, 0), utc(2104, time.March, 1, 0, 0))
	if count != 1 {
		t.Fatalf("count = %d, want 1 (2104-02-29)", count)
	}
	requireNextDue(t, nextDue, "2108-02-29T00:00:00+00:00")
}

// Parity: a syntactically valid but never-matching schedule (Feb 30) reports
// no next tick instead of hanging; croniter's full-cycle search has no match.
func TestCronNextMatchNeverMatchingScheduleTerminates(t *testing.T) {
	if next, ok := NextCronMatch("0 0 30 2 *", utc(2026, time.January, 1, 0, 0)); ok {
		t.Fatalf("next = %s, want no match for Feb 30", FormatISO(next))
	}
}

type fakeViewSession struct {
	available []TriggerDefinition
	active    map[string]struct{}
	nextFire  map[string]float64
	fireStats map[string]TriggerFireStat
	mono      float64
	wall      float64
}

func (f *fakeViewSession) AvailableTriggers() []TriggerDefinition { return f.available }
func (f *fakeViewSession) ActiveTriggerIDs() map[string]struct{}  { return f.active }
func (f *fakeViewSession) TriggerNextFire() map[string]float64    { return f.nextFire }
func (f *fakeViewSession) TriggerFireStats() map[string]TriggerFireStat {
	return f.fireStats
}
func (f *fakeViewSession) NowMonotonic() float64 { return f.mono }
func (f *fakeViewSession) NowWall() float64      { return f.wall }

// Parity: core/tests/test_sse_trigger_rehydration.py::test_view_lists_every_trigger_with_enabled_status,
func TestBuildTriggerViewProjection(t *testing.T) {
	lastFired := "2026-05-21T09:00:00+00:00"
	session := &fakeViewSession{
		available: []TriggerDefinition{
			{
				ID:            "t1",
				TriggerType:   "timer",
				TriggerConfig: map[string]any{"interval_minutes": 60},
				Task:          "do it",
				LastFiredAt:   &lastFired,
			},
			{ID: "t2", TriggerType: "webhook", Description: "Hooks", TriggerConfig: map[string]any{}},
		},
		active:   map[string]struct{}{"t1": {}},
		nextFire: map[string]float64{"t1": 1050.0},
		fireStats: map[string]TriggerFireStat{
			"t1": {FireCount: 4, LastFiredAt: int64Ptr(1700000000000)},
		},
		mono: 1000.0,
		wall: 1700000000.0,
	}
	view := BuildTriggerView(session)
	if len(view) != 2 {
		t.Fatalf("len(view) = %d, want 2", len(view))
	}
	v1 := view[0]
	if v1.TriggerID != "t1" || !v1.Enabled || v1.Name != "t1" || v1.Task != "do it" {
		t.Fatalf("unexpected first view: %+v", v1)
	}
	if v1.TriggerConfig["next_fire_in"] != 50.0 {
		t.Fatalf("next_fire_in = %v, want 50", v1.TriggerConfig["next_fire_in"])
	}
	if v1.TriggerConfig["next_fire_at"] != int64(1700000050000) {
		t.Fatalf("next_fire_at = %v, want 1700000050000", v1.TriggerConfig["next_fire_at"])
	}
	if v1.TriggerConfig["fire_count"] != 4 {
		t.Fatalf("fire_count = %v, want 4", v1.TriggerConfig["fire_count"])
	}
	if v1.TriggerConfig["last_fired_at"] != int64(1700000000000) {
		t.Fatalf("stats last_fired_at = %v", v1.TriggerConfig["last_fired_at"])
	}
	v2 := view[1]
	if v2.Name != "Hooks" || v2.Enabled {
		t.Fatalf("unexpected second view: %+v", v2)
	}
	if _, ok := v2.TriggerConfig["next_fire_in"]; ok {
		t.Fatalf("t2 should have no countdown: %+v", v2.TriggerConfig)
	}
}

// Parity: core/tests/test_sse_trigger_rehydration.py::test_view_empty_when_no_triggers
func TestBuildTriggerViewEmptySession(t *testing.T) {
	if view := BuildTriggerView(&fakeViewSession{}); len(view) != 0 {
		t.Fatalf("view = %+v, want empty", view)
	}
}

func int64Ptr(v int64) *int64 { return &v }
