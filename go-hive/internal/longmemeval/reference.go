package longmemeval

// Reference points outside Hive: the official reader on a given history
// (answer sessions only for the oracle condition, or the whole haystack),
// graded by the same judge.
//
// Parity: tests/e2e/longmemeval.py::reference_run
// Parity: tests/e2e/longmemeval.py::reader_prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"
)

// Verbatim from LongMemEval's src/generation/run_generation.py
// (history_format "json", cot "true").
const readerPromptTemplate = "I will give you several history chats between you and a user. Please answer the question based on the relevant " +
	"chat history. Answer the question step by step: first extract all the relevant information, and then reason over " +
	"the information to get the answer.\n\n\nHistory Chats:\n\n{}\n\nCurrent Date: {}\nQuestion: {}\nAnswer (step by step):"

// ReaderPrompt renders the official reading prompt over the answer sessions
// (oracle) or the whole history.
//
// Parity: tests/e2e/longmemeval.py::reader_prompt
func ReaderPrompt(item Item, oracle bool) string {
	chosen := map[string]bool{}
	if oracle {
		for _, id := range item.AnswerSessionIDs {
			chosen[id] = true
		}
	}
	type session struct {
		date  string
		turns []Turn
	}
	sessions := make([]session, 0, len(item.HaystackSessionIDs))
	for i, id := range item.HaystackSessionIDs {
		if oracle && !chosen[id] {
			continue
		}
		sessions = append(sessions, session{date: item.HaystackDates[i], turns: item.HaystackSessions[i]})
	}
	// The official script sorts by date only.
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].date < sessions[j].date })
	var history strings.Builder
	for i, s := range sessions {
		fmt.Fprintf(&history, "\n### Session %d:\nSession Date: %s\nSession Content:\n\n%s\n", i+1, s.date, pythonJSON(s.turns))
	}
	return fillTemplate(readerPromptTemplate, history.String(), item.QuestionDate, item.Question)
}

// pythonJSON renders turns the way Python's json.dumps does: ", "/": "
// separators and ASCII-only escapes. The official reader prompt embeds the
// history as json.dumps(turns); byte-level parity keeps the reference
// condition comparable with the published numbers.
func pythonJSON(turns []Turn) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for i, turn := range turns {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(`{"role": `)
		builder.WriteString(pythonJSONString(turn.Role))
		builder.WriteString(`, "content": `)
		builder.WriteString(pythonJSONString(turn.Content))
		builder.WriteByte('}')
	}
	builder.WriteByte(']')
	return builder.String()
}

// pythonJSONString escapes like Python's json.encoder.encode_basestring_ascii:
// non-ASCII characters become \uXXXX escapes (surrogate pairs above the BMP),
// and nothing else is escaped that Python would keep.
func pythonJSONString(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(&builder, `\u%04x`, r)
			case r < utf8.RuneSelf:
				builder.WriteRune(r)
			case r <= 0xFFFF:
				fmt.Fprintf(&builder, `\u%04x`, r)
			default:
				high, low := utf16.EncodeRune(r)
				fmt.Fprintf(&builder, `\u%04x\u%04x`, high, low)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// ReferenceOptions configures a reference run.
type ReferenceOptions struct {
	SplitDir     string
	OutDir       string
	IDs          []string
	Oracle       bool
	ReaderModel  string
	ReaderClient *APIClient
	Judge        *Judge
	Concurrency  int
	Title        string
	Logf         Logf
}

// Reference answers the selected questions with the official reader prompt
// and grades them, appending each row as soon as it is graded. Questions
// already in the results file are skipped, so a stopped run resumes.
//
// Parity: tests/e2e/longmemeval.py::reference_run
func Reference(ctx context.Context, opts ReferenceOptions) (string, *Summary, error) {
	if opts.ReaderClient == nil || opts.Judge == nil {
		return "", nil, fmt.Errorf("reference: reader and judge endpoints are required")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(opts.OutDir, "results.jsonl")
	done, err := DoneIDs(opts.OutDir)
	if err != nil {
		return "", nil, err
	}
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	condition := "full-history"
	if opts.Oracle {
		condition = "oracle"
	}

	var mu sync.Mutex
	gate := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, questionID := range opts.IDs {
		if done[questionID] {
			logf("skip %s (already recorded)", questionID)
			continue
		}
		wg.Add(1)
		go func(qid string) {
			defer wg.Done()
			gate <- struct{}{}
			defer func() { <-gate }()
			row, err := referenceRow(ctx, opts, qid, condition)
			if err != nil {
				logf("%s: %v", qid, err)
				return
			}
			mu.Lock()
			appendErr := AppendRow(path, row)
			mu.Unlock()
			if appendErr != nil {
				logf("%s: append: %v", qid, appendErr)
			}
		}(questionID)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	report, summary, err := Summarize(path, opts.Title)
	if err != nil {
		return "", nil, err
	}
	if report != "" {
		if err := writeSummary(opts.OutDir, report, summary); err != nil {
			return "", nil, err
		}
	}
	return report, summary, nil
}

func referenceRow(ctx context.Context, opts ReferenceOptions, questionID, condition string) (map[string]any, error) {
	item, err := LoadItem(opts.SplitDir, questionID)
	if err != nil {
		return nil, err
	}
	temperature := 0.0
	hypothesis, readerErr := opts.ReaderClient.Chat(ctx, opts.ReaderModel,
		[]Message{{Role: "user", Content: ReaderPrompt(item, opts.Oracle)}}, &temperature, nil)
	verdict := Verdict{JudgeModel: opts.Judge.Model}
	var judgeErr error
	if readerErr == nil {
		verdict, judgeErr = opts.Judge.Grade(ctx, item, hypothesis)
		if judgeErr != nil {
			verdict = Verdict{JudgeModel: opts.Judge.Model}
		}
	}
	row := map[string]any{
		"question_id":   item.QuestionID,
		"question_type": item.QuestionType,
		"abstention":    IsAbstention(item.QuestionID),
		"question":      item.Question,
		"answer":        item.Answer.Value(),
		"hypothesis":    hypothesis,
		"correct":       verdict.Correct,
		"judge":         verdict.Raw,
		"judge_model":   opts.Judge.Model,
		"reader_model":  opts.ReaderModel,
		"condition":     condition,
	}
	if readerErr != nil {
		row["hypothesis"] = ""
		row["error"] = truncateError(readerErr)
	}
	if judgeErr != nil {
		row["judge_error"] = truncateError(judgeErr)
	}
	return row, nil
}
