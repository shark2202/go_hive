package llmjudge

// Parity: framework/testing/llm_judge.py::LLMJudge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type Provider interface {
	Complete(ctx context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error)
}

type AnthropicClient interface {
	Create(ctx context.Context, model string, maxTokens int, messages []map[string]any) (string, error)
}

const anthropicPackageRequiredMessage = "anthropic package required for LLM judge"

const defaultExplanation = "No explanation provided"

type Judge struct {
	provider Provider
	client   AnthropicClient

	FallbackFactory func() Provider
	ClientFactory   func() (AnthropicClient, error)
	AuxMaxTokens    func() int
}

// Parity: framework/testing/llm_judge.py::LLMJudge.__init__
func New(provider Provider) *Judge {
	return &Judge{provider: provider}
}

func (j *Judge) HasProvider() bool { return j.provider != nil }

// Parity: framework/testing/llm_judge.py::LLMJudge._get_client
func (j *Judge) GetClient() (AnthropicClient, error) {
	if j.client != nil {
		return j.client, nil
	}
	factory := j.ClientFactory
	if factory == nil {
		factory = defaultClientFactory
	}
	client, err := factory()
	if err != nil {
		return nil, err
	}
	j.client = client
	return client, nil
}

func defaultClientFactory() (AnthropicClient, error) {
	return nil, errors.New(anthropicPackageRequiredMessage)
}

// Parity: framework/testing/llm_judge.py::LLMJudge._get_fallback_provider
func (j *Judge) fallbackProvider() Provider {
	if j.FallbackFactory != nil {
		return j.FallbackFactory()
	}
	return defaultFallbackProvider()
}

func defaultFallbackProvider() Provider {
	if os.Getenv("OPENAI_API_KEY") != "" {
		return llm.NewProvider(llm.ProviderOptions{Model: "gpt-4o-mini"})
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		provider, err := llm.NewAnthropicProvider("", llm.DefaultAnthropicModel)
		if err != nil {
			return nil
		}
		return provider
	}
	return nil
}

func (j *Judge) auxMaxTokens() int {
	if j.AuxMaxTokens != nil {
		return j.AuxMaxTokens()
	}
	return llm.AuxMaxTokens()
}

func buildPrompt(constraint, sourceDocument, summary, criteria string) string {
	return fmt.Sprintf(`You are evaluating whether a summary meets a specific constraint.

CONSTRAINT: %s
CRITERIA: %s

SOURCE DOCUMENT:
%s

SUMMARY TO EVALUATE:
%s

Respond with JSON: {"passes": true/false, "explanation": "..."}`,
		constraint, criteria, sourceDocument, summary)
}

// Parity: framework/testing/llm_judge.py::LLMJudge.evaluate
func (j *Judge) Evaluate(constraint, sourceDocument, summary, criteria string) map[string]any {
	return j.EvaluateContext(context.Background(), constraint, sourceDocument, summary, criteria)
}

// EvaluateContext is Evaluate with a caller-supplied context, so a slow judge
// call can be cancelled with the turn that requested it.
func (j *Judge) EvaluateContext(ctx context.Context, constraint, sourceDocument, summary, criteria string) map[string]any {
	prompt := buildPrompt(constraint, sourceDocument, summary, criteria)
	result, err := j.evaluate(ctx, prompt)
	if err != nil {
		return map[string]any{
			"passes":      false,
			"explanation": "LLM judge error: " + err.Error(),
		}
	}
	return result
}

func (j *Judge) evaluate(ctx context.Context, prompt string) (map[string]any, error) {
	if j.provider != nil {
		return j.evaluateWith(ctx, j.provider, prompt)
	}
	fallback := j.fallbackProvider()
	if j.ClientFactory != nil || fallback == nil {
		client, err := j.GetClient()
		if err != nil {
			return nil, err
		}
		text, err := client.Create(ctx, llm.DefaultAnthropicModel, j.auxMaxTokens(),
			[]map[string]any{{"role": "user", "content": prompt}})
		if err != nil {
			return nil, err
		}
		return parseJSONResult(strings.TrimSpace(text))
	}
	return j.evaluateWith(ctx, fallback, prompt)
}

func (j *Judge) evaluateWith(ctx context.Context, provider Provider, prompt string) (map[string]any, error) {
	response, err := provider.Complete(ctx,
		[]map[string]any{{"role": "user", "content": prompt}},
		llm.CompleteOptions{System: "", MaxTokens: j.auxMaxTokens(), JSONMode: true})
	if err != nil {
		return nil, err
	}
	return parseJSONResult(strings.TrimSpace(response.Content))
}

// Parity: framework/testing/llm_judge.py::LLMJudge._parse_json_result
func parseJSONResult(text string) (map[string]any, error) {
	if strings.Contains(text, "```") {
		parts := strings.Split(text, "```")
		text = strings.ReplaceAll(parts[1], "json", "")
	}
	text = strings.TrimSpace(text)
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, fmt.Errorf("LLM judge error: Failed to parse JSON: %v", err)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("LLM judge error: Failed to parse JSON: %s is not a JSON object", jsonKind(decoded))
	}
	explanation := any(defaultExplanation)
	if value, present := object["explanation"]; present {
		explanation = value
	}
	return map[string]any{
		"passes":      pyTruthy(object["passes"]),
		"explanation": explanation,
	}, nil
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case []any:
		return "list"
	case string:
		return "str"
	case float64:
		return "float"
	case bool:
		return "bool"
	}
	return fmt.Sprintf("%T", value)
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}
