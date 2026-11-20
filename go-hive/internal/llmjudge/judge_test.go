package llmjudge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

type mockProvider struct {
	responseContent string
	calls           []mockCall
	err             error
}

type mockCall struct {
	Messages  []map[string]any
	System    string
	MaxTokens int
	JSONMode  bool
}

func (m *mockProvider) Complete(_ context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error) {
	m.calls = append(m.calls, mockCall{
		Messages:  messages,
		System:    opts.System,
		MaxTokens: opts.MaxTokens,
		JSONMode:  opts.JSONMode,
	})
	if m.err != nil {
		return llm.Completion{}, m.err
	}
	return llm.Completion{Content: m.responseContent, Model: "mock-model", InputTokens: 100, OutputTokens: 50}, nil
}

type fakeClient struct {
	text      string
	model     string
	maxTokens int
	messages  []map[string]any
	calls     int
	err       error
}

func (c *fakeClient) Create(_ context.Context, model string, maxTokens int, messages []map[string]any) (string, error) {
	c.calls++
	c.model = model
	c.maxTokens = maxTokens
	c.messages = messages
	if c.err != nil {
		return "", c.err
	}
	return c.text, nil
}

func newProvider(content string) *mockProvider {
	return &mockProvider{responseContent: content}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_init_with_provider
func TestInitWithProvider(t *testing.T) {
	provider := newProvider(`{"passes": true, "explanation": "Test passed"}`)
	judge := New(provider)

	if judge.provider != provider {
		t.Fatalf("judge.provider = %v, want the injected provider", judge.provider)
	}
	if judge.client != nil {
		t.Fatalf("judge.client = %v, want nil", judge.client)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_uses_provider
func TestEvaluateUsesProvider(t *testing.T) {
	provider := newProvider(`{"passes": true, "explanation": "Summary is accurate"}`)
	judge := New(provider)

	result := judge.Evaluate("no-hallucination", "The sky is blue.", "The sky is blue.",
		"Summary must only contain facts from source")

	if result["passes"] != true {
		t.Fatalf("passes = %v, want true", result["passes"])
	}
	if result["explanation"] != "Summary is accurate" {
		t.Fatalf("explanation = %v", result["explanation"])
	}
	if len(provider.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(provider.calls))
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_passes_correct_arguments
func TestEvaluatePassesCorrectArguments(t *testing.T) {
	provider := newProvider(`{"passes": true, "explanation": "Test passed"}`)
	judge := New(provider)

	judge.Evaluate("test-constraint", "Source text", "Summary text", "Test criteria")

	call := provider.calls[0]
	if call.MaxTokens != llm.ResolveAuxMaxTokens(0) {
		t.Fatalf("max_tokens = %d, want %d", call.MaxTokens, llm.ResolveAuxMaxTokens(0))
	}
	if !call.JSONMode {
		t.Fatal("json_mode = false, want true")
	}
	if call.System != "" {
		t.Fatalf("system = %q, want empty", call.System)
	}
	if len(call.Messages) != 1 {
		t.Fatalf("messages length = %d, want 1", len(call.Messages))
	}
	if call.Messages[0]["role"] != "user" {
		t.Fatalf("role = %v, want user", call.Messages[0]["role"])
	}
	prompt, _ := call.Messages[0]["content"].(string)
	for _, want := range []string{"test-constraint", "Source text", "Summary text", "Test criteria"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_failing_result
func TestEvaluateFailingResult(t *testing.T) {
	provider := newProvider(`{"passes": false, "explanation": "Summary has hallucinated facts"}`)
	judge := New(provider)

	result := judge.Evaluate("no-hallucination", "The sky is blue.", "The sky is green and has rainbows.",
		"Summary must only contain facts from source")

	if result["passes"] != false {
		t.Fatalf("passes = %v, want false", result["passes"])
	}
	explanation, _ := result["explanation"].(string)
	if !strings.Contains(explanation, "hallucinated") {
		t.Fatalf("explanation = %q", explanation)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_parse_plain_json
func TestParsePlainJSON(t *testing.T) {
	judge := New(newProvider(`{"passes": true, "explanation": "OK"}`))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true || result["explanation"] != "OK" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_parse_json_in_markdown_code_block
func TestParseJSONInMarkdownCodeBlock(t *testing.T) {
	judge := New(newProvider("```json\n{\"passes\": false, \"explanation\": \"Failed\"}\n```"))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != false || result["explanation"] != "Failed" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_parse_json_in_plain_code_block
func TestParseJSONInPlainCodeBlock(t *testing.T) {
	judge := New(newProvider("```\n{\"passes\": true, \"explanation\": \"Passed\"}\n```"))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true || result["explanation"] != "Passed" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_parse_response_with_whitespace
func TestParseResponseWithWhitespace(t *testing.T) {
	judge := New(newProvider("\n  {\"passes\": true, \"explanation\": \"Clean\"}  \n"))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true {
		t.Fatalf("passes = %v, want true", result["passes"])
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_default_explanation_when_missing
func TestDefaultExplanationWhenMissing(t *testing.T) {
	judge := New(newProvider(`{"passes": true}`))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true || result["explanation"] != "No explanation provided" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_passes_coerced_to_bool
func TestPassesCoercedToBool(t *testing.T) {
	judge := New(newProvider(`{"passes": "yes", "explanation": "OK"}`))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true {
		t.Fatalf("passes = %v, want true", result["passes"])
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeResponseParsing::test_passes_false_when_missing
func TestPassesFalseWhenMissing(t *testing.T) {
	judge := New(newProvider(`{"explanation": "No pass key"}`))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != false {
		t.Fatalf("passes = %v, want false", result["passes"])
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeErrorHandling::test_invalid_json_response
func TestInvalidJSONResponse(t *testing.T) {
	judge := New(newProvider("This is not JSON"))

	result := judge.Evaluate("test", "doc", "sum", "crit")

	explanation, _ := result["explanation"].(string)
	if result["passes"] != false || !strings.Contains(explanation, "LLM judge error") {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeErrorHandling::test_provider_raises_exception
func TestProviderRaisesException(t *testing.T) {
	provider := newProvider(`{"passes": true, "explanation": "Test passed"}`)
	provider.err = errors.New("API error")
	judge := New(provider)

	result := judge.Evaluate("test", "doc", "sum", "crit")

	explanation, _ := result["explanation"].(string)
	if result["passes"] != false {
		t.Fatalf("passes = %v, want false", result["passes"])
	}
	if !strings.Contains(explanation, "LLM judge error") || !strings.Contains(explanation, "API error") {
		t.Fatalf("explanation = %q", explanation)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_init_without_provider
func TestInitWithoutProvider(t *testing.T) {
	judge := New(nil)

	if judge.provider != nil {
		t.Fatalf("provider = %v, want nil", judge.provider)
	}
	if judge.client != nil {
		t.Fatalf("client = %v, want nil", judge.client)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_evaluate_without_provider_uses_anthropic
func TestEvaluateWithoutProviderUsesAnthropic(t *testing.T) {
	judge := New(nil)
	client := &fakeClient{text: `{"passes": true, "explanation": "Anthropic response"}`}
	judge.ClientFactory = func() (AnthropicClient, error) { return client, nil }

	result := judge.Evaluate("test", "doc", "sum", "crit")

	if result["passes"] != true || result["explanation"] != "Anthropic response" {
		t.Fatalf("result = %v", result)
	}
	if client.calls != 1 {
		t.Fatalf("client calls = %d, want 1", client.calls)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_anthropic_client_lazy_loaded
func TestAnthropicClientLazyLoaded(t *testing.T) {
	judge := New(nil)

	if judge.client != nil {
		t.Fatalf("client = %v, want nil before first use", judge.client)
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_anthropic_import_error_handling
func TestAnthropicImportErrorHandling(t *testing.T) {
	judge := New(nil)

	client, err := judge.GetClient()
	if err == nil {
		t.Fatalf("GetClient error = nil, client = %v", client)
	}
	if !strings.Contains(err.Error(), "anthropic package required") {
		t.Fatalf("error = %q, want 'anthropic package required'", err.Error())
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_anthropic_client_uses_correct_model
func TestAnthropicClientUsesCorrectModel(t *testing.T) {
	judge := New(nil)
	client := &fakeClient{text: `{"passes": true, "explanation": "OK"}`}
	judge.ClientFactory = func() (AnthropicClient, error) { return client, nil }

	judge.Evaluate("test", "doc", "sum", "crit")

	if client.model != "claude-haiku-4-5-20251001" {
		t.Fatalf("model = %q", client.model)
	}
	if client.maxTokens != llm.ResolveAuxMaxTokens(0) {
		t.Fatalf("max_tokens = %d, want %d", client.maxTokens, llm.ResolveAuxMaxTokens(0))
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeBackwardCompatibility::test_openai_fallback_uses_litellm_provider
func TestOpenAIFallbackUsesLiteLLMProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test-openai")
	t.Setenv("ANTHROPIC_API_KEY", "")

	fallback := defaultFallbackProvider()
	provider, ok := fallback.(*llm.Provider)
	if !ok {
		t.Fatalf("fallback provider = %T, want *llm.Provider", fallback)
	}
	if provider.Model != "gpt-4o-mini" {
		t.Fatalf("fallback model = %q, want gpt-4o-mini", provider.Model)
	}

	recorded := newProvider(`{"passes": true, "explanation": "OK"}`)
	judge := New(nil)
	judge.FallbackFactory = func() Provider { return recorded }

	result := judge.Evaluate("no-hallucination", "The sky is blue.", "The sky is blue.",
		"Summary must only contain facts from source")

	if result["passes"] != true || result["explanation"] != "OK" {
		t.Fatalf("result = %v", result)
	}
	if len(recorded.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(recorded.calls))
	}
	call := recorded.calls[0]
	if call.MaxTokens != llm.ResolveAuxMaxTokens(0) {
		t.Fatalf("max_tokens = %d, want %d", call.MaxTokens, llm.ResolveAuxMaxTokens(0))
	}
	if !call.JSONMode {
		t.Fatal("json_mode = false, want true")
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeIntegrationPatterns::test_with_anthropic_provider
func TestWithAnthropicProvider(t *testing.T) {
	judge := New(newProvider(`{"passes": true, "explanation": "Matches source"}`))

	result := judge.Evaluate("factual-accuracy", "Python was created by Guido van Rossum.",
		"Python's creator is Guido van Rossum.", "Summary must be factually accurate")

	if result["passes"] != true {
		t.Fatalf("passes = %v, want true", result["passes"])
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeIntegrationPatterns::test_with_multiple_evaluations
func TestWithMultipleEvaluations(t *testing.T) {
	provider := newProvider(`{"passes": true, "explanation": "Test passed"}`)
	judge := New(provider)

	for i := 0; i < 3; i++ {
		judge.Evaluate("constraint", "Source", "Summary", "Criteria")
	}

	if len(provider.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(provider.calls))
	}
}

// Parity: tests/test_llm_judge.py::TestLLMJudgeIntegrationPatterns::test_provider_reuse_across_judges
func TestProviderReuseAcrossJudges(t *testing.T) {
	shared := newProvider(`{"passes": true, "explanation": "Test passed"}`)

	judge1 := New(shared)
	judge2 := New(shared)

	judge1.Evaluate("c1", "d1", "s1", "cr1")
	judge2.Evaluate("c2", "d2", "s2", "cr2")

	if len(shared.calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(shared.calls))
	}
}

// TestConfiguredAuxMaxTokensReachesTheJudge proves llm.aux_max_tokens is
// consumed: the process-wide budget installed from configuration.json is what
// the judge sends, not the built-in 8192.
//
// Parity: tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_passes_correct_arguments
func TestConfiguredAuxMaxTokensReachesTheJudge(t *testing.T) {
	previous := llm.AuxMaxTokens()
	llm.SetAuxMaxTokens(2048)
	t.Cleanup(func() { llm.SetAuxMaxTokens(previous) })

	provider := newProvider(`{"passes": true, "explanation": "ok"}`)
	New(provider).Evaluate("c", "d", "s", "cr")
	if len(provider.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(provider.calls))
	}
	if provider.calls[0].MaxTokens != 2048 {
		t.Fatalf("max_tokens = %d, want the configured 2048", provider.calls[0].MaxTokens)
	}

	// Clearing the override restores the built-in cap.
	llm.SetAuxMaxTokens(0)
	provider = newProvider(`{"passes": true, "explanation": "ok"}`)
	New(provider).Evaluate("c", "d", "s", "cr")
	if provider.calls[0].MaxTokens != llm.DefaultAuxMaxTokens {
		t.Fatalf("max_tokens = %d, want the default %d", provider.calls[0].MaxTokens, llm.DefaultAuxMaxTokens)
	}
}
