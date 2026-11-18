package llm

import (
	"errors"
	"strings"
	"testing"
)

// Parity: core/tests/test_litellm_provider.py::test_an_openai_style_model_is_asked_to_batch_tool_calls, core/tests/test_litellm_provider.py::test_a_request_without_tools_does_not_mention_it, core/tests/test_litellm_provider.py::test_an_anthropic_model_already_batches, core/tests/test_litellm_provider.py::test_a_gateway_that_refuses_it_is_asked_once_and_never_again
func TestParallelToolCallsDecision(t *testing.T) {
	openAI := NewProvider(ProviderOptions{Model: "openai/gpt-x", APIKey: "test-key", APIBase: "http://gateway.test/v1"})
	if !openAI.RequestParallelToolCalls(true) {
		t.Fatal("openai-style model with tools should ask for parallel_tool_calls")
	}
	if openAI.RequestParallelToolCalls(false) {
		t.Fatal("a request without tools must not mention parallel_tool_calls")
	}

	anthropic := NewProvider(ProviderOptions{Model: "anthropic/claude-x", APIKey: "test-key"})
	if anthropic.RequestParallelToolCalls(true) {
		t.Fatal("anthropic batches by default; the flag must not be sent")
	}

	refusal := errors.New("400: unknown parameter parallel_tool_calls")
	if !openAI.RequestParallelToolCalls(true) {
		t.Fatal("first request should still ask")
	}
	if !openAI.NoteError(refusal) {
		t.Fatal("a refusal of parallel_tool_calls should trigger one retry")
	}
	if openAI.RequestParallelToolCalls(true) {
		t.Fatal("after a refusal the flag must be dropped")
	}
	if openAI.NoteError(refusal) {
		t.Fatal("a second refusal must not trigger another retry")
	}

	other := NewProvider(ProviderOptions{Model: "openai/gpt-x", APIKey: "test-key"})
	if other.NoteError(errors.New("the model call failed")) {
		t.Fatal("unrelated errors must not latch the refusal")
	}
	if !other.RequestParallelToolCalls(true) {
		t.Fatal("unrelated errors must not stop the flag being sent")
	}
}

// Parity: core/tests/test_litellm_provider.py::test_tool_compat_messages_split_for_cache_capable_model, core/tests/test_litellm_provider.py::test_tool_compat_messages_stay_concatenated_for_liquid
func TestToolCompatMessageShape(t *testing.T) {
	tools := []ToolDef{{
		Name:        "web_search",
		Description: "Search the web",
		Parameters: map[string]any{
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []any{"query"},
		},
	}}
	messages := []map[string]any{{"role": "user", "content": "hi"}}

	cacheCapable := BuildOpenRouterToolCompatMessages(messages, "You are a queen.", tools,
		"Current time: 2026-04-23T00:00:00Z", "openrouter/anthropic/claude-opus-4.5")
	if len(cacheCapable) != 2 {
		t.Fatalf("expected system + user, got %d messages", len(cacheCapable))
	}
	systemMessage := cacheCapable[0]
	if systemMessage["role"] != "system" {
		t.Fatalf("first message role = %v", systemMessage["role"])
	}
	blocks, ok := systemMessage["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected a two-block system content, got %#v", systemMessage["content"])
	}
	static := blocks[0].(map[string]any)
	if cacheControl, _ := static["cache_control"].(map[string]any); cacheControl["type"] != "ephemeral" {
		t.Fatalf("static block should be cache-marked, got %#v", static)
	}
	staticText, _ := static["text"].(string)
	for _, want := range []string{"You are a queen.", "Tool compatibility mode is active", "web_search"} {
		if !strings.Contains(staticText, want) {
			t.Fatalf("static block missing %q: %s", want, staticText)
		}
	}
	if strings.Contains(staticText, "2026-04-23") {
		t.Fatal("dynamic suffix leaked into the cache-marked static block")
	}
	dynamic := blocks[1].(map[string]any)
	if _, present := dynamic["cache_control"]; present {
		t.Fatal("dynamic block must not carry cache_control")
	}
	if dynamic["text"] != "Current time: 2026-04-23T00:00:00Z" {
		t.Fatalf("dynamic block text = %v", dynamic["text"])
	}

	liquid := BuildOpenRouterToolCompatMessages(messages, "You are a queen.", tools,
		"Current time: 2026-04-23T00:00:00Z", "openrouter/liquid/lfm-2.5-1.2b-thinking:free")
	liquidSystem := liquid[0]
	content, ok := liquidSystem["content"].(string)
	if !ok {
		t.Fatalf("expected a string content for liquid, got %#v", liquidSystem["content"])
	}
	if !strings.Contains(content, "2026-04-23") {
		t.Fatal("concatenated path should include the dynamic suffix")
	}
	if _, present := liquidSystem["cache_control"]; present {
		t.Fatal("liquid must not carry cache_control")
	}
}

// Parity-exempt: 上游 _build_openrouter_tool_compat_messages 尾部的空 assistant 过滤没有独立测试，Go 侧补契约并确认不污染调用方消息。
func TestToolCompatDropsEmptyAssistantMessages(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": "hi"},
		{"role": "assistant", "content": ""},
		{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "1"}}},
		{"role": "assistant", "content": "answer"},
	}
	built := BuildOpenRouterToolCompatMessages(messages, "", nil, "", "openrouter/openai/gpt-4o")
	if len(built) != 4 {
		t.Fatalf("expected the empty assistant message to be dropped, got %d", len(built))
	}
	if built[0]["role"] != "system" {
		t.Fatal("an empty user system still needs the compat instruction")
	}

	if _, mutated := messages[3]["cache_control"]; mutated {
		t.Fatal("input messages should not be mutated")
	}
}

// Parity: core/tests/test_litellm_provider.py::test_ollama_provider_returns_num_ctx, core/tests/test_litellm_provider.py::test_ollama_provider_respects_custom_num_ctx, core/tests/test_litellm_provider.py::test_non_ollama_provider_returns_empty, core/tests/test_litellm_provider.py::test_empty_config_returns_empty
func TestExtraLLMKWargs(t *testing.T) {
	ollama := map[string]any{"provider": "ollama", "model": "ollama/llama3"}
	kwargs := ExtraLLMKWargs(ollama, "", "")
	if len(kwargs) != 1 || kwargs["num_ctx"] != 16384 {
		t.Fatalf("ollama default num_ctx = %#v", kwargs)
	}

	custom := map[string]any{"provider": "ollama", "model": "ollama/llama3", "num_ctx": float64(32768)}
	if kwargs := ExtraLLMKWargs(custom, "", ""); kwargs["num_ctx"] != 32768 {
		t.Fatalf("custom num_ctx = %#v", kwargs)
	}

	anthropic := map[string]any{"provider": "anthropic", "model": "claude-3-haiku"}
	if kwargs := ExtraLLMKWargs(anthropic, "", ""); len(kwargs) != 0 {
		t.Fatalf("non-ollama provider should return no kwargs, got %#v", kwargs)
	}

	if kwargs := ExtraLLMKWargs(map[string]any{}, "", ""); len(kwargs) != 0 {
		t.Fatalf("empty config should return no kwargs, got %#v", kwargs)
	}
}

// Parity-exempt: 上游 get_llm_extra_kwargs 的订阅分支（Claude Code / Codex）没有测试覆盖，Go 侧按实现补齐契约。
func TestExtraLLMKWargsSubscriptionsAndExtraBody(t *testing.T) {
	claude := map[string]any{"use_claude_code_subscription": true, "provider": "anthropic"}
	kwargs := ExtraLLMKWargs(claude, "sk-ant-oauth", "")
	headers, _ := kwargs["extra_headers"].(map[string]any)
	if headers["authorization"] != "Bearer sk-ant-oauth" {
		t.Fatalf("claude subscription headers = %#v", kwargs)
	}
	if out := ExtraLLMKWargs(claude, "", ""); len(out) != 0 {
		t.Fatalf("missing key should fall through, got %#v", out)
	}

	codex := map[string]any{"use_codex_subscription": true}
	kwargs = ExtraLLMKWargs(codex, "tok", "acct-1")
	headers, _ = kwargs["extra_headers"].(map[string]any)
	if headers["Authorization"] != "Bearer tok" || headers["User-Agent"] != "CodexBar" || headers["ChatGPT-Account-Id"] != "acct-1" {
		t.Fatalf("codex headers = %#v", headers)
	}
	if kwargs["store"] != false {
		t.Fatalf("codex must send store=false, got %#v", kwargs["store"])
	}
	if allowed, _ := kwargs["allowed_openai_params"].([]any); len(allowed) != 1 || allowed[0] != "store" {
		t.Fatalf("codex allowed_openai_params = %#v", kwargs["allowed_openai_params"])
	}

	extraBody := map[string]any{"provider": "openai", "extra_body": map[string]any{"chat_template_kwargs": map[string]any{"thinking": false}}}
	kwargs = ExtraLLMKWargs(extraBody, "", "")
	body, _ := kwargs["extra_body"].(map[string]any)
	if len(body) != 1 {
		t.Fatalf("extra_body passthrough = %#v", kwargs)
	}
	if kwargs := ExtraLLMKWargs(map[string]any{"extra_body": map[string]any{}}, "", ""); len(kwargs) != 0 {
		t.Fatalf("empty extra_body should be omitted, got %#v", kwargs)
	}
}
