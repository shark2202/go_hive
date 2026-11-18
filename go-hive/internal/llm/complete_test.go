package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type completionAttempt struct {
	reply CompletionResponse
	err   error
}

type scriptedTransport struct {
	calls   []map[string]any
	replies []completionAttempt
}

func (s *scriptedTransport) call(_ context.Context, kwargs map[string]any) (CompletionResponse, error) {
	index := len(s.calls)
	s.calls = append(s.calls, kwargs)
	if index >= len(s.replies) {
		return CompletionResponse{}, errors.New("unexpected extra completion call")
	}
	return s.replies[index].reply, s.replies[index].err
}

func recordingSleep(durations *[]time.Duration) func(time.Duration) {
	return func(duration time.Duration) {
		*durations = append(*durations, duration)
	}
}

func newTestProvider(transport *scriptedTransport, sleeps *[]time.Duration) *Provider {
	p := &Provider{Model: "gpt-4o-mini", APIKey: "test-key", Call: transport.call}
	if sleeps != nil {
		p.SleepFunc = recordingSleep(sleeps)
	}
	return p
}

func firstTool(t *testing.T, kwargs map[string]any) map[string]any {
	t.Helper()
	tools, ok := kwargs["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools = %#v, want non-empty []any", kwargs["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tools[0] = %#v, want map[string]any", tools[0])
	}
	return tool
}

func systemMessage(t *testing.T, kwargs map[string]any) map[string]any {
	t.Helper()
	messages, ok := kwargs["messages"].([]map[string]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want non-empty []map[string]any", kwargs["messages"])
	}
	return messages[0]
}

// Parity: core/tests/test_litellm_provider.py::test_complete_basic
func TestCompleteBasic(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{
			Content:      "Hello! I'm an AI assistant.",
			Model:        "gpt-4o-mini",
			FinishReason: "stop",
			Usage:        &Usage{PromptTokens: 10, CompletionTokens: 20},
		},
	}}}
	provider := newTestProvider(transport, nil)

	result, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Hello"},
	}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if result.Content != "Hello! I'm an AI assistant." {
		t.Errorf("content = %q", result.Content)
	}
	if result.Model != "gpt-4o-mini" {
		t.Errorf("model = %q", result.Model)
	}
	if result.InputTokens != 10 || result.OutputTokens != 20 {
		t.Errorf("tokens = %d/%d, want 10/20", result.InputTokens, result.OutputTokens)
	}
	if result.StopReason != "stop" {
		t.Errorf("stop reason = %q, want stop", result.StopReason)
	}

	if len(transport.calls) != 1 {
		t.Fatalf("completion calls = %d, want 1", len(transport.calls))
	}
	call := transport.calls[0]
	if call["model"] != "gpt-4o-mini" {
		t.Errorf("kwargs model = %#v", call["model"])
	}
	if call["api_key"] != "test-key" {
		t.Errorf("kwargs api_key = %#v", call["api_key"])
	}
	if call["max_tokens"] != DefaultAuxMaxTokens {
		t.Errorf("max_tokens = %#v, want %d", call["max_tokens"], DefaultAuxMaxTokens)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_complete_with_system_prompt
func TestCompleteWithSystemPrompt(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: "Response", Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Hello"},
	}, CompleteOptions{System: "You are a helpful assistant."}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	message := systemMessage(t, transport.calls[0])
	if message["role"] != "system" {
		t.Errorf("messages[0] role = %#v, want system", message["role"])
	}
	if message["content"] != "You are a helpful assistant." {
		t.Errorf("messages[0] content = %#v", message["content"])
	}
}

// Parity: core/tests/test_litellm_provider.py::test_complete_with_tools
func TestCompleteWithTools(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: "Response", Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	tools := []ToolDef{{
		Name:        "get_weather",
		Description: "Get the weather for a location",
		Parameters: map[string]any{
			"properties": map[string]any{"location": map[string]any{"type": "string", "description": "City name"}},
			"required":   []any{"location"},
		},
	}}

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "What's the weather?"},
	}, CompleteOptions{Tools: tools}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	call := transport.calls[0]
	if _, ok := call["tools"]; !ok {
		t.Fatal("kwargs has no tools key")
	}
	tool := firstTool(t, call)
	if tool["type"] != "function" {
		t.Errorf("tools[0] type = %#v, want function", tool["type"])
	}
	function, _ := tool["function"].(map[string]any)
	if function["name"] != "get_weather" {
		t.Errorf("tools[0] function name = %#v", function["name"])
	}
	if _, ok := call["tool_choice"]; ok {
		t.Errorf("tool_choice = %#v, want unset for a non-Ollama, non-proxy model", call["tool_choice"])
	}
}

// Parity: core/tests/test_litellm_provider.py::test_json_mode_adds_instruction_to_system_prompt
func TestJSONModeAddsInstructionToSystemPrompt(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: `{"key": "value"}`, Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Return JSON"},
	}, CompleteOptions{System: "You are helpful.", JSONMode: true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	call := transport.calls[0]
	if _, ok := call["response_format"]; ok {
		t.Errorf("response_format = %#v, want unset (prompt engineering instead)", call["response_format"])
	}
	message := systemMessage(t, call)
	content, _ := message["content"].(string)
	if message["role"] != "system" {
		t.Errorf("messages[0] role = %#v, want system", message["role"])
	}
	if !strings.Contains(content, "You are helpful.") {
		t.Errorf("system content = %q, want the original prompt", content)
	}
	if !strings.Contains(content, "Please respond with a valid JSON object") {
		t.Errorf("system content = %q, want the JSON instruction", content)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_json_mode_creates_system_prompt_if_none
func TestJSONModeCreatesSystemPromptIfNone(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: `{"key": "value"}`, Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Return JSON"},
	}, CompleteOptions{JSONMode: true}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	message := systemMessage(t, transport.calls[0])
	content, _ := message["content"].(string)
	if message["role"] != "system" {
		t.Errorf("messages[0] role = %#v, want system", message["role"])
	}
	if !strings.Contains(content, "Please respond with a valid JSON object") {
		t.Errorf("system content = %q, want the JSON instruction", content)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_json_mode_false_no_instruction
func TestJSONModeFalseNoInstruction(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: "Hello", Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Hello"},
	}, CompleteOptions{System: "You are helpful.", JSONMode: false}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	call := transport.calls[0]
	if _, ok := call["response_format"]; ok {
		t.Errorf("response_format = %#v, want unset", call["response_format"])
	}
	message := systemMessage(t, call)
	content, _ := message["content"].(string)
	if message["role"] != "system" {
		t.Errorf("messages[0] role = %#v, want system", message["role"])
	}
	if strings.Contains(content, "Please respond with a valid JSON object") {
		t.Errorf("system content = %q, want no JSON instruction", content)
	}
}

// Parity: core/tests/test_litellm_provider.py::test_json_mode_default_is_false
func TestJSONModeDefaultIsFalse(t *testing.T) {
	transport := &scriptedTransport{replies: []completionAttempt{{
		reply: CompletionResponse{Content: "Hello", Model: "gpt-4o-mini", FinishReason: "stop"},
	}}}
	provider := newTestProvider(transport, nil)

	if _, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "Hello"},
	}, CompleteOptions{System: "You are helpful."}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	call := transport.calls[0]
	if _, ok := call["response_format"]; ok {
		t.Errorf("response_format = %#v, want unset", call["response_format"])
	}
	message := systemMessage(t, call)
	if message["content"] != "You are helpful." {
		t.Errorf("messages[0] content = %#v, want the system prompt unchanged", message["content"])
	}
}

// Parity-exempt: 上游 complete 的重试循环依赖 litellm.RateLimitError 异常类型，
func TestCompleteRateLimitRetry(t *testing.T) {
	rateLimited := &ProviderError{
		Message:    "rate limited",
		StatusCode: 429,
		Headers:    map[string]string{"retry-after": "2"},
	}

	t.Run("recovers on the second attempt", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{err: rateLimited},
			{reply: CompletionResponse{Content: "ok", FinishReason: "stop"}},
		}}
		provider := newTestProvider(transport, &sleeps)

		result, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if result.Content != "ok" {
			t.Errorf("content = %q, want ok", result.Content)
		}
		if len(transport.calls) != 2 {
			t.Errorf("calls = %d, want 2", len(transport.calls))
		}
		if len(sleeps) != 1 || sleeps[0] <= 0 || sleeps[0] > time.Duration(RateLimitMaxDelay)*time.Second {
			t.Errorf("sleeps = %v, want one bounded backoff", sleeps)
		}
	})

	t.Run("gives up after the retry budget", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{err: rateLimited}, {err: rateLimited}, {err: rateLimited},
		}}
		provider := newTestProvider(transport, &sleeps)
		retries := 2

		_, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{MaxRetries: &retries})
		if err == nil {
			t.Fatal("Complete succeeded, want the rate-limit error")
		}
		if len(transport.calls) != 3 {
			t.Errorf("calls = %d, want 3", len(transport.calls))
		}
	})

	t.Run("does not retry other errors", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{err: errors.New("bad request")},
		}}
		provider := newTestProvider(transport, &sleeps)

		if _, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{}); err == nil {
			t.Fatal("Complete succeeded, want the transport error")
		}
		if len(transport.calls) != 1 {
			t.Errorf("calls = %d, want 1", len(transport.calls))
		}
		if len(sleeps) != 0 {
			t.Errorf("sleeps = %v, want none", sleeps)
		}
	})
}

// Parity-exempt: 上游空响应重试没有独立测试（只在 complete 的成功路径间接覆盖）；
func TestCompleteEmptyResponseRetry(t *testing.T) {
	t.Run("retries an empty reply", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{reply: CompletionResponse{FinishReason: "stop"}},
			{reply: CompletionResponse{Content: "recovered", FinishReason: "stop"}},
		}}
		provider := newTestProvider(transport, &sleeps)

		result, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if result.Content != "recovered" {
			t.Errorf("content = %q, want recovered", result.Content)
		}
		if len(transport.calls) != 2 {
			t.Errorf("calls = %d, want 2", len(transport.calls))
		}
		if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
			t.Errorf("sleeps = %v, want one 2s backoff", sleeps)
		}
	})

	t.Run("empty reply after an assistant turn is expected", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{reply: CompletionResponse{FinishReason: "stop"}},
		}}
		provider := newTestProvider(transport, &sleeps)

		result, err := provider.Complete(context.Background(), []map[string]any{
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
		}, CompleteOptions{})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if result.Content != "" {
			t.Errorf("content = %q, want empty", result.Content)
		}
		if len(transport.calls) != 1 {
			t.Errorf("calls = %d, want 1", len(transport.calls))
		}
	})

	t.Run("length truncation is not retried", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{reply: CompletionResponse{FinishReason: "length"}},
		}}
		provider := newTestProvider(transport, &sleeps)

		if _, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if len(transport.calls) != 1 {
			t.Errorf("calls = %d, want 1", len(transport.calls))
		}
	})

	t.Run("gives up after the empty-response cap", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{
			{reply: CompletionResponse{FinishReason: "stop"}},
			{reply: CompletionResponse{FinishReason: "stop"}},
			{reply: CompletionResponse{FinishReason: "stop"}},
			{reply: CompletionResponse{FinishReason: "stop"}},
		}}
		provider := newTestProvider(transport, &sleeps)

		if _, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if len(transport.calls) != 4 {
			t.Errorf("calls = %d, want 4", len(transport.calls))
		}
		if len(sleeps) != 3 {
			t.Errorf("sleeps = %d, want 3", len(sleeps))
		}
	})
}

// Parity-exempt: tool_choice 默认值、extra_kwargs 优先级与 response_format 透传
func TestCompleteRequestKwargs(t *testing.T) {
	reply := CompletionResponse{Content: "ok", FinishReason: "stop"}
	messages := []map[string]any{{"role": "user", "content": "hi"}}
	tool := ToolDef{Name: "t", Description: "d"}

	t.Run("ollama asks for automatic tool choice", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := &Provider{Model: "ollama_chat/llama3", Call: transport.call}

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{Tools: []ToolDef{tool}}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got := transport.calls[0]["tool_choice"]; got != "auto" {
			t.Errorf("tool_choice = %#v, want auto", got)
		}
	})

	t.Run("hive proxy forces a tool call", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := &Provider{Model: "hive/glm-4.6", HiveProxyAuth: true, Call: transport.call}

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{Tools: []ToolDef{tool}}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got := transport.calls[0]["tool_choice"]; got != "required" {
			t.Errorf("tool_choice = %#v, want required", got)
		}
	})

	t.Run("extra kwargs win over tool_choice defaults", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := &Provider{
			Model:         "hive/glm-4.6",
			HiveProxyAuth: true,
			Call:          transport.call,
			ExtraKwargs:   map[string]any{"tool_choice": "none", "temperature": 0.2},
		}

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{Tools: []ToolDef{tool}}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		call := transport.calls[0]
		if got := call["tool_choice"]; got != "none" {
			t.Errorf("tool_choice = %#v, want none", got)
		}
		if got := call["temperature"]; got != 0.2 {
			t.Errorf("temperature = %#v, want 0.2", got)
		}
	})

	t.Run("response_format is passed through", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := newTestProvider(transport, nil)
		format := map[string]any{"type": "json_object"}

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{ResponseFormat: format}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got := transport.calls[0]["response_format"]; got == nil {
			t.Error("response_format missing from kwargs")
		}
	})

	t.Run("explicit max_tokens is preserved", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := newTestProvider(transport, nil)

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{MaxTokens: 20000}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got := transport.calls[0]["max_tokens"]; got != 20000 {
			t.Errorf("max_tokens = %#v, want 20000", got)
		}
	})

	t.Run("api_base is only sent when configured", func(t *testing.T) {
		transport := &scriptedTransport{replies: []completionAttempt{{reply: reply}}}
		provider := newTestProvider(transport, nil)

		if _, err := provider.Complete(context.Background(), messages, CompleteOptions{}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if _, ok := transport.calls[0]["api_base"]; ok {
			t.Errorf("api_base = %#v, want unset", transport.calls[0]["api_base"])
		}
	})
}
