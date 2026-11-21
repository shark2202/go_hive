package longmemeval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordItem() Item {
	return Item{
		QuestionID:         "fixture_commute_01",
		QuestionType:       "single-session-user",
		Question:           "How long is my commute?",
		Answer:             TextAnswer("45 minutes"),
		QuestionDate:       "2023/05/20 (Sat) 10:00",
		HaystackSessionIDs: []string{"fixture_s1"},
		HaystackDates:      []string{"2023/05/18 (Thu) 08:12"},
		HaystackSessions:   [][]Turn{{{Role: "user", Content: "45 minutes each way"}}},
		AnswerSessionIDs:   []string{"fixture_s1"},
	}
}

// Parity-exempt: Go-side coverage for the tail repair in
// tests/e2e/longmemeval.py::read_results.
func TestReadResultsRepairsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.jsonl")
	rows := "{\"question_id\": \"a\", \"correct\": true}\n{\"question_id\": \"b\", \"correct\": false}\n"
	if err := os.WriteFile(path, []byte(rows+`{"question_id": "c", "cor`), 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := ReadResults(path)
	if err != nil {
		t.Fatalf("ReadResults: %v", err)
	}
	if len(read) != 2 {
		t.Fatalf("rows = %d, want the 2 complete ones", len(read))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != rows {
		t.Fatalf("file not cut back to the last complete row: %q", data)
	}
	if err := AppendRow(path, map[string]any{"question_id": "c", "correct": true}); err != nil {
		t.Fatal(err)
	}
	read, err = ReadResults(path)
	if err != nil || len(read) != 3 {
		t.Fatalf("after append rows = %d, %v", len(read), err)
	}
	if read[2]["question_id"] != "c" || read[2]["correct"] != true {
		t.Fatalf("appended row = %v", read[2])
	}
}

// Parity-exempt: Go-side coverage for tests/e2e/longmemeval.py::record — the
// row shape, retrieval diagnostics, and the saved transcript.
func TestRecordWritesRowAndTranscript(t *testing.T) {
	dir := t.TempDir()
	reader := "fake-model"
	row, err := Record(dir, RecordInput{
		Item: recordItem(),
		Answer: Answer{
			Text:          "45 minutes each way",
			Tools:         []string{"search_messages"},
			Searches:      []SearchCall{{Tool: "search_messages", Input: map[string]any{"pattern": "commute"}}},
			SearchResults: []string{"found session_20230518_081200_dead in the haystack"},
			Turns:         []map[string]any{{"role": "assistant", "content": "45 minutes each way"}},
			Seconds:       12.5,
		},
		Verdict:     Verdict{Correct: true, Raw: "yes", JudgeModel: OfficialJudgeModel},
		SessionMap:  map[string]string{"fixture_s1": "session_20230518_081200_dead"},
		ReaderModel: &reader,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if row["searched"] != true || row["evidence_retrieved"] != true {
		t.Fatalf("diagnostics = %v / %v", row["searched"], row["evidence_retrieved"])
	}
	calls, ok := row["search_calls"].([]map[string]any)
	if !ok || len(calls) != 1 || calls[0]["tool"] != "search_messages" || calls[0]["pattern"] != "commute" {
		t.Fatalf("search_calls = %#v", row["search_calls"])
	}
	if row["reader_model"] != "fake-model" || row["judge_model"] != OfficialJudgeModel {
		t.Fatalf("models = %v / %v", row["reader_model"], row["judge_model"])
	}
	transcript := filepath.Join(dir, "transcripts", "fixture_commute_01.json")
	data, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["result"] == nil || payload["turns"] == nil {
		t.Fatalf("transcript payload = %v", payload)
	}
	done, err := DoneIDs(dir)
	if err != nil || !done["fixture_commute_01"] {
		t.Fatalf("DoneIDs = %v, %v", done, err)
	}
}

// Parity-exempt: abstention questions have no evidence to find, so the
// diagnostic stays nil; a search that misses the evidence reports false.
func TestRecordEvidenceDiagnostics(t *testing.T) {
	abstention := recordItem()
	abstention.QuestionID = "fixture_abs_01_abs"
	row, err := Record(t.TempDir(), RecordInput{
		Item:    abstention,
		Answer:  Answer{SearchResults: []string{"nothing useful"}},
		Verdict: Verdict{JudgeModel: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value, present := row["evidence_retrieved"]; !present || value != nil {
		t.Fatalf("abstention evidence_retrieved = %v (present=%v)", value, present)
	}

	missed, err := Record(t.TempDir(), RecordInput{
		Item:       recordItem(),
		Answer:     Answer{SearchResults: []string{"nothing useful"}},
		Verdict:    Verdict{JudgeModel: "m"},
		SessionMap: map[string]string{"fixture_s1": "session_20230518_081200_dead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if missed["evidence_retrieved"] != false {
		t.Fatalf("missed evidence_retrieved = %v", missed["evidence_retrieved"])
	}
}

// Parity-exempt: Go-side coverage for tests/e2e/longmemeval.py::record_error.
func TestRecordErrorCountsAsMiss(t *testing.T) {
	dir := t.TempDir()
	if err := RecordError(dir, recordItem(), errTimeout); err != nil {
		t.Fatalf("RecordError: %v", err)
	}
	rows, err := ReadResults(filepath.Join(dir, "results.jsonl"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %d, %v", len(rows), err)
	}
	row := rows[0]
	if row["correct"] != false || row["hypothesis"] != "" || row["searched"] != false {
		t.Fatalf("error row = %v", row)
	}
	if !strings.Contains(row["error"].(string), "timeout") {
		t.Fatalf("error = %v", row["error"])
	}
}

type stubError string

func (e stubError) Error() string { return string(e) }

const errTimeout = stubError("queen did not finish: timeout")

// Parity-exempt: judge failures keep the answer and mark it ungraded, so a
// rejudge can grade it later (counted wrong until then).
func TestRecordKeepsUngradedAnswers(t *testing.T) {
	row, err := Record(t.TempDir(), RecordInput{
		Item:       recordItem(),
		Answer:     Answer{Text: "45 minutes"},
		Verdict:    Verdict{JudgeModel: "judge-model"},
		JudgeError: stubError("judge exploded"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if row["correct"] != false || row["hypothesis"] != "45 minutes" {
		t.Fatalf("row = %v", row)
	}
	if !strings.Contains(row["judge_error"].(string), "judge exploded") {
		t.Fatalf("judge_error = %v", row["judge_error"])
	}
}

// Parity-exempt: model ids become directory names in rejudged-<model>.
func TestSafeModelName(t *testing.T) {
	if got := SafeModelName("openai/gpt-4o:2024-08-06"); got != "openai_gpt-4o_2024-08-06" {
		t.Fatalf("SafeModelName = %q", got)
	}
}
