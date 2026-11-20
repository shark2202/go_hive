package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// streamCapture records the request bodies an SSE server received.
type streamCapture struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (c *streamCapture) record(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	body := map[string]any{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Errorf("decode request body: %v", err)
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	return body
}

func (c *streamCapture) snapshot() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.bodies...)
}

func writeSSEFrame(t *testing.T, writer http.ResponseWriter, frame string) {
	t.Helper()
	if _, err := io.WriteString(writer, frame+"\n\n"); err != nil {
		t.Errorf("write SSE frame: %v", err)
	}
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func collectStreamEvents(events *[]StreamEvent) func(StreamEvent) {
	return func(event StreamEvent) { *events = append(*events, event) }
}

// Parity: core/tests/test_litellm_streaming.py::TestRealAPITextStreaming::test_text_stream
// Parity: core/tests/test_event_type_extension.py::test_roundtrip_snapshot_accumulation
func TestProviderStreamsTextDeltas(t *testing.T) {
	capture := &streamCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"id":"c1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, ": keep-alive")
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, `data: {"id":"c1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	body := capture.snapshot()[0]
	if body["stream"] != true {
		t.Fatalf("request stream = %#v, want true", body["stream"])
	}
	options, _ := body["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage true", body["stream_options"])
	}

	var deltas []TextDeltaEvent
	for _, event := range events {
		switch typed := event.(type) {
		case TextDeltaEvent:
			deltas = append(deltas, typed)
		case ToolCallEvent:
			t.Fatalf("unexpected tool call event: %#v", typed)
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
	if !ok || finish.StopReason != "stop" || finish.InputTokens != 11 || finish.OutputTokens != 3 {
		t.Fatalf("finish = %#v, want stop with usage 11/3", events[4])
	}

	if completion.Content != "Hello world" || completion.StopReason != "stop" {
		t.Fatalf("completion = %#v, want the assembled answer", completion)
	}
	if completion.Model != "gpt-4o-mini" || completion.InputTokens != 11 || completion.OutputTokens != 3 {
		t.Fatalf("completion usage/model = %#v", completion)
	}
}

// Parity: core/tests/test_litellm_streaming.py::TestRealAPIToolCallStreaming::test_tool_call_stream
// Parity: core/tests/test_litellm_streaming.py::TestRealAPIToolCallStreaming::test_multi_tool_call_stream
func TestProviderStreamsToolCallsAcrossFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Checking. "},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ty\":\"Tokyo\"}"}}]},"finish_reason":null}]}`)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"clock","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "weather?"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}

	if completion.Content != "Checking. " {
		t.Fatalf("content = %q, want the streamed preamble", completion.Content)
	}
	if len(completion.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want 2", completion.ToolCalls)
	}
	if completion.ToolCalls[0].ID != "call_1" || completion.ToolCalls[0].Name != "get_weather" ||
		completion.ToolCalls[0].Arguments != `{"city":"Tokyo"}` {
		t.Fatalf("tool call 0 = %#v", completion.ToolCalls[0])
	}
	if completion.ToolCalls[1].ID != "call_2" || completion.ToolCalls[1].Name != "clock" ||
		completion.ToolCalls[1].Arguments != "{}" {
		t.Fatalf("tool call 1 = %#v", completion.ToolCalls[1])
	}

	// Upstream yields tool calls once assembled, then text_end, then finish.
	var kinds []string
	for _, event := range events {
		kinds = append(kinds, event.StreamEventType())
	}
	wantKinds := []string{"text_delta", "tool_call", "tool_call", "text_end", "finish"}
	if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("event kinds = %v, want %v", kinds, wantKinds)
	}
	first, _ := events[1].(ToolCallEvent)
	if first.ToolUseID != "call_1" || first.ToolName != "get_weather" || first.ToolInput["city"] != "Tokyo" {
		t.Fatalf("first tool call event = %#v", first)
	}
	second, _ := events[2].(ToolCallEvent)
	if second.ToolUseID != "call_2" || second.ToolName != "clock" || len(second.ToolInput) != 0 {
		t.Fatalf("second tool call event = %#v", second)
	}
}

// Parity-exempt: fault injection for a truncated SSE body; the Python suite has
// no equivalent mid-stream disconnect test.
func TestProviderStreamInterruptedMidStreamFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		request := make([]byte, 8192)
		_, _ = conn.Read(request)
		payload := `data: {"choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}` + "\n\n"
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n",
			len(payload), payload)
	}()

	provider := NewHTTPProvider(ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		APIBase: "http://" + listener.Addr().String(),
	})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err == nil {
		t.Fatalf("ACompleteStream succeeded on a truncated stream: %#v", completion)
	}
	if !strings.Contains(err.Error(), "openai stream") {
		t.Fatalf("error = %v, want the streaming read failure", err)
	}
	if completion.Content != "" || len(completion.ToolCalls) != 0 {
		t.Fatalf("partial completion returned: %#v", completion)
	}
	var deltas, streamErrors int
	for _, event := range events {
		switch event.(type) {
		case TextDeltaEvent:
			deltas++
		case StreamErrorEvent:
			streamErrors++
		}
	}
	if deltas != 1 {
		t.Fatalf("text deltas = %d, want the one frame that did arrive", deltas)
	}
	if streamErrors != 1 {
		t.Fatalf("stream error events = %d, want 1", streamErrors)
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderComplete
// Parity-exempt: the plain-JSON answer of a gateway that ignored stream:true is
// a Go-only fallback; no Python test exercises it.
func TestProviderStreamFallsBackToNonSSEBody(t *testing.T) {
	capture := &streamCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{
			"id":"chatcmpl-1",
			"model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"whole answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":2}
		}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if len(capture.snapshot()) != 1 {
		t.Fatalf("requests = %d, want the streaming request only", len(capture.snapshot()))
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
	if _, ok := events[len(events)-1].(FinishEvent); !ok {
		t.Fatalf("last event = %T, want FinishEvent", events[len(events)-1])
	}
}

// Parity: core/tests/test_litellm_provider.py::TestLiteLLMProviderComplete
// Parity-exempt: retrying without the stream parameter after a 400 is a
// transport fallback with no upstream Python counterpart.
func TestProviderStreamFallsBackWhenStreamRejected(t *testing.T) {
	capture := &streamCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := capture.record(t, request)
		if body["stream"] == true {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, `{"error":{"message":"stream is not supported"}}`)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"fallback answer"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if completion.Content != "fallback answer" {
		t.Fatalf("completion = %#v, want the non-streaming answer", completion)
	}
	bodies := capture.snapshot()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want stream then non-stream", len(bodies))
	}
	if bodies[0]["stream"] != true {
		t.Fatalf("first request stream = %#v, want true", bodies[0]["stream"])
	}
	if _, streamed := bodies[1]["stream"]; streamed {
		t.Fatalf("fallback request still carries stream: %#v", bodies[1])
	}
	if len(events) == 0 {
		t.Fatal("no events emitted for the fallback completion")
	}
}

// Parity-exempt: context cancellation of a live SSE read; covered upstream only
// implicitly by the asyncio client.
func TestProviderStreamHonoursContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"choices":[{"index":0,"delta":{"content":"first"},"finish_reason":null}]}`)
		select {
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var events []StreamEvent
	_, err := provider.ACompleteStream(ctx,
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		func(event StreamEvent) {
			events = append(events, event)
			if _, ok := event.(TextDeltaEvent); ok {
				cancel()
			}
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(events) == 0 {
		t.Fatal("no events emitted before cancellation")
	}
}

// Parity: core/tests/test_litellm_provider.py::TestMiniMaxStreamFallback::test_stream_uses_nonstream_fallback_for_minimax
func TestProviderStreamUnsupportedTransports(t *testing.T) {
	// MiniMax chunks are known to break the upstream parser, so the provider
	// deliberately stays on the non-streaming path and reports it up front.
	// Anthropic messages streaming is covered by stream_anthropic_test.go.
	minimax := NewHTTPProvider(ProviderOptions{Model: "minimax/minimax-m2", APIKey: "test-key", APIBase: "http://127.0.0.1:1"})
	if _, err := minimax.ACompleteStream(context.Background(), CompletionRequest{}, nil); !errors.Is(err, ErrStreamingUnsupported) {
		t.Fatalf("minimax error = %v, want ErrStreamingUnsupported", err)
	}
}

// Parity-exempt: the Python client (litellm over httpx) stops reading at the
// [DONE] sentinel; this pins the same contract against a proxy that keeps the
// SSE socket open after it.
func TestProviderStreamStopsAtDoneSentinel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"id":"c1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
		// A non-compliant gateway can leave the connection open after the
		// sentinel; the client must not block waiting for EOF.
		select {
		case <-release:
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	type streamOutcome struct {
		completion Completion
		err        error
	}
	outcome := make(chan streamOutcome, 1)
	go func() {
		completion, err := provider.ACompleteStream(context.Background(),
			CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
			func(StreamEvent) {})
		outcome <- streamOutcome{completion: completion, err: err}
	}()

	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("ACompleteStream: %v", got.err)
		}
		if got.completion.Content != "done" {
			t.Fatalf("completion = %#v, want the streamed answer", got.completion)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ACompleteStream blocked after [DONE]; it must terminate at the sentinel")
	}
}
