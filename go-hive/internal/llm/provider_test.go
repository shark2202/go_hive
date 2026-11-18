package llm

import (
	"errors"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func approxEqual(got, want, tolerance float64) bool {
	return math.Abs(got-want) <= tolerance
}

func retryHeadersError(headers map[string]string) error {
	return &ProviderError{Message: "rate limited", Headers: headers}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_with_defaults
func TestProviderInitWithDefaults(t *testing.T) {
	p := NewProvider(ProviderOptions{})
	if p.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", p.Model)
	}
	if p.APIKey != "" || p.APIBase != "" {
		t.Errorf("unexpected defaults: %+v", p)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_with_custom_model
func TestProviderInitWithCustomModel(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "claude-3-haiku-20240307"})
	if p.Model != "claude-3-haiku-20240307" {
		t.Errorf("model = %q", p.Model)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_deepseek_model
func TestProviderInitDeepseekModel(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "deepseek/deepseek-chat"})
	if p.Model != "deepseek/deepseek-chat" {
		t.Errorf("model = %q", p.Model)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_with_api_key
func TestProviderInitWithAPIKey(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "my-api-key"})
	if p.APIKey != "my-api-key" {
		t.Errorf("api_key = %q", p.APIKey)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_with_api_base
func TestProviderInitWithAPIBase(t *testing.T) {
	p := NewProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "my-key",
		APIBase: "https://my-proxy.com/v1",
	})
	if p.APIBase != "https://my-proxy.com/v1" {
		t.Errorf("api_base = %q", p.APIBase)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_minimax_defaults_api_base
func TestProviderInitMinimaxDefaultsAPIBase(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "minimax/MiniMax-M2.1", APIKey: "my-key"})
	if p.APIBase != MinimaxAPIBase {
		t.Errorf("api_base = %q, want %q", p.APIBase, MinimaxAPIBase)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_minimax_keeps_custom_api_base
func TestProviderInitMinimaxKeepsCustomAPIBase(t *testing.T) {
	p := NewProvider(ProviderOptions{
		Model:   "minimax/MiniMax-M2.1",
		APIKey:  "my-key",
		APIBase: "https://proxy.example/v1",
	})
	if p.APIBase != "https://proxy.example/v1" {
		t.Errorf("api_base = %q", p.APIBase)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_openrouter_defaults_api_base
func TestProviderInitOpenRouterDefaultsAPIBase(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "openrouter/x-ai/grok-4.20-beta", APIKey: "my-key"})
	if p.APIBase != OpenRouterAPIBase {
		t.Errorf("api_base = %q, want %q", p.APIBase, OpenRouterAPIBase)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_openrouter_keeps_custom_api_base
func TestProviderInitOpenRouterKeepsCustomAPIBase(t *testing.T) {
	p := NewProvider(ProviderOptions{
		Model:   "openrouter/x-ai/grok-4.20-beta",
		APIKey:  "my-key",
		APIBase: "https://proxy.example/v1",
	})
	if p.APIBase != "https://proxy.example/v1" {
		t.Errorf("api_base = %q", p.APIBase)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_init_ollama_no_key_needed
func TestProviderInitOllamaNoKeyNeeded(t *testing.T) {
	p := NewProvider(ProviderOptions{Model: "ollama/llama3"})
	if p.Model != "ollama_chat/llama3" {
		t.Errorf("model = %q, want ollama_chat/llama3", p.Model)
	}
	if p.APIKey != "" {
		t.Errorf("api_key = %q, want empty", p.APIKey)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderInit::test_summarize_request_flags_system_only_payload
func TestSummarizeRequestFlagsSystemOnlyPayload(t *testing.T) {
	summary := SummarizeRequestForLog(map[string]any{
		"model":    "openai/glm-5",
		"api_base": "https://api.z.ai/api/coding/paas/v4",
		"messages": []map[string]any{
			{"role": "system", "content": "You are helpful."},
		},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": "read_file"}},
		},
		"stream":     true,
		"max_tokens": 8192,
	})

	if summary["message_count"] != 1 {
		t.Errorf("message_count = %v", summary["message_count"])
	}
	if summary["non_system_message_count"] != 0 {
		t.Errorf("non_system_message_count = %v", summary["non_system_message_count"])
	}
	if summary["first_non_system_role"] != nil || summary["last_non_system_role"] != nil {
		t.Errorf("non-system roles should be nil: %v / %v",
			summary["first_non_system_role"], summary["last_non_system_role"])
	}
	if summary["system_only"] != true {
		t.Errorf("system_only = %v", summary["system_only"])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestToolConversion::test_tool_to_openai_format
func TestToolToOpenAIFormat(t *testing.T) {
	tool := ToolDef{
		Name:        "search",
		Description: "Search the web",
		Parameters: map[string]any{
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query"},
			},
			"required": []any{"query"},
		},
	}

	result := ToolToOpenAIFormat(tool)
	if result["type"] != "function" {
		t.Errorf("type = %v", result["type"])
	}
	fn, ok := result["function"].(map[string]any)
	if !ok {
		t.Fatalf("function = %T", result["function"])
	}
	if fn["name"] != "search" || fn["description"] != "Search the web" {
		t.Errorf("function = %v", fn)
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %T", fn["parameters"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T", params["properties"])
	}
	query, ok := properties["query"].(map[string]any)
	if !ok || query["type"] != "string" {
		t.Errorf("query = %v", properties["query"])
	}
	if !reflect.DeepEqual(params["required"], []any{"query"}) {
		t.Errorf("required = %v", params["required"])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestToolConversion::test_parse_tool_call_arguments_repairs_truncated_json
func TestParseToolCallArgumentsRepairsTruncatedJSON(t *testing.T) {
	parsed, err := ParseToolCallArguments(
		`{"question":"What story structure should the agent use?","options":["3-act structure","Beginning-Middle-End","Random paragraph"`,
		"ask_user",
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]any{
		"question": "What story structure should the agent use?",
		"options": []any{
			"3-act structure",
			"Beginning-Middle-End",
			"Random paragraph",
		},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("parsed = %#v, want %#v", parsed, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestToolConversion::test_parse_tool_call_arguments_raises_when_unrepairable
func TestParseToolCallArgumentsRaisesWhenUnrepairable(t *testing.T) {
	_, err := ParseToolCallArguments(`{"question": foo`, "ask_user")
	if err == nil {
		t.Fatal("expected error for unrepairable JSON")
	}
	if !strings.Contains(err.Error(), "Failed to parse tool call arguments") {
		t.Errorf("error = %v", err)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_fallback_exponential_backoff
func TestComputeRetryDelayFallbackExponential(t *testing.T) {
	cases := map[int]float64{0: 2, 1: 4, 2: 8, 3: 16}
	for attempt, want := range cases {
		if got := ComputeRetryDelay(attempt, nil, DefaultRetryPolicy()); got != want {
			t.Errorf("attempt %d = %v, want %v", attempt, got, want)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_max_delay_cap
func TestComputeRetryDelayMaxCap(t *testing.T) {
	if got := ComputeRetryDelay(10, nil, DefaultRetryPolicy()); got != RateLimitMaxDelay {
		t.Errorf("delay = %v, want %v", got, RateLimitMaxDelay)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_custom_max_delay
func TestComputeRetryDelayCustomMaxDelay(t *testing.T) {
	if got := ComputeRetryDelay(5, nil, RetryPolicy{MaxDelay: 10}); got != 10 {
		t.Errorf("delay = %v, want 10", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_ms_header
func TestComputeRetryDelayRetryAfterMSHeader(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after-ms": "5000"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 5.0 {
		t.Errorf("delay = %v, want 5.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_ms_fractional
func TestComputeRetryDelayRetryAfterMSFractional(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after-ms": "1500"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 1.5 {
		t.Errorf("delay = %v, want 1.5", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_seconds_header
func TestComputeRetryDelayRetryAfterSecondsHeader(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "3"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 3.0 {
		t.Errorf("delay = %v, want 3.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_seconds_fractional
func TestComputeRetryDelayRetryAfterSecondsFractional(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "2.5"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 2.5 {
		t.Errorf("delay = %v, want 2.5", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_ms_takes_priority
func TestComputeRetryDelayRetryAfterMSTakesPriority(t *testing.T) {
	err := retryHeadersError(map[string]string{
		"retry-after-ms": "2000",
		"retry-after":    "10",
	})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 2.0 {
		t.Errorf("delay = %v, want 2.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_http_date
func TestComputeRetryDelayRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().UTC().Add(5 * time.Second).Format(http.TimeFormat)
	err := retryHeadersError(map[string]string{"retry-after": future})
	delay := ComputeRetryDelay(0, err, DefaultRetryPolicy())
	if delay < 3.0 || delay > 6.0 {
		t.Errorf("delay = %v, want within [3.0, 6.0]", delay)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_exception_without_response
func TestComputeRetryDelayExceptionWithoutResponse(t *testing.T) {
	if got := ComputeRetryDelay(0, errors.New("test"), DefaultRetryPolicy()); got != 2 {
		t.Errorf("delay = %v, want 2", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_exception_without_response_attr
func TestComputeRetryDelayExceptionWithoutResponseAttr(t *testing.T) {
	if got := ComputeRetryDelay(0, errors.New("no response attr"), DefaultRetryPolicy()); got != 2 {
		t.Errorf("delay = %v, want 2", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_negative_retry_after_clamped_to_zero
func TestComputeRetryDelayNegativeRetryAfterClampedToZero(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "-5"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 0 {
		t.Errorf("delay = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_negative_retry_after_ms_clamped_to_zero
func TestComputeRetryDelayNegativeRetryAfterMSClampedToZero(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after-ms": "-1000"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 0 {
		t.Errorf("delay = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_invalid_retry_after_falls_back
func TestComputeRetryDelayInvalidRetryAfterFallsBack(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "not-a-number-or-date"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 2 {
		t.Errorf("delay = %v, want 2", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_invalid_retry_after_ms_falls_back_to_retry_after
func TestComputeRetryDelayInvalidRetryAfterMSFallsBackToRetryAfter(t *testing.T) {
	err := retryHeadersError(map[string]string{
		"retry-after-ms": "garbage",
		"retry-after":    "7",
	})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 7.0 {
		t.Errorf("delay = %v, want 7.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_capped_at_max_delay
func TestComputeRetryDelayRetryAfterCappedAtMaxDelay(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "3600"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != RateLimitMaxDelay {
		t.Errorf("delay = %v, want %v", got, RateLimitMaxDelay)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_ms_capped_at_max_delay
func TestComputeRetryDelayRetryAfterMSCappedAtMaxDelay(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after-ms": "300000"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != RateLimitMaxDelay {
		t.Errorf("delay = %v, want %v", got, RateLimitMaxDelay)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_retry_after_from_exception_headers_attr
func TestComputeRetryDelayFromExceptionHeadersAttr(t *testing.T) {
	err := retryHeadersError(map[string]string{"retry-after": "1"})
	if got := ComputeRetryDelay(3, err, DefaultRetryPolicy()); got != 1.0 {
		t.Errorf("delay = %v, want 1.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_backpressure_message_implicit_short_retry
func TestComputeRetryDelayBackpressureMessageImplicitShortRetry(t *testing.T) {
	err := errors.New(`AnthropicException - {"type":"error","error":{"type":"backpressure","message":"concurrent limit (per_user)"}}`)
	if got := ComputeRetryDelay(3, err, DefaultRetryPolicy()); got != BackpressureRetryFloor {
		t.Errorf("delay = %v, want %v", got, BackpressureRetryFloor)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_backpressure_does_not_override_explicit_retry_after
func TestComputeRetryDelayBackpressureDoesNotOverrideExplicitRetryAfter(t *testing.T) {
	err := &ProviderError{
		Message: "backpressure: concurrent limit",
		Headers: map[string]string{"retry-after": "7"},
	}
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 7.0 {
		t.Errorf("delay = %v, want 7.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_jitter_widens_exponential_backoff
func TestComputeRetryDelayJitterWidensExponentialBackoff(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.Jitter = true
	seen := map[float64]bool{}
	for i := 0; i < 100; i++ {
		delay := ComputeRetryDelay(3, nil, policy)
		if delay < 12.0 || delay > 20.0 {
			t.Fatalf("delay = %v, want within [12, 20]", delay)
		}
		seen[delay] = true
	}
	if len(seen) < 3 {
		t.Errorf("distinct delays = %d, want >= 3", len(seen))
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_jitter_lengthens_short_retry_after
func TestComputeRetryDelayJitterLengthensShortRetryAfter(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.Jitter = true
	err := retryHeadersError(map[string]string{"retry-after": "1"})
	seen := map[float64]bool{}
	for i := 0; i < 100; i++ {
		delay := ComputeRetryDelay(0, err, policy)
		if delay < 1.0 || delay > 2.0 {
			t.Fatalf("delay = %v, want within [1, 2]", delay)
		}
		seen[delay] = true
	}
	if len(seen) < 3 {
		t.Errorf("distinct delays = %d, want >= 3", len(seen))
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_jitter_skips_long_retry_after
func TestComputeRetryDelayJitterSkipsLongRetryAfter(t *testing.T) {
	policy := DefaultRetryPolicy()
	policy.Jitter = true
	err := retryHeadersError(map[string]string{"retry-after": "60"})
	if got := ComputeRetryDelay(0, err, policy); got != 60.0 {
		t.Errorf("delay = %v, want 60.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestComputeRetryDelay::test_jitter_default_off_preserves_exact_values
func TestComputeRetryDelayJitterDefaultOffPreservesExactValues(t *testing.T) {
	if got := ComputeRetryDelay(3, nil, DefaultRetryPolicy()); got != 16 {
		t.Errorf("delay = %v, want 16", got)
	}
	err := retryHeadersError(map[string]string{"retry-after": "1"})
	if got := ComputeRetryDelay(0, err, DefaultRetryPolicy()); got != 1.0 {
		t.Errorf("delay = %v, want 1.0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestMiniMaxStreamFallback::test_is_minimax_model_variants
func TestIsMinimaxModelVariants(t *testing.T) {
	if !IsMinimaxModel("minimax-text-01") {
		t.Error("minimax-text-01 should be MiniMax")
	}
	if !IsMinimaxModel("minimax/minimax-text-01") {
		t.Error("minimax/minimax-text-01 should be MiniMax")
	}
	if IsMinimaxModel("gpt-4o-mini") {
		t.Error("gpt-4o-mini should not be MiniMax")
	}
}

// Parity: core/tests/test_litellm_provider.py::TestIsLocalModel::test_local_models_return_true
func TestLocalModelsReturnTrue(t *testing.T) {
	for _, model := range []string{
		"ollama/llama3",
		"ollama/mistral",
		"ollama_chat/llama3",
		"vllm/mistral",
		"lm_studio/phi3",
		"llamacpp/llama-7b",
		"Ollama/Llama3",
		"VLLM/Mistral",
	} {
		if !IsLocalModel(model) {
			t.Errorf("%q should be local", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestIsLocalModel::test_cloud_models_return_false
func TestCloudModelsReturnFalse(t *testing.T) {
	for _, model := range []string{
		"anthropic/claude-3-haiku",
		"openai/gpt-4o",
		"gpt-4o-mini",
		"claude-3-haiku-20240307",
		"gemini/gemini-1.5-flash",
		"groq/llama3-70b",
		"mistral/mistral-large",
		"azure/gpt-4",
		"cohere/command-r",
		"together/llama3-70b",
	} {
		if IsLocalModel(model) {
			t.Errorf("%q should not be local", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestIsOllamaModel::test_ollama_models_return_true
func TestOllamaModelsReturnTrue(t *testing.T) {
	for _, model := range []string{
		"ollama/llama3",
		"ollama/mistral:7b",
		"ollama_chat/llama3",
		"ollama_chat/qwen2.5:72b",
	} {
		if !IsOllamaModel(model) {
			t.Errorf("%q should be Ollama", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestIsOllamaModel::test_non_ollama_models_return_false
func TestNonOllamaModelsReturnFalse(t *testing.T) {
	for _, model := range []string{
		"gpt-4o-mini",
		"anthropic/claude-3-haiku",
		"openai/gpt-4o",
		"gemini/gemini-1.5-flash",
		"llama3",
		"",
	} {
		if IsOllamaModel(model) {
			t.Errorf("%q should not be Ollama", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestEnsureOllamaChatPrefix::test_rewrites_ollama_to_ollama_chat
func TestRewritesOllamaToOllamaChat(t *testing.T) {
	cases := map[string]string{
		"ollama/llama3":               "ollama_chat/llama3",
		"ollama/mistral:7b":           "ollama_chat/mistral:7b",
		"ollama/qwen2.5:72b-instruct": "ollama_chat/qwen2.5:72b-instruct",
	}
	for input, want := range cases {
		if got := EnsureOllamaChatPrefix(input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestEnsureOllamaChatPrefix::test_leaves_non_ollama_prefix_unchanged
func TestLeavesNonOllamaPrefixUnchanged(t *testing.T) {
	for _, model := range []string{
		"ollama_chat/llama3",
		"gpt-4o-mini",
		"anthropic/claude-3-haiku",
		"gemini/gemini-1.5-flash",
		"",
	} {
		if got := EnsureOllamaChatPrefix(model); got != model {
			t.Errorf("%q = %q, want unchanged", model, got)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_skips_on_non_cache_control_model
func TestHistoryCacheBreakpointSkipsOnNonCacheControlModel(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "u1"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "u2"},
	}
	ApplyHistoryCacheBreakpoint(msgs, "openai/gpt-4o-mini")
	for _, m := range msgs {
		if _, ok := m["cache_control"]; ok {
			t.Errorf("unexpected cache_control on %v", m)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_anchors_on_last_message_tool_result
func TestHistoryCacheBreakpointAnchorsOnLastToolResult(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "task"},
		{"role": "assistant", "content": "a1"},
		{"role": "tool", "content": "t1", "tool_call_id": "x"},
	}
	ApplyHistoryCacheBreakpoint(msgs, "anthropic/claude-opus-4-5")
	want := map[string]any{"type": "ephemeral"}
	if !reflect.DeepEqual(msgs[3]["cache_control"], want) {
		t.Errorf("anchor = %v", msgs[3]["cache_control"])
	}
	for i, m := range msgs {
		if i == 3 {
			continue
		}
		if _, ok := m["cache_control"]; ok {
			t.Errorf("collateral cache_control at %d", i)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_anchors_on_last_user_message_at_turn_start
func TestHistoryCacheBreakpointAnchorsOnLastUserMessage(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "u1"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "u2"},
	}
	ApplyHistoryCacheBreakpoint(msgs, "claude-3-5-sonnet-20241022")
	if !reflect.DeepEqual(msgs[3]["cache_control"], map[string]any{"type": "ephemeral"}) {
		t.Errorf("anchor = %v", msgs[3]["cache_control"])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_first_turn_anchors_on_the_user_message
func TestHistoryCacheBreakpointFirstTurnAnchorsOnUserMessage(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "u1"},
	}
	ApplyHistoryCacheBreakpoint(msgs, "anthropic/claude-opus-4-5")
	if _, ok := msgs[0]["cache_control"]; ok {
		t.Error("system message should not carry the anchor")
	}
	if !reflect.DeepEqual(msgs[1]["cache_control"], map[string]any{"type": "ephemeral"}) {
		t.Errorf("anchor = %v", msgs[1]["cache_control"])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_skips_on_system_only_request
func TestHistoryCacheBreakpointSkipsOnSystemOnlyRequest(t *testing.T) {
	msgs := []map[string]any{{"role": "system", "content": "s"}}
	ApplyHistoryCacheBreakpoint(msgs, "anthropic/claude-opus-4-5")
	if _, ok := msgs[0]["cache_control"]; ok {
		t.Error("system-only request should not be anchored")
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_skips_on_empty_list
func TestHistoryCacheBreakpointSkipsOnEmptyList(t *testing.T) {
	var msgs []map[string]any
	ApplyHistoryCacheBreakpoint(msgs, "anthropic/claude-opus-4-5")
	if len(msgs) != 0 {
		t.Errorf("empty list mutated: %v", msgs)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestApplyHistoryCacheBreakpoint::test_handles_openrouter_anthropic_routing
func TestHistoryCacheBreakpointHandlesOpenRouterAnthropic(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "s"},
		{"role": "user", "content": "u1"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "u2"},
	}
	ApplyHistoryCacheBreakpoint(msgs, "openrouter/anthropic/claude-opus-4.5")
	if !reflect.DeepEqual(msgs[3]["cache_control"], map[string]any{"type": "ephemeral"}) {
		t.Errorf("anchor = %v", msgs[3]["cache_control"])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestAppendJsonInstruction::test_appends_to_string_system_content
func TestAppendJSONInstructionAppendsToStringSystemContent(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "u"},
	}
	AppendJSONInstruction(&msgs)
	want := "sys\n\nPlease respond with a valid JSON object."
	if msgs[0]["content"] != want {
		t.Errorf("content = %q, want %q", msgs[0]["content"], want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestAppendJsonInstruction::test_appends_text_block_to_list_system_content
func TestAppendJSONInstructionAppendsTextBlockToListSystemContent(t *testing.T) {
	msgs := []map[string]any{
		{
			"role": "system",
			"content": []any{
				map[string]any{"type": "text", "text": "static", "cache_control": map[string]any{"type": "ephemeral"}},
				map[string]any{"type": "text", "text": "suffix"},
			},
		},
		{"role": "user", "content": "u"},
	}
	AppendJSONInstruction(&msgs)
	blocks, ok := msgs[0]["content"].([]any)
	if !ok {
		t.Fatalf("content = %T", msgs[0]["content"])
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(blocks))
	}
	want := map[string]any{"type": "text", "text": "Please respond with a valid JSON object."}
	if !reflect.DeepEqual(blocks[2], want) {
		t.Errorf("block = %v, want %v", blocks[2], want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestAppendJsonInstruction::test_inserts_system_message_when_absent
func TestAppendJSONInstructionInsertsSystemMessage(t *testing.T) {
	msgs := []map[string]any{{"role": "user", "content": "u"}}
	AppendJSONInstruction(&msgs)
	want := map[string]any{"role": "system", "content": "Please respond with a valid JSON object."}
	if !reflect.DeepEqual(msgs[0], want) {
		t.Errorf("first message = %v, want %v", msgs[0], want)
	}
	if len(msgs) != 2 {
		t.Errorf("messages = %d, want 2", len(msgs))
	}
}

// Parity: core/tests/test_litellm_provider.py::TestModelSupportsCacheControl::test_supported
func TestModelSupportsCacheControlSupported(t *testing.T) {
	for _, model := range []string{
		"anthropic/claude-opus-4-5",
		"claude-3-5-sonnet-20241022",
		"minimax/minimax-text-01",
		"MiniMax-Text-01",
		"zai-glm-4.6",
		"glm-4.6",
		"openrouter/anthropic/claude-opus-4.5",
		"openrouter/anthropic/claude-sonnet-4.5",
		"openrouter/google/gemini-2.5-pro",
		"openrouter/google/gemini-2.5-flash",
		"openrouter/z-ai/glm-5.1",
		"openrouter/z-ai/glm-4.6",
		"openrouter/minimax/minimax-text-01",
	} {
		if !ModelSupportsCacheControl(model) {
			t.Errorf("%q should support cache_control", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestModelSupportsCacheControl::test_unsupported
func TestModelSupportsCacheControlUnsupported(t *testing.T) {
	for _, model := range []string{
		"gpt-4o-mini",
		"gemini/gemini-1.5-flash",
		"ollama_chat/llama3",
		"openrouter/openai/gpt-4o",
		"openrouter/deepseek/deepseek-chat",
		"openrouter/x-ai/grok-2",
		"openrouter/moonshotai/kimi-k2",
		"openrouter/liquid/lfm-2.5-1.2b-thinking:free",
	} {
		if ModelSupportsCacheControl(model) {
			t.Errorf("%q should not support cache_control", model)
		}
	}
}

// Parity: core/tests/test_litellm_provider.py::TestBuildSystemMessageOpenRouter::test_openrouter_anthropic_splits_into_two_blocks
func TestBuildSystemMessageOpenRouterAnthropicSplits(t *testing.T) {
	got := BuildSystemMessage("static prefix", "dynamic tail", "openrouter/anthropic/claude-opus-4.5")
	want := map[string]any{
		"role": "system",
		"content": []any{
			map[string]any{
				"type":          "text",
				"text":          "static prefix",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
			map[string]any{"type": "text", "text": "dynamic tail"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestBuildSystemMessageOpenRouter::test_openrouter_gemini_splits_into_two_blocks
func TestBuildSystemMessageOpenRouterGeminiSplits(t *testing.T) {
	got := BuildSystemMessage("static prefix", "dynamic tail", "openrouter/google/gemini-2.5-pro")
	blocks, ok := got["content"].([]any)
	if !ok {
		t.Fatalf("content = %T", got["content"])
	}
	first, _ := blocks[0].(map[string]any)
	if !reflect.DeepEqual(first["cache_control"], map[string]any{"type": "ephemeral"}) {
		t.Errorf("first block = %v", first)
	}
	if !reflect.DeepEqual(blocks[1], map[string]any{"type": "text", "text": "dynamic tail"}) {
		t.Errorf("second block = %v", blocks[1])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestBuildSystemMessageOpenRouter::test_openrouter_glm_splits_into_two_blocks
func TestBuildSystemMessageOpenRouterGLMSplits(t *testing.T) {
	got := BuildSystemMessage("static prefix", "dynamic tail", "openrouter/z-ai/glm-5.1")
	blocks, ok := got["content"].([]any)
	if !ok {
		t.Fatalf("content = %T", got["content"])
	}
	first, _ := blocks[0].(map[string]any)
	if !reflect.DeepEqual(first["cache_control"], map[string]any{"type": "ephemeral"}) {
		t.Errorf("first block = %v", first)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestBuildSystemMessageOpenRouter::test_openrouter_openai_stays_concatenated
func TestBuildSystemMessageOpenRouterOpenAIStaysConcatenated(t *testing.T) {
	got := BuildSystemMessage("static prefix", "dynamic tail", "openrouter/openai/gpt-4o")
	want := map[string]any{
		"role":    "system",
		"content": "static prefix\n\ndynamic tail",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestBuildSystemMessageOpenRouter::test_no_suffix_anthropic_gets_top_level_cache_control
func TestBuildSystemMessageNoSuffixAnthropicTopLevelCacheControl(t *testing.T) {
	got := BuildSystemMessage("static prefix", "", "openrouter/anthropic/claude-opus-4.5")
	want := map[string]any{
		"role":          "system",
		"content":       "static prefix",
		"cache_control": map[string]any{"type": "ephemeral"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_none_usage_returns_zero
func TestExtractCacheTokensNoneUsage(t *testing.T) {
	read, creation := ExtractCacheTokens(nil)
	if read != 0 || creation != 0 {
		t.Errorf("got (%d, %d), want (0, 0)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_openai_shape
func TestExtractCacheTokensOpenAIShape(t *testing.T) {
	usage := &Usage{
		PromptTokensDetails:      &PromptTokensDetails{CachedTokens: 120},
		CacheCreationInputTokens: 0,
	}
	read, creation := ExtractCacheTokens(usage)
	if read != 120 || creation != 0 {
		t.Errorf("got (%d, %d), want (120, 0)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_openrouter_cache_write_tokens_shape
func TestExtractCacheTokensOpenRouterCacheWriteShape(t *testing.T) {
	usage := &Usage{
		PromptTokensDetails:      &PromptTokensDetails{CachedTokens: 80, CacheWriteTokens: 50},
		CacheCreationInputTokens: 0,
	}
	read, creation := ExtractCacheTokens(usage)
	if read != 80 || creation != 50 {
		t.Errorf("got (%d, %d), want (80, 50)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_anthropic_native_cache_creation_field_still_works
func TestExtractCacheTokensAnthropicNativeCreationField(t *testing.T) {
	usage := &Usage{
		PromptTokensDetails:      &PromptTokensDetails{CachedTokens: 80},
		CacheCreationInputTokens: 50,
	}
	read, creation := ExtractCacheTokens(usage)
	if read != 80 || creation != 50 {
		t.Errorf("got (%d, %d), want (80, 50)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_raw_anthropic_shape_falls_back
func TestExtractCacheTokensRawAnthropicShape(t *testing.T) {
	usage := &Usage{
		CacheReadInputTokens:     200,
		CacheCreationInputTokens: 75,
	}
	read, creation := ExtractCacheTokens(usage)
	if read != 200 || creation != 75 {
		t.Errorf("got (%d, %d), want (200, 75)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCacheTokens::test_no_cache_fields_returns_zero
func TestExtractCacheTokensNoCacheFields(t *testing.T) {
	read, creation := ExtractCacheTokens(&Usage{})
	if read != 0 || creation != 0 {
		t.Errorf("got (%d, %d), want (0, 0)", read, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestStreamingChunksFallbackPreservesCacheFields::test_chunks_with_cache_fields_recovered
func TestStreamingChunksFallbackRecoversCacheWriteTokens(t *testing.T) {
	chunks := []*Usage{
		nil,
		nil,
		{
			PromptTokensDetails:      &PromptTokensDetails{CachedTokens: 0, CacheWriteTokens: 5601},
			CacheCreationInputTokens: 0,
		},
	}
	var cached, creation int
	for i := len(chunks) - 1; i >= 0; i-- {
		usage := chunks[i]
		if usage == nil {
			continue
		}
		cr, cc := ExtractCacheTokens(usage)
		if cr != 0 || cc != 0 {
			cached, creation = cr, cc
			break
		}
	}
	if cached != 0 || creation != 5601 {
		t.Errorf("got (%d, %d), want (0, 5601)", cached, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestStreamingChunksFallbackPreservesCacheFields::test_chunks_with_cache_read_recovered
func TestStreamingChunksFallbackRecoversCacheReadTokens(t *testing.T) {
	chunks := []*Usage{
		{
			PromptTokensDetails:      &PromptTokensDetails{CachedTokens: 5601, CacheWriteTokens: 0},
			CacheCreationInputTokens: 0,
		},
	}
	var cached, creation int
	for i := len(chunks) - 1; i >= 0; i-- {
		usage := chunks[i]
		if usage == nil {
			continue
		}
		cr, cc := ExtractCacheTokens(usage)
		if cr != 0 || cc != 0 {
			cached, creation = cr, cc
			break
		}
	}
	if cached != 5601 || creation != 0 {
		t.Errorf("got (%d, %d), want (5601, 0)", cached, creation)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_none_response_returns_zero
func TestExtractCostNoneUsage(t *testing.T) {
	if got := ExtractCost(nil, map[string]any{"response_cost": 99.99}, "gpt-4o-mini"); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_openrouter_usage_cost_is_preferred
func TestExtractCostOpenRouterUsageCostPreferred(t *testing.T) {
	usage := &Usage{Cost: 0.00123}
	got := ExtractCost(usage, map[string]any{"response_cost": 99.99}, "openrouter/anthropic/claude-opus-4.5")
	if got != 0.00123 {
		t.Errorf("cost = %v, want 0.00123", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_hidden_params_response_cost_used_when_no_usage_cost
func TestExtractCostHiddenParamsResponseCost(t *testing.T) {
	got := ExtractCost(&Usage{}, map[string]any{"response_cost": 0.0042}, "gpt-4o-mini")
	if got != 0.0042 {
		t.Errorf("cost = %v, want 0.0042", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_falls_back_to_completion_cost_when_nothing_pre_populated
func TestExtractCostFallsBackToCatalogPricing(t *testing.T) {
	usage := &Usage{
		PromptTokens:     1000,
		CompletionTokens: 500,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens:     200,
			CacheWriteTokens: 100,
		},
	}
	want := (700*1.74 + 200*0.145 + 100*1.74 + 500*3.48) / 1_000_000
	got := ExtractCost(usage, nil, "deepseek-v4-pro")
	if !approxEqual(got, want, 1e-12) {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_completion_cost_exception_returns_zero
func TestExtractCostUnpricedModelReturnsZero(t *testing.T) {
	usage := &Usage{PromptTokens: 1000, CompletionTokens: 500}
	if got := ExtractCost(usage, map[string]any{}, "openrouter/mystery/model"); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestExtractCost::test_zero_cost_falls_through_to_next_source
func TestExtractCostZeroCostFallsThrough(t *testing.T) {
	usage := &Usage{Cost: 0.0}
	got := ExtractCost(usage, map[string]any{"response_cost": 0.0055}, "gpt-4o-mini")
	if got != 0.0055 {
		t.Errorf("cost = %v, want 0.0055", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestCostFromTokens::test_zero_tokens_returns_zero_without_calling_litellm
func TestCostFromTokensZeroTokens(t *testing.T) {
	if got := CostFromTokens("claude-opus-4.5", 0, 0, 0, 0); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestCostFromTokens::test_empty_model_returns_zero
func TestCostFromTokensEmptyModel(t *testing.T) {
	if got := CostFromTokens("", 1000, 500, 0, 0); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestCostFromTokens::test_computes_from_tokens
func TestCostFromTokensComputesFromTokens(t *testing.T) {
	cost := CostFromTokens("deepseek-v4-pro", 1000, 500, 200, 100)
	want := (700*1.74 + 200*0.145 + 100*1.74 + 500*3.48) / 1_000_000
	if !approxEqual(cost, want, 1e-12) {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestCostFromTokens::test_exception_returns_zero
func TestCostFromTokensUnpricedModelReturnsZero(t *testing.T) {
	if got := CostFromTokens("mystery/model", 1000, 500, 0, 0); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestCostFromTokens::test_negative_or_none_components_coerce_to_zero
func TestCostFromTokensNegativeComponentsCoerceToZero(t *testing.T) {
	if got := CostFromTokens("deepseek-v4-pro", -10, -5, 0, 0); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLLMResponseAndFinishEventHaveCostUsd::test_llm_response_defaults_cost_to_zero
func TestCompletionDefaultsCostToZero(t *testing.T) {
	r := Completion{Content: "", Model: "m"}
	if r.CostUSD != 0.0 {
		t.Errorf("cost_usd = %v, want 0", r.CostUSD)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLLMResponseAndFinishEventHaveCostUsd::test_finish_event_defaults_cost_to_zero
func TestFinishEventDefaultsCostToZero(t *testing.T) {
	e := FinishEvent{}
	if e.CostUSD != 0.0 {
		t.Errorf("cost_usd = %v, want 0", e.CostUSD)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLLMResponseAndFinishEventHaveCostUsd::test_finish_event_accepts_cost
func TestFinishEventAcceptsCost(t *testing.T) {
	e := FinishEvent{CostUSD: 0.0123}
	if e.CostUSD != 0.0123 {
		t.Errorf("cost_usd = %v, want 0.0123", e.CostUSD)
	}
}
