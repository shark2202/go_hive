package colony

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/schemas"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// newGoalRuntime builds a live colony runtime with the given goal. Tests drive
// it through RunBatch, the same entry run_worker and playbook dispatch use.
func newGoalRuntime(t *testing.T, spec agentloop.AgentSpec, goal *schemas.Goal, provider llm.Completer, bus *eventbus.EventBus, storage string) *ColonyRuntime {
	t.Helper()
	if storage == "" {
		storage = t.TempDir()
	}
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		Goal:        goal,
		StoragePath: storage,
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		EventBus: bus,
		StreamID: "goal_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

// Parity-exempt: framework/schemas/goal.py::Goal.is_success is schema-only;
// executing the weighted criteria at worker completion is Go-side wiring.
func TestWorkerRunExecutesGoalCriteriaIntoResultAndReport(t *testing.T) {
	bus := eventbus.NewEventBus()
	progress := collectEvents(bus, eventbus.EventGoalProgress)
	reports := collectEvents(bus, eventbus.EventSubagentReport)
	goal := &schemas.Goal{
		ID:   "report-goal",
		Name: "Report goal",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "sections", Description: "has a summary section", Metric: agentloop.GoalMetricOutputContains, Target: "## Summary", Weight: 0.6},
			{ID: "closing", Description: "closes with DONE", Metric: agentloop.GoalMetricOutputEquals, Target: "## Summary\nDONE", Weight: 0.4},
		},
	}
	// A worker ends by reporting, not by prose: the criteria execute against
	// the report summary (goalOutputText keeps the report as the result).
	provider := newScriptedLLM(reportResponse("success", "## Summary\nwork is done", nil))
	runtime := newGoalRuntime(t, testAgentSpec(), goal, provider, bus, "")

	result, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "write it"}}, RunSpawnOptions{})
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.Status != RunCompleted || result.Succeeded != 1 {
		t.Fatalf("result = %#v, want one completed worker", result)
	}
	if len(result.WorkerResults) != 1 {
		t.Fatalf("worker results = %d, want 1", len(result.WorkerResults))
	}
	worker := runtime.GetWorker(result.WorkerResults[0].WorkerID)
	if worker == nil {
		t.Fatal("worker not registered")
	}

	// The verdict is in the worker result the HTTP surface renders.
	evaluation := worker.Result().GoalEvaluation
	if evaluation == nil {
		t.Fatal("result GoalEvaluation = nil, want the executed criteria")
	}
	if evaluation.Success {
		t.Fatalf("goal success = true, want false at score %v", evaluation.Score)
	}
	if evaluation.Score < 0.59 || evaluation.Score > 0.61 {
		t.Fatalf("score = %v, want 0.6 (only output_contains met)", evaluation.Score)
	}
	if ids := evaluation.UnmetCriterionIDs(); len(ids) != 1 || ids[0] != "closing" {
		t.Fatalf("unmet = %v, want [closing]", ids)
	}

	// The goal_progress event carries the weighted score and per-criterion status.
	if !waitForColony(t, 2*time.Second, func() bool { return progress.count() == 1 }) {
		t.Fatalf("goal_progress events = %d, want 1", progress.count())
	}
	event := progress.snapshot()[0]
	if event.Data["progress"] != 0.6 {
		t.Fatalf("event progress = %v, want 0.6", event.Data["progress"])
	}
	status, ok := event.Data["criteria_status"].(map[string]any)
	if !ok || status["sections"] == nil || status["closing"] == nil {
		t.Fatalf("criteria_status = %#v, want both criteria", event.Data["criteria_status"])
	}

	// The [WORKER_REPORT] payload carries the same verdict, both top-level and
	// inside data (the block FormatWorkerReport renders into <data>).
	if !waitForColony(t, 2*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("worker reports = %d, want 1", reports.count())
	}
	payload := reports.snapshot()[0].Data
	if payload["goal_evaluation"] == nil {
		t.Fatalf("report goal_evaluation = nil, want the verdict: %#v", payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data["goal_evaluation"] == nil {
		t.Fatalf("report data = %#v, want goal_evaluation inside data", payload["data"])
	}

	// The shared goal keeps its Met flags false: workers evaluate copies.
	if runtime.Goal().SuccessCriteria[1].Met {
		t.Fatal("runtime goal was mutated; criteria must be evaluated on a copy")
	}

	// The miss is an improvement signal on disk and in memory.
	signals := runtime.ImprovementSignals()
	if len(signals) != 1 {
		t.Fatalf("improvement signals = %d, want one unmet criterion: %#v", len(signals), signals)
	}
	if signals[0].Kind != string(ImprovementKindUnmetCriterion) || signals[0].CriterionID != "closing" {
		t.Fatalf("signal = %#v, want unmet_criterion/closing", signals[0])
	}
	ledger := filepath.Join(runtime.storagePath, improvementDirName, improvementSignalsFile)
	raw, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("read improvement ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"closing"`) {
		t.Fatalf("ledger = %s, want the closing criterion", raw)
	}
}

// Parity: framework/agent_loop/agent_loop.py::_build_initial_message (the
// goal context is model-visible; see also ColonyRuntime._build_worker).
func TestWorkerSystemPromptCarriesGoalConstraintsAndContext(t *testing.T) {
	goalContext := schemas.NewOrderedMap()
	goalContext.Set("audience", "CFO")
	goal := &schemas.Goal{
		ID:   "prompt-goal",
		Name: "Prompt goal",
		Constraints: []schemas.Constraint{
			{ID: "cite", Description: "Cite every source", ConstraintType: "hard", Category: "quality"},
			{ID: "tone", Description: "Keep it under 500 words", ConstraintType: "soft", Category: "quality"},
		},
		Context: goalContext,
	}
	provider := newScriptedLLM(textResponse("done"))
	runtime := newGoalRuntime(t, testAgentSpec(), goal, provider, nil, "")

	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "write it"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	request, ok := provider.firstCall()
	if !ok {
		t.Fatal("no LLM call recorded")
	}
	prompt := request.System
	for _, want := range []string{"[MUST] Cite every source", "[SHOULD] Keep it under 500 words", "audience: CFO"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Parity-exempt: upstream injects judge feedback but keeps no structured
// retry signal; the improvement ledger is the Go-side consumer this adds.
func TestWorkerJudgeRetryRecordsImprovementSignal(t *testing.T) {
	criteria := "the worker delivers a final answer"
	spec := testAgentSpec()
	spec.SuccessCriteria = &criteria
	goal := &schemas.Goal{
		ID: "judge-goal",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "answer", Description: "the final answer is delivered", Metric: agentloop.GoalMetricLLMJudge, Weight: 1},
		},
	}
	provider := newScriptedLLM(
		// A judged worker turn must be a work turn (a tool call); a text-only
		// turn is a stall and never reaches the judge.
		toolResponse("t1", "task_list", `{}`),
		textResponse(`{"passes": false, "explanation": "missing citations"}`),
		toolResponse("t2", "task_list", `{}`),
		textResponse(`{"passes": true, "explanation": "criteria met"}`),
		// Goal-criteria execution asks the judge about the single criterion.
		textResponse(`{"passes": true, "explanation": "delivered"}`),
	)
	runtime := newGoalRuntime(t, spec, goal, provider, nil, "")

	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "answer it"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}

	var retry *ImprovementSignal
	for _, signal := range runtime.ImprovementSignals() {
		if signal.Kind == string(ImprovementKindJudgeRetry) {
			copied := signal
			retry = &copied
			break
		}
	}
	if retry == nil {
		t.Fatalf("no judge_retry signal recorded: %#v", runtime.ImprovementSignals())
	}
	if !strings.Contains(retry.Feedback, "missing citations") {
		t.Fatalf("retry feedback = %q, want the judge critique", retry.Feedback)
	}
	if retry.Iteration != 0 {
		t.Fatalf("retry iteration = %d, want the first iteration", retry.Iteration)
	}

	// The llm_judge criterion itself executed: the goal verdict is recorded.
	evaluation := runtime.GetWorker(runtime.ListWorkers()[0].WorkerID).Result().GoalEvaluation
	if evaluation == nil || len(evaluation.Criteria) != 1 {
		t.Fatalf("goal evaluation = %#v, want the llm_judge criterion executed", evaluation)
	}
	if evaluation.Criteria[0].Metric != agentloop.GoalMetricLLMJudge || !evaluation.Criteria[0].Met {
		t.Fatalf("criterion = %#v, want a met llm_judge criterion", evaluation.Criteria[0])
	}
}

// Parity-exempt: no upstream evaluator exists for the custom metric; the
// runtime must say unsupported in the worker report instead of faking a pass.
func TestWorkerRunReportsCustomCriterionUnsupported(t *testing.T) {
	bus := eventbus.NewEventBus()
	reports := collectEvents(bus, eventbus.EventSubagentReport)
	goal := &schemas.Goal{
		ID: "custom-goal",
		SuccessCriteria: []schemas.SuccessCriterion{
			{ID: "hook", Description: "a bespoke check nobody registered", Metric: agentloop.GoalMetricCustom, Weight: 1},
		},
	}
	provider := newScriptedLLM(textResponse("claiming success"))
	runtime := newGoalRuntime(t, testAgentSpec(), goal, provider, bus, "")
	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "do it"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}

	worker := runtime.GetWorker(runtime.ListWorkers()[0].WorkerID)
	evaluation := worker.Result().GoalEvaluation
	if evaluation == nil {
		t.Fatal("goal evaluation = nil, want the custom criterion executed (and reported unsupported)")
	}
	if evaluation.Success || evaluation.Criteria[0].Met || evaluation.Criteria[0].Supported {
		t.Fatalf("evaluation = %#v, want custom reported unsupported and unmet", evaluation)
	}
	if len(evaluation.Unsupported) != 1 || evaluation.Unsupported[0] != "hook" {
		t.Fatalf("unsupported = %v, want [hook]", evaluation.Unsupported)
	}

	if !waitForColony(t, 2*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("worker reports = %d, want 1", reports.count())
	}
	payload := reports.snapshot()[0].Data
	verdict, ok := payload["goal_evaluation"].(map[string]any)
	if !ok {
		t.Fatalf("report goal_evaluation = %#v, want a map", payload["goal_evaluation"])
	}
	unsupported, _ := verdict["unsupported"].([]string)
	if len(unsupported) != 1 || unsupported[0] != "hook" {
		t.Fatalf("report unsupported = %v, want [hook]", verdict["unsupported"])
	}
	criteria, _ := verdict["criteria"].([]any)
	if len(criteria) != 1 {
		t.Fatalf("report criteria = %#v, want one row", verdict["criteria"])
	}
	row, _ := criteria[0].(map[string]any)
	detail, _ := row["detail"].(string)
	if !strings.Contains(detail, "unsupported") {
		t.Fatalf("criterion detail = %q, want an explicit unsupported note", detail)
	}
}

// Parity-exempt: upstream has no empty-tracker detector; the concept docs
// require the miss ("an empty tracker column") to surface as a signal.
func TestWorkerRunDetectsEmptyTrackerTable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{"name":"empty-tracker"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if _, err := tracker.EnsureTrackerDB(dir); err != nil {
		t.Fatalf("EnsureTrackerDB: %v", err)
	}
	binding, err := tracker.BindingForColonyDir(dir, "goal_colony")
	if err != nil {
		t.Fatalf("BindingForColonyDir: %v", err)
	}
	if created := tracker.TrackerSQL(binding, `CREATE TABLE tasks (id TEXT PRIMARY KEY, status TEXT)`, 10); created["success"] != true {
		t.Fatalf("create table: %#v", created)
	}
	if registered := tracker.RegisterWritable(binding, "tasks", []string{"status"}, []string{"id"}); registered["success"] != true {
		t.Fatalf("register writable: %#v", registered)
	}

	bus := eventbus.NewEventBus()
	reports := collectEvents(bus, eventbus.EventSubagentReport)
	provider := newScriptedLLM(textResponse("did nothing"))
	runtime := newGoalRuntime(t, testAgentSpec(), testGoal(), provider, bus, dir)
	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "leave it empty"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}

	var detected *ImprovementSignal
	for _, signal := range runtime.ImprovementSignals() {
		if signal.Kind == string(ImprovementKindEmptyTracker) {
			copied := signal
			detected = &copied
			break
		}
	}
	if detected == nil {
		t.Fatalf("no empty_tracker signal: %#v", runtime.ImprovementSignals())
	}
	if table, _ := detected.Evidence["table"].(string); table != "tasks" {
		t.Fatalf("signal table = %v, want tasks", detected.Evidence["table"])
	}
	if !waitForColony(t, 2*time.Second, func() bool { return reports.count() == 1 }) {
		t.Fatalf("worker reports = %d, want 1", reports.count())
	}
	if signals, _ := reports.snapshot()[0].Data["improvement_signals"].([]any); len(signals) == 0 {
		t.Fatal("report carries no improvement_signals; the detection never reached the report")
	}
}
