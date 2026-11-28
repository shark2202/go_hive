package testframework

// Parity: core/framework/testing/debug_tool.py::DebugTool

import "fmt"

// RuntimeReport is the subset of a Runtime run the debug report exposes:
// which nodes executed, the decisions/problems recorded, and the run status.
type RuntimeReport struct {
	ExecutionPath []string
	Decisions     []any
	Problems      []any
	Status        string
}

// RuntimeStorage is the synchronous run loader upstream calls
// load_run_sync(run_id) on.
type RuntimeStorage interface {
	LoadRunSync(runID string) (*RuntimeReport, error)
}

// DebugInfo is the comprehensive debug report for a failed test.
type DebugInfo struct {
	TestID   string `json:"test_id"`
	TestName string `json:"test_name"`

	Input    map[string]any `json:"input"`
	Expected map[string]any `json:"expected"`

	Actual any  `json:"actual"`
	Passed bool `json:"passed"`

	ErrorMessage  *string `json:"error_message"`
	ErrorCategory *string `json:"error_category"`
	StackTrace    *string `json:"stack_trace"`

	Logs        []map[string]any `json:"logs"`
	RuntimeData map[string]any   `json:"runtime_data"`

	SuggestedFix      *string        `json:"suggested_fix"`
	IterationGuidance map[string]any `json:"iteration_guidance"`
}

func newDebugInfo(testID, testName string) *DebugInfo {
	return &DebugInfo{
		TestID:            testID,
		TestName:          testName,
		Input:             map[string]any{},
		Expected:          map[string]any{},
		Logs:              []map[string]any{},
		RuntimeData:       map[string]any{},
		IterationGuidance: map[string]any{},
	}
}

// ToDict renders the report as the dict upstream's to_dict returns.
func (d *DebugInfo) ToDict() map[string]any {
	return map[string]any{
		"test_id":            d.TestID,
		"test_name":          d.TestName,
		"input":              d.Input,
		"expected":           d.Expected,
		"actual":             d.Actual,
		"passed":             d.Passed,
		"error_message":      d.ErrorMessage,
		"error_category":     d.ErrorCategory,
		"stack_trace":        d.StackTrace,
		"logs":               d.Logs,
		"runtime_data":       d.RuntimeData,
		"suggested_fix":      d.SuggestedFix,
		"iteration_guidance": d.IterationGuidance,
	}
}

// DebugTool builds debug reports from stored tests, their latest result, and
// an optional Runtime loader.
type DebugTool struct {
	storage     *TestStorage
	runtime     RuntimeStorage
	categorizer *ErrorCategorizer
}

// NewDebugTool wires a debug tool to the test store and an optional Runtime
// loader (pass nil when no run data is available).
func NewDebugTool(storage *TestStorage, runtime RuntimeStorage) *DebugTool {
	return &DebugTool{storage: storage, runtime: runtime, categorizer: NewErrorCategorizer()}
}

// Categorizer exposes the classifier used for uncategorized failures.
func (d *DebugTool) Categorizer() *ErrorCategorizer { return d.categorizer }

// Analyze loads a test and its latest result and returns the debug report.
// A missing test is reported in the returned DebugInfo (upstream behavior), not
// as an error.
func (d *DebugTool) Analyze(goalID, testID, runID string) (*DebugInfo, error) {
	test, err := d.storage.LoadTest(goalID, testID)
	if err != nil {
		return nil, err
	}
	if test == nil {
		info := newDebugInfo(testID, "unknown")
		message := fmt.Sprintf("Test %s not found in goal %s", testID, goalID)
		info.ErrorMessage = &message
		return info, nil
	}
	result, err := d.storage.GetLatestResult(testID)
	if err != nil {
		return nil, err
	}
	return d.build(test, result, runID), nil
}

// AnalyzeResult builds a report directly from a test and a result, without
// touching storage.
func (d *DebugTool) AnalyzeResult(test *Test, result *TestResult, runID string) *DebugInfo {
	return d.build(test, result, runID)
}

func (d *DebugTool) build(test *Test, result *TestResult, runID string) *DebugInfo {
	info := newDebugInfo(test.ID, test.TestName)
	info.Input = nonNilMap(test.Input)
	info.Expected = nonNilMap(test.ExpectedOutput)
	if result != nil {
		info.Actual = result.ActualOutput
		info.Passed = result.Passed
		info.ErrorMessage = result.ErrorMessage
		info.StackTrace = result.StackTrace
		info.Logs = nonNilLogs(result.RuntimeLogs)
		if result.ErrorCategory != nil {
			category := string(*result.ErrorCategory)
			info.ErrorCategory = &category
		} else if !result.Passed {
			if category := d.categorizer.Categorize(result); category != nil {
				value := string(*category)
				info.ErrorCategory = &value
			}
		}
	}
	if runID != "" && d.runtime != nil {
		info.RuntimeData = d.runtimeData(runID)
	}
	if info.ErrorCategory != nil {
		category := ErrorCategory(*info.ErrorCategory)
		suggestion := d.categorizer.GetFixSuggestion(category)
		info.SuggestedFix = &suggestion
		info.IterationGuidance = d.categorizer.GetIterationGuidance(category)
	}
	return info
}

func (d *DebugTool) runtimeData(runID string) map[string]any {
	run, err := d.runtime.LoadRunSync(runID)
	if err != nil {
		return map[string]any{"error": "Failed to load runtime data: " + err.Error()}
	}
	if run == nil {
		return map[string]any{"error": "Run " + runID + " not found"}
	}
	status := run.Status
	if status == "" {
		status = "unknown"
	}
	return map[string]any{
		"execution_path": nonNilStrings(run.ExecutionPath),
		"decisions":      nonNilAny(run.Decisions),
		"problems":       nonNilAny(run.Problems),
		"status":         status,
	}
}

// FailureSummary aggregates a goal's failed tests by category.
type FailureSummary struct {
	GoalID               string              `json:"goal_id"`
	TotalFailures        int                 `json:"total_failures"`
	ByCategory           map[string][]string `json:"by_category"`
	IterationSuggestions []string            `json:"iteration_suggestions"`
}

// GetFailureSummary groups a goal's failed tests by the category recorded on
// their latest result (uncategorized when the result has none).
func (d *DebugTool) GetFailureSummary(goalID string) (*FailureSummary, error) {
	tests, err := d.storage.GetTestsByGoal(goalID)
	if err != nil {
		return nil, err
	}
	byCategory := map[string][]string{
		string(ErrorCategoryLogic):          {},
		string(ErrorCategoryImplementation): {},
		string(ErrorCategoryEdgeCase):       {},
		"uncategorized":                     {},
	}
	for _, test := range tests {
		if test.LastResult == nil || *test.LastResult != "failed" {
			continue
		}
		result, err := d.storage.GetLatestResult(test.ID)
		if err != nil {
			return nil, err
		}
		key := "uncategorized"
		if result != nil && result.ErrorCategory != nil {
			key = string(*result.ErrorCategory)
		}
		byCategory[key] = append(byCategory[key], test.ID)
	}
	total := 0
	for _, ids := range byCategory {
		total += len(ids)
	}
	return &FailureSummary{
		GoalID:               goalID,
		TotalFailures:        total,
		ByCategory:           byCategory,
		IterationSuggestions: iterationSuggestions(byCategory),
	}, nil
}

func iterationSuggestions(byCategory map[string][]string) []string {
	suggestions := []string{}
	if len(byCategory[string(ErrorCategoryLogic)]) > 0 {
		suggestions = append(suggestions, fmt.Sprintf(
			"Found %d logic errors. Review and update Goal success_criteria/constraints, then restart the full Goal → Agent → Eval flow.",
			len(byCategory[string(ErrorCategoryLogic)]),
		))
	}
	if len(byCategory[string(ErrorCategoryImplementation)]) > 0 {
		suggestions = append(suggestions, fmt.Sprintf(
			"Found %d implementation errors. Fix agent node/edge code and re-run Eval.",
			len(byCategory[string(ErrorCategoryImplementation)]),
		))
	}
	if len(byCategory[string(ErrorCategoryEdgeCase)]) > 0 {
		suggestions = append(suggestions, fmt.Sprintf(
			"Found %d edge cases. These are new scenarios - add tests for them.",
			len(byCategory[string(ErrorCategoryEdgeCase)]),
		))
	}
	if len(byCategory["uncategorized"]) > 0 {
		suggestions = append(suggestions, fmt.Sprintf(
			"Found %d uncategorized failures. Manual review required.",
			len(byCategory["uncategorized"]),
		))
	}
	return suggestions
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nonNilLogs(value []map[string]any) []map[string]any {
	if value == nil {
		return []map[string]any{}
	}
	return value
}

func nonNilStrings(value []string) []string {
	if value == nil {
		return []string{}
	}
	return value
}

func nonNilAny(value []any) []any {
	if value == nil {
		return []any{}
	}
	return value
}
