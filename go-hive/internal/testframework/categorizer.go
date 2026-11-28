package testframework

// Parity: core/framework/testing/categorizer.py::ErrorCategorizer

import (
	"regexp"
	"strings"
)

// LogicErrorPatterns indicate the goal/criteria definition is wrong.
var LogicErrorPatterns = []string{
	`goal not achieved`,
	`constraint violated:?\s*core`,
	`fundamental assumption`,
	`success criteria mismatch`,
	`criteria not met`,
	`expected behavior incorrect`,
	`specification error`,
	`requirement mismatch`,
}

// ImplementationErrorPatterns indicate a code/implementation bug.
var ImplementationErrorPatterns = []string{
	`TypeError`,
	`AttributeError`,
	`KeyError`,
	`IndexError`,
	`ValueError`,
	`NameError`,
	`ImportError`,
	`ModuleNotFoundError`,
	`RuntimeError`,
	`NullPointerException`,
	`NoneType.*has no attribute`,
	`tool call failed`,
	`node execution error`,
	`agent execution failed`,
	`assertion.*failed`,
	`AssertionError`,
	`expected.*but got`,
	`unexpected.*type`,
	`missing required`,
	`invalid.*argument`,
}

// EdgeCasePatterns indicate a new scenario rather than a broken goal or bug.
var EdgeCasePatterns = []string{
	`boundary condition`,
	`timeout`,
	`connection.*timeout`,
	`request.*timeout`,
	`unexpected format`,
	`unexpected response`,
	`rare input`,
	`empty.*result`,
	`null.*value`,
	`empty.*response`,
	`no.*results`,
	`rate.*limit`,
	`quota.*exceeded`,
	`retry.*exhausted`,
	`unicode.*error`,
	`encoding.*error`,
	`special.*character`,
}

// ErrorCategorizer classifies failures with upstream's case-insensitive
// pattern heuristics. Logic errors win over implementation errors, which win
// over edge cases; an unmatched failure defaults to implementation_error.
type ErrorCategorizer struct {
	logicPatterns []*regexp.Regexp
	implPatterns  []*regexp.Regexp
	edgePatterns  []*regexp.Regexp
}

// NewErrorCategorizer compiles the upstream pattern tables.
func NewErrorCategorizer() *ErrorCategorizer {
	return &ErrorCategorizer{
		logicPatterns: compilePatterns(LogicErrorPatterns),
		implPatterns:  compilePatterns(ImplementationErrorPatterns),
		edgePatterns:  compilePatterns(EdgeCasePatterns),
	}
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		compiled = append(compiled, regexp.MustCompile("(?i)"+pattern))
	}
	return compiled
}

// Categorize returns the category for a failed result and nil for a pass.
func (c *ErrorCategorizer) Categorize(result *TestResult) *ErrorCategory {
	if result == nil || result.Passed {
		return nil
	}
	text := errorText(result)
	for _, pattern := range c.logicPatterns {
		if pattern.MatchString(text) {
			return categoryPointer(ErrorCategoryLogic)
		}
	}
	for _, pattern := range c.implPatterns {
		if pattern.MatchString(text) {
			return categoryPointer(ErrorCategoryImplementation)
		}
	}
	for _, pattern := range c.edgePatterns {
		if pattern.MatchString(text) {
			return categoryPointer(ErrorCategoryEdgeCase)
		}
	}
	return categoryPointer(ErrorCategoryImplementation)
}

// CategorizeWithConfidence returns the category plus the upstream confidence
// score (1.0 for a pass, 0.3 when nothing matched).
func (c *ErrorCategorizer) CategorizeWithConfidence(result *TestResult) (*ErrorCategory, float64) {
	if result == nil || result.Passed {
		return nil, 1.0
	}
	text := errorText(result)
	logicMatches := countMatches(c.logicPatterns, text)
	implMatches := countMatches(c.implPatterns, text)
	edgeMatches := countMatches(c.edgePatterns, text)
	total := logicMatches + implMatches + edgeMatches
	if total == 0 {
		return categoryPointer(ErrorCategoryImplementation), 0.3
	}
	if logicMatches >= implMatches && logicMatches >= edgeMatches {
		return categoryPointer(ErrorCategoryLogic), confidenceFor(logicMatches, total)
	}
	if implMatches >= logicMatches && implMatches >= edgeMatches {
		return categoryPointer(ErrorCategoryImplementation), confidenceFor(implMatches, total)
	}
	return categoryPointer(ErrorCategoryEdgeCase), confidenceFor(edgeMatches, total)
}

func countMatches(patterns []*regexp.Regexp, text string) int {
	matches := 0
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			matches++
		}
	}
	return matches
}

func confidenceFor(matches, total int) float64 {
	confidence := float64(matches) / float64(total)
	value := 0.5 + confidence*0.4
	if value > 0.9 {
		return 0.9
	}
	return value
}

// GetFixSuggestion returns the upstream human-readable remediation.
func (c *ErrorCategorizer) GetFixSuggestion(category ErrorCategory) string {
	switch category {
	case ErrorCategoryLogic:
		return "Review and update success_criteria or constraints in the Goal definition. " +
			"The goal specification may not accurately describe the desired behavior."
	case ErrorCategoryImplementation:
		return "Fix the code in agent nodes/edges. There's a bug in the implementation that needs to be corrected."
	case ErrorCategoryEdgeCase:
		return "Add a new test for this edge case scenario. This is a valid scenario that wasn't covered by existing tests."
	default:
		return "Review the test and agent implementation."
	}
}

// GetIterationGuidance returns the upstream stage/action/restart guidance.
// A fresh map is returned so callers cannot mutate the shared table.
func (c *ErrorCategorizer) GetIterationGuidance(category ErrorCategory) map[string]any {
	switch category {
	case ErrorCategoryLogic:
		return map[string]any{
			"stage":            "Goal",
			"action":           "Update success_criteria or constraints",
			"restart_required": true,
			"description": "The goal definition is incorrect. Update the success criteria or constraints, " +
				"then restart the full Goal → Agent → Eval flow.",
		}
	case ErrorCategoryImplementation:
		return map[string]any{
			"stage":            "Agent",
			"action":           "Fix nodes/edges implementation",
			"restart_required": false,
			"description":      "There's a code bug. Fix the agent implementation, then re-run Eval (skip Goal stage).",
		}
	case ErrorCategoryEdgeCase:
		return map[string]any{
			"stage":            "Eval",
			"action":           "Add new test only",
			"restart_required": false,
			"description":      "This is a new scenario. Add a test for it and continue in the Eval stage.",
		}
	default:
		return map[string]any{
			"stage":            "Unknown",
			"action":           "Review manually",
			"restart_required": false,
			"description":      "Unable to determine category. Manual review required.",
		}
	}
}

// errorText concatenates every error source upstream analyses. Note the log
// key is "msg" (not "message"), matching upstream's log.get("msg").
func errorText(result *TestResult) string {
	parts := make([]string, 0, 3+len(result.RuntimeLogs))
	if result.ErrorMessage != nil {
		parts = append(parts, *result.ErrorMessage)
	}
	if result.StackTrace != nil {
		parts = append(parts, *result.StackTrace)
	}
	for _, log := range result.RuntimeLogs {
		level, _ := log["level"].(string)
		switch level {
		case "ERROR", "CRITICAL", "WARNING":
			message, _ := log["msg"].(string)
			parts = append(parts, message)
		}
	}
	return strings.Join(parts, " ")
}

func categoryPointer(category ErrorCategory) *ErrorCategory {
	return &category
}
