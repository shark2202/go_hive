package llmjudge

// Prompt parity: the judge prompt both implementations send to the model for
// the same contract. The golden is the Python reference's prompt template
// (framework/testing/llm_judge.py::LLMJudge.evaluate) with %s placeholders, so
// the two sides can be compared without either tree being present.
//
// Regenerate (from the repository root):
//
//	uv run --no-project python go_hive/scripts/judge_prompt_golden.py --write
//
// The golden is also re-verified against the Python source by
// go_hive/scripts/judge_scope_probe.py, which is what keeps this test honest if
// the reference changes without the golden being refreshed.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const judgePromptGoldenPath = "testdata/prompt-parity/judge.prompt.txt"

// renderJudgeTemplate renders buildPrompt with %s as every argument, so the
// result is the prompt template itself: exactly what the golden stores.
func renderJudgeTemplate() string {
	return buildPrompt("%s", "%s", "%s", "%s")
}

// Parity: core/framework/testing/llm_judge.py::LLMJudge.evaluate
func TestJudgePromptMatchesPythonGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join(judgePromptGoldenPath))
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with go_hive/scripts/judge_prompt_golden.py --write)", err)
	}
	rendered := renderJudgeTemplate()
	if rendered != string(golden) {
		t.Fatalf(
			"judge prompt diverged from the Python reference golden:\n  want: %s\n  got:  %s",
			strconv.Quote(string(golden)),
			strconv.Quote(rendered),
		)
	}
}

// TestJudgePromptKeepsTheContractSections guards the structure the golden pins:
// dropping a section would otherwise only surface as a behaviour change at the
// model, not as a build failure.
// Parity: core/framework/testing/llm_judge.py::LLMJudge.evaluate
func TestJudgePromptKeepsTheContractSections(t *testing.T) {
	rendered := renderJudgeTemplate()
	for _, section := range []string{
		"You are evaluating whether a summary meets a specific constraint.",
		"CONSTRAINT: %s",
		"CRITERIA: %s",
		"SOURCE DOCUMENT:\n%s",
		"SUMMARY TO EVALUATE:\n%s",
		`Respond with JSON: {"passes": true/false, "explanation": "..."}`,
	} {
		if !strings.Contains(rendered, section) {
			t.Fatalf("judge prompt lost section %q:\n%s", section, rendered)
		}
	}
}
