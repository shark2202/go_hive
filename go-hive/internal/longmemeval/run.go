package longmemeval

// Run orchestration: for every selected question, seed an isolated Hive
// home with the haystack, ask the question through the real queen runtime,
// grade the reply, append the result, and report.
//
// Parity-exempt: Python drives this from pytest (`test_longmemeval.py`), one
// test per question with xdist parallelism; this port is a single-process
// Go command, so the per-question flow lives here instead.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logf logs one progress line.
type Logf func(format string, args ...any)

// Label renders the dataset label the report uses ("LongMemEval-S").
func Label(variant string) string {
	normalized, err := NormalizeVariant(variant)
	if err != nil {
		normalized = strings.ToLower(strings.TrimSpace(variant))
	}
	return "LongMemEval-" + strings.ToUpper(normalized)
}

// RunOptions configures one benchmark run.
type RunOptions struct {
	SplitDir  string
	RunDir    string
	Variant   string
	Questions string
	Resume    bool
	Queen     string
	// Timeout bounds one question's queen turn; zero means no bound.
	Timeout time.Duration
	// KeepHomes keeps each question's seeded Hive home after it finishes.
	KeepHomes bool
	// Model names the answering model the report prints (optional).
	Model string
	Asker Asker
	Judge *Judge
	Logf  Logf
}

// Run executes the selected questions and writes results.jsonl,
// transcripts/, summary.txt and summary.json under RunDir.
//
// Parity: tests/e2e/test_longmemeval.py::test_longmemeval
func Run(ctx context.Context, opts RunOptions) (*Summary, string, error) {
	if opts.Asker == nil {
		return nil, "", errors.New("run: no queen asker configured")
	}
	if opts.Judge == nil {
		return nil, "", errors.New("run: no judge configured")
	}
	if strings.TrimSpace(opts.Queen) == "" {
		return nil, "", errors.New("run: no queen configured")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	index, err := LoadIndex(opts.SplitDir)
	if err != nil {
		return nil, "", err
	}
	ids, err := Select(index, opts.Questions)
	if err != nil {
		return nil, "", err
	}
	runDir := opts.RunDir
	if runDir == "" {
		return nil, "", errors.New("run: empty run directory")
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, "", err
	}
	done := map[string]bool{}
	if opts.Resume {
		done, err = DoneIDs(runDir)
		if err != nil {
			return nil, "", err
		}
	}

	label := Label(opts.Variant)
	planned := 0
	for _, id := range ids {
		if !done[id] {
			planned++
		}
	}
	logf("%s: %d question(s) selected, %d to run (run dir %s)", label, len(ids), planned, runDir)

	resultsPath := filepath.Join(runDir, "results.jsonl")
	completed := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if done[id] {
			logf("skip %s (already recorded)", id)
			continue
		}
		item, err := LoadItem(opts.SplitDir, id)
		if err != nil {
			return nil, "", err
		}
		home := filepath.Join(runDir, "homes", id)
		if err := os.RemoveAll(home); err != nil {
			return nil, "", err
		}
		if err := os.MkdirAll(home, 0o755); err != nil {
			return nil, "", err
		}
		seed, err := SeedHaystack(home, opts.Queen, item)
		if err != nil {
			if recordErr := RecordError(runDir, item, err); recordErr != nil {
				return nil, "", recordErr
			}
			logf("seed failed for %s: %v", id, err)
			cleanupHome(home, opts.KeepHomes)
			continue
		}
		when, err := ParseDate(item.QuestionDate)
		if err != nil {
			if recordErr := RecordError(runDir, item, err); recordErr != nil {
				return nil, "", recordErr
			}
			logf("bad question date for %s: %v", id, err)
			cleanupHome(home, opts.KeepHomes)
			continue
		}

		askCtx := ctx
		var cancel context.CancelFunc
		if opts.Timeout > 0 {
			askCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		}
		answer, askErr := opts.Asker.Ask(askCtx, AskRequest{
			HomeRoot: home,
			Question: item.Question,
			When:     when,
		})
		if cancel != nil {
			cancel()
		}
		if askErr != nil {
			if recordErr := RecordError(runDir, item, askErr); recordErr != nil {
				return nil, "", recordErr
			}
			logf("%s: no gradable answer: %v", id, askErr)
			cleanupHome(home, opts.KeepHomes)
			continue
		}

		verdict, judgeErr := opts.Judge.Grade(ctx, item, answer.Text)
		var readerModel *string
		if opts.Model != "" {
			model := opts.Model
			readerModel = &model
		}
		if _, err := Record(runDir, RecordInput{
			Item:        item,
			Answer:      answer,
			Verdict:     verdict,
			SessionMap:  seed.Mapping,
			ReaderModel: readerModel,
			JudgeError:  judgeErr,
		}); err != nil {
			return nil, "", err
		}
		completed++
		status := "wrong"
		if verdict.Correct {
			status = "correct"
		}
		judgeNote := ""
		if judgeErr != nil {
			status = "ungraded"
			judgeNote = fmt.Sprintf(" (judge: %v)", judgeErr)
		}
		logf("[%d/%d] %s %s: %s%s", completed, planned, id, item.QuestionType, status, judgeNote)
		cleanupHome(home, opts.KeepHomes)
	}

	report, summary, err := Summarize(resultsPath, label+" through Hive")
	if err != nil {
		return nil, "", err
	}
	if report == "" {
		return summary, "", nil
	}
	if err := writeSummary(runDir, report, summary); err != nil {
		return nil, "", err
	}
	return summary, report, nil
}

func cleanupHome(home string, keep bool) {
	if !keep {
		_ = os.RemoveAll(home)
	}
}

func writeSummary(runDir, report string, summary *Summary) error {
	if err := os.WriteFile(filepath.Join(runDir, "summary.txt"), []byte(report+"\n"), 0o644); err != nil {
		return err
	}
	if summary == nil {
		return nil
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "summary.json"), encoded, 0o644)
}

// RejudgeOptions configures re-grading a saved run with another judge.
type RejudgeOptions struct {
	RunDir      string
	SplitDir    string
	Judge       *Judge
	Concurrency int
	Title       string
	Logf        Logf
}

// Rejudge re-grades every saved answer and writes
// run/n/rejudged-<model>/results.jsonl. A judge call that still fails after
// retries keeps its row, marked with judge_error and counted wrong, so the
// other grades are not lost and a second rejudge can finish the job.
//
// Parity: tests/e2e/longmemeval.py::rejudge
func Rejudge(ctx context.Context, opts RejudgeOptions) (string, *Summary, error) {
	if opts.Judge == nil {
		return "", nil, errors.New("rejudge: no judge configured")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	rows, err := ReadResults(filepath.Join(opts.RunDir, "results.jsonl"))
	if err != nil {
		return "", nil, err
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("rejudge: no results in %s", opts.RunDir)
	}
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	outDir := filepath.Join(opts.RunDir, "rejudged-"+SafeModelName(opts.Judge.Model))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", nil, err
	}

	graded := make([]map[string]any, len(rows))
	gate := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, row := range rows {
		wg.Add(1)
		go func(index int, row map[string]any) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			graded[index] = rejudgeRow(ctx, opts.SplitDir, opts.Judge, row)
		}(i, row)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	path := filepath.Join(outDir, "results.jsonl")
	var builder strings.Builder
	for _, row := range graded {
		encoded, err := json.Marshal(row)
		if err != nil {
			return "", nil, err
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		return "", nil, err
	}
	report, summary, err := Summarize(path, opts.Title)
	if err != nil {
		return "", nil, err
	}
	if report != "" {
		if err := writeSummary(outDir, report, summary); err != nil {
			return "", nil, err
		}
	}
	logf("re-graded %d row(s) with %s -> %s", len(graded), opts.Judge.Model, path)
	return report, summary, nil
}

func rejudgeRow(ctx context.Context, splitDir string, judge *Judge, row map[string]any) map[string]any {
	if stringField(row, "error") != "" {
		out := cloneRow(row)
		out["judge_model"] = judge.Model
		return out // no answer to grade; stays wrong
	}
	out := cloneRow(row)
	delete(out, "judge_error")
	questionID := stringField(row, "question_id")
	item, err := LoadItem(splitDir, questionID)
	if err != nil {
		out["correct"] = false
		out["judge"] = ""
		out["judge_model"] = judge.Model
		out["judge_error"] = truncateError(err)
		return out
	}
	verdict, err := judge.Grade(ctx, item, stringField(row, "hypothesis"))
	if err != nil {
		out["correct"] = false
		out["judge"] = ""
		out["judge_model"] = judge.Model
		out["judge_error"] = truncateError(err)
		return out
	}
	out["correct"] = verdict.Correct
	out["judge"] = verdict.Raw
	out["original_judge"] = row["judge_model"]
	out["judge_model"] = verdict.JudgeModel
	return out
}

func cloneRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}
