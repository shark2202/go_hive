package agentloop

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/llmjudge"
)

type fakeJudgeProvider struct {
	mu       sync.Mutex
	contents []string
	calls    []fakeJudgeCall
}

type fakeJudgeCall struct {
	messages []map[string]any
	opts     llm.CompleteOptions
}

func (p *fakeJudgeProvider) Complete(_ context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fakeJudgeCall{messages: messages, opts: opts})
	index := len(p.calls) - 1
	if index >= len(p.contents) {
		index = len(p.contents) - 1
	}
	return llm.Completion{Content: p.contents[index], Model: "fake-judge"}, nil
}

func (p *fakeJudgeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *fakeJudgeProvider) prompt(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls[index].messages) == 0 {
		return ""
	}
	content, _ := p.calls[index].messages[0]["content"].(string)
	return content
}

func judgeAdapterRequest() JudgeRequest {
	return JudgeRequest{
		AgentSpec:           NewAgentSpec("agent", "Agent", "deliver it"),
		Criteria:            "the report names every risk",
		AssistantText:       "final report",
		ConversationSummary: "[RECENT_MESSAGES]\n  [user] do the work",
		Iteration:           1,
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_uses_provider
func TestLLMJudgeAdapterAcceptsWhenCriteriaMet(t *testing.T) {
	provider := &fakeJudgeProvider{contents: []string{`{"passes": true, "explanation": "criteria met"}`}}
	adapter := NewLLMJudgeAdapter(llmjudge.New(provider))

	verdict, err := adapter.Evaluate(context.Background(), judgeAdapterRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Action != JudgeAccept || verdict.Feedback != "criteria met" {
		t.Fatalf("verdict = %#v", verdict)
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.callCount())
	}
	prompt := provider.prompt(0)
	for _, want := range []string{"success-criteria", "the report names every risk", "final report", "[RECENT_MESSAGES]"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if !provider.calls[0].opts.JSONMode {
		t.Fatal("JSONMode = false, want true")
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_failing_result
func TestLLMJudgeAdapterRetriesWhenCriteriaFail(t *testing.T) {
	provider := &fakeJudgeProvider{contents: []string{`{"passes": false, "explanation": "risks are missing"}`}}
	adapter := NewLLMJudgeAdapter(llmjudge.New(provider))

	verdict, err := adapter.Evaluate(context.Background(), judgeAdapterRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Action != JudgeRetry || verdict.Feedback != "risks are missing" {
		t.Fatalf("verdict = %#v", verdict)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeIntegrationPatterns::test_with_multiple_evaluations
func TestLLMJudgeAdapterEscalatesAfterConsecutiveFailures(t *testing.T) {
	provider := &fakeJudgeProvider{contents: []string{`{"passes": false, "explanation": "still missing"}`}}
	adapter := NewLLMJudgeAdapter(llmjudge.New(provider))
	adapter.EscalateAfterRetries = 2

	first, err := adapter.Evaluate(context.Background(), judgeAdapterRequest())
	if err != nil {
		t.Fatalf("first Evaluate: %v", err)
	}
	if first.Action != JudgeRetry {
		t.Fatalf("first verdict = %#v, want RETRY", first)
	}
	second, err := adapter.Evaluate(context.Background(), judgeAdapterRequest())
	if err != nil {
		t.Fatalf("second Evaluate: %v", err)
	}
	if second.Action != JudgeEscalate {
		t.Fatalf("second verdict = %#v, want ESCALATE", second)
	}
	if provider.callCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.callCount())
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_init_with_provider
func TestLLMJudgeAdapterFromCompleter(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: `{"passes": true, "explanation": "ok"}`},
	}}
	adapter := NewLLMJudgeAdapterFromCompleter(completer)

	verdict, err := adapter.Evaluate(context.Background(), judgeAdapterRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Action != JudgeAccept {
		t.Fatalf("verdict = %#v, want ACCEPT", verdict)
	}
	if completer.callCount() != 1 {
		t.Fatalf("completer calls = %d, want 1", completer.callCount())
	}
	request := completer.request(0)
	if !request.JSONMode {
		t.Fatal("JSONMode = false, want true")
	}
	if !strings.Contains(request.Messages[0].Content, "the report names every risk") {
		t.Fatalf("judge prompt missing criteria: %#v", request.Messages)
	}
}

// Parity-exempt: unlike upstream LLMJudge, the adapter refuses to spend a
// model call when no success criteria exist.
func TestLLMJudgeAdapterSkipsWithoutCriteria(t *testing.T) {
	provider := &fakeJudgeProvider{contents: []string{`{"passes": false, "explanation": "should not run"}`}}
	adapter := NewLLMJudgeAdapter(llmjudge.New(provider))
	request := judgeAdapterRequest()
	request.Criteria = "   "

	verdict, err := adapter.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if verdict.Action != JudgeAccept {
		t.Fatalf("verdict = %#v, want ACCEPT for an unevaluated turn", verdict)
	}
	if provider.callCount() != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.callCount())
	}
}
