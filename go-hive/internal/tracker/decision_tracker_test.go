package tracker

// DecisionTracker runtime tests: lifecycle, decision/outcome/problem recording,
// ledger round-trip and the graceful post-end paths upstream documents for
// exception cascades.
//
// The parity anchors point at core/tests/test_runtime.py (the upstream suite for
// this runtime); Go-only guards carry a Parity-exempt note.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
)

func newTestDecisionTracker(t *testing.T) (*DecisionTracker, string) {
	t.Helper()
	dir := t.TempDir()
	runtime, err := NewDecisionTracker(dir)
	if err != nil {
		t.Fatalf("NewDecisionTracker: %v", err)
	}
	return runtime, dir
}

// Parity: core/tests/test_runtime.py::TestRuntimeBasics.test_start_and_end_run
func TestDecisionTrackerStartAndEndRun(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)

	runID := runtime.StartRun("test_goal", "Test goal description", map[string]any{"key": "value"})
	if !strings.HasPrefix(runID, "run_") {
		t.Fatalf("run id = %q, want run_ prefix", runID)
	}
	run := runtime.CurrentRun()
	if run == nil {
		t.Fatal("current run is nil after start_run")
	}
	if run.GoalID != "test_goal" || run.GoalDescription != "Test goal description" {
		t.Fatalf("run = %+v, want goal fields as passed", run)
	}
	if got := run.InputData["key"]; got != "value" {
		t.Fatalf("input_data[key] = %v, want value", got)
	}
	if run.Status != schemas.RunRunning || run.CompletedAt != nil {
		t.Fatalf("status = %q completed_at = %v, want running/nil", run.Status, run.CompletedAt)
	}

	if err := runtime.EndRun(true, "Test completed", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if runtime.CurrentRun() != nil {
		t.Fatal("current run still set after end_run")
	}
}

// Parity: core/tests/test_runtime.py::TestRuntimeBasics.test_end_without_start_is_graceful
func TestDecisionTrackerEndWithoutStartIsGraceful(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)

	if err := runtime.EndRun(true, "", nil); err != nil {
		t.Fatalf("EndRun without a run = %v, want nil", err)
	}
	if runtime.CurrentRun() != nil {
		t.Fatal("current run set without start_run")
	}
}

// Parity: core/tests/test_runtime.py::TestRuntimeBasics.test_run_saved_on_end
func TestDecisionTrackerRunSavedOnEnd(t *testing.T) {
	runtime, dir := newTestDecisionTracker(t)

	runID := runtime.StartRun("test_goal", "Test", nil)
	if err := runtime.EndRun(true, "Saved", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}

	path := filepath.Join(dir, "runs", runID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persisted run %s: %v", path, err)
	}
	loaded, err := storage.NewDefaultConcurrentStorage(dir).LoadRunSync(runID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(%s) = %v, %v", runID, loaded, err)
	}
	if loaded.Status != schemas.RunCompleted || loaded.Narrative != "Saved" {
		t.Fatalf("loaded status/narrative = %q/%q, want completed/Saved", loaded.Status, loaded.Narrative)
	}
}

// Parity-exempt: Go constructor guard. Upstream would mkdir Path("") (the
// current directory); an unwired ledger is a construction error here so no
// run can be silently written into an unrelated working directory.
func TestDecisionTrackerRequiresStorage(t *testing.T) {
	for _, path := range []string{"", "   "} {
		if _, err := NewDecisionTracker(path); !errors.Is(err, ErrDecisionStorageRequired) {
			t.Fatalf("NewDecisionTracker(%q) err = %v, want ErrDecisionStorageRequired", path, err)
		}
	}
}

// Parity: core/tests/test_runtime.py::TestDecisionRecording.test_basic_decision
func TestDecisionTrackerBasicDecision(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	decisionID := runtime.Decide(
		"Choose a greeting",
		[]DecisionOption{
			{ID: "hello", Description: "Say hello"},
			{ID: "hi", Description: "Say hi"},
		},
		"hello",
		"More formal",
		DecisionParams{},
	)
	if decisionID != "dec_0" {
		t.Fatalf("decision id = %q, want dec_0", decisionID)
	}
	run := runtime.CurrentRun()
	if len(run.Decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(run.Decisions))
	}
	decision := run.Decisions[0]
	if decision.Intent != "Choose a greeting" || decision.ChosenOptionID != "hello" {
		t.Fatalf("decision = %+v, want intent/chosen as passed", decision)
	}
	if len(decision.Options) != 2 {
		t.Fatalf("options = %d, want 2", len(decision.Options))
	}
	if decision.Options[0].ActionType != "unknown" {
		t.Fatalf("action_type = %q, want unknown default", decision.Options[0].ActionType)
	}
	if got := decision.Options[0].EffectiveConfidence(); got != 0.5 {
		t.Fatalf("confidence = %v, want 0.5 default", got)
	}
	if decision.NodeID != "unknown" {
		t.Fatalf("node = %q, want unknown default", decision.NodeID)
	}
	if next := runtime.Decide("second", nil, "", "", DecisionParams{}); next != "dec_1" {
		t.Fatalf("second decision id = %q, want dec_1", next)
	}
}

// Parity-exempt: pins the explicit-zero confidence edge (Python dict.get keeps
// a supplied 0 where the schema default is 0.5).
func TestDecisionOptionExplicitZeroConfidence(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)
	zero := 0.0
	runtime.Decide("zero", []DecisionOption{{ID: "a", Description: "A", Confidence: &zero}}, "a", "test", DecisionParams{})
	if got := runtime.CurrentRun().Decisions[0].Options[0].EffectiveConfidence(); got != 0 {
		t.Fatalf("confidence = %v, want explicit 0", got)
	}
}

// Parity: core/tests/test_runtime.py::TestDecisionRecording.test_decision_without_run_is_graceful
func TestDecisionTrackerDecisionWithoutRunIsGraceful(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)

	decisionID := runtime.Decide("Test", []DecisionOption{{ID: "a", Description: "A"}}, "a", "Test", DecisionParams{})
	if decisionID != "" {
		t.Fatalf("decision id = %q, want empty without a run", decisionID)
	}
}

// Parity: core/tests/test_runtime.py::TestDecisionRecording.test_decision_with_node_context
func TestDecisionTrackerDecisionWithNodeContext(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	runtime.SetNode("search-node")
	runtime.Decide("Search query", []DecisionOption{{ID: "web", Description: "Web search"}}, "web", "Need web results", DecisionParams{})
	if got := runtime.CurrentRun().Decisions[0].NodeID; got != "search-node" {
		t.Fatalf("node = %q, want search-node", got)
	}

	runtime.Decide("Explicit", []DecisionOption{{ID: "x", Description: "X"}}, "x", "override", DecisionParams{NodeID: "explicit-node"})
	if got := runtime.CurrentRun().Decisions[1].NodeID; got != "explicit-node" {
		t.Fatalf("explicit node = %q, want explicit-node", got)
	}
}

// Parity: core/tests/test_runtime.py::TestDecisionRecording.test_decision_type
func TestDecisionTrackerDecisionType(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	runtime.Decide(
		"Which tool to use",
		[]DecisionOption{
			{ID: "search", Description: "Use search API"},
			{ID: "cache", Description: "Use cached data"},
		},
		"search",
		"Need fresh data",
		DecisionParams{
			DecisionType: schemas.DecisionToolSelection,
			Constraints:  []string{"must cite"},
			Context:      map[string]any{"query": "fresh"},
		},
	)
	decision := runtime.CurrentRun().Decisions[0]
	if decision.DecisionType != schemas.DecisionToolSelection {
		t.Fatalf("decision_type = %q, want tool_selection", decision.DecisionType)
	}
	if len(decision.ActiveConstraints) != 1 || decision.ActiveConstraints[0] != "must cite" {
		t.Fatalf("constraints = %v, want [must cite]", decision.ActiveConstraints)
	}
	if decision.InputContext["query"] != "fresh" {
		t.Fatalf("context = %v, want query=fresh", decision.InputContext)
	}
}

// Parity: core/tests/test_runtime.py::TestOutcomeRecording.test_record_successful_outcome
func TestDecisionTrackerRecordSuccessfulOutcome(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)
	decisionID := runtime.Decide("Test action", []DecisionOption{{ID: "a", Description: "Action A"}}, "a", "Test", DecisionParams{})

	runtime.RecordOutcome(OutcomeRecord{
		DecisionID: decisionID,
		Success:    true,
		Result:     map[string]any{"data": "success"},
		Summary:    "Action completed successfully",
		TokensUsed: 100,
		LatencyMS:  50,
	})
	decision := runtime.CurrentRun().Decisions[0]
	if decision.Outcome == nil || !decision.Outcome.Success {
		t.Fatalf("outcome = %+v, want success", decision.Outcome)
	}
	result, ok := decision.Outcome.Result.(map[string]any)
	if !ok || result["data"] != "success" {
		t.Fatalf("outcome result = %v, want map with data=success", decision.Outcome.Result)
	}
	if decision.Outcome.Summary != "Action completed successfully" {
		t.Fatalf("summary = %q", decision.Outcome.Summary)
	}
	if !decision.WasSuccessful() {
		t.Fatal("WasSuccessful = false, want true")
	}
	metrics := runtime.CurrentRun().Metrics
	if metrics.SuccessfulDecisions != 1 || metrics.TotalTokens != 100 || metrics.TotalLatencyMS != 50 {
		t.Fatalf("metrics = %+v, want successful=1 tokens=100 latency=50", metrics)
	}
}

// Parity: core/tests/test_runtime.py::TestOutcomeRecording.test_record_failed_outcome
func TestDecisionTrackerRecordFailedOutcome(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)
	decisionID := runtime.Decide("Test action", []DecisionOption{{ID: "a", Description: "Action A"}}, "a", "Test", DecisionParams{})

	errText := "API rate limited"
	runtime.RecordOutcome(OutcomeRecord{DecisionID: decisionID, Success: false, Error: &errText})
	decision := runtime.CurrentRun().Decisions[0]
	if decision.Outcome == nil || decision.Outcome.Success {
		t.Fatalf("outcome = %+v, want failure", decision.Outcome)
	}
	if decision.Outcome.Error == nil || *decision.Outcome.Error != errText {
		t.Fatalf("error = %v, want %q", decision.Outcome.Error, errText)
	}
	if decision.WasSuccessful() {
		t.Fatal("WasSuccessful = true, want false")
	}
	if runtime.CurrentRun().Metrics.FailedDecisions != 1 {
		t.Fatalf("failed decisions = %d, want 1", runtime.CurrentRun().Metrics.FailedDecisions)
	}
}

// Parity: core/tests/test_runtime.py::TestOutcomeRecording.test_metrics_updated_on_outcome
func TestDecisionTrackerMetricsUpdatedOnOutcome(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	d1 := runtime.Decide("Action 1", []DecisionOption{{ID: "a", Description: "A"}}, "a", "Test", DecisionParams{})
	runtime.RecordOutcome(OutcomeRecord{DecisionID: d1, Success: true, TokensUsed: 100})
	d2 := runtime.Decide("Action 2", []DecisionOption{{ID: "b", Description: "B"}}, "b", "Test", DecisionParams{})
	runtime.RecordOutcome(OutcomeRecord{DecisionID: d2, Success: false})

	metrics := runtime.CurrentRun().Metrics
	if metrics.TotalDecisions != 2 || metrics.SuccessfulDecisions != 1 || metrics.FailedDecisions != 1 {
		t.Fatalf("metrics = %+v, want 2 total / 1 success / 1 failure", metrics)
	}
	if metrics.TotalTokens != 100 {
		t.Fatalf("tokens = %d, want 100", metrics.TotalTokens)
	}
	if rate := metrics.SuccessRate(); rate != 0.5 {
		t.Fatalf("success rate = %v, want 0.5", rate)
	}
}

// Parity: core/tests/test_runtime.py::TestProblemReporting.test_report_problem
func TestDecisionTrackerReportProblem(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	rootCause := "Service outage"
	suggested := "Implement fallback to cached data"
	problemID := runtime.ReportProblem(ProblemReport{
		Severity:     "critical",
		Description:  "API is unavailable",
		RootCause:    &rootCause,
		SuggestedFix: &suggested,
	})
	if problemID != "prob_0" {
		t.Fatalf("problem id = %q, want prob_0", problemID)
	}
	problems := runtime.CurrentRun().Problems
	if len(problems) != 1 {
		t.Fatalf("problems = %d, want 1", len(problems))
	}
	if problems[0].Severity != "critical" || problems[0].Description != "API is unavailable" {
		t.Fatalf("problem = %+v", problems[0])
	}
	if problems[0].RootCause == nil || *problems[0].RootCause != rootCause {
		t.Fatalf("root cause = %v, want %q", problems[0].RootCause, rootCause)
	}
	if problems[0].SuggestedFix == nil || *problems[0].SuggestedFix != suggested {
		t.Fatalf("suggested fix = %v, want %q", problems[0].SuggestedFix, suggested)
	}
	if next := runtime.ReportProblem(ProblemReport{Severity: "warning", Description: "second"}); next != "prob_1" {
		t.Fatalf("second problem id = %q, want prob_1", next)
	}
}

// Parity: core/tests/test_runtime.py::TestProblemReporting.test_problem_linked_to_decision
func TestDecisionTrackerProblemLinkedToDecision(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)
	decisionID := runtime.Decide("Call API", []DecisionOption{{ID: "call", Description: "Make API call"}}, "call", "Need data", DecisionParams{})

	runtime.ReportProblem(ProblemReport{Severity: "warning", Description: "API slow", DecisionID: &decisionID})
	problem := runtime.CurrentRun().Problems[0]
	if problem.DecisionID == nil || *problem.DecisionID != decisionID {
		t.Fatalf("problem decision_id = %v, want %q", problem.DecisionID, decisionID)
	}
}

// Parity: core/tests/test_runtime.py::TestConvenienceMethods.test_quick_decision
func TestDecisionTrackerQuickDecision(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	runtime.QuickDecision("Log message", "Write to stdout", "Standard logging", "")
	decision := runtime.CurrentRun().Decisions[0]
	if decision.Intent != "Log message" || len(decision.Options) != 1 {
		t.Fatalf("decision = %+v, want one option", decision)
	}
	option := decision.Options[0]
	if option.ID != "action" || option.Description != "Write to stdout" || option.ActionType != "execute" {
		t.Fatalf("option = %+v, want action/execute", option)
	}
}

// Parity: core/tests/test_runtime.py::TestConvenienceMethods.test_decide_and_execute_success
func TestDecisionTrackerDecideAndExecuteSuccess(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	decisionID, result, err := runtime.DecideAndExecute(
		"Compute value",
		[]DecisionOption{{ID: "compute", Description: "Run computation"}},
		"compute",
		"Need the value",
		DecisionParams{},
		func() (any, error) { return map[string]any{"computed": 42}, nil },
	)
	if err != nil {
		t.Fatalf("DecideAndExecute: %v", err)
	}
	if decisionID != "dec_0" {
		t.Fatalf("decision id = %q, want dec_0", decisionID)
	}
	computed, ok := result.(map[string]any)
	if !ok || computed["computed"] != 42 {
		t.Fatalf("result = %v, want computed=42", result)
	}
	decision := runtime.CurrentRun().Decisions[0]
	if !decision.WasSuccessful() || decision.Outcome == nil {
		t.Fatalf("decision = %+v, want successful outcome", decision)
	}
	if decision.Outcome.LatencyMS < 0 {
		t.Fatalf("latency = %d, want non-negative", decision.Outcome.LatencyMS)
	}
}

// Parity: core/tests/test_runtime.py::TestConvenienceMethods.test_decide_and_execute_failure
func TestDecisionTrackerDecideAndExecuteFailure(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	sentinel := errors.New("Something went wrong")
	_, _, err := runtime.DecideAndExecute(
		"Failing action",
		[]DecisionOption{{ID: "fail", Description: "Will fail"}},
		"fail",
		"Test failure",
		DecisionParams{},
		func() (any, error) { return nil, sentinel },
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the executor error", err)
	}
	decision := runtime.CurrentRun().Decisions[0]
	if decision.WasSuccessful() || decision.Outcome == nil || decision.Outcome.Error == nil {
		t.Fatalf("decision = %+v, want failed outcome with error", decision)
	}
	if !strings.Contains(*decision.Outcome.Error, "Something went wrong") {
		t.Fatalf("outcome error = %q, want executor message", *decision.Outcome.Error)
	}
}

// Parity: core/tests/test_runtime.py::TestRuntimeBasics.test_run_saved_on_end
func TestDecisionTrackerLedgerReadWrite(t *testing.T) {
	runtime, dir := newTestDecisionTracker(t)
	runID := runtime.StartRun("ledger_goal", "Ledger", nil)

	d1 := runtime.Decide("Action 1", []DecisionOption{{ID: "a", Description: "A"}}, "a", "first", DecisionParams{})
	runtime.RecordOutcome(OutcomeRecord{DecisionID: d1, Success: true, TokensUsed: 100})
	d2 := runtime.Decide("Action 2", []DecisionOption{{ID: "b", Description: "B"}}, "b", "second", DecisionParams{})
	errText := "boom"
	runtime.RecordOutcome(OutcomeRecord{DecisionID: d2, Success: false, Error: &errText})
	runtime.ReportProblem(ProblemReport{Severity: "critical", Description: "ledger problem"})

	if err := runtime.EndRun(true, "Ledger narrative", map[string]any{"done": true}); err != nil {
		t.Fatalf("EndRun: %v", err)
	}

	loaded, err := storage.NewDefaultConcurrentStorage(dir).LoadRunSync(runID)
	if err != nil || loaded == nil {
		t.Fatalf("LoadRunSync(%s) = %v, %v", runID, loaded, err)
	}
	if loaded.Status != schemas.RunCompleted || loaded.Narrative != "Ledger narrative" {
		t.Fatalf("loaded status/narrative = %q/%q", loaded.Status, loaded.Narrative)
	}
	if loaded.OutputData["done"] != true {
		t.Fatalf("output_data = %v, want done=true", loaded.OutputData)
	}
	if len(loaded.Decisions) != 2 || len(loaded.Problems) != 1 {
		t.Fatalf("loaded decisions/problems = %d/%d, want 2/1", len(loaded.Decisions), len(loaded.Problems))
	}
	metrics := loaded.Metrics
	if metrics.TotalDecisions != 2 || metrics.SuccessfulDecisions != 1 || metrics.FailedDecisions != 1 || metrics.TotalTokens != 100 {
		t.Fatalf("loaded metrics = %+v", metrics)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "runs", runID+".json"))
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode ledger file: %v", err)
	}
	if payload["status"] != "completed" || payload["narrative"] != "Ledger narrative" {
		t.Fatalf("ledger status/narrative = %v/%v", payload["status"], payload["narrative"])
	}
	if _, ok := payload["duration_ms"]; !ok {
		t.Fatal("ledger is missing the computed duration_ms field")
	}
	decisions, ok := payload["decisions"].([]any)
	if !ok || len(decisions) != 2 {
		t.Fatalf("ledger decisions = %v", payload["decisions"])
	}
	first, ok := decisions[0].(map[string]any)
	if !ok {
		t.Fatalf("ledger decision[0] = %v", decisions[0])
	}
	if first["id"] != "dec_0" || first["chosen_option_id"] != "a" || first["was_successful"] != true {
		t.Fatalf("ledger decision[0] = %v", first)
	}
	rawMetrics, ok := payload["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("ledger metrics = %v", payload["metrics"])
	}
	if rawMetrics["total_decisions"] != float64(2) || rawMetrics["success_rate"] != 0.5 {
		t.Fatalf("ledger metrics = %v, want total=2 rate=0.5", rawMetrics)
	}
	problems, ok := payload["problems"].([]any)
	if !ok || len(problems) != 1 {
		t.Fatalf("ledger problems = %v", payload["problems"])
	}
	if problem, ok := problems[0].(map[string]any); !ok || problem["severity"] != "critical" {
		t.Fatalf("ledger problem = %v", problems[0])
	}
}

// Parity-exempt: Go pin for upstream's graceful post-end path (decide /
// record_outcome / report_problem during exception handling after end_run).
func TestDecisionTrackerAfterEndRunCancellationIsGraceful(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)
	decisionID := runtime.Decide("action", []DecisionOption{{ID: "a", Description: "A"}}, "a", "test", DecisionParams{})

	if err := runtime.EndRun(false, "cancelled", nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	if got := runtime.Decide("late", nil, "", "", DecisionParams{}); got != "" {
		t.Fatalf("late decision id = %q, want empty", got)
	}
	errText := "late"
	runtime.RecordOutcome(OutcomeRecord{DecisionID: decisionID, Success: false, Error: &errText})
	if got := runtime.ReportProblem(ProblemReport{Severity: "critical", Description: "late"}); got != "" {
		t.Fatalf("late problem id = %q, want empty", got)
	}
	if err := runtime.EndRun(true, "", nil); err != nil {
		t.Fatalf("second EndRun = %v, want nil", err)
	}
	if runtime.CurrentRun() != nil {
		t.Fatal("current run resurrected after end")
	}
}

// Parity-exempt: Go concurrency pin. Upstream's asyncio loop serializes decide
// calls; the Go runtime must keep the same invariant (one unique dec_N per
// accepted call / one decision per call) under real goroutines.
func TestDecisionTrackerConcurrentDecisions(t *testing.T) {
	runtime, _ := newTestDecisionTracker(t)
	runtime.StartRun("test_goal", "Test", nil)

	const workers = 64
	ids := make([]string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i] = runtime.Decide(fmt.Sprintf("action %d", i), []DecisionOption{{ID: "a", Description: "A"}}, "a", "test", DecisionParams{})
		}(i)
	}
	wg.Wait()

	seen := make(map[string]struct{}, workers)
	for i, id := range ids {
		if !strings.HasPrefix(id, "dec_") {
			t.Fatalf("decision %d id = %q, want dec_N", i, id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate decision id %q", id)
		}
		seen[id] = struct{}{}
	}
	run := runtime.CurrentRun()
	if len(run.Decisions) != workers || run.Metrics.TotalDecisions != workers {
		t.Fatalf("decisions = %d metrics.total = %d, want %d", len(run.Decisions), run.Metrics.TotalDecisions, workers)
	}
}
