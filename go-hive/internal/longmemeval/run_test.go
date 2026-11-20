package longmemeval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAsker stands in for the queen: it verifies the home has seeded history,
// "searches" it for the needle, and returns the answer the fixture expects.
type fakeAsker struct {
	t     *testing.T
	queen string
}

func (f fakeAsker) Ask(_ context.Context, req AskRequest) (Answer, error) {
	sessions, err := os.ReadDir(filepath.Join(req.HomeRoot, "queens", f.queen, "sessions"))
	if err != nil {
		f.t.Fatalf("seeded sessions missing: %v", err)
	}
	var evidence string
	for _, entry := range sessions {
		data, err := os.ReadFile(filepath.Join(req.HomeRoot, "queens", f.queen, "sessions", entry.Name(), "events.jsonl"))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "45 minutes") {
			evidence = entry.Name()
		}
	}
	switch {
	case strings.Contains(req.Question, "commute"):
		return Answer{
			Text:          "Your commute is 45 minutes each way.",
			Tools:         []string{"search_messages"},
			Searches:      []SearchCall{{Tool: "search_messages", Input: map[string]any{"pattern": "commute"}}},
			SearchResults: []string{`{"matches":[{"session":"` + evidence + `","total_matches":1}]`},
			Turns:         []map[string]any{{"role": "assistant", "content": "Your commute is 45 minutes each way."}},
			Seconds:       1.5,
		}, nil
	case strings.Contains(req.Question, "programming language"):
		return Answer{Text: "You are using Go now.", Turns: []map[string]any{{"role": "assistant", "content": "You are using Go now."}}, Seconds: 1}, nil
	default:
		return Answer{Text: "You never told me about a dog.", Turns: []map[string]any{{"role": "assistant", "content": "You never told me about a dog."}}, Seconds: 1}, nil
	}
}

// fakeJudgeServer grades by looking for the expected answer inside the model
// response section of the official prompt.
func fakeJudgeServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		prompt := body.Messages[0].Content
		correct := "no"
		if strings.Contains(prompt, "unanswerable question") {
			if strings.Contains(prompt, "never told me") {
				correct = "yes"
			}
		} else {
			answer := between(prompt, "Correct Answer: ", "\n\nModel Response: ")
			response := between(prompt, "Model Response: ", "\n\nIs the model response correct?")
			if answer != "" && strings.Contains(response, answer) {
				correct = "yes"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": correct}}},
		})
	}))
}

func between(text, start, end string) string {
	first := strings.Index(text, start)
	if first < 0 {
		return ""
	}
	rest := text[first+len(start):]
	if last := strings.Index(rest, end); last >= 0 {
		return rest[:last]
	}
	return rest
}

func stageSplit(t *testing.T) string {
	t.Helper()
	dataDir := stageFixture(t)
	splitDir, err := Prepare(context.Background(), SourceConfig{Variant: VariantS, DataPath: dataDir, Download: false})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return splitDir
}

// Parity-exempt: end-to-end Go coverage of the pytest runner
// (tests/e2e/test_longmemeval.py::test_longmemeval) with a scripted queen and
// a fake OpenAI-compatible judge.
func TestRunEndToEndWithFixture(t *testing.T) {
	splitDir := stageSplit(t)
	judgeServer := fakeJudgeServer(t)
	defer judgeServer.Close()
	judge := NewJudge(fastClient(judgeServer), "fake-judge")
	runDir := filepath.Join(t.TempDir(), "run")

	summary, report, err := Run(context.Background(), RunOptions{
		SplitDir:  splitDir,
		RunDir:    runDir,
		Variant:   VariantS,
		Questions: "all",
		Queen:     "queen_technology",
		Model:     "fake-queen",
		Timeout:   5 * time.Second,
		Asker:     fakeAsker{t: t, queen: "queen_technology"},
		Judge:     judge,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if summary == nil || summary.Questions != 3 || summary.Correct != 3 {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(report, "overall 100.0%") {
		t.Fatalf("report:\n%s", report)
	}
	rows, err := ReadResults(filepath.Join(runDir, "results.jsonl"))
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	commute := rows[0]
	if commute["question_id"] != "fixture_commute_01" || commute["correct"] != true || commute["evidence_retrieved"] != true {
		t.Fatalf("commute row = %v", commute)
	}
	if commute["reader_model"] != "fake-queen" {
		t.Fatalf("reader_model = %v", commute["reader_model"])
	}
	if _, err := os.Stat(filepath.Join(runDir, "summary.txt")); err != nil {
		t.Fatalf("summary.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "summary.json")); err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "homes", "fixture_commute_01")); !os.IsNotExist(err) {
		t.Fatalf("question home was not cleaned up: %v", err)
	}

	// --resume skips the three recorded questions and appends nothing.
	again, _, err := Run(context.Background(), RunOptions{
		SplitDir:  splitDir,
		RunDir:    runDir,
		Variant:   VariantS,
		Questions: "all",
		Resume:    true,
		Queen:     "queen_technology",
		Asker:     fakeAsker{t: t, queen: "queen_technology"},
		Judge:     judge,
	})
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if again == nil || again.Questions != 3 {
		t.Fatalf("resume summary = %+v", again)
	}
	rows, err = ReadResults(filepath.Join(runDir, "results.jsonl"))
	if err != nil || len(rows) != 3 {
		t.Fatalf("resume appended rows: %d, %v", len(rows), err)
	}
}

// Parity-exempt: a queen that never finishes is recorded as a miss, counted
// wrong, exactly like the upstream timeout path.
func TestRunTimeoutRecordsMiss(t *testing.T) {
	splitDir := stageSplit(t)
	runDir := filepath.Join(t.TempDir(), "run")
	judgeServer := fakeJudgeServer(t)
	defer judgeServer.Close()

	_, _, err := Run(context.Background(), RunOptions{
		SplitDir:  splitDir,
		RunDir:    runDir,
		Variant:   VariantS,
		Questions: "fixture_commute_01",
		Queen:     "queen_technology",
		Timeout:   20 * time.Millisecond,
		Asker:     slowAsker{},
		Judge:     NewJudge(fastClient(judgeServer), "fake-judge"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows, err := ReadResults(filepath.Join(runDir, "results.jsonl"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	if rows[0]["correct"] != false || !strings.Contains(rows[0]["error"].(string), "context deadline exceeded") {
		t.Fatalf("timeout row = %v", rows[0])
	}
}

type slowAsker struct{}

func (slowAsker) Ask(ctx context.Context, _ AskRequest) (Answer, error) {
	<-ctx.Done()
	return Answer{}, ctx.Err()
}

// Parity-exempt: a judge that fails after retries keeps the answer as
// ungraded (counted wrong) instead of dropping the row.
func TestRunJudgeFailureKeepsUngradedRow(t *testing.T) {
	splitDir := stageSplit(t)
	runDir := filepath.Join(t.TempDir(), "run")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client := fastClient(server)
	client.MaxAttempts = 2

	_, _, err := Run(context.Background(), RunOptions{
		SplitDir:  splitDir,
		RunDir:    runDir,
		Variant:   VariantS,
		Questions: "fixture_commute_01",
		Queen:     "queen_technology",
		Timeout:   5 * time.Second,
		Asker:     fakeAsker{t: t, queen: "queen_technology"},
		Judge:     NewJudge(client, "fake-judge"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows, err := ReadResults(filepath.Join(runDir, "results.jsonl"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	if rows[0]["correct"] != false || rows[0]["hypothesis"] == "" || rows[0]["judge_error"] == nil {
		t.Fatalf("ungraded row = %v", rows[0])
	}
}

// Parity-exempt: rejudge re-grades saved answers, keeps the original judge id
// and writes a fresh results file under rejudged-<model>.
func TestRejudgeRegradesSavedRun(t *testing.T) {
	splitDir := stageSplit(t)
	runDir := filepath.Join(t.TempDir(), "run")
	goodJudge := NewJudge(fastClient(fakeJudgeServer(t)), "fake-judge")
	if _, _, err := Run(context.Background(), RunOptions{
		SplitDir:  splitDir,
		RunDir:    runDir,
		Variant:   VariantS,
		Questions: "fixture_commute_01",
		Queen:     "queen_technology",
		Timeout:   5 * time.Second,
		Asker:     fakeAsker{t: t, queen: "queen_technology"},
		Judge:     goodJudge,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	noServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "no"}}},
		})
	}))
	defer noServer.Close()
	report, _, err := Rejudge(context.Background(), RejudgeOptions{
		RunDir:   runDir,
		SplitDir: splitDir,
		Judge:    NewJudge(fastClient(noServer), "strict-judge"),
		Title:    "LongMemEval-S through Hive (re-graded)",
	})
	if err != nil {
		t.Fatalf("Rejudge: %v", err)
	}
	if !strings.Contains(report, "overall 0.0%") {
		t.Fatalf("rejudge report:\n%s", report)
	}
	rows, err := ReadResults(filepath.Join(runDir, "rejudged-strict-judge", "results.jsonl"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rejudged rows = %d, %v", len(rows), err)
	}
	if rows[0]["correct"] != false || rows[0]["original_judge"] != "fake-judge" || rows[0]["judge_model"] != "strict-judge" {
		t.Fatalf("rejudged row = %v", rows[0])
	}
}
