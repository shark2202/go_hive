package testframework

import (
	"strings"
	"testing"
)

// Parity: core/tests/test_testing_framework.py::test_categorize_passed
func TestCategorizePassed(t *testing.T) {
	categorizer := NewErrorCategorizer()
	if category := categorizer.Categorize(NewTestResult("t1", true, 100)); category != nil {
		t.Fatalf("category = %v, want nil", *category)
	}
	if category, confidence := categorizer.CategorizeWithConfidence(NewTestResult("t1", true, 100)); category != nil || confidence != 1.0 {
		t.Fatalf("confidence = %v/%v, want nil/1.0", category, confidence)
	}
}

// Parity: core/tests/test_testing_framework.py::test_categorize_logic_error
func TestCategorizeLogicError(t *testing.T) {
	result := NewTestResult("t1", false, 100)
	result.ErrorMessage = stringPointer("goal not achieved: expected success criteria was not met")
	category := NewErrorCategorizer().Categorize(result)
	if category == nil || *category != ErrorCategoryLogic {
		t.Fatalf("category = %v, want logic_error", category)
	}
}

// Parity: core/tests/test_testing_framework.py::test_categorize_implementation_error
func TestCategorizeImplementationError(t *testing.T) {
	result := NewTestResult("t1", false, 100)
	result.ErrorMessage = stringPointer("TypeError: 'NoneType' object has no attribute 'get'")
	category := NewErrorCategorizer().Categorize(result)
	if category == nil || *category != ErrorCategoryImplementation {
		t.Fatalf("category = %v, want implementation_error", category)
	}
}

// Parity: core/tests/test_testing_framework.py::test_categorize_edge_case
func TestCategorizeEdgeCase(t *testing.T) {
	result := NewTestResult("t1", false, 100)
	result.ErrorMessage = stringPointer("timeout: request took longer than expected")
	category := NewErrorCategorizer().Categorize(result)
	if category == nil || *category != ErrorCategoryEdgeCase {
		t.Fatalf("category = %v, want edge_case", category)
	}
}

// Parity: core/tests/test_testing_framework.py::test_categorize_from_stack_trace
func TestCategorizeFromStackTrace(t *testing.T) {
	result := NewTestResult("t1", false, 100)
	result.ErrorMessage = stringPointer("Error occurred")
	result.StackTrace = stringPointer("KeyError: 'missing_key'\n  at line 42")
	category := NewErrorCategorizer().Categorize(result)
	if category == nil || *category != ErrorCategoryImplementation {
		t.Fatalf("category = %v, want implementation_error", category)
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_fix_suggestion
func TestGetFixSuggestion(t *testing.T) {
	categorizer := NewErrorCategorizer()
	if !strings.Contains(categorizer.GetFixSuggestion(ErrorCategoryLogic), "Goal") {
		t.Fatal("logic suggestion must mention the Goal")
	}
	if !strings.Contains(strings.ToLower(categorizer.GetFixSuggestion(ErrorCategoryImplementation)), "code") {
		t.Fatal("implementation suggestion must mention code")
	}
	if !strings.Contains(strings.ToLower(categorizer.GetFixSuggestion(ErrorCategoryEdgeCase)), "test") {
		t.Fatal("edge-case suggestion must mention a test")
	}
	if got := categorizer.GetFixSuggestion("unknown"); got != "Review the test and agent implementation." {
		t.Fatalf("unknown suggestion = %q", got)
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_iteration_guidance
func TestGetIterationGuidance(t *testing.T) {
	categorizer := NewErrorCategorizer()
	logic := categorizer.GetIterationGuidance(ErrorCategoryLogic)
	if logic["stage"] != "Goal" || logic["restart_required"] != true {
		t.Fatalf("logic guidance = %+v", logic)
	}
	implementation := categorizer.GetIterationGuidance(ErrorCategoryImplementation)
	if implementation["stage"] != "Agent" || implementation["restart_required"] != false {
		t.Fatalf("implementation guidance = %+v", implementation)
	}
	edge := categorizer.GetIterationGuidance(ErrorCategoryEdgeCase)
	if edge["stage"] != "Eval" || edge["action"] != "Add new test only" {
		t.Fatalf("edge guidance = %+v", edge)
	}
	unknown := categorizer.GetIterationGuidance("nope")
	if unknown["stage"] != "Unknown" {
		t.Fatalf("unknown guidance = %+v", unknown)
	}
}

// Parity-exempt: Go-only guard for the full upstream pattern tables. The
// category precedence (logic > implementation > edge > default) is the part
// that changes behaviour, so pin one representative per pattern family.
func TestCategorizerPatternTables(t *testing.T) {
	categorizer := NewErrorCategorizer()
	cases := []struct {
		name    string
		message string
		want    ErrorCategory
	}{
		{"logic constraint core", "constraint violated: core requirement", ErrorCategoryLogic},
		{"logic criteria", "criteria not met", ErrorCategoryLogic},
		{"logic requirement", "requirement mismatch in spec", ErrorCategoryLogic},
		{"impl KeyError", "KeyError: 'x'", ErrorCategoryImplementation},
		{"impl assertion", "assertion failed: 1 != 2", ErrorCategoryImplementation},
		{"impl expected but got", "expected 3 but got 4", ErrorCategoryImplementation},
		{"impl tool call", "tool call failed after 2 attempts", ErrorCategoryImplementation},
		{"edge boundary", "boundary condition at 0", ErrorCategoryEdgeCase},
		{"edge rate limit", "rate limit exceeded", ErrorCategoryEdgeCase},
		{"edge encoding", "encoding error on read", ErrorCategoryEdgeCase},
		{"default", "something completely unrelated", ErrorCategoryImplementation},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := NewTestResult("t1", false, 1)
			result.ErrorMessage = stringPointer(testCase.message)
			category := categorizer.Categorize(result)
			if category == nil || *category != testCase.want {
				t.Fatalf("message %q -> %v, want %s", testCase.message, category, testCase.want)
			}
		})
	}
}

// Parity-exempt: Go-only guard for the log-derived error text and upstream's
// "msg" key (a log entry with "message" must not be analysed).
func TestCategorizeFromRuntimeLogs(t *testing.T) {
	categorizer := NewErrorCategorizer()
	result := NewTestResult("t1", false, 1)
	result.RuntimeLogs = []map[string]any{
		{"level": "INFO", "msg": "goal not achieved"},
		{"level": "ERROR", "msg": "connection timeout"},
	}
	category := categorizer.Categorize(result)
	if category == nil || *category != ErrorCategoryEdgeCase {
		t.Fatalf("category = %v, want edge_case from the ERROR msg", category)
	}

	result.RuntimeLogs = []map[string]any{
		{"level": "ERROR", "message": "goal not achieved"},
	}
	category = categorizer.Categorize(result)
	if category == nil || *category != ErrorCategoryImplementation {
		t.Fatalf("category = %v, want default implementation_error (message key ignored)", category)
	}
}

// Parity-exempt: Go-only guard for the confidence scoring formula and the
// logic > implementation > edge tie-break order.
func TestCategorizeConfidence(t *testing.T) {
	categorizer := NewErrorCategorizer()
	result := NewTestResult("t1", false, 1)
	result.ErrorMessage = stringPointer("goal not achieved; TypeError: bug")
	category, confidence := categorizer.CategorizeWithConfidence(result)
	if category == nil || *category != ErrorCategoryLogic {
		t.Fatalf("category = %v, want logic (logic wins ties)", category)
	}
	if confidence < 0.6999 || confidence > 0.7001 {
		t.Fatalf("confidence = %v, want 0.7 (0.5 + 0.5*0.4)", confidence)
	}

	result.ErrorMessage = stringPointer("nothing matches here")
	category, confidence = categorizer.CategorizeWithConfidence(result)
	if category == nil || *category != ErrorCategoryImplementation || confidence != 0.3 {
		t.Fatalf("default = %v/%v, want implementation/0.3", category, confidence)
	}
}
