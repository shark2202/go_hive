package longmemeval

import (
	"path/filepath"
	"strings"
	"testing"
)

func writeRows(t *testing.T, rows ...map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "results.jsonl")
	for _, row := range rows {
		if err := AppendRow(path, row); err != nil {
			t.Fatalf("AppendRow: %v", err)
		}
	}
	return path
}

func summaryRow(qtype string, correct bool, extra map[string]any) map[string]any {
	row := map[string]any{
		"question_id":        qtype,
		"question_type":      qtype,
		"abstention":         false,
		"question":           "q",
		"answer":             "a",
		"hypothesis":         "h",
		"correct":            correct,
		"judge":              "yes",
		"judge_model":        "judge-model",
		"reader_model":       "reader-model",
		"searched":           true,
		"search_calls":       []map[string]any{},
		"evidence_retrieved": nil,
		"tools":              []string{},
		"seconds":            float64(10),
	}
	for key, value := range extra {
		row[key] = value
	}
	return row
}

// Parity-exempt: Go-side coverage for tests/e2e/longmemeval.py::summarize
// (overall accuracy, Wilson interval, task average, per-type table).
func TestSummarizeOverallAndPerType(t *testing.T) {
	path := writeRows(t,
		summaryRow("multi-session", true, map[string]any{"seconds": float64(10), "evidence_retrieved": true}),
		summaryRow("multi-session", true, map[string]any{"seconds": float64(20), "evidence_retrieved": false}),
		summaryRow("temporal-reasoning", false, map[string]any{"seconds": float64(30)}),
		summaryRow("temporal-reasoning", false, map[string]any{"seconds": float64(40)}),
	)
	report, summary, err := Summarize(path, "LongMemEval-S through Hive")
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	lines := strings.Split(report, "\n")
	if lines[0] != "LongMemEval-S through Hive - 4 question(s), reader reader-model, judge judge-model" {
		t.Fatalf("title line = %q", lines[0])
	}
	// wilson(2, 4, 1.96) = [0.15004, 0.84996].
	if lines[1] != "overall 50.0% (95% CI 15.0-85.0), task-averaged 50.0%" {
		t.Fatalf("overall line = %q", lines[1])
	}
	if !strings.Contains(report, "multi-session") || !strings.Contains(report, " 100.0%") {
		t.Fatalf("per-type table missing the perfect type:\n%s", report)
	}
	if !strings.Contains(report, "median seconds per question: 25") {
		t.Fatalf("median missing:\n%s", report)
	}
	if summary == nil || summary.Questions != 4 || summary.Correct != 2 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.PerType["multi-session"].Correct != 2 || *summary.PerType["multi-session"].Evidence != 0.5 {
		t.Fatalf("multi-session summary = %+v", summary.PerType["multi-session"])
	}
	if !summary.Diagnostics {
		t.Fatal("retrieval diagnostics not reported for a Hive run")
	}
}

// Parity-exempt: abstention, error, ungraded, and retrieval-diagnostic lines
// mirror the upstream report.
func TestSummarizeAbstentionAndFailureLines(t *testing.T) {
	path := writeRows(t,
		summaryRow("single-session-user", true, map[string]any{"abstention": true}),
		summaryRow("single-session-user", false, map[string]any{"error": "timeout"}),
		summaryRow("knowledge-update", false, map[string]any{"judge_error": "judge exploded"}),
	)
	report, summary, err := Summarize(path, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"abstention questions: 100.0% of 1",
		"no gradable answer (timeout/error, counted wrong): 1",
		"answered but not graded (judge failed; counted wrong until `rejudge`): 1",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if summary == nil || summary.Errors != 1 || summary.Ungraded != 1 || summary.AbstentionN == nil || *summary.AbstentionN != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

// Parity-exempt: an empty results file yields no report rather than a
// zero-division panic.
func TestSummarizeEmpty(t *testing.T) {
	report, summary, err := Summarize(filepath.Join(t.TempDir(), "missing.jsonl"), "title")
	if err != nil || report != "" || summary != nil {
		t.Fatalf("empty = %q, %+v, %v", report, summary, err)
	}
}

// Parity-exempt: the Wilson helper is the ported upstream formula.
func TestWilsonBounds(t *testing.T) {
	if lo, hi := wilson(0, 0, 1.96); lo != 0 || hi != 0 {
		t.Fatalf("wilson(0,0) = %v, %v", lo, hi)
	}
	lo, hi := wilson(0, 10, 1.96)
	if lo > 1e-9 || hi <= 0 || hi >= 1 {
		t.Fatalf("wilson(0,10) = %v, %v", lo, hi)
	}
}
