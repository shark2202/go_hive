package llm

import (
	"context"
	"strings"
	"testing"
)

func anthropicReply(model string) CompletionResponse {
	return CompletionResponse{
		Content:      "Hello from Claude!",
		Model:        model,
		FinishReason: "stop",
		Usage:        &Usage{PromptTokens: 10, CompletionTokens: 5},
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_is_llm_provider
func TestAnthropicProviderIsLLMProvider(t *testing.T) {
	provider, err := NewAnthropicProvider("test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}

	var completer Completer = provider
	if completer == nil {
		t.Fatal("AnthropicProvider must satisfy Completer, the LLMProvider analogue")
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_init_defaults
func TestAnthropicProviderInitDefaults(t *testing.T) {
	provider, err := NewAnthropicProvider("test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if provider.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want claude-haiku-4-5-20251001", provider.Model)
	}
	if provider.APIKey != "test-key" {
		t.Errorf("api key = %q, want test-key", provider.APIKey)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_init_custom_model
func TestAnthropicProviderInitCustomModel(t *testing.T) {
	provider, err := NewAnthropicProvider("test-key", "claude-3-haiku-20240307")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if provider.Model != "claude-3-haiku-20240307" {
		t.Errorf("model = %q, want claude-3-haiku-20240307", provider.Model)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_uses_litellm_internally
func TestAnthropicProviderUsesProviderInternally(t *testing.T) {
	provider, err := NewAnthropicProvider("test-key", "claude-3-haiku-20240307")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	if provider.Provider == nil {
		t.Fatal("the delegated provider is nil")
	}
	if provider.Provider.Model != "claude-3-haiku-20240307" {
		t.Errorf("delegated model = %q", provider.Provider.Model)
	}
	if provider.Provider.APIKey != "test-key" {
		t.Errorf("delegated api key = %q", provider.Provider.APIKey)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_complete
func TestAnthropicProviderComplete(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{reply: anthropicReply("claude-3-haiku-20240307")}}}
	provider, err := NewAnthropicProvider("test-key", "claude-3-haiku-20240307")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	provider.Provider.Call = transport.call

	result, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Hello"},
	}, CompleteOptions{System: "You are helpful.", MaxTokens: 100})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content != "Hello from Claude!" {
		t.Errorf("content = %q", result.Content)
	}
	if result.Model != "claude-3-haiku-20240307" {
		t.Errorf("model = %q", result.Model)
	}
	if result.InputTokens != 10 || result.OutputTokens != 5 {
		t.Errorf("tokens = %d/%d, want 10/5", result.InputTokens, result.OutputTokens)
	}

	if len(transport.calls) != 1 {
		t.Fatalf("completion calls = %d, want 1", len(transport.calls))
	}
	call := transport.calls[0]
	if call["model"] != "claude-3-haiku-20240307" {
		t.Errorf("kwargs model = %#v", call["model"])
	}
	if call["api_key"] != "test-key" {
		t.Errorf("kwargs api_key = %#v", call["api_key"])
	}
	if call["max_tokens"] != 100 {
		t.Errorf("kwargs max_tokens = %#v, want 100", call["max_tokens"])
	}
	system := systemMessage(t, call)
	if system["content"] != "You are helpful." {
		t.Errorf("system content = %#v", system["content"])
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_passes_response_format
func TestAnthropicProviderPassesResponseFormat(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{reply: anthropicReply("claude-3-haiku-20240307")}}}
	provider, err := NewAnthropicProvider("test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	provider.Provider.Call = transport.call

	format := map[string]any{"type": "json_object"}
	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "hi"},
	}, CompleteOptions{ResponseFormat: format}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got := transport.calls[0]["response_format"]; got == nil {
		t.Fatal("response_format missing from kwargs")
	}
}

// Parity: core/tests/test_litellm_provider.py::test_anthropic_provider_passes_json_mode
func TestAnthropicProviderPassesJSONMode(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{reply: anthropicReply("claude-haiku-4-5-20251001")}}}
	provider, err := NewAnthropicProvider("test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	provider.Provider.Call = transport.call

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Return JSON"},
	}, CompleteOptions{System: "You are helpful.", JSONMode: true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	call := transport.calls[0]
	if _, ok := call["response_format"]; ok {
		t.Errorf("json_mode must not send response_format, got %#v", call["response_format"])
	}
	system := systemMessage(t, call)
	if system["role"] != "system" {
		t.Errorf("messages[0] role = %#v, want system", system["role"])
	}
	content, _ := system["content"].(string)
	if !strings.Contains(content, "Please respond with a valid JSON object") {
		t.Errorf("system content = %q, want the JSON instruction", content)
	}
}

// Parity-exempt: 上游没有缺失密钥的测试（__init__ 直接 raise）；Go 构造函数无法
func TestAnthropicProviderResolvesAPIKey(t *testing.T) {
	defer SetAnthropicCredentialLookup(nil)

	t.Run("explicit key wins over store and environment", func(t *testing.T) {
		SetAnthropicCredentialLookup(func() (string, bool) { return "store-key", true })
		t.Setenv("ANTHROPIC_API_KEY", "env-key")
		defer SetAnthropicCredentialLookup(nil)

		provider, err := NewAnthropicProvider("explicit-key", "")
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}
		if provider.APIKey != "explicit-key" {
			t.Errorf("api key = %q, want explicit-key", provider.APIKey)
		}
	})

	t.Run("credential store wins over environment", func(t *testing.T) {
		SetAnthropicCredentialLookup(func() (string, bool) { return "store-key", true })
		t.Setenv("ANTHROPIC_API_KEY", "env-key")
		defer SetAnthropicCredentialLookup(nil)

		provider, err := NewAnthropicProvider("", "")
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}
		if provider.APIKey != "store-key" {
			t.Errorf("api key = %q, want store-key", provider.APIKey)
		}
	})

	t.Run("environment is the last resort", func(t *testing.T) {
		SetAnthropicCredentialLookup(nil)
		t.Setenv("ANTHROPIC_API_KEY", "env-key")

		provider, err := NewAnthropicProvider("", "")
		if err != nil {
			t.Fatalf("NewAnthropicProvider: %v", err)
		}
		if provider.APIKey != "env-key" {
			t.Errorf("api key = %q, want env-key", provider.APIKey)
		}
	})

	t.Run("missing key is an error", func(t *testing.T) {
		SetAnthropicCredentialLookup(nil)
		t.Setenv("ANTHROPIC_API_KEY", "")

		_, err := NewAnthropicProvider("", "")
		if err == nil {
			t.Fatal("expected an error when no key is available")
		}
		if err.Error() != "Anthropic API key required. Set ANTHROPIC_API_KEY env var or pass api_key." {
			t.Errorf("error = %q", err.Error())
		}
	})
}

// Parity-exempt: Go 的 Completer 用结构化 ChatMessage，Python 各调用点直接传
func TestAnthropicProviderACompleteBridgesRequest(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{reply: anthropicReply("claude-haiku-4-5-20251001")}}}
	provider, err := NewAnthropicProvider("test-key", "")
	if err != nil {
		t.Fatalf("NewAnthropicProvider: %v", err)
	}
	provider.Provider.Call = transport.call

	result, err := provider.AComplete(context.Background(), CompletionRequest{
		System:              "Static prompt",
		SystemDynamicSuffix: "Current time: now",
		MaxTokens:           256,
		Tools:               []ToolDef{{Name: "search", Description: "search the web"}},
		Messages: []ChatMessage{
			{Role: "user", Content: "find it"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "tc_1", Name: "search", Arguments: `{"q":"x"}`}}},
			{Role: "tool", Content: "result", ToolCallID: "tc_1"},
		},
	})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}
	if result.Content != "Hello from Claude!" {
		t.Errorf("content = %q", result.Content)
	}

	call := transport.calls[0]
	if call["max_tokens"] != 256 {
		t.Errorf("max_tokens = %#v, want 256", call["max_tokens"])
	}
	messages, ok := call["messages"].([]map[string]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %#v, want 4 entries", call["messages"])
	}
	systemBlocks, ok := messages[0]["content"].([]any)
	if !ok || len(systemBlocks) != 2 {
		t.Fatalf("system content = %#v, want the two-block split", messages[0]["content"])
	}
	staticBlock, _ := systemBlocks[0].(map[string]any)
	if staticBlock["text"] != "Static prompt" {
		t.Errorf("static block = %#v", staticBlock)
	}
	if cacheControl, _ := staticBlock["cache_control"].(map[string]any); cacheControl["type"] != "ephemeral" {
		t.Errorf("static block cache_control = %#v", staticBlock["cache_control"])
	}
	dynamicBlock, _ := systemBlocks[1].(map[string]any)
	if dynamicBlock["text"] != "Current time: now" {
		t.Errorf("dynamic block = %#v", dynamicBlock)
	}
	if _, present := dynamicBlock["cache_control"]; present {
		t.Errorf("dynamic block must stay uncached: %#v", dynamicBlock)
	}
	if messages[2]["content"] != nil {
		t.Errorf("assistant content = %#v, want nil", messages[2]["content"])
	}
	calls, ok := messages[2]["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", messages[2]["tool_calls"])
	}
	if calls[0]["id"] != "tc_1" || calls[0]["type"] != "function" {
		t.Errorf("tool call = %#v", calls[0])
	}
	fn, _ := calls[0]["function"].(map[string]any)
	if fn["name"] != "search" || fn["arguments"] != `{"q":"x"}` {
		t.Errorf("tool function = %#v", fn)
	}
	if messages[3]["role"] != "tool" || messages[3]["tool_call_id"] != "tc_1" {
		t.Errorf("tool result = %#v", messages[3])
	}
	if tools, ok := call["tools"].([]any); !ok || len(tools) != 1 {
		t.Errorf("tools = %#v, want one entry", call["tools"])
	}
}
