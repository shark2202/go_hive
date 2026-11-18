package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var _ Completer = (*Provider)(nil)

type transportCapture struct {
	mu     sync.Mutex
	path   string
	header http.Header
	body   map[string]any
}

func (c *transportCapture) record(request *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = request.URL.Path
	c.header = request.Header.Clone()
	decoded := map[string]any{}
	if err := json.NewDecoder(request.Body).Decode(&decoded); err == nil {
		c.body = decoded
	}
}

func (c *transportCapture) snapshot() (string, http.Header, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.header, c.body
}

func newCompletionServer(t *testing.T, status int, reply string) (*httptest.Server, *transportCapture) {
	t.Helper()
	capture := &transportCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(request)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		if _, err := writer.Write([]byte(reply)); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, capture
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete
func TestHTTPProviderOpenAITextCompletion(t *testing.T) {
	server, capture := newCompletionServer(t, http.StatusOK, `{
		"id": "chatcmpl-1",
		"model": "gpt-4o-mini-2024-07-18",
		"choices": [{
			"finish_reason": "stop",
			"message": {"role": "assistant", "content": "Hello from OpenAI"}
		}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}
	}`)

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "sk-test-key",
		APIBase: server.URL + "/v1",
	})
	if provider.Call == nil {
		t.Fatal("NewHTTPProvider must set Provider.Call")
	}
	if NewProvider(ProviderOptions{Model: "gpt-4o-mini"}).Call != nil {
		t.Fatal("NewProvider must keep a nil transport")
	}

	completion, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "hi"},
	}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.Content != "Hello from OpenAI" {
		t.Errorf("content = %q", completion.Content)
	}
	if completion.StopReason != "stop" {
		t.Errorf("stop reason = %q", completion.StopReason)
	}
	if completion.Model != "gpt-4o-mini-2024-07-18" {
		t.Errorf("model = %q", completion.Model)
	}
	if completion.InputTokens != 12 || completion.OutputTokens != 3 {
		t.Errorf("usage = %d/%d", completion.InputTokens, completion.OutputTokens)
	}

	path, headers, body := capture.snapshot()
	if path != "/v1/chat/completions" {
		t.Errorf("path = %q", path)
	}
	if got := headers.Get("Authorization"); got != "Bearer sk-test-key" {
		t.Errorf("authorization = %q", got)
	}
	if body["model"] != "gpt-4o-mini" {
		t.Errorf("request model = %v", body["model"])
	}
	if _, ok := body["api_key"]; ok {
		t.Error("api_key must not reach the request body")
	}
	if _, ok := body["api_base"]; ok {
		t.Error("api_base must not reach the request body")
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	if messages[0].(map[string]any)["content"] != "hi" {
		t.Errorf("messages[0] = %#v", messages[0])
	}
	if transportInt(body["max_tokens"]) <= 0 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete
func TestHTTPProviderOpenAIToolCall(t *testing.T) {
	server, _ := newCompletionServer(t, http.StatusOK, `{
		"model": "gpt-4o-mini",
		"choices": [{
			"finish_reason": "tool_calls",
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			}
		}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 7}
	}`)

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "sk-test-key",
		APIBase: server.URL + "/v1",
	})
	completion, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "read a.txt"},
	}, CompleteOptions{Tools: []ToolDef{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []any{"path"},
		},
	}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", completion.StopReason)
	}
	if completion.Content != "" {
		t.Errorf("content = %q", completion.Content)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read_file" || call.Arguments != `{"path":"a.txt"}` {
		t.Errorf("tool call = %#v", call)
	}
	if completion.InputTokens != 20 || completion.OutputTokens != 7 {
		t.Errorf("usage = %d/%d", completion.InputTokens, completion.OutputTokens)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete (rate limit mapping)
func TestHTTPProviderMapsRateLimitErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "7")
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"rate limited by upstream"}}`))
	}))
	t.Cleanup(server.Close)

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "sk-test-key",
		APIBase: server.URL + "/v1",
	})

	_, err := provider.Call(context.Background(), map[string]any{
		"model":    "gpt-4o-mini",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	if err == nil {
		t.Fatal("expected a ProviderError")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want *ProviderError", err)
	}
	if providerErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d", providerErr.StatusCode)
	}
	if !IsRateLimitError(err) {
		t.Error("IsRateLimitError must recognise the mapped 429")
	}
	if !strings.Contains(providerErr.Message, "rate limited by upstream") {
		t.Errorf("message = %q", providerErr.Message)
	}
	delay, ok := ParseRetryAfter(providerErr.RetryHeaders(), 120)
	if !ok || delay != 7 {
		t.Errorf("retry after = %v/%v", delay, ok)
	}
}

// Parity: core/framework/llm/anthropic.py::AnthropicProvider.complete,
// core/framework/llm/litellm.py::rewrite_proxy_model
func TestHTTPProviderAnthropicTextCompletion(t *testing.T) {
	server, capture := newCompletionServer(t, http.StatusOK, `{
		"id": "msg_1",
		"model": "claude-sonnet-4-5-20250929",
		"content": [{"type": "text", "text": "Hi from Claude"}],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 4,
			"cache_read_input_tokens": 2,
			"cache_creation_input_tokens": 3
		}
	}`)

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "anthropic/claude-sonnet-4-5",
		APIKey:  "sk-ant-test",
		APIBase: server.URL,
	})
	completion, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "hi"},
	}, CompleteOptions{System: "You are Hive", MaxTokens: 64})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.Content != "Hi from Claude" {
		t.Errorf("content = %q", completion.Content)
	}
	if completion.StopReason != "stop" {
		t.Errorf("stop reason = %q", completion.StopReason)
	}
	if completion.InputTokens != 15 {
		t.Errorf("input tokens = %d, want 15 (input + cache read + cache creation)", completion.InputTokens)
	}
	if completion.OutputTokens != 4 {
		t.Errorf("output tokens = %d", completion.OutputTokens)
	}
	if completion.CachedTokens != 2 || completion.CacheCreationTokens != 3 {
		t.Errorf("cache tokens = %d/%d", completion.CachedTokens, completion.CacheCreationTokens)
	}

	path, headers, body := capture.snapshot()
	if path != "/v1/messages" {
		t.Errorf("path = %q", path)
	}
	if got := headers.Get("x-api-key"); got != "sk-ant-test" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := headers.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("authorization = %q, want empty for the public Anthropic endpoint", got)
	}
	if body["model"] != "claude-sonnet-4-5" {
		t.Errorf("request model = %v", body["model"])
	}
	if body["system"] != "You are Hive" {
		t.Errorf("system = %#v", body["system"])
	}
	if transportInt(body["max_tokens"]) != 64 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	entry := messages[0].(map[string]any)
	if entry["role"] != "user" {
		t.Errorf("message role = %v", entry["role"])
	}
	blocks, ok := entry["content"].([]any)
	if !ok || len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "hi" {
		t.Errorf("message content = %#v", entry["content"])
	}
}

// Parity: core/framework/llm/anthropic.py::AnthropicProvider.complete
func TestHTTPProviderAnthropicToolUse(t *testing.T) {
	server, capture := newCompletionServer(t, http.StatusOK, `{
		"model": "claude-sonnet-4-5",
		"content": [{
			"type": "tool_use",
			"id": "toolu_2",
			"name": "write_file",
			"input": {"path": "b.txt", "content": "x"}
		}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 30, "output_tokens": 12}
	}`)

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "claude-sonnet-4-5",
		APIKey:  "sk-ant-test",
		APIBase: server.URL + "/v1",
	})
	completion, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "read a.txt then write b.txt"},
		{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id":   "toolu_1",
			"type": "function",
			"function": map[string]any{
				"name":      "read_file",
				"arguments": `{"path":"a.txt"}`,
			},
		}}},
		{"role": "tool", "tool_call_id": "toolu_1", "content": "file body"},
	}, CompleteOptions{Tools: []ToolDef{{
		Name:        "read_file",
		Description: "Read a file",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
		},
	}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.StopReason != "tool_calls" {
		t.Errorf("stop reason = %q", completion.StopReason)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "toolu_2" || call.Name != "write_file" {
		t.Errorf("tool call = %#v", call)
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
		t.Fatalf("arguments %q are not JSON: %v", call.Arguments, err)
	}
	if arguments["path"] != "b.txt" {
		t.Errorf("arguments = %#v", arguments)
	}

	_, headers, body := capture.snapshot()
	if headers.Get("x-api-key") != "sk-ant-test" {
		t.Errorf("x-api-key = %q", headers.Get("x-api-key"))
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	assistant := messages[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Errorf("assistant role = %v", assistant["role"])
	}
	assistantBlocks, _ := assistant["content"].([]any)
	if len(assistantBlocks) != 1 {
		t.Fatalf("assistant blocks = %#v", assistant["content"])
	}
	toolUse, _ := assistantBlocks[0].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_1" || toolUse["name"] != "read_file" {
		t.Errorf("tool_use block = %#v", toolUse)
	}
	input, _ := toolUse["input"].(map[string]any)
	if input["path"] != "a.txt" {
		t.Errorf("tool_use input = %#v", toolUse["input"])
	}

	toolResultMessage := messages[2].(map[string]any)
	if toolResultMessage["role"] != "user" {
		t.Errorf("tool result role = %v", toolResultMessage["role"])
	}
	toolBlocks, _ := toolResultMessage["content"].([]any)
	if len(toolBlocks) != 1 {
		t.Fatalf("tool result blocks = %#v", toolResultMessage["content"])
	}
	result, _ := toolBlocks[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" || result["content"] != "file body" {
		t.Errorf("tool_result block = %#v", result)
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "read_file" {
		t.Errorf("tool name = %v", tool["name"])
	}
	schema, ok := tool["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("input_schema = %#v", tool["input_schema"])
	}
	if _, ok := tool["parameters"]; ok {
		t.Error("tool must use input_schema, not parameters")
	}
}

// Parity: core/framework/llm/anthropic.py::AnthropicProvider.acomplete
func TestHTTPProviderACompleteSatisfiesCompleter(t *testing.T) {
	server, capture := newCompletionServer(t, http.StatusOK, `{
		"model": "gpt-4o-mini",
		"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "ok"}}],
		"usage": {"prompt_tokens": 5, "completion_tokens": 1}
	}`)

	var completer Completer = NewHTTPProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "sk-test-key",
		APIBase: server.URL + "/v1",
	})
	completion, err := completer.AComplete(context.Background(), CompletionRequest{
		System:    "system prompt",
		Messages:  []ChatMessage{{Role: "user", Content: "hello"}},
		MaxTokens: 32,
	})
	if err != nil {
		t.Fatalf("AComplete: %v", err)
	}
	if completion.Content != "ok" {
		t.Errorf("content = %q", completion.Content)
	}
	_, _, body := capture.snapshot()
	messages, _ := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", body["messages"])
	}
	if messages[0].(map[string]any)["role"] != "system" {
		t.Errorf("messages[0] = %#v", messages[0])
	}
	if messages[1].(map[string]any)["content"] != "hello" {
		t.Errorf("messages[1] = %#v", messages[1])
	}
	if transportInt(body["max_tokens"]) != 32 {
		t.Errorf("max_tokens = %v", body["max_tokens"])
	}
}

// Parity-exempt: internal construction guard; Python has no equivalent symbol.
func TestNewHTTPProviderDefaultsToNilCallForNewProvider(t *testing.T) {
	if NewProvider(ProviderOptions{Model: "gpt-4o-mini"}).Call != nil {
		t.Fatal("NewProvider must not install a transport")
	}
	provider := NewHTTPProvider(ProviderOptions{Model: "claude-sonnet-4-5", APIKey: "sk-ant-test", APIBase: "https://api.anthropic.com"})
	if provider.Call == nil {
		t.Fatal("NewHTTPProvider must install a transport")
	}
	if transportBase := resolveCompletionAPIBase("claude-sonnet-4-5", ""); transportBase != AnthropicAPIBase {
		t.Errorf("default anthropic base = %q", transportBase)
	}
	if endpoint := anthropicMessagesEndpoint("https://api.anthropic.com/v1"); endpoint != "https://api.anthropic.com/v1/messages" {
		t.Errorf("anthropic endpoint = %q", endpoint)
	}
	if endpoint := openAIChatEndpoint("https://api.openai.com/v1"); endpoint != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("openai endpoint = %q", endpoint)
	}
}
