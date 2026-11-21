package llm

// Reasoning and parallel-tool-call parity for OpenAI-compatible streams.
//
// The real-LLM crosscheck (docs/llm-real-crosscheck.md) found that the Go
// transport dropped `delta.reasoning_content` entirely: reasoning models
// (DeepSeek, Qwen, GLM) streamed their thinking, the reference surfaced it as
// ReasoningDeltaEvent events and echoed it back on the turn's later steps, and
// the port emitted nothing. These tests pin the fixed behavior at the
// transport seam.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
func TestStreamReasoningDeltasSurfaceAndAccumulate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"id":"c1","model":"deepseek-flash-1017","choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"Think"},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"","reasoning_content":" twice."},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12}}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "deepseek-flash-1017", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	var types []string
	var reasoning strings.Builder
	for _, event := range events {
		types = append(types, event.StreamEventType())
		if delta, ok := event.(ReasoningDeltaEvent); ok {
			reasoning.WriteString(delta.Content)
		}
	}
	want := []string{"reasoning_delta", "reasoning_delta", "text_delta", "text_end", "finish"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	if got := reasoning.String(); got != "Think twice." {
		t.Fatalf("reasoning delta content = %q, want %q", got, "Think twice.")
	}
	if completion.ReasoningContent != "Think twice." {
		t.Fatalf("completion.ReasoningContent = %q, want %q", completion.ReasoningContent, "Think twice.")
	}
	var finish *FinishEvent
	for _, event := range events {
		if typed, ok := event.(FinishEvent); ok {
			copy := typed
			finish = &copy
		}
	}
	if finish == nil {
		t.Fatal("no finish event")
	}
	if finish.ReasoningContent != "Think twice." {
		t.Fatalf("finish.ReasoningContent = %q, want %q", finish.ReasoningContent, "Think twice.")
	}
	if completion.Content != "pong" {
		t.Fatalf("completion.Content = %q, want pong", completion.Content)
	}
}

// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict
// TestChatMessagesToMapsCarriesReasoningContent pins the request-side echo:
// an assistant step's reasoning_content must reach the wire for the turn's
// later steps, and messages without reasoning must not grow the key.
func TestChatMessagesToMapsCarriesReasoningContent(t *testing.T) {
	maps := ChatMessagesToMaps([]ChatMessage{
		{Role: "assistant", ReasoningContent: "ponder", ToolCalls: []ToolCall{{ID: "c1", Name: "get_current_time", Arguments: "{}"}}},
		{Role: "user", Content: "hi"},
	})
	encoded, err := json.Marshal(maps[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"reasoning_content":"ponder"`) {
		t.Fatalf("assistant message lost reasoning_content: %s", encoded)
	}
	second, err := json.Marshal(maps[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(second), "reasoning_content") {
		t.Fatalf("user message grew a reasoning_content key: %s", second)
	}
}

// TestParallelToolCallsSentAndLatchedOffOnRefusal pins the second crosscheck
// finding: the reference asks for parallel tool calls on every non-Anthropic
// streaming request with tools, and drops the parameter (process-wide latch)
// when a gateway rejects it. The port carried the latch but never sent the
// field.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
func TestParallelToolCallsSentAndLatchedOffOnRefusal(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := map[string]any{}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		attempt := len(bodies)
		mu.Unlock()
		if attempt == 1 {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":{"message":"unknown parameter: parallel_tool_calls"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "deepseek-flash-1017", APIBase: server.URL})
	tools := []ToolDef{{Name: "get_current_time", Description: "time", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	call := func() error {
		_, err := provider.ACompleteStream(context.Background(),
			CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, Tools: tools},
			func(StreamEvent) {})
		return err
	}
	if err := call(); err != nil {
		t.Fatalf("first (refused) call: %v", err)
	}
	if err := call(); err != nil {
		t.Fatalf("second call: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 {
		t.Fatalf("request count = %d, want 3 (refused + retry + latched call)", len(bodies))
	}
	if bodies[0]["parallel_tool_calls"] != true {
		t.Fatalf("first request parallel_tool_calls = %#v, want true", bodies[0]["parallel_tool_calls"])
	}
	if _, ok := bodies[1]["parallel_tool_calls"]; ok {
		t.Fatalf("retry kept parallel_tool_calls: %#v", bodies[1]["parallel_tool_calls"])
	}
	if _, ok := bodies[2]["parallel_tool_calls"]; ok {
		t.Fatalf("refusal latch did not stick: %#v", bodies[2]["parallel_tool_calls"])
	}
	if _, ok := bodies[0]["tools"]; !ok {
		t.Fatalf("tool request lost its tools: %#v", bodies[0])
	}
}
