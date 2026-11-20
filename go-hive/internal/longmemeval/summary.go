package longmemeval

// The end-of-run report: overall accuracy with a 95% Wilson interval,
// per-type accuracy, and retrieval diagnostics.
//
// Parity: tests/e2e/longmemeval.py::summarize
// Parity: tests/e2e/longmemeval.py::_wilson

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// TypeSummary is one question type's slice of the report.
type TypeSummary struct {
	N        int      `json:"n"`
	Correct  int      `json:"correct"`
	Accuracy *float64 `json:"accuracy"`
	Searched *float64 `json:"searched,omitempty"`
	Evidence *float64 `json:"evidence,omitempty"`
}

// Summary is the machine-readable report written next to summary.txt.
type Summary struct {
	Title        string                 `json:"title"`
	Questions    int                    `json:"questions"`
	Correct      int                    `json:"correct"`
	Accuracy     float64                `json:"accuracy"`
	CI95         [2]float64             `json:"ci95"`
	TaskAveraged float64                `json:"task_averaged"`
	PerType      map[string]TypeSummary `json:"per_type"`
	JudgeModel   string                 `json:"judge_model"`
	ReaderModel  string                 `json:"reader_model,omitempty"`
	AbstentionN  *int                   `json:"abstention_n,omitempty"`
	AbstentionOK *int                   `json:"abstention_correct,omitempty"`
	Errors       int                    `json:"errors"`
	Ungraded     int                    `json:"ungraded"`
	MedianSec    *float64               `json:"median_seconds,omitempty"`
	Diagnostics  bool                   `json:"retrieval_diagnostics"`
}

// reportStats is everything the text report and the JSON summary share.
type reportStats struct {
	rows        []map[string]any
	groups      map[string][]map[string]any
	types       []string
	judgeModel  string
	readerModel string
	correct     int
	lo, hi      float64
	taskAvg     float64
	hiveRun     bool
}

// Summarize reads a results file and returns the text report plus the
// structured summary. Empty results yield an empty report.
func Summarize(path string, title string) (string, *Summary, error) {
	rows, err := ReadResults(path)
	if err != nil {
		return "", nil, err
	}
	if len(rows) == 0 {
		return "", nil, nil
	}
	stats := collectReportStats(rows)
	report := strings.Join(stats.render(title), "\n")
	return report, stats.buildSummary(title), nil
}

func collectReportStats(rows []map[string]any) *reportStats {
	stats := &reportStats{rows: rows, groups: map[string][]map[string]any{}}
	for _, row := range rows {
		qtype := stringField(row, "question_type")
		stats.groups[qtype] = append(stats.groups[qtype], row)
	}
	for qtype := range stats.groups {
		stats.types = append(stats.types, qtype)
	}
	sort.Strings(stats.types)
	for _, row := range rows {
		if stats.judgeModel == "" {
			stats.judgeModel = stringField(row, "judge_model")
		}
		if stats.readerModel == "" {
			stats.readerModel = stringField(row, "reader_model")
		}
		if _, ok := row["searched"]; ok {
			stats.hiveRun = true
		}
		if boolField(row, "correct") {
			stats.correct++
		}
	}
	if stats.judgeModel == "" {
		stats.judgeModel = "?"
	}
	stats.lo, stats.hi = wilson(stats.correct, len(rows), 1.96)
	for _, qtype := range stats.types {
		rs := stats.groups[qtype]
		correct, _, _ := rowCounts(rs)
		stats.taskAvg += float64(correct) / float64(len(rs))
	}
	stats.taskAvg /= float64(len(stats.types))
	return stats
}

func (s *reportStats) render(title string) []string {
	if strings.TrimSpace(title) == "" {
		title = "LongMemEval through Hive"
	}
	var lines []string
	header := fmt.Sprintf("%s - %d question(s), ", title, len(s.rows))
	if s.readerModel != "" {
		header += fmt.Sprintf("reader %s, ", s.readerModel)
	}
	header += fmt.Sprintf("judge %s", s.judgeModel)
	lines = append(lines, header)
	lines = append(lines, fmt.Sprintf("overall %.1f%% (95%% CI %.1f-%.1f), task-averaged %.1f%%",
		100*float64(s.correct)/float64(len(s.rows)), 100*s.lo, 100*s.hi, 100*s.taskAvg))
	lines = append(lines, s.tableHeader())

	for _, qtype := range append(append([]string{}, s.types...), "ALL") {
		rs := s.rows
		if qtype != "ALL" {
			rs = s.groups[qtype]
		}
		lines = append(lines, s.typeLine(qtype, rs))
	}
	lines = append(lines, s.tailLines()...)
	return lines
}

func (s *reportStats) tableHeader() string {
	header := fmt.Sprintf("%-28s %4s %8s", "type", "n", "correct")
	if s.hiveRun {
		header += fmt.Sprintf(" %9s %9s", "searched", "evidence")
	}
	return header
}

func (s *reportStats) typeLine(qtype string, rs []map[string]any) string {
	correct, searched, found := rowCounts(rs)
	line := fmt.Sprintf("%-28s %4d %8s", qtype, len(rs), pctWidth(correct, len(rs), 8))
	if s.hiveRun {
		evidence := 0
		for _, row := range found {
			if boolField(row, "evidence_retrieved") {
				evidence++
			}
		}
		line += fmt.Sprintf(" %9s %9s", pctWidth(searched, len(rs), 9), pctWidth(evidence, len(found), 9))
	}
	return line
}

func (s *reportStats) tailLines() []string {
	var lines []string
	var abstainRows []map[string]any
	errors, ungraded := 0, 0
	var seconds []float64
	for _, row := range s.rows {
		if boolField(row, "abstention") {
			abstainRows = append(abstainRows, row)
		}
		if stringField(row, "error") != "" {
			errors++
		}
		if stringField(row, "judge_error") != "" {
			ungraded++
		}
		if value, ok := numberField(row, "seconds"); ok {
			seconds = append(seconds, value)
		}
	}
	if len(abstainRows) > 0 {
		correct, _, _ := rowCounts(abstainRows)
		lines = append(lines, fmt.Sprintf("abstention questions: %s of %d",
			strings.TrimSpace(pctString(correct, len(abstainRows))), len(abstainRows)))
	}
	if errors > 0 {
		lines = append(lines, fmt.Sprintf("no gradable answer (timeout/error, counted wrong): %d", errors))
	}
	if ungraded > 0 {
		lines = append(lines, fmt.Sprintf("answered but not graded (judge failed; counted wrong until `rejudge`): %d", ungraded))
	}
	if len(seconds) > 0 {
		lines = append(lines, fmt.Sprintf("median seconds per question: %.0f", median(seconds)))
	}
	if s.hiveRun {
		lines = append(lines, "searched = called search_messages or search_timeline; evidence = an answer session appeared in their results "+
			"(auto-injected excerpts don't count, so 'correct' can exceed both)")
	}
	return lines
}

func (s *reportStats) buildSummary(title string) *Summary {
	if strings.TrimSpace(title) == "" {
		title = "LongMemEval through Hive"
	}
	summary := &Summary{
		Title:        title,
		Questions:    len(s.rows),
		Correct:      s.correct,
		Accuracy:     float64(s.correct) / float64(len(s.rows)),
		CI95:         [2]float64{s.lo, s.hi},
		TaskAveraged: s.taskAvg,
		PerType:      map[string]TypeSummary{},
		JudgeModel:   s.judgeModel,
		ReaderModel:  s.readerModel,
		Diagnostics:  s.hiveRun,
	}
	for _, qtype := range s.types {
		rs := s.groups[qtype]
		correct, searched, found := rowCounts(rs)
		entry := TypeSummary{N: len(rs), Correct: correct}
		accuracy := float64(correct) / float64(len(rs))
		entry.Accuracy = &accuracy
		if s.hiveRun {
			searchedRate := float64(searched) / float64(len(rs))
			entry.Searched = &searchedRate
			if len(found) > 0 {
				evidence := 0
				for _, row := range found {
					if boolField(row, "evidence_retrieved") {
						evidence++
					}
				}
				evidenceRate := float64(evidence) / float64(len(found))
				entry.Evidence = &evidenceRate
			}
		}
		summary.PerType[qtype] = entry
	}

	var abstainRows []map[string]any
	var seconds []float64
	for _, row := range s.rows {
		if boolField(row, "abstention") {
			abstainRows = append(abstainRows, row)
		}
		if stringField(row, "error") != "" {
			summary.Errors++
		}
		if stringField(row, "judge_error") != "" {
			summary.Ungraded++
		}
		if value, ok := numberField(row, "seconds"); ok {
			seconds = append(seconds, value)
		}
	}
	if len(abstainRows) > 0 {
		correct, _, _ := rowCounts(abstainRows)
		abstainN := len(abstainRows)
		summary.AbstentionN = &abstainN
		summary.AbstentionOK = &correct
	}
	if len(seconds) > 0 {
		medianSec := median(seconds)
		summary.MedianSec = &medianSec
	}
	return summary
}

// rowCounts returns (correct, searched, rows carrying an evidence diagnostic).
func rowCounts(rows []map[string]any) (int, int, []map[string]any) {
	correct, searched := 0, 0
	found := make([]map[string]any, 0)
	for _, row := range rows {
		if boolField(row, "correct") {
			correct++
		}
		if boolField(row, "searched") {
			searched++
		}
		if value, ok := row["evidence_retrieved"]; ok && value != nil {
			found = append(found, row)
		}
	}
	return correct, searched, found
}

// wilson is the 95% Wilson interval for k successes out of n.
//
// Parity: tests/e2e/longmemeval.py::_wilson
func wilson(k, n int, z float64) (float64, float64) {
	if n == 0 {
		return 0, 0
	}
	p := float64(k) / float64(n)
	centre := (p + z*z/(2*float64(n))) / (1 + z*z/float64(n))
	half := z * math.Sqrt(p*(1-p)/float64(n)+z*z/(4*float64(n)*float64(n))) / (1 + z*z/float64(n))
	return centre - half, centre + half
}

// pctString mirrors upstream's pct: 5-wide percentage or "    -".
func pctString(n, d int) string {
	if d == 0 {
		return "    -"
	}
	return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(d))
}

func pctWidth(n, d, width int) string {
	return fmt.Sprintf("%*s", width, pctString(n, d))
}

func median(values []float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func stringField(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

func boolField(row map[string]any, key string) bool {
	value, _ := row[key].(bool)
	return value
}

func numberField(row map[string]any, key string) (float64, bool) {
	switch value := row[key].(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	}
	return 0, false
}
