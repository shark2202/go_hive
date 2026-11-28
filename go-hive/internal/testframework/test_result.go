package testframework

// Parity: core/framework/testing/test_result.py::TestResult

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// ErrorCategory classifies a failed test so the iteration strategy is clear:
// a wrong goal definition, an implementation bug, or a newly discovered edge
// case.
type ErrorCategory string

const (
	// ErrorCategoryLogic means the goal definition is wrong.
	ErrorCategoryLogic ErrorCategory = "logic_error"
	// ErrorCategoryImplementation means the agent code is buggy.
	ErrorCategoryImplementation ErrorCategory = "implementation_error"
	// ErrorCategoryEdgeCase means a valid new scenario was discovered.
	ErrorCategoryEdgeCase ErrorCategory = "edge_case"
)

// ErrorCategories lists every category in upstream declaration order.
var ErrorCategories = []ErrorCategory{ErrorCategoryLogic, ErrorCategoryImplementation, ErrorCategoryEdgeCase}

// TestResult is the outcome of one test execution.
type TestResult struct {
	TestID     string `json:"test_id"`
	Passed     bool   `json:"passed"`
	DurationMS int    `json:"duration_ms"`

	ActualOutput   any `json:"actual_output"`
	ExpectedOutput any `json:"expected_output"`

	ErrorMessage  *string        `json:"error_message"`
	ErrorCategory *ErrorCategory `json:"error_category"`
	StackTrace    *string        `json:"stack_trace"`

	RuntimeLogs   []map[string]any `json:"runtime_logs"`
	NodeOutputs   map[string]any   `json:"node_outputs"`
	ExecutionPath []string         `json:"execution_path"`

	RunID     *string   `json:"run_id"`
	Timestamp Timestamp `json:"timestamp"`

	// Extra preserves unknown keys, mirroring model_config extra="allow".
	Extra map[string]json.RawMessage `json:"-"`
}

// NewTestResult builds a passed/failed result with upstream defaults (empty
// logs/node outputs/execution path, fresh timestamp, duration of 0).
func NewTestResult(testID string, passed bool, durationMS int) *TestResult {
	return &TestResult{
		TestID:        testID,
		Passed:        passed,
		DurationMS:    durationMS,
		RuntimeLogs:   []map[string]any{},
		NodeOutputs:   map[string]any{},
		ExecutionPath: []string{},
		Timestamp:     Timestamp{Now()},
	}
}

// SummaryDict returns the compact overview upstream uses for quick reporting.
func (r *TestResult) SummaryDict() map[string]any {
	var category any
	if r.ErrorCategory != nil {
		category = string(*r.ErrorCategory)
	}
	var message any
	if r.ErrorMessage != nil {
		message = truncateRunes(*r.ErrorMessage, 100)
	}
	return map[string]any{
		"test_id":        r.TestID,
		"passed":         r.Passed,
		"duration_ms":    r.DurationMS,
		"error_category": category,
		"error_message":  message,
	}
}

// MarshalJSON emits declared fields plus preserved extras.
func (r TestResult) MarshalJSON() ([]byte, error) {
	type alias TestResult
	return marshalWithExtras(alias(r), r.Extra)
}

// UnmarshalJSON applies pydantic defaults, rejects missing required fields,
// and validates the non-negative duration.
func (r *TestResult) UnmarshalJSON(data []byte) error {
	type alias TestResult
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for _, field := range []string{"test_id", "passed", "duration_ms"} {
		if _, ok := presence[field]; !ok {
			return &ValidationError{Field: field, Problem: "field required"}
		}
	}
	extras, err := decodeExtras(data, decoded)
	if err != nil {
		return err
	}
	decoded.Extra = extras
	if decoded.DurationMS < 0 {
		return &ValidationError{Field: "duration_ms", Problem: "must be greater than or equal to 0"}
	}
	if decoded.ErrorCategory != nil && !validErrorCategory(*decoded.ErrorCategory) {
		return &ValidationError{Field: "error_category", Problem: fmt.Sprintf("invalid value %q", *decoded.ErrorCategory)}
	}
	if decoded.RuntimeLogs == nil {
		decoded.RuntimeLogs = []map[string]any{}
	}
	if decoded.NodeOutputs == nil {
		decoded.NodeOutputs = map[string]any{}
	}
	if decoded.ExecutionPath == nil {
		decoded.ExecutionPath = []string{}
	}
	if decoded.Timestamp.IsZero() {
		decoded.Timestamp = Timestamp{Now()}
	}
	*r = TestResult(decoded)
	return nil
}

// TestSuiteResult aggregates a suite run for one goal.
type TestSuiteResult struct {
	GoalID     string       `json:"goal_id"`
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	Errors     int          `json:"errors"`
	Skipped    int          `json:"skipped"`
	Results    []TestResult `json:"results"`
	DurationMS int          `json:"duration_ms"`
	Timestamp  Timestamp    `json:"timestamp"`

	// Extra preserves unknown keys, mirroring model_config extra="allow".
	Extra map[string]json.RawMessage `json:"-"`
}

// AllPassed reports whether the suite had no failures and no errors.
func (s *TestSuiteResult) AllPassed() bool {
	return s.Failed == 0 && s.Errors == 0
}

// PassRate returns passed/total, or 0 for an empty suite.
func (s *TestSuiteResult) PassRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Total)
}

// SummaryDict renders the reporting shape upstream returns.
func (s *TestSuiteResult) SummaryDict() map[string]any {
	return map[string]any{
		"goal_id":        s.GoalID,
		"overall_passed": s.AllPassed(),
		"summary": map[string]any{
			"total":   s.Total,
			"passed":  s.Passed,
			"failed":  s.Failed,
			"errors":  s.Errors,
			"skipped": s.Skipped,
		},
		"pass_rate":   fmt.Sprintf("%.1f%%", s.PassRate()*100),
		"duration_ms": s.DurationMS,
	}
}

// GetFailedResults returns the failed results for debugging.
func (s *TestSuiteResult) GetFailedResults() []TestResult {
	out := []TestResult{}
	for _, result := range s.Results {
		if !result.Passed {
			out = append(out, result)
		}
	}
	return out
}

// GetResultsByCategory returns failed results in the given category.
func (s *TestSuiteResult) GetResultsByCategory(category ErrorCategory) []TestResult {
	out := []TestResult{}
	for _, result := range s.Results {
		if result.Passed || result.ErrorCategory == nil {
			continue
		}
		if *result.ErrorCategory == category {
			out = append(out, result)
		}
	}
	return out
}

// MarshalJSON emits declared fields plus preserved extras.
func (s TestSuiteResult) MarshalJSON() ([]byte, error) {
	type alias TestSuiteResult
	return marshalWithExtras(alias(s), s.Extra)
}

// UnmarshalJSON applies pydantic defaults for the optional counters.
func (s *TestSuiteResult) UnmarshalJSON(data []byte) error {
	type alias TestSuiteResult
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for _, field := range []string{"goal_id", "total", "passed", "failed"} {
		if _, ok := presence[field]; !ok {
			return &ValidationError{Field: field, Problem: "field required"}
		}
	}
	extras, err := decodeExtras(data, decoded)
	if err != nil {
		return err
	}
	decoded.Extra = extras
	if decoded.Results == nil {
		decoded.Results = []TestResult{}
	}
	if decoded.Timestamp.IsZero() {
		decoded.Timestamp = Timestamp{Now()}
	}
	*s = TestSuiteResult(decoded)
	return nil
}

func validErrorCategory(value ErrorCategory) bool {
	for _, candidate := range ErrorCategories {
		if value == candidate {
			return true
		}
	}
	return false
}

// truncateRunes keeps the first limit characters (Python string slicing is
// character-based, not byte-based).
func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}
