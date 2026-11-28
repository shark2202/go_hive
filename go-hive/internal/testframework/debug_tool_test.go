package testframework

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Parity: core/tests/test_testing_framework.py::test_analyze_missing_test
func TestAnalyzeMissingTest(t *testing.T) {
	tool := NewDebugTool(newStorage(t), nil)
	info, err := tool.Analyze("goal_001", "nonexistent", "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if info.TestID != "nonexistent" {
		t.Fatalf("test_id = %q", info.TestID)
	}
	if info.ErrorMessage == nil || !strings.Contains(strings.ToLower(*info.ErrorMessage), "not found") {
		t.Fatalf("error_message = %v, want not found", info.ErrorMessage)
	}
	if info.Input == nil || info.Expected == nil || info.Logs == nil || info.RuntimeData == nil {
		t.Fatalf("default containers missing: %+v", info)
	}
}

// Parity: core/tests/test_testing_framework.py::test_analyze_with_result
func TestAnalyzeWithResult(t *testing.T) {
	storage := newStorage(t)
	test := NewTest("test_001", "goal_001", "c1", TestTypeConstraint, "test_something", "pass", "A test")
	test.Input = map[string]any{"key": "value"}
	test.ExpectedOutput = map[string]any{"result": "expected"}
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest: %v", err)
	}
	result := NewTestResult("test_001", false, 100)
	result.ErrorMessage = stringPointer("TypeError: something went wrong")
	result.ErrorCategory = categoryPointer(ErrorCategoryImplementation)
	if err := storage.SaveResult("test_001", result); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	info, err := NewDebugTool(storage, nil).Analyze("goal_001", "test_001", "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if info.TestID != "test_001" || info.TestName != "test_something" {
		t.Fatalf("identity = %q/%q", info.TestID, info.TestName)
	}
	if info.Passed {
		t.Fatal("failed result reported as passed")
	}
	if info.ErrorCategory == nil || *info.ErrorCategory != "implementation_error" {
		t.Fatalf("error_category = %v", info.ErrorCategory)
	}
	if info.SuggestedFix == nil || !strings.Contains(*info.SuggestedFix, "code") {
		t.Fatalf("suggested_fix = %v", info.SuggestedFix)
	}
	if info.Input["key"] != "value" || info.Expected["result"] != "expected" {
		t.Fatalf("input/expected = %+v/%+v", info.Input, info.Expected)
	}
	if info.IterationGuidance["stage"] != "Agent" {
		t.Fatalf("iteration_guidance = %+v", info.IterationGuidance)
	}
}

// Parity-exempt: Go-only guard for the debug report's full field set, which
// the CLI serializes to JSON.
func TestDebugInfoReportFields(t *testing.T) {
	storage := newStorage(t)
	test := NewTest("test_001", "goal_001", "c1", TestTypeConstraint, "test_something", "pass", "A test")
	test.Input = map[string]any{"key": "value"}
	test.ExpectedOutput = map[string]any{"result": "expected"}
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest: %v", err)
	}
	result := NewTestResult("test_001", false, 42)
	result.ActualOutput = map[string]any{"result": "actual"}
	result.ErrorMessage = stringPointer("timeout waiting for connection")
	result.StackTrace = stringPointer("Traceback...")
	result.RuntimeLogs = []map[string]any{{"level": "ERROR", "msg": "timeout"}}
	if err := storage.SaveResult("test_001", result); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	info, err := NewDebugTool(storage, nil).Analyze("goal_001", "test_001", "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if info.ErrorCategory == nil || *info.ErrorCategory != string(ErrorCategoryEdgeCase) {
		t.Fatalf("error_category = %v, want edge_case (categorized from the message)", info.ErrorCategory)
	}
	fields := info.ToDict()
	for _, key := range []string{
		"test_id", "test_name", "input", "expected", "actual", "passed",
		"error_message", "error_category", "stack_trace", "logs", "runtime_data",
		"suggested_fix", "iteration_guidance",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("report missing field %q: %+v", key, fields)
		}
	}
	if fields["actual"] == nil || fields["passed"] != false {
		t.Fatalf("actual/passed = %v/%v", fields["actual"], fields["passed"])
	}
	if info.IterationGuidance["stage"] != "Eval" {
		t.Fatalf("iteration_guidance = %+v", info.IterationGuidance)
	}
}

// Parity-exempt: Go-only guard for the optional Runtime loader shape and the
// two upstream error strings.
func TestDebugRuntimeData(t *testing.T) {
	storage := newStorage(t)
	test := NewTest("test_001", "goal_001", "c1", TestTypeConstraint, "test_something", "pass", "A test")
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest: %v", err)
	}
	loader := &stubRuntimeLoader{report: &RuntimeReport{
		ExecutionPath: []string{"n1", "n2"},
		Decisions:     []any{map[string]any{"kind": "continue"}},
		Problems:      []any{"none"},
		Status:        "completed",
	}}
	info, err := NewDebugTool(storage, loader).Analyze("goal_001", "test_001", "run-1")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if info.RuntimeData["status"] != "completed" {
		t.Fatalf("runtime_data = %+v", info.RuntimeData)
	}
	path, _ := info.RuntimeData["execution_path"].([]string)
	if len(path) != 2 || path[1] != "n2" {
		t.Fatalf("execution_path = %v", info.RuntimeData["execution_path"])
	}

	loader.report = nil
	info, err = NewDebugTool(storage, loader).Analyze("goal_001", "test_001", "missing")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if info.RuntimeData["error"] != "Run missing not found" {
		t.Fatalf("missing-run error = %v", info.RuntimeData["error"])
	}

	loader.err = errStub
	info, err = NewDebugTool(storage, loader).Analyze("goal_001", "test_001", "broken")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	message, _ := info.RuntimeData["error"].(string)
	if !strings.Contains(message, "Failed to load runtime data:") {
		t.Fatalf("load-failure error = %v", info.RuntimeData["error"])
	}
}

// Parity-exempt: Go-only guard for the failure summary buckets and the
// iteration suggestions derived from them.
func TestFailureSummary(t *testing.T) {
	storage := newStorage(t)
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	freezeClock(t, base)
	for index, category := range []ErrorCategory{
		ErrorCategoryLogic, ErrorCategoryImplementation, "", ErrorCategoryEdgeCase,
	} {
		test := NewTest(stringID(index), "goal_001", "c1", TestTypeConstraint, stringID(index), "pass", "test")
		test.RecordResult(false)
		if err := storage.SaveTest(test); err != nil {
			t.Fatalf("SaveTest: %v", err)
		}
		result := NewTestResult(test.ID, false, 10)
		if category != "" {
			result.ErrorCategory = categoryPointer(category)
		}
		if err := storage.SaveResult(test.ID, result); err != nil {
			t.Fatalf("SaveResult: %v", err)
		}
	}
	// A passed test must not appear in the failure summary.
	passed := NewTest("passed", "goal_001", "c1", TestTypeConstraint, "passed", "pass", "test")
	passed.RecordResult(true)
	if err := storage.SaveTest(passed); err != nil {
		t.Fatalf("SaveTest: %v", err)
	}

	summary, err := NewDebugTool(storage, nil).GetFailureSummary("goal_001")
	if err != nil {
		t.Fatalf("GetFailureSummary: %v", err)
	}
	if summary.TotalFailures != 4 {
		t.Fatalf("total_failures = %d, want 4", summary.TotalFailures)
	}
	if len(summary.ByCategory["logic_error"]) != 1 ||
		len(summary.ByCategory["implementation_error"]) != 1 ||
		len(summary.ByCategory["edge_case"]) != 1 ||
		len(summary.ByCategory["uncategorized"]) != 1 {
		t.Fatalf("by_category = %+v", summary.ByCategory)
	}
	if len(summary.IterationSuggestions) != 4 {
		t.Fatalf("suggestions = %v", summary.IterationSuggestions)
	}
	if !strings.Contains(summary.IterationSuggestions[0], "logic errors") {
		t.Fatalf("first suggestion = %q", summary.IterationSuggestions[0])
	}
	if !strings.Contains(summary.IterationSuggestions[3], "uncategorized failures") {
		t.Fatalf("last suggestion = %q", summary.IterationSuggestions[3])
	}
}

func stringID(index int) string {
	return "test_" + strconv.Itoa(index)
}

type stubRuntimeLoader struct {
	report *RuntimeReport
	err    error
}

func (s *stubRuntimeLoader) LoadRunSync(runID string) (*RuntimeReport, error) {
	return s.report, s.err
}

var errStub = stubError("boom")

type stubError string

func (e stubError) Error() string { return string(e) }
