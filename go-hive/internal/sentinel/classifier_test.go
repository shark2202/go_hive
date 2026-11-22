package sentinel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type fakeLLM struct {
	content string
	err     error
	system  string
	prompt  string
	calls   int
}

func (f *fakeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.calls++
	f.system = req.System
	if len(req.Messages) > 0 {
		f.prompt = req.Messages[0].Content
	}
	if f.err != nil {
		return llm.Completion{}, f.err
	}
	return llm.Completion{Content: f.content}, nil
}

func classifierContext() ParkContext {
	goal := "enrich 1000 leads"
	return ParkContext{
		ParkReason:        "turn_done",
		Goal:              &goal,
		OpenTasks:         []string{"enrich batch 3"},
		LastAssistantText: "I've done 200. Shall I continue?",
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_needs_human_verdict
func TestNeedsHumanVerdict(t *testing.T) {
	model := &fakeLLM{content: `{"verdict": "needs_human", "reason": "login expired"}`}
	verdict := ClassifyPark(context.Background(), classifierContext(), model)
	if verdict.Verdict != VerdictNeedsHuman || !verdict.NeedsHuman() {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_continue_verdict
func TestContinueVerdict(t *testing.T) {
	model := &fakeLLM{content: `{"verdict": "continue", "reason": "just a checkpoint"}`}
	verdict := ClassifyPark(context.Background(), classifierContext(), model)
	if verdict.Verdict != VerdictContinue || verdict.NeedsHuman() {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_no_llm_defaults_continue
func TestNoLLMDefaultsContinue(t *testing.T) {
	verdict := ClassifyPark(context.Background(), classifierContext(), nil)
	if verdict.Verdict != VerdictContinue {
		t.Fatalf("verdict = %#v", verdict)
	}
	if !verdict.Errored {
		t.Fatal("no-LLM fallback must be flagged errored")
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_bad_json_defaults_continue
func TestBadJSONDefaultsContinue(t *testing.T) {
	verdict := ClassifyPark(context.Background(), classifierContext(), &fakeLLM{content: "not json at all"})
	if verdict.Verdict != VerdictContinue || !verdict.Errored {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_llm_exception_defaults_continue
func TestLLMExceptionDefaultsContinue(t *testing.T) {
	verdict := ClassifyPark(context.Background(), classifierContext(), &fakeLLM{err: errors.New("llm down")})
	if verdict.Verdict != VerdictContinue || !verdict.Errored {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_unknown_verdict_defaults_continue
func TestUnknownVerdictDefaultsContinue(t *testing.T) {
	verdict := ClassifyPark(context.Background(), classifierContext(), &fakeLLM{content: `{"verdict": "maybe"}`})
	if verdict.Verdict != VerdictContinue {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_recent_user_message_reaches_classifier
func TestRecentUserMessageReachesClassifier(t *testing.T) {
	park := classifierContext()
	park.RecentUserText = "Stop here and wait for me, I want to review before you continue."
	model := &fakeLLM{content: `{"verdict": "needs_human", "reason": "user asked to wait"}`}
	ClassifyPark(context.Background(), park, model)

	if !strings.Contains(model.prompt, "wait for me") {
		t.Fatalf("prompt missing user steer: %s", model.prompt)
	}
	if !strings.Contains(strings.ToLower(model.prompt), "most recent message") {
		t.Fatalf("prompt missing user-message section: %s", model.prompt)
	}
	if !strings.Contains(strings.ToLower(model.system), "stop") || !strings.Contains(model.system, "needs_human") {
		t.Fatal("system prompt must license overriding standing authority on a stop")
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_no_user_message_omits_section
func TestNoUserMessageOmitsSection(t *testing.T) {
	model := &fakeLLM{content: `{"verdict": "needs_human"}`}
	ClassifyPark(context.Background(), classifierContext(), model)
	if strings.Contains(strings.ToLower(model.prompt), "most recent message") {
		t.Fatalf("prompt should omit the user section: %s", model.prompt)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_running_workers_reach_classifier
func TestRunningWorkersReachClassifier(t *testing.T) {
	park := classifierContext()
	park.RunningWorkers = []map[string]any{{
		"worker_id":       "w1",
		"status":          "running",
		"task":            "scrape influencer A",
		"elapsed_seconds": 750,
	}}
	model := &fakeLLM{content: `{"verdict": "needs_human"}`}
	ClassifyPark(context.Background(), park, model)

	for _, want := range []string{"w1", "scrape influencer A", "12m"} {
		if !strings.Contains(model.prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, model.prompt)
		}
	}
	if !strings.Contains(strings.ToLower(model.prompt), "still running") {
		t.Fatalf("prompt missing worker section: %s", model.prompt)
	}
	if !strings.Contains(strings.ToLower(model.system), "still running") ||
		!strings.Contains(strings.ToLower(model.system), "continue") {
		t.Fatal("system prompt must license continue while workers run")
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_format_running_workers_renders_elapsed
func TestFormatRunningWorkersRendersElapsed(t *testing.T) {
	cases := []struct {
		elapsed  float64
		expected string
	}{
		{45, "45s"},
		{90, "1m"},
		{750, "12m"},
		{3600, "1h"},
		{7380, "2h3m"},
	}
	for _, testCase := range cases {
		out := FormatRunningWorkers([]map[string]any{{
			"worker_id":       "w1",
			"status":          "running",
			"elapsed_seconds": testCase.elapsed,
		}}, 8)
		if !strings.Contains(out, "[running, "+testCase.expected+"]") {
			t.Fatalf("elapsed %v rendered %q, want [running, %s]", testCase.elapsed, out, testCase.expected)
		}
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_format_running_workers_omits_elapsed_when_absent
func TestFormatRunningWorkersOmitsElapsedWhenAbsent(t *testing.T) {
	out := FormatRunningWorkers([]map[string]any{{
		"worker_id": "w1",
		"status":    "running",
	}}, 8)
	if !strings.Contains(out, "[running]") {
		t.Fatalf("output = %q, want bare [running]", out)
	}
	if strings.Contains(out, "0s") {
		t.Fatalf("output should not imply just-started: %q", out)
	}
}

// Parity: core/framework/sentinel/classifier.py, core/tests/test_sentinel_classifier.py::test_no_running_workers_omits_section
func TestNoRunningWorkersOmitsSection(t *testing.T) {
	model := &fakeLLM{content: `{"verdict": "needs_human"}`}
	ClassifyPark(context.Background(), classifierContext(), model)
	if strings.Contains(strings.ToLower(model.prompt), "still running") {
		t.Fatalf("prompt should omit the worker section: %s", model.prompt)
	}
}
