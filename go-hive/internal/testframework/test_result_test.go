package testframework

import (
	"encoding/json"
	"strings"
	"testing"
)

// Parity: core/tests/test_testing_framework.py::test_create_passed_result
func TestCreatePassedResult(t *testing.T) {
	result := NewTestResult("test_001", true, 100)
	result.ActualOutput = map[string]any{"status": "ok"}
	result.ExpectedOutput = map[string]any{"status": "ok"}

	if !result.Passed || result.DurationMS != 100 {
		t.Fatalf("passed/duration = %v/%d", result.Passed, result.DurationMS)
	}
	if result.ErrorCategory != nil {
		t.Fatalf("error_category = %v, want nil", result.ErrorCategory)
	}
}

// Parity: core/tests/test_testing_framework.py::test_create_failed_result
func TestCreateFailedResult(t *testing.T) {
	result := NewTestResult("test_001", false, 50)
	result.ErrorMessage = stringPointer("Assertion failed")
	result.ErrorCategory = categoryPointer(ErrorCategoryImplementation)
	result.StackTrace = stringPointer("Traceback...")

	if result.Passed {
		t.Fatal("failed result must not report passed")
	}
	if result.ErrorCategory == nil || *result.ErrorCategory != ErrorCategoryImplementation {
		t.Fatalf("error_category = %v", result.ErrorCategory)
	}
}

// Parity: core/tests/test_testing_framework.py::test_summary_dict
func TestSummaryDict(t *testing.T) {
	result := NewTestResult("test_001", false, 50)
	result.ErrorMessage = stringPointer(strings.Repeat("Very long error ", 20))
	result.ErrorCategory = categoryPointer(ErrorCategoryLogic)

	summary := result.SummaryDict()
	if summary["test_id"] != "test_001" {
		t.Fatalf("test_id = %v", summary["test_id"])
	}
	if summary["passed"] != false {
		t.Fatalf("passed = %v", summary["passed"])
	}
	if summary["error_category"] != "logic_error" {
		t.Fatalf("error_category = %v", summary["error_category"])
	}
	message, _ := summary["error_message"].(string)
	if len([]rune(message)) != 100 {
		t.Fatalf("error_message length = %d, want 100", len([]rune(message)))
	}
}

// Parity: core/tests/test_testing_framework.py::test_summary_dict
func TestSummaryDictWithoutError(t *testing.T) {
	summary := NewTestResult("test_001", true, 10).SummaryDict()
	if summary["error_message"] != nil {
		t.Fatalf("error_message = %v, want nil", summary["error_message"])
	}
	if summary["error_category"] != nil {
		t.Fatalf("error_category = %v, want nil", summary["error_category"])
	}
}

// Parity: core/tests/test_testing_framework.py::test_suite_result_properties
func TestSuiteResultProperties(t *testing.T) {
	suite := &TestSuiteResult{
		GoalID:     "goal_001",
		Total:      3,
		Passed:     2,
		Failed:     1,
		DurationMS: 225,
		Results: []TestResult{
			{TestID: "t1", Passed: true, DurationMS: 100},
			{TestID: "t2", Passed: true, DurationMS: 50},
			{TestID: "t3", Passed: false, DurationMS: 75, ErrorCategory: categoryPointer(ErrorCategoryImplementation)},
		},
	}
	if suite.AllPassed() {
		t.Fatal("suite with a failure must not be all-passed")
	}
	rate := suite.PassRate()
	if rate < 0.666 || rate > 0.667 {
		t.Fatalf("pass_rate = %v, want 2/3", rate)
	}
	if len(suite.GetFailedResults()) != 1 {
		t.Fatalf("failed results = %d, want 1", len(suite.GetFailedResults()))
	}
	summary := suite.SummaryDict()
	if summary["pass_rate"] != "66.7%" {
		t.Fatalf("summary pass_rate = %v, want 66.7%%", summary["pass_rate"])
	}
	if summary["overall_passed"] != false {
		t.Fatalf("overall_passed = %v", summary["overall_passed"])
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_results_by_category
func TestGetResultsByCategory(t *testing.T) {
	suite := &TestSuiteResult{
		GoalID: "goal_001",
		Total:  3,
		Failed: 3,
		Results: []TestResult{
			{TestID: "t1", Passed: false, DurationMS: 100, ErrorCategory: categoryPointer(ErrorCategoryLogic)},
			{TestID: "t2", Passed: false, DurationMS: 50, ErrorCategory: categoryPointer(ErrorCategoryImplementation)},
			{TestID: "t3", Passed: false, DurationMS: 75, ErrorCategory: categoryPointer(ErrorCategoryImplementation)},
		},
	}
	if got := len(suite.GetResultsByCategory(ErrorCategoryImplementation)); got != 2 {
		t.Fatalf("implementation errors = %d, want 2", got)
	}
	if got := len(suite.GetResultsByCategory(ErrorCategoryEdgeCase)); got != 0 {
		t.Fatalf("edge cases = %d, want 0", got)
	}
}

// Parity-exempt: Go-only guard for pydantic's 0..1/ge=0 validation and the
// required-field check on stored documents.
func TestTestResultValidation(t *testing.T) {
	var result TestResult
	if err := json.Unmarshal([]byte(`{"test_id":"t1","passed":true,"duration_ms":-1}`), &result); err == nil {
		t.Fatal("negative duration must be rejected")
	}
	if err := json.Unmarshal([]byte(`{"test_id":"t1","passed":true}`), &result); err == nil {
		t.Fatal("missing duration_ms must be rejected")
	}
	if err := json.Unmarshal([]byte(`{"test_id":"t1","passed":false,"duration_ms":1,"error_category":"nope"}`), &result); err == nil {
		t.Fatal("unknown error_category must be rejected")
	}
}

// Parity-exempt: Go-only round-trip guard for TestResult extras and defaults.
func TestTestResultDefaultsAndExtras(t *testing.T) {
	var result TestResult
	document := `{
	  "test_id": "t1",
	  "passed": false,
	  "duration_ms": 5,
	  "runtime_logs": null,
	  "node_outputs": null,
	  "execution_path": null,
	  "timestamp": "2026-11-26T10:20:30",
	  "custom": "kept"
	}`
	if err := json.Unmarshal([]byte(document), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.RuntimeLogs == nil || result.NodeOutputs == nil || result.ExecutionPath == nil {
		t.Fatalf("defaults not applied: %+v", result)
	}
	data, err := json.Marshal(&result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"custom":"kept"`) {
		t.Fatalf("extra not preserved: %s", data)
	}
}

// Parity: framework/testing/test_result.py::ErrorCategory (class)
func TestErrorCategoryValues(t *testing.T) {
	if string(ErrorCategoryLogic) != "logic_error" ||
		string(ErrorCategoryImplementation) != "implementation_error" ||
		string(ErrorCategoryEdgeCase) != "edge_case" {
		t.Fatal("error category wire values drifted")
	}
	if len(ErrorCategories) != 3 {
		t.Fatalf("categories = %d, want 3", len(ErrorCategories))
	}
}
