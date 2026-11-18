package playbook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeColony struct {
	mu     sync.Mutex
	rows   map[string]bool
	order  []string
	failOn map[string]bool
	always map[string]bool
	badRec map[string]bool

	attempts map[string]int
	profiles map[string]string
	schemas  map[string]map[string]any

	trackConcurrency bool
	dispatchDelay    time.Duration
	inflight         int
	maxInflight      int
}

func newFakeColony(slugs ...string) *fakeColony {
	c := &fakeColony{
		rows:          map[string]bool{},
		order:         slugs,
		failOn:        map[string]bool{},
		always:        map[string]bool{},
		badRec:        map[string]bool{},
		attempts:      map[string]int{},
		profiles:      map[string]string{},
		schemas:       map[string]map[string]any{},
		dispatchDelay: 10 * time.Millisecond,
	}
	for _, slug := range slugs {
		c.rows[slug] = false
	}
	return c
}

func (c *fakeColony) queryRows(_ string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]map[string]any, 0, len(c.order))
	for _, slug := range c.order {
		if !c.rows[slug] {
			out = append(out, map[string]any{"slug": slug})
		}
	}
	return out
}

func (c *fakeColony) dispatchOne(_ context.Context, _ string, opts DispatchOptions) (map[string]any, error) {
	slug := ""
	if opts.Data != nil {
		slug = fmt.Sprint(opts.Data["slug"])
	}

	c.mu.Lock()
	c.attempts[slug]++
	attempt := c.attempts[slug]
	c.profiles[slug] = opts.Profile
	c.schemas[slug] = opts.Schema
	track := c.trackConcurrency
	if track {
		c.inflight++
		if c.inflight > c.maxInflight {
			c.maxInflight = c.inflight
		}
	}
	c.mu.Unlock()

	if track {
		time.Sleep(c.dispatchDelay)
	}

	var out map[string]any
	switch {
	case c.always[slug]:
		out = map[string]any{"status": "failed", "summary": "boom", "data": map[string]any{}, "error": "permanent"}
	case c.failOn[slug] && attempt == 1:
		out = map[string]any{"status": "failed", "summary": "boom", "data": map[string]any{}, "error": "transient"}
	case c.badRec[slug]:
		out = map[string]any{"status": "success", "summary": "ok", "data": map[string]any{"oops": true}}
	default:
		c.mu.Lock()
		c.rows[slug] = true
		c.mu.Unlock()
		out = map[string]any{"status": "success", "summary": "ok", "data": map[string]any{"slug": slug, "status": "enriched"}}
	}

	if track {
		c.mu.Lock()
		c.inflight--
		c.mu.Unlock()
	}
	return out, nil
}

func (c *fakeColony) attemptsFor(slug string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempts[slug]
}

func (c *fakeColony) done(slug string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows[slug]
}

func (c *fakeColony) profileFor(slug string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profiles[slug]
}

func (c *fakeColony) schemaFor(slug string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schemas[slug]
}

func (c *fakeColony) peakConcurrency() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInflight
}

type scriptOptions struct {
	maxRounds       int
	retries         int
	chunk           *int
	lane            bool
	laneConcurrency int
	ratePerMin      *int
}

func intPtr(v int) *int { return &v }

func defaultScriptOptions() scriptOptions {
	return scriptOptions{
		maxRounds:       4,
		retries:         2,
		chunk:           intPtr(2),
		lane:            true,
		laneConcurrency: 2,
	}
}

func receiptSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"slug", "status"},
		"properties": map[string]any{
			"slug":   map[string]any{"type": "string"},
			"status": map[string]any{"enum": []any{"enriched", "no-data"}},
		},
	}
}

func makeScript(opts scriptOptions) *Script {
	return &Script{
		Meta: map[string]any{
			"name":        "test-enrich",
			"description": "converge the leads table",
			"phases":      []any{"Enrich"},
		},
		Run: func(ctx context.Context, _ any, h *Hooks) (any, error) {
			if opts.lane {
				h.Lane("acct-1", LaneOptions{Concurrency: opts.laneConcurrency, RatePerMin: opts.ratePerMin})
			}
			h.Converge(ctx, ConvergeOptions{
				Pending: func() []map[string]any {
					return h.TrackerQuery("SELECT slug FROM leads WHERE done=0")
				},
				Dispatch: func(ctx context.Context, row map[string]any, _ int) (any, error) {
					slug := fmt.Sprint(row["slug"])
					wopts := WorkerOptions{
						Data:    map[string]any{"slug": slug},
						Retries: opts.retries,
						Schema:  receiptSchema(),
					}
					if opts.lane {
						wopts.Lane = "acct-1"
					}
					return h.Worker(ctx, "Enrich "+slug, wopts)
				},
				MaxRounds: opts.maxRounds,
				Chunk:     opts.chunk,
			})
			remaining := h.TrackerCount("SELECT slug FROM leads WHERE done=0")
			h.Log(fmt.Sprintf("remaining=%d", remaining))
			return map[string]any{"remaining": remaining, "dead": h.Deadletter.Size()}, nil
		},
	}
}

func runPlaybook(t *testing.T, colony *fakeColony, script *Script, concurrencyCap *int) map[string]any {
	t.Helper()
	out, err := RunPlaybookScript(context.Background(), script, RunOptions{
		DispatchOne:    colony.dispatchOne,
		QueryRows:      colony.queryRows,
		RunID:          "test",
		ConcurrencyCap: concurrencyCap,
	}, nil)
	if err != nil {
		t.Fatalf("run_playbook_script: %v", err)
	}
	return out
}

func resultMap(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("result is %T, want map[string]any", out["result"])
	}
	return res
}

func intResult(t *testing.T, res map[string]any, key string) int {
	t.Helper()
	v, ok := res[key].(int)
	if !ok {
		t.Fatalf("result[%q] is %T, want int", key, res[key])
	}
	return v
}

func logLines(out map[string]any) []string {
	logs, _ := out["logs"].([]string)
	return logs
}

func logsContain(out map[string]any, needle string) bool {
	for _, line := range logLines(out) {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_playbook_runner.py::test_converges_all_rows
func TestConvergesAllRows(t *testing.T) {
	colony := newFakeColony("a", "b", "c", "d", "e")
	out := runPlaybook(t, colony, makeScript(defaultScriptOptions()), nil)
	res := resultMap(t, out)

	if got := intResult(t, res, "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if got := intResult(t, res, "dead"); got != 0 {
		t.Fatalf("dead = %d, want 0", got)
	}
	for _, slug := range colony.order {
		if !colony.done(slug) {
			t.Errorf("row %q did not converge", slug)
		}
		if got := colony.attemptsFor(slug); got != 1 {
			t.Errorf("attempts[%q] = %d, want 1", slug, got)
		}
	}
}

// Parity: core/tests/test_playbook_runner.py::test_retry_then_succeed
func TestRetryThenSucceed(t *testing.T) {
	colony := newFakeColony("a", "b", "c")
	colony.failOn["b"] = true

	out := runPlaybook(t, colony, makeScript(defaultScriptOptions()), nil)
	res := resultMap(t, out)
	if got := intResult(t, res, "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if got := intResult(t, res, "dead"); got != 0 {
		t.Fatalf("dead = %d, want 0", got)
	}
	if got := colony.attemptsFor("b"); got != 2 {
		t.Fatalf("attempts[b] = %d, want 2", got)
	}
	if got := colony.attemptsFor("a"); got != 1 {
		t.Fatalf("attempts[a] = %d, want 1", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_permanent_failure_is_dead_lettered_once
func TestPermanentFailureDeadLetteredOnce(t *testing.T) {
	colony := newFakeColony("a", "b")
	colony.always["b"] = true
	opts := defaultScriptOptions()
	opts.maxRounds = 1

	out := runPlaybook(t, colony, makeScript(opts), nil)
	res := resultMap(t, out)

	if !colony.done("a") {
		t.Error("row a should have converged")
	}
	if colony.done("b") {
		t.Error("row b should still be pending")
	}
	if got := intResult(t, res, "remaining"); got != 1 {
		t.Fatalf("remaining = %d, want 1", got)
	}
	if got := intResult(t, res, "dead"); got != 1 {
		t.Fatalf("dead = %d, want 1", got)
	}
	dead, ok := out["deadletter"].([]map[string]any)
	if !ok || len(dead) != 1 {
		t.Fatalf("deadletter = %v, want one item", out["deadletter"])
	}
	row, _ := dead[0]["row"].(map[string]any)
	if row["slug"] != "b" {
		t.Fatalf("dead-lettered row = %v, want slug b", row)
	}
	if got := colony.attemptsFor("b"); got != 3 {
		t.Fatalf("attempts[b] = %d, want 3", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_tracker_is_truth_not_the_receipt
func TestTrackerIsTruthNotTheReceipt(t *testing.T) {
	colony := newFakeColony("a")
	colony.badRec["a"] = true
	opts := defaultScriptOptions()
	opts.maxRounds = 1

	out := runPlaybook(t, colony, makeScript(opts), nil)
	res := resultMap(t, out)

	if colony.done("a") {
		t.Error("row a should never have advanced")
	}
	if got := intResult(t, res, "remaining"); got != 1 {
		t.Fatalf("remaining = %d, want 1", got)
	}
	if got := intResult(t, res, "dead"); got != 1 {
		t.Fatalf("dead = %d, want 1", got)
	}
	if got := colony.attemptsFor("a"); got != 1 {
		t.Fatalf("attempts[a] = %d, want 1", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_schema_rides_the_spawn_spec
func TestSchemaRidesSpawnSpec(t *testing.T) {
	colony := newFakeColony("a", "b")
	opts := defaultScriptOptions()
	opts.maxRounds = 1

	runPlaybook(t, colony, makeScript(opts), nil)

	for _, slug := range []string{"a", "b"} {
		schema := colony.schemaFor(slug)
		if schema == nil {
			t.Fatalf("schema seen for %q is nil", slug)
		}
		required, _ := schema["required"].([]any)
		if len(required) != 2 || required[0] != "slug" || required[1] != "status" {
			t.Fatalf("schema required = %v, want [slug status]", schema["required"])
		}
	}
}

// Parity: core/tests/test_playbook_runner.py::test_lane_limits_concurrency
func TestLaneLimitsConcurrency(t *testing.T) {
	slugs := make([]string, 8)
	for i := range slugs {
		slugs[i] = fmt.Sprint(i)
	}
	colony := newFakeColony(slugs...)
	colony.trackConcurrency = true

	out := runPlaybook(t, colony, makeScript(defaultScriptOptions()), nil)
	if got := intResult(t, resultMap(t, out), "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if got := colony.peakConcurrency(); got > 2 {
		t.Fatalf("max in-flight = %d, want <= 2", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rate_per_min_spaces_dispatches
func TestRatePerMinSpacesDispatches(t *testing.T) {
	slugs := make([]string, 4)
	for i := range slugs {
		slugs[i] = fmt.Sprint(i)
	}
	colony := newFakeColony(slugs...)
	colony.trackConcurrency = true
	opts := defaultScriptOptions()
	opts.maxRounds = 1
	opts.laneConcurrency = 4
	opts.chunk = intPtr(4)
	opts.ratePerMin = intPtr(600)

	start := time.Now()
	out := runPlaybook(t, colony, makeScript(opts), nil)
	elapsed := time.Since(start)

	if got := intResult(t, resultMap(t, out), "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if elapsed < 290*time.Millisecond {
		t.Fatalf("expected >= 3*0.1s of spacing, got %s", elapsed)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rate_per_min_none_no_throttle
func TestRatePerMinNoneNoThrottle(t *testing.T) {
	slugs := make([]string, 8)
	for i := range slugs {
		slugs[i] = fmt.Sprint(i)
	}
	colony := newFakeColony(slugs...)
	colony.trackConcurrency = true
	opts := defaultScriptOptions()
	opts.maxRounds = 1
	opts.laneConcurrency = 8
	opts.chunk = intPtr(8)

	start := time.Now()
	out := runPlaybook(t, colony, makeScript(opts), nil)
	elapsed := time.Since(start)

	if got := intResult(t, resultMap(t, out), "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("no throttle expected, but took %s", elapsed)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rate_gate_composes_with_concurrency
func TestRateGateComposesWithConcurrency(t *testing.T) {
	slugs := make([]string, 4)
	for i := range slugs {
		slugs[i] = fmt.Sprint(i)
	}
	colony := newFakeColony(slugs...)
	colony.trackConcurrency = true
	colony.dispatchDelay = 200 * time.Millisecond
	opts := defaultScriptOptions()
	opts.maxRounds = 1
	opts.laneConcurrency = 2
	opts.chunk = intPtr(4)
	opts.ratePerMin = intPtr(600)

	out := runPlaybook(t, colony, makeScript(opts), nil)
	if got := intResult(t, resultMap(t, out), "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	peak := colony.peakConcurrency()
	if peak > 2 {
		t.Fatalf("max in-flight = %d, lane semaphore must cap at 2", peak)
	}
	if peak != 2 {
		t.Fatalf("max in-flight = %d, rows must actually overlap", peak)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_rerun_is_resume
func TestRerunIsResume(t *testing.T) {
	colony := newFakeColony("a", "b", "c")
	first := runPlaybook(t, colony, makeScript(defaultScriptOptions()), nil)
	if got := intResult(t, resultMap(t, first), "remaining"); got != 0 {
		t.Fatalf("first remaining = %d, want 0", got)
	}
	second := runPlaybook(t, colony, makeScript(defaultScriptOptions()), nil)
	dispatched, _ := second["dispatched"].(int)
	if dispatched != 0 {
		t.Fatalf("second run dispatched = %d, want 0", dispatched)
	}
	if got := intResult(t, resultMap(t, second), "remaining"); got != 0 {
		t.Fatalf("second remaining = %d, want 0", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_profile_rotates_to_dispatch
func TestProfileRotatesToDispatch(t *testing.T) {
	accounts := []string{"p1", "p2", "p3"}
	script := &Script{
		Meta: map[string]any{"name": "rotate", "description": "x"},
		Run: func(ctx context.Context, _ any, h *Hooks) (any, error) {
			h.Converge(ctx, ConvergeOptions{
				Pending: func() []map[string]any {
					return h.TrackerQuery("SELECT slug FROM leads WHERE done=0")
				},
				Dispatch: func(ctx context.Context, row map[string]any, idx int) (any, error) {
					slug := fmt.Sprint(row["slug"])
					return h.Worker(ctx, "do "+slug, WorkerOptions{
						Data:    map[string]any{"slug": slug},
						Profile: accounts[idx%len(accounts)],
					})
				},
				MaxRounds: 1,
			})
			return nil, nil
		},
	}
	colony := newFakeColony("a", "b", "c", "d")
	runPlaybook(t, colony, script, nil)

	want := map[string]string{"a": "p1", "b": "p2", "c": "p3", "d": "p1"}
	for slug, profile := range want {
		if got := colony.profileFor(slug); got != profile {
			t.Errorf("profile[%q] = %q, want %q", slug, got, profile)
		}
	}
}

// Parity: core/tests/test_playbook_runner.py::test_bad_script_missing_run
func TestBadScriptMissingRun(t *testing.T) {
	colony := newFakeColony("a")
	_, err := RunPlaybookScript(context.Background(), &Script{
		Meta: map[string]any{"name": "x"},
	}, RunOptions{DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "test"}, nil)
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	if !strings.Contains(err.Error(), "run(args)") {
		t.Fatalf("error %q does not name run(args)", err)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_bad_script_missing_meta
func TestBadScriptMissingMeta(t *testing.T) {
	colony := newFakeColony("a")
	_, err := RunPlaybookScript(context.Background(), &Script{
		Run: func(context.Context, any, *Hooks) (any, error) { return 1, nil },
	}, RunOptions{DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "test"}, nil)
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	if !strings.Contains(err.Error(), "meta") {
		t.Fatalf("error %q does not mention meta", err)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_load_is_synchronous_and_validates
func TestLoadIsSynchronousAndValidates(t *testing.T) {
	colony := newFakeColony()
	run := NewPlaybookRun(RunOptions{DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "t"})
	err := run.Load(&Script{Meta: map[string]any{"name": "x"}})
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	if !strings.Contains(err.Error(), "run(args)") {
		t.Fatalf("error %q does not name run(args)", err)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_playbook_can_use_stdlib_imports
func TestPlaybookCanUseStdlibImports(t *testing.T) {
	colony := newFakeColony()
	script := &Script{
		Meta: map[string]any{"name": "x"},
		Run: func(context.Context, any, *Hooks) (any, error) {
			encoded, err := json.Marshal(map[string]any{"a": 1})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"j":      string(encoded),
				"has_dt": !time.Now().IsZero(),
			}, nil
		},
	}
	out, err := RunPlaybookScript(context.Background(), script, RunOptions{
		DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "t",
	}, nil)
	if err != nil {
		t.Fatalf("run_playbook_script: %v", err)
	}
	res := resultMap(t, out)
	if got := res["j"]; got != `{"a":1}` {
		t.Fatalf("j = %v, want {\"a\":1}", got)
	}
	if got := res["has_dt"]; got != true {
		t.Fatalf("has_dt = %v, want true", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_run_loaded_surfaces_runtime_error
func TestRunLoadedSurfacesRuntimeError(t *testing.T) {
	colony := newFakeColony()
	run := NewPlaybookRun(RunOptions{DispatchOne: colony.dispatchOne, QueryRows: colony.queryRows, RunID: "t"})
	err := run.Load(&Script{
		Meta: map[string]any{"name": "x"},
		Run: func(context.Context, any, *Hooks) (any, error) {
			return nil, &ScriptRuntimeError{Kind: "NameError", Message: "name 'undefined_name' is not defined", Line: 3}
		},
	})
	if err != nil {
		t.Fatalf("load should succeed, got %v", err)
	}
	_, err = run.RunLoaded(context.Background(), nil)
	if err == nil {
		t.Fatal("expected PlaybookScriptError")
	}
	if !strings.Contains(err.Error(), "run() raised") {
		t.Fatalf("error %q does not say run() raised", err)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_worker_goal_passes_through_to_dispatch
func TestWorkerGoalPassesThroughToDispatch(t *testing.T) {
	var (
		mu       sync.Mutex
		captured []DispatchOptions
	)
	dispatch := func(_ context.Context, _ string, opts DispatchOptions) (map[string]any, error) {
		mu.Lock()
		captured = append(captured, opts)
		mu.Unlock()
		return map[string]any{"status": "success", "summary": "ok", "data": map[string]any{"done": true}}, nil
	}
	script := &Script{
		Meta: map[string]any{"name": "g"},
		Run: func(ctx context.Context, _ any, h *Hooks) (any, error) {
			goal := "Checking 6 profiles"
			a, err := h.Worker(ctx, "job A", WorkerOptions{Goal: &goal})
			if err != nil {
				return nil, err
			}
			b, err := h.Worker(ctx, "job B", WorkerOptions{})
			if err != nil {
				return nil, err
			}
			return map[string]any{"a": a.Status, "b": b.Status}, nil
		},
	}
	run := NewPlaybookRun(RunOptions{DispatchOne: dispatch, QueryRows: func(string) []map[string]any { return nil }, RunID: "t"})
	out, err := run.Execute(context.Background(), script, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := resultMap(t, out)
	if res["a"] != "done" || res["b"] != "done" {
		t.Fatalf("result = %v, want both done", res)
	}
	if len(captured) != 2 {
		t.Fatalf("captured %d dispatches, want 2", len(captured))
	}
	if captured[0].Goal == nil || *captured[0].Goal != "Checking 6 profiles" {
		t.Fatalf("first goal = %v, want Checking 6 profiles", captured[0].Goal)
	}
	if captured[1].Goal != nil {
		t.Fatalf("second goal = %v, want nil (omitted when unset)", *captured[1].Goal)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_chunk_defaults_to_cap
func TestChunkDefaultsToCap(t *testing.T) {
	slugs := make([]string, 8)
	for i := range slugs {
		slugs[i] = fmt.Sprint(i)
	}
	colony := newFakeColony(slugs...)
	colony.trackConcurrency = true
	opts := defaultScriptOptions()
	opts.chunk = nil
	opts.lane = false

	out := runPlaybook(t, colony, makeScript(opts), intPtr(2))
	if got := intResult(t, resultMap(t, out), "remaining"); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if got := colony.peakConcurrency(); got > 2 {
		t.Fatalf("max in-flight = %d, want <= 2 (chunk defaulted to cap)", got)
	}
}

// Parity: core/tests/test_playbook_runner.py::test_warns_when_chunk_exceeds_cap
func TestWarnsWhenChunkExceedsCap(t *testing.T) {
	colony := newFakeColony("a", "b")
	opts := defaultScriptOptions()
	opts.chunk = intPtr(50)
	opts.lane = false

	out := runPlaybook(t, colony, makeScript(opts), intPtr(4))
	if !logsContain(out, "bounds this run") {
		t.Fatalf("logs %v do not warn that the cap bounds the run", logLines(out))
	}
}

// Parity: core/tests/test_playbook_runner.py::test_warns_when_lane_exceeds_cap
func TestWarnsWhenLaneExceedsCap(t *testing.T) {
	colony := newFakeColony("a")
	opts := defaultScriptOptions()
	opts.chunk = intPtr(1)

	out := runPlaybook(t, colony, makeScript(opts), intPtr(1))
	if !logsContain(out, "exceeds the colony cap") {
		t.Fatalf("logs %v do not warn that the lane exceeds the cap", logLines(out))
	}
}
