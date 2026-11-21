package agentloop

// J2 parity matrix: the judge-failure contract of AgentLoop._judge_turn,
// exercised on this side against a scripted judge.
//
// Python side (real functions, no service/model):
//   PYTHONPATH=<repo>/core .venv/bin/python go_hive/scripts/judge_j2_probe.py
//
// Reference: core/framework/agent_loop/agent_loop.py::_judge_turn (6202-6270)
//   - transient failure + attempts left → retry after min(base*2**attempt, max)
//   - transient failure, last attempt  → ACCEPT + "[judge unavailable after
//     N attempts: <Type>; accepting to avoid stalling the loop]"
//   - any other failure                → re-raise (the turn ends with it)
//   - a failing *verdict* is never retried
//
// Parity: core/framework/agent_loop/internals/judge_pipeline.py::judge_turn

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

type j2JudgeStep struct {
	verdict JudgeVerdict
	err     error
}

// j2ScriptedJudge replays one step per call; the last step repeats.
type j2ScriptedJudge struct {
	mu    sync.Mutex
	steps []j2JudgeStep
	calls int
}

func (j *j2ScriptedJudge) Evaluate(_ context.Context, _ JudgeRequest) (JudgeVerdict, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	index := j.calls
	j.calls++
	if index >= len(j.steps) {
		index = len(j.steps) - 1
	}
	return j.steps[index].verdict, j.steps[index].err
}

func (j *j2ScriptedJudge) callCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls
}

// j2MatrixLoop builds a loop whose judge retry budget is fast but otherwise
// the real configuration: zero only the backoff waits.
func j2MatrixLoop(retries int, steps ...j2JudgeStep) (*AgentLoop, *j2ScriptedJudge) {
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:          3,
		MaxStreamRetries:       retries,
		StreamRetryBackoffBase: 0,
		StreamRetryMaxDelay:    0,
	}, nil)
	judge := &j2ScriptedJudge{steps: steps}
	loop.Judge = judge
	return loop, judge
}

// j2Evaluate drives the real evaluateJudge seam for one scripted scenario.
func j2Evaluate(t *testing.T, loop *AgentLoop) (JudgeVerdict, error) {
	t.Helper()
	criteria := "criteria"
	verdict, err := loop.evaluateJudge(
		context.Background(),
		judgeTestContext(nil, &criteria),
		NewNodeConversation("system"),
		"assistant text",
		nil,
		0,
	)
	return verdict, err
}

// TestJudgeFailureMatrixMatchesPython mirrors the probe's case list one for
// one; each case cites the probe row it was measured against.
//
// Parity: go_hive/scripts/judge_j2_probe.py (J2 matrix)
func TestJudgeFailureMatrixMatchesPython(t *testing.T) {
	accept := JudgeVerdict{Action: JudgeAccept, Feedback: "criteria met"}
	retry := JudgeVerdict{Action: JudgeRetry, Feedback: "criteria not met yet"}
	escalate := JudgeVerdict{Action: JudgeEscalate, Feedback: "hopeless"}

	cases := []struct {
		name      string
		retries   int
		steps     []j2JudgeStep
		action    JudgeAction
		feedback  string
		errText   string
		calls     int
		wantBumps map[string]int
	}{
		{
			// Probe row "first-eval-fails(RETRY)": a failing verdict is not an
			// exception, so there is no retry.
			name:      "first-eval-fails(RETRY)",
			retries:   5,
			steps:     []j2JudgeStep{{verdict: retry}},
			action:    JudgeRetry,
			feedback:  "criteria not met yet",
			calls:     1,
			wantBumps: map[string]int{"judge_retry": 1},
		},
		{
			// Probe row "transient-then-accept": the retry's verdict is used.
			name:      "transient-then-accept",
			retries:   5,
			steps:     []j2JudgeStep{{err: context.DeadlineExceeded}, {verdict: accept}},
			action:    JudgeAccept,
			feedback:  "criteria met",
			calls:     2,
			wantBumps: map[string]int{"judge_transient_retry": 1, "judge_accept": 1},
		},
		{
			// Probe row "transient-then-eval-fails": the retry returns a
			// failing verdict; no third attempt.
			name:    "transient-then-eval-fails",
			retries: 5,
			steps: []j2JudgeStep{
				{err: errors.New("dial tcp 127.0.0.1:7: connection refused")},
				{verdict: retry},
			},
			action:    JudgeRetry,
			feedback:  "criteria not met yet",
			calls:     2,
			wantBumps: map[string]int{"judge_transient_retry": 1, "judge_retry": 1},
		},
		{
			// Probe row "transient-exhausted(cap=2)".
			name:    "transient-exhausted(cap=2)",
			retries: 2,
			steps:   []j2JudgeStep{{err: context.DeadlineExceeded}},
			action:  JudgeAccept,
			feedback: "[judge unavailable after 2 attempts: TimeoutError; " +
				"accepting to avoid stalling the loop]",
			calls: 2,
			wantBumps: map[string]int{
				"judge_transient_retry": 1,
				"judge_fallback_accept": 1,
				"judge_accept":          1,
			},
		},
		{
			// Probe row "transient-exhausted(default cap=5)".
			name:    "transient-exhausted(default cap=5)",
			retries: 5,
			steps:   []j2JudgeStep{{err: context.DeadlineExceeded}},
			action:  JudgeAccept,
			feedback: "[judge unavailable after 5 attempts: TimeoutError; " +
				"accepting to avoid stalling the loop]",
			calls: 5,
			wantBumps: map[string]int{
				"judge_transient_retry": 4,
				"judge_fallback_accept": 1,
				"judge_accept":          1,
			},
		},
		{
			// Probe row "non-transient-raises": upstream re-raises, the seam
			// returns the error to the caller.
			name:    "non-transient-raises",
			retries: 5,
			steps:   []j2JudgeStep{{err: errors.New("bad judge wiring")}},
			errText: "bad judge wiring",
			calls:   1,
		},
		{
			// Probe row "verdict-escalate".
			name:      "verdict-escalate",
			retries:   5,
			steps:     []j2JudgeStep{{verdict: escalate}},
			action:    JudgeEscalate,
			feedback:  "hopeless",
			calls:     1,
			wantBumps: map[string]int{"judge_escalate": 1},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loop, judge := j2MatrixLoop(tc.retries, tc.steps...)
			verdict, err := j2Evaluate(t, loop)

			if tc.errText != "" {
				if err == nil || err.Error() != tc.errText {
					t.Fatalf("err = %v, want %q", err, tc.errText)
				}
			} else if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if verdict.Action != tc.action {
				t.Fatalf("action = %q, want %q", verdict.Action, tc.action)
			}
			if verdict.Feedback != tc.feedback {
				t.Fatalf("feedback = %q, want %q", verdict.Feedback, tc.feedback)
			}
			if judge.callCount() != tc.calls {
				t.Fatalf("judge calls = %d, want %d", judge.callCount(), tc.calls)
			}
			for counter, want := range tc.wantBumps {
				if got := loop.Counter(counter); got != want {
					t.Fatalf("counter %s = %d, want %d (all: %v)", counter, got, want, loop.Stats())
				}
			}
			if tc.errText != "" && loop.Counter("judge_retry") != 0 {
				t.Fatalf("propagated failure must not bump judge_retry: %v", loop.Stats())
			}
		})
	}
}

// TestJudgeRetryWithoutFeedbackUsesUpstreamSentence pins the upstream
// "Custom judge returned RETRY." default for a feedback-less RETRY verdict.
//
// Parity: core/framework/agent_loop/internals/judge_pipeline.py::judge_turn
// (Level 1: `if verdict.action == "RETRY" and not verdict.feedback`).
func TestJudgeRetryWithoutFeedbackUsesUpstreamSentence(t *testing.T) {
	loop, _ := j2MatrixLoop(5, j2JudgeStep{verdict: JudgeVerdict{Action: JudgeRetry}})
	verdict, err := j2Evaluate(t, loop)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if verdict.Action != JudgeRetry || verdict.Feedback != "Custom judge returned RETRY." {
		t.Fatalf("verdict = %#v, want RETRY with the upstream sentence", verdict)
	}
}

// TestJudgeTransientClassification mirrors the probe's is_transient_error
// table (real helper) on this side.
//
// Parity: core/framework/agent_loop/internals/tool_result_handler.py::is_transient_error
func TestJudgeTransientClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"timeout", context.DeadlineExceeded, true},
		{"connection keyword", errors.New("dial tcp 127.0.0.1:7: connection refused"), true},
		{"net op error", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, true},
		{"rate limit keyword", errors.New("stream error: 429 rate limit exceeded"), true},
		{"cancelled context", context.Canceled, false},
		{"permanent error", errors.New("bad judge wiring"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientJudgeError(tc.err); got != tc.want {
				t.Fatalf("isTransientJudgeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestJudgeTransientFallbackKeepsTheLoopMoving proves the fallback end to end:
// a judge outage that exhausts the retry budget accepts the turn (fail-open)
// and the verdict event carries the unavailability feedback.
//
// Parity: core/framework/agent_loop/agent_loop.py::_judge_turn (the fallback
// "accepting to avoid stalling the loop").
func TestJudgeTransientFallbackKeepsTheLoopMoving(t *testing.T) {
	criteria := "the report exists"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	judge := &j2ScriptedJudge{steps: []j2JudgeStep{{err: context.DeadlineExceeded}}}
	events := &recordingJudgeEvents{}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:          3,
		MaxStreamRetries:       2,
		StreamRetryBackoffBase: 0,
		StreamRetryMaxDelay:    0,
	}, nil)
	loop.Judge = judge
	loop.JudgeEvents = events

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success || result.FinalText != "done" || result.InnerTurns != 1 {
		t.Fatalf("result = %#v, want the turn accepted despite the judge outage", result)
	}
	if judge.callCount() != 2 {
		t.Fatalf("judge calls = %d, want 2 (the exhausted budget)", judge.callCount())
	}
	if events.count() != 1 {
		t.Fatalf("judge events = %d, want 1", events.count())
	}
	event := events.events[0]
	if event.action != string(JudgeAccept) {
		t.Fatalf("event action = %q, want ACCEPT", event.action)
	}
	if !strings.Contains(event.feedback, "[judge unavailable after 2 attempts: TimeoutError; accepting to avoid stalling the loop]") {
		t.Fatalf("event feedback = %q, want the upstream fallback sentence", event.feedback)
	}
	if got := loop.Counter("judge_fallback_accept"); got != 1 {
		t.Fatalf("judge_fallback_accept = %d, want 1", got)
	}
}

// TestJudgeNonTransientFailureEndsTurn proves the propagation branch end to
// end: the turn fails with the judge error instead of retrying the model.
//
// Parity: core/framework/agent_loop/agent_loop.py::_judge_turn (non-transient
// re-raise).
func TestJudgeNonTransientFailureEndsTurn(t *testing.T) {
	criteria := "the report exists"
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}, {content: "should not run"}}}
	judge := &j2ScriptedJudge{steps: []j2JudgeStep{{err: errors.New("bad judge wiring")}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 3, MaxStreamRetries: 5}, nil)
	loop.Judge = judge

	result, err := loop.ExecuteTurn(context.Background(), judgeTestContext(completer, &criteria), NewNodeConversation("system"), nil)
	if err == nil || !strings.Contains(err.Error(), "bad judge wiring") {
		t.Fatalf("err = %v, want the propagated judge failure", err)
	}
	if result.Success {
		t.Fatalf("result.Success = true, want false")
	}
	if completer.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1 (no extra model turn after the failure)", completer.callCount())
	}
}

// TestJudgeRetryBackoffFollowsUpstreamFormula pins min(base*2**attempt, max)
// on the real config accessors.
//
// Parity: core/framework/agent_loop/agent_loop.py::_judge_turn (delay formula).
func TestJudgeRetryBackoffFollowsUpstreamFormula(t *testing.T) {
	loop := NewAgentLoop(LoopConfig{
		StreamRetryBackoffBase: 2.0,
		StreamRetryMaxDelay:    5.0,
	}, nil)
	want := []int{2, 4, 5, 5} // seconds: 2*2^0, 2*2^1, capped
	for attempt, seconds := range want {
		if got := loop.judgeRetryDelay(attempt).Seconds(); got != float64(seconds) {
			t.Fatalf("judgeRetryDelay(%d) = %vs, want %ds", attempt, got, seconds)
		}
	}
	zero := NewAgentLoop(LoopConfig{}, nil)
	if got := zero.judgeMaxAttempts(); got != 1 {
		t.Fatalf("judgeMaxAttempts with zero config = %d, want max(1, 0) = 1", got)
	}
}
