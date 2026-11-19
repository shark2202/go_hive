package agentloop

import "strings"

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
