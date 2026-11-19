package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Parity: core/tests/test_litellm_streaming.py::TestRealAPITextStreaming::test_text_stream
// Parity: core/tests/test_event_type_extension.py::test_roundtrip_snapshot_accumulation
func TestAnthropicProviderStreamsTextDeltas(t *testing.T) {
	capture := &streamCapture{}
	frames := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":11,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":1}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`event: ping`,
		`data: {"type":"ping"}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		// Frames alternate event/data lines; an empty line terminates each one.
		for index := 0; index < len(frames); index += 2 {
			writeSSEFrame(t, writer, frames[index]+"\n"+frames[index+1])
		}
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-haiku-4-5-20251001", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		System:    "You are helpful.",
		MaxTokens: 128,
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	bodies := capture.snapshot()
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want 1", len(bodies))
	}
	body := bodies[0]
	if body["stream"] != true {
		t.Fatalf("request stream = %#v, want true", body["stream"])
	}
	if body["system"] != "You are helpful." {
		t.Fatalf("request system = %#v, want the folded system prompt", body["system"])
	}
	if _, ok := body["messages"].([]any); !ok {
		t.Fatalf("request messages = %#v, want the Anthropic message array", body["messages"])
	}
	if _, ok := body["stream_options"]; ok {
		t.Fatalf("Anthropic request must not carry stream_options: %#v", body)
	}

	var deltas []TextDeltaEvent
	for _, event := range events {
		if delta, ok := event.(TextDeltaEvent); ok {
			deltas = append(deltas, delta)
		}
	}
	if len(deltas) != 3 {
		t.Fatalf("text deltas = %d (%#v), want 3", len(deltas), deltas)
	}
	wantContents := []string{"Hel", "lo", " world"}
	wantSnapshots := []string{"Hel", "Hello", "Hello world"}
	for i, delta := range deltas {
		if delta.Content != wantContents[i] || delta.Snapshot != wantSnapshots[i] {
			t.Fatalf("delta[%d] = %#v, want %q / %q", i, delta, wantContents[i], wantSnapshots[i])
		}
	}
	if len(events) != 5 {
		t.Fatalf("event count = %d (%#v), want 3 deltas + text_end + finish", len(events), events)
	}
	if end, ok := events[3].(TextEndEvent); !ok || end.FullText != "Hello world" {
		t.Fatalf("text_end = %#v, want the full text", events[3])
	}
	finish, ok := events[4].(FinishEvent)
	if !ok || finish.StopReason != "stop" || finish.InputTokens != 14 || finish.OutputTokens != 5 {
		t.Fatalf("finish = %#v, want stop with usage 14/5", events[4])
	}
	if finish.CachedTokens != 2 || finish.CacheCreationTokens != 1 {
		t.Fatalf("finish cache tokens = %d/%d, want 2/1", finish.CachedTokens, finish.CacheCreationTokens)
	}
	if completion.Content != "Hello world" || completion.StopReason != "stop" {
		t.Fatalf("completion = %#v, want the assembled answer", completion)
	}
	if completion.Model != "claude-haiku-4-5-20251001" || completion.InputTokens != 14 || completion.OutputTokens != 5 {
		t.Fatalf("completion usage/model = %#v", completion)
	}
}

// Parity: core/tests/test_litellm_streaming.py::TestRealAPIToolCallStreaming::test_tool_call_stream
func TestAnthropicProviderStreamsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":9,"output_tokens":1}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"To"}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"kyo\"}"}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_stop","index":0}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_stop"}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-haiku-4-5-20251001", server.URL)
	if err != nil {
		t.Fatalf("NewAnthropicProviderWithBase: %v", err)
	}
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "weather?"}},
		Tools:    []ToolDef{{Name: "get_weather", Description: "Get the weather"}},
	}, collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v, want 1", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "get_weather" || call.Arguments != `{"city":"Tokyo"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if completion.StopReason != "tool_calls" {
		t.Fatalf("stop reason = %q, want tool_calls", completion.StopReason)
	}

	var toolEvents []ToolCallEvent
	for _, event := range events {
		if tool, ok := event.(ToolCallEvent); ok {
			toolEvents = append(toolEvents, tool)
		}
	}
	if len(toolEvents) != 1 {
		t.Fatalf("tool call events = %d (%#v), want 1", len(toolEvents), toolEvents)
	}
	if toolEvents[0].ToolUseID != "toolu_1" || toolEvents[0].ToolName != "get_weather" || toolEvents[0].ToolInput["city"] != "Tokyo" {
		t.Fatalf("tool call event = %#v", toolEvents[0])
	}
	if _, ok := events[len(events)-1].(FinishEvent); !ok {
		t.Fatalf("last event = %T, want FinishEvent", events[len(events)-1])
	}
}

// Parity-exempt: a proxy that ignores stream:true and returns one Messages
// JSON body; the Go transport forwards the whole answer as a single honest
// delta instead of failing.
func TestAnthropicProviderStreamFallsBackToPlainMessageJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"msg_1",
			"model":"claude-haiku-4-5-20251001",
			"content":[{"type":"text","text":"whole answer"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":7,"output_tokens":2}
		}`)
	}))
	defer server.Close()

	provider, err := NewAnthropicProviderWithBase("test-key", "claude-haiku-4-5-20251001", server.URL)
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
	if completion.Content != "whole answer" || completion.StopReason != "stop" {
		t.Fatalf("completion = %#v", completion)
	}
	var deltas []TextDeltaEvent
	for _, event := range events {
		if delta, ok := event.(TextDeltaEvent); ok {
			deltas = append(deltas, delta)
		}
	}
	if len(deltas) != 1 || deltas[0].Content != "whole answer" || deltas[0].Snapshot != "whole answer" {
		t.Fatalf("deltas = %#v, want one full-text delta", deltas)
	}
}

// Parity-exempt: verifies the wire model id keeps the Anthropic prefix out of
// the request body while the transport stays on /v1/messages.
func TestAnthropicProviderStreamStripsRoutingPrefix(t *testing.T) {
	capture := &streamCapture{}
	captureServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":1}}}`)
		writeSSEFrame(t, writer, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`)
		writeSSEFrame(t, writer, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)
	}))
	defer captureServer.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "anthropic/claude-haiku-4-5-20251001", APIKey: "test-key", APIBase: captureServer.URL})
	if _, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	body := capture.snapshot()[0]
	if body["model"] != "claude-haiku-4-5-20251001" {
		t.Fatalf("wire model = %#v, want the prefix stripped", body["model"])
	}
}
