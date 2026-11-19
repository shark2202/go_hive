package agentloop

// LLMJudgeAdapter adapts internal/llmjudge to the loop's Judge interface.

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/llmjudge"
)

const defaultJudgeConstraint = "success-criteria"

// LLMJudgeAdapter evaluates AgentSpec.SuccessCriteria with internal/llmjudge.
// A passing evaluation accepts the turn; a failing one retries, optionally
// escalating after a configured number of consecutive failures.
//
// Parity: framework/testing/llm_judge.py::LLMJudge.evaluate
type LLMJudgeAdapter struct {
	Judge *llmjudge.Judge

	// Constraint labels the check in the judge prompt.
	Constraint string
	// EscalateAfterRetries turns the Nth consecutive failed evaluation into
	// ESCALATE. Zero keeps failing evaluations on RETRY (the loop's iteration
	// cap still terminates the turn).
	EscalateAfterRetries int

	mu                  sync.Mutex
	consecutiveFailures int
}

func NewLLMJudgeAdapter(judge *llmjudge.Judge) *LLMJudgeAdapter {
	return &LLMJudgeAdapter{Judge: judge}
}

// completerJudgeProvider adapts llm.Completer to llmjudge.Provider, letting the
// loop's own model back the judge without constructing a second provider.
type completerJudgeProvider struct {
	completer llm.Completer
}

func (p completerJudgeProvider) Complete(ctx context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error) {
	return p.completer.AComplete(ctx, llm.CompletionRequest{
		Messages:  toChatMessages(messages),
		System:    opts.System,
		MaxTokens: opts.MaxTokens,
		JSONMode:  opts.JSONMode,
	})
}

// NewLLMJudgeAdapterFromCompleter builds an adapter backed by the same
// llm.Completer that runs the agent.
func NewLLMJudgeAdapterFromCompleter(completer llm.Completer) *LLMJudgeAdapter {
	if completer == nil {
		return &LLMJudgeAdapter{}
	}
	return NewLLMJudgeAdapter(llmjudge.New(completerJudgeProvider{completer: completer}))
}

// Evaluate implements Judge.
//
// Parity: framework/testing/llm_judge.py::LLMJudge.evaluate
func (a *LLMJudgeAdapter) Evaluate(ctx context.Context, request JudgeRequest) (JudgeVerdict, error) {
	if a == nil || a.Judge == nil {
		return JudgeVerdict{}, errors.New("agent loop: nil llm judge")
	}
	// The loop never calls a judge without criteria; keep that invariant here
	// too so a direct caller cannot spend an LLM call on nothing.
	if strings.TrimSpace(request.Criteria) == "" {
		return JudgeVerdict{Action: JudgeAccept, Feedback: "No success criteria configured."}, nil
	}

	constraint := a.Constraint
	if strings.TrimSpace(constraint) == "" {
		constraint = defaultJudgeConstraint
	}
	source := request.ConversationSummary
	if strings.TrimSpace(source) == "" {
		source = "(no conversation summary available)"
	}
	summary := request.AssistantText
	if strings.TrimSpace(summary) == "" {
		summary = "(no assistant text produced)"
	}

	result := a.Judge.EvaluateContext(ctx, constraint, source, summary, request.Criteria)
	explanation, _ := result["explanation"].(string)
	if passes, _ := result["passes"].(bool); passes {
		a.mu.Lock()
		a.consecutiveFailures = 0
		a.mu.Unlock()
		return JudgeVerdict{Action: JudgeAccept, Feedback: explanation}, nil
	}

	a.mu.Lock()
	a.consecutiveFailures++
	failures := a.consecutiveFailures
	a.mu.Unlock()

	action := JudgeRetry
	if a.EscalateAfterRetries > 0 && failures >= a.EscalateAfterRetries {
		action = JudgeEscalate
	}
	return JudgeVerdict{Action: action, Feedback: explanation}, nil
}
