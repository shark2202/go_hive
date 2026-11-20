package longmemeval

// Results: the append-only rows a run writes, transcript files, and the
// resume index. The row shape matches the Python harness so results.jsonl
// files from either implementation read the same way.
//
// Parity: tests/e2e/longmemeval.py::record
// Parity: tests/e2e/longmemeval.py::read_results

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SearchCall is one search_messages / search_timeline call the queen made.
type SearchCall struct {
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input"`
}

// Answer is what one question's queen run produced.
type Answer struct {
	// Text is the queen's final reply.
	Text string
	// Tools lists every tool the queen called, in order.
	Tools []string
	// Searches lists the history-search calls.
	Searches []SearchCall
	// SearchResults holds the full text of each search call's result, with
	// spilled results already followed to their file (evidence matching).
	SearchResults []string
	// Turns is the raw conversation transcript.
	Turns []map[string]any
	// Seconds is the wall-clock time of the turn.
	Seconds float64
}

// AskRequest is one question to run against a fresh, seeded Hive home.
type AskRequest struct {
	HomeRoot string
	Question string
	// When is the instant the queen believes it is (frozen clock).
	When time.Time
}

// Asker runs one question through go_hive and returns what the queen did.
// internal/cli provides the production implementation; tests substitute a
// scripted one.
type Asker interface {
	Ask(ctx context.Context, req AskRequest) (Answer, error)
}

// RecordInput is everything a result row needs.
type RecordInput struct {
	Item        Item
	Answer      Answer
	Verdict     Verdict
	SessionMap  map[string]string
	ReaderModel *string
	JudgeError  error
}

// ReadResults returns the rows of a results file, repairing a tail left by
// an interrupted write. Skipping a truncated last line is not enough: the
// next append would continue it and corrupt the new row too, so the file is
// cut back to the end of the last complete row.
//
// Parity: tests/e2e/longmemeval.py::read_results
func ReadResults(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	rows := make([]map[string]any, 0)
	goodEnd := 0
	pos := 0
	for pos < len(data) {
		next := bytes.IndexByte(data[pos:], '\n')
		var raw []byte
		if next < 0 {
			raw = data[pos:]
		} else {
			raw = data[pos : pos+next+1]
		}
		pos += len(raw)
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			goodEnd = pos
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			break
		}
		if !bytes.HasSuffix(raw, []byte("\n")) {
			break // parses, but the write may have stopped mid-line
		}
		rows = append(rows, row)
		goodEnd = pos
	}
	if goodEnd < len(data) {
		withFile, err := os.OpenFile(path, os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		if err := withFile.Truncate(int64(goodEnd)); err != nil {
			withFile.Close()
			return nil, err
		}
		withFile.Close()
	}
	return rows, nil
}

// AppendRow appends one JSON line to a results file.
func AppendRow(path string, row map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = fh.Write(append(encoded, '\n'))
	return err
}

// DoneIDs reports the question ids already recorded in a run (resume).
//
// Parity: tests/e2e/longmemeval.py::done_ids
func DoneIDs(runDir string) (map[string]bool, error) {
	rows, err := ReadResults(filepath.Join(runDir, "results.jsonl"))
	if err != nil {
		return nil, err
	}
	done := make(map[string]bool, len(rows))
	for _, row := range rows {
		if id, ok := row["question_id"].(string); ok {
			done[id] = true
		}
	}
	return done, nil
}

// BuildRow renders one graded result row.
//
// Parity: tests/e2e/longmemeval.py::record
func BuildRow(in RecordInput) map[string]any {
	abstention := IsAbstention(in.Item.QuestionID)
	evidence := []string{}
	if !abstention {
		for _, lmeID := range in.Item.AnswerSessionIDs {
			if hiveID, ok := in.SessionMap[lmeID]; ok {
				evidence = append(evidence, hiveID)
			}
		}
	}
	seen := strings.Join(in.Answer.SearchResults, " ")
	var evidenceRetrieved any
	if len(evidence) > 0 {
		evidenceRetrieved = false
		for _, hiveID := range evidence {
			if strings.Contains(seen, hiveID) {
				evidenceRetrieved = true
				break
			}
		}
	}
	searchCalls := make([]map[string]any, 0, len(in.Answer.Searches))
	for _, call := range in.Answer.Searches {
		entry := map[string]any{"tool": call.Tool}
		for key, value := range call.Input {
			entry[key] = value
		}
		searchCalls = append(searchCalls, entry)
	}
	tools := in.Answer.Tools
	if tools == nil {
		tools = []string{}
	}
	var readerModel any
	if in.ReaderModel != nil {
		readerModel = *in.ReaderModel
	}
	row := map[string]any{
		"question_id":        in.Item.QuestionID,
		"question_type":      in.Item.QuestionType,
		"abstention":         abstention,
		"question":           in.Item.Question,
		"answer":             in.Item.Answer,
		"hypothesis":         in.Answer.Text,
		"correct":            in.Verdict.Correct,
		"judge":              in.Verdict.Raw,
		"judge_model":        in.Verdict.JudgeModel,
		"reader_model":       readerModel,
		"searched":           len(in.Answer.Searches) > 0,
		"search_calls":       searchCalls,
		"evidence_retrieved": evidenceRetrieved,
		"tools":              tools,
		"seconds":            in.Answer.Seconds,
	}
	if in.JudgeError != nil {
		row["judge_error"] = truncateError(in.JudgeError)
	}
	return row
}

// Record appends one result line and saves the transcript.
//
// Parity: tests/e2e/longmemeval.py::record
func Record(runDir string, in RecordInput) (map[string]any, error) {
	row := BuildRow(in)
	if err := AppendRow(filepath.Join(runDir, "results.jsonl"), row); err != nil {
		return nil, err
	}
	transcripts := filepath.Join(runDir, "transcripts")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		return nil, err
	}
	turns := in.Answer.Turns
	if turns == nil {
		turns = []map[string]any{}
	}
	payload := map[string]any{"result": row, "turns": turns}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(transcripts, in.Item.QuestionID+".json"), encoded, 0o644); err != nil {
		return nil, err
	}
	return row, nil
}

// RecordError appends a miss for a question that never got a gradable answer.
//
// Parity: tests/e2e/longmemeval.py::record_error
func RecordError(runDir string, item Item, cause error) error {
	row := map[string]any{
		"question_id":        item.QuestionID,
		"question_type":      item.QuestionType,
		"abstention":         IsAbstention(item.QuestionID),
		"question":           item.Question,
		"answer":             item.Answer,
		"hypothesis":         "",
		"correct":            false,
		"judge":              "",
		"judge_model":        "",
		"searched":           false,
		"search_calls":       []map[string]any{},
		"evidence_retrieved": nil,
		"tools":              []string{},
		"seconds":            0,
	}
	if cause != nil {
		row["error"] = truncateError(cause)
	}
	return AppendRow(filepath.Join(runDir, "results.jsonl"), row)
}

func truncateError(err error) string {
	text := fmt.Sprintf("%T: %v", err, err)
	if len(text) > 500 {
		text = text[:500]
	}
	return text
}

var modelNameCleaner = regexp.MustCompile(`[^\w.-]`)

// SafeModelName turns a model id into a directory-safe name.
func SafeModelName(model string) string {
	return modelNameCleaner.ReplaceAllString(model, "_")
}
