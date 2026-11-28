package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity: core/tests/test_litellm_provider.py::TestThinkingBlockStreaming::test_thinking_blocks_reassembled_onto_finish_event
func TestAnthropicStreamReassemblesSignedThinkingBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":12,"output_tokens":1}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weigh it"}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG-ABC"}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_stop","index":0}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"The answer."}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_stop","index":1}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	var reasoning []string
	for _, event := range events {
		if delta, ok := event.(ReasoningDeltaEvent); ok {
			reasoning = append(reasoning, delta.Content)
		}
	}
	if strings.Join(reasoning, "|") != "Let me |weigh it" {
		t.Fatalf("reasoning deltas = %#v, want the thinking text streamed live", reasoning)
	}

	want := []map[string]any{{
		"type":      "thinking",
		"thinking":  "Let me weigh it",
		"signature": "SIG-ABC",
	}}
	if len(completion.ThinkingBlocks) != 1 {
		t.Fatalf("completion thinking blocks = %#v, want one signed block", completion.ThinkingBlocks)
	}
	if got := completion.ThinkingBlocks[0]; got["type"] != want[0]["type"] ||
		got["thinking"] != want[0]["thinking"] || got["signature"] != want[0]["signature"] {
		t.Fatalf("completion thinking block = %#v, want %#v", got, want[0])
	}

	finish, ok := events[len(events)-1].(FinishEvent)
	if !ok {
		t.Fatalf("last event = %T, want FinishEvent", events[len(events)-1])
	}
	if len(finish.ThinkingBlocks) != 1 || finish.ThinkingBlocks[0]["signature"] != "SIG-ABC" {
		t.Fatalf("finish thinking blocks = %#v, want the signed block", finish.ThinkingBlocks)
	}
	if completion.Content != "The answer." {
		t.Fatalf("content = %q, want the assistant answer", completion.Content)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestThinkingBlockStreaming::test_unsigned_thinking_block_is_dropped
func TestAnthropicStreamDropsUnsignedThinkingBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":5}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"half a "}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_stop","index":0}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"cut off"}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if len(completion.ThinkingBlocks) != 0 {
		t.Fatalf("completion thinking blocks = %#v, want none for an unsigned block", completion.ThinkingBlocks)
	}
	finish, ok := events[len(events)-1].(FinishEvent)
	if !ok {
		t.Fatalf("last event = %T, want FinishEvent", events[len(events)-1])
	}
	if len(finish.ThinkingBlocks) != 0 {
		t.Fatalf("finish thinking blocks = %#v, want none for an unsigned block", finish.ThinkingBlocks)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream (redacted_thinking
// blocks are self-contained and carried verbatim, with no signature delta)
func TestAnthropicStreamPreservesRedactedThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":5}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"ENCRYPTED"}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_stop","index":0}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if len(completion.ThinkingBlocks) != 1 ||
		completion.ThinkingBlocks[0]["type"] != "redacted_thinking" ||
		completion.ThinkingBlocks[0]["data"] != "ENCRYPTED" {
		t.Fatalf("completion thinking blocks = %#v, want the redacted block verbatim", completion.ThinkingBlocks)
	}
}

// Parity: core/framework/agent_loop/conversation.py::Message.to_llm_dict and
// core/framework/llm/litellm.py Anthropic request transform (thinking blocks
// precede text/tool_use blocks on the echoed assistant turn)
func TestAnthropicRequestEchoesThinkingBlocks(t *testing.T) {
	capture := &streamCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-sonnet-4","usage":{"input_tokens":3}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-sonnet-4", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	blocks := []map[string]any{{
		"type":      "thinking",
		"thinking":  "Let me weigh it",
		"signature": "SIG-ABC",
	}}
	if _, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "hi"},
			{
				Role:           "assistant",
				Content:        "",
				ThinkingBlocks: blocks,
				ToolCalls: []ToolCall{{
					ID:        "toolu_1",
					Name:      "read_file",
					Arguments: `{"path":"a.txt"}`,
				}},
			},
			{Role: "tool", ToolCallID: "toolu_1", Content: "file body"},
			{Role: "user", Content: "continue"},
		},
	}, func(StreamEvent) {}); err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	body := capture.snapshot()[0]
	messages, _ := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("request messages = %d (%#v), want 3 (the tool result and follow-up user turn merge)", len(messages), messages)
	}
	assistant, _ := messages[1].(map[string]any)
	content, _ := assistant["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content blocks = %#v, want thinking + tool_use", content)
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "thinking" || first["thinking"] != "Let me weigh it" || first["signature"] != "SIG-ABC" {
		t.Fatalf("echoed thinking block = %#v, want it verbatim before the tool call", first)
	}
	second, _ := content[1].(map[string]any)
	if second["type"] != "tool_use" || second["id"] != "toolu_1" {
		t.Fatalf("second block = %#v, want the tool_use", second)
	}
}

// Parity-exempt: the OpenAI chat wire format has no thinking_blocks message
// field; the projection must not forward an unknown key. The Anthropic-only
// echo path is covered above.
func TestOpenAIRequestStripsThinkingBlocks(t *testing.T) {
	capture := &streamCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	blocks := []map[string]any{{"type": "thinking", "thinking": "x", "signature": "S"}}
	if _, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "ok", ThinkingBlocks: blocks},
		},
	}, func(StreamEvent) {}); err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	body := capture.snapshot()[0]
	messages, _ := body["messages"].([]any)
	assistant, _ := messages[1].(map[string]any)
	if _, present := assistant["thinking_blocks"]; present {
		t.Fatalf("OpenAI request leaked thinking_blocks: %#v", assistant)
	}
}

// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream (the
// non-streaming Messages transport also preserves signed thinking blocks so a
// reasoning turn can be echoed back)
func TestAnthropicNonStreamPreservesThinkingBlocks(t *testing.T) {
	server, capture := newCompletionServer(t, http.StatusOK, `{
		"model": "claude-sonnet-4",
		"content": [
			{"type": "thinking", "thinking": "Let me weigh it", "signature": "SIG-ABC"},
			{"type": "redacted_thinking", "data": "ENCRYPTED"},
			{"type": "text", "text": "answer"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 5, "output_tokens": 2}
	}`)
	provider := NewHTTPProvider(ProviderOptions{
		Model:   "anthropic/claude-sonnet-4",
		APIKey:  "test-key",
		APIBase: server.URL,
	})
	completion, err := provider.Complete(context.Background(), []map[string]any{
		{"role": "user", "content": "hi"},
	}, CompleteOptions{MaxTokens: 64})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(completion.ThinkingBlocks) != 2 {
		t.Fatalf("thinking blocks = %#v, want the signed and redacted blocks", completion.ThinkingBlocks)
	}
	if completion.ThinkingBlocks[0]["type"] != "thinking" ||
		completion.ThinkingBlocks[0]["thinking"] != "Let me weigh it" ||
		completion.ThinkingBlocks[0]["signature"] != "SIG-ABC" {
		t.Fatalf("signed block = %#v", completion.ThinkingBlocks[0])
	}
	if completion.ThinkingBlocks[1]["type"] != "redacted_thinking" || completion.ThinkingBlocks[1]["data"] != "ENCRYPTED" {
		t.Fatalf("redacted block = %#v", completion.ThinkingBlocks[1])
	}
	if completion.Content != "answer" {
		t.Fatalf("content = %q, want the text block only", completion.Content)
	}
	_ = capture
}
