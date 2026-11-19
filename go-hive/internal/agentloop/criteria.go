package agentloop

import (
	"strings"

	"github.com/aden-hive/hive-go/internal/schemas"
)

// SuccessCriteriaText compiles a goal/worker criteria list into the single
// natural-language paragraph AgentSpec carries. Blank entries are dropped, so
// an empty or whitespace-only list yields nil ("no criteria").
//
// Parity-exempt: upstream keeps Goal.success_criteria as a list and
// AgentSpec.success_criteria as the compiled str | None; the loader performs
// this compilation before the loop sees the spec.
func SuccessCriteriaText(criteria []string) *string {
	items := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		if text := strings.TrimSpace(criterion); text != "" {
			items = append(items, text)
		}
	}
	if len(items) == 0 {
		return nil
	}
	text := strings.Join(items, "\n")
	return &text
}

// GoalCriteriaText compiles a structured goal's criteria descriptions into the
// paragraph the loop judge evaluates. It exists so a goal declared without a
// node-level success_criteria string still gets a judge: the criteria are the
// goal's, not a separate authoring surface.
//
// Parity-exempt: upstream compiles Goal.success_criteria in the loader only;
// this is the same compilation for a goal that reaches a runtime directly.
func GoalCriteriaText(goal *schemas.Goal) *string {
	if goal == nil {
		return nil
	}
	items := make([]string, 0, len(goal.SuccessCriteria))
	for _, criterion := range goal.SuccessCriteria {
		if text := strings.TrimSpace(criterion.Description); text != "" {
			items = append(items, text)
		}
	}
	if len(items) == 0 {
		return nil
	}
	text := strings.Join(items, "\n")
	return &text
}
