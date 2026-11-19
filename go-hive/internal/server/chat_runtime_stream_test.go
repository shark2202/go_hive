package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// streamScriptStep is one scripted ACompleteStream call: the text increments
// the fake upstream would send, then the assembled completion.
type streamScriptStep struct {
	chunks     []string
	completion llm.Completion
	err        error
}

// scriptedStreamLLM is a Completer + StreamCompleter double. Each streaming
// call consumes the next step, so a tool round trip (two LLM calls) can be
// scripted end to end.
type scriptedStreamLLM struct {
	mu         sync.Mutex
	steps      []streamScriptStep
	streamed   int
	plainCalls int
	plainReply string
}

func (f *scriptedStreamLLM) ModelName() string { return "stream-fake-model" }

func (f *scriptedStreamLLM) AComplete(_ context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plainCalls++
	reply := f.plainReply
	if reply == "" {
		reply = "plain fallback"
	}
	return llm.Completion{Content: reply, Model: "stream-fake-model", StopReason: "end_turn"}, nil
}

func (f *scriptedStreamLLM) ACompleteStream(_ context.Context, _ llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	f.mu.Lock()
	f.streamed++
	step := streamScriptStep{}
	if len(f.steps) > 0 {
		step = f.steps[0]
		f.steps = f.steps[1:]
	}
	f.mu.Unlock()

	accumulated := ""
	for _, chunk := range step.chunks {
		accumulated += chunk
		emit(llm.TextDeltaEvent{Content: chunk, Snapshot: accumulated})
	}
	if step.err != nil {
		return llm.Completion{}, step.err
	}
	if accumulated != "" {
		emit(llm.TextEndEvent{FullText: accumulated})
	}
	completion := step.completion
	if completion.Model == "" {
		completion.Model = "stream-fake-model"
	}
	emit(llm.FinishEvent{StopReason: completion.StopReason, Model: completion.Model})
	return completion, nil
}

func (f *scriptedStreamLLM) callCounts() (streamed, plain int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.streamed, f.plainCalls
}

func streamContents(events []*eventbus.AgentEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		content, _ := event.Data["content"].(string)
		out = append(out, content)
	}
	return out
}

// Parity: tests/test_event_loop_node.py::test_queen_stream_uses_client_output_delta
// Parity: core/tests/test_stream_events.py::TestEventConstruction::test_text_delta_with_values
func TestChatRuntimeStreamsOneFramePerUpstreamDelta(t *testing.T) {
	model := &scriptedStreamLLM{steps: []streamScriptStep{{
		chunks:     []string{"Hel", "lo", "!"},
		completion: llm.Completion{Content: "Hello!", StopReason: "stop", Model: "stream-fake-model"},
	}}}
	runtime, bus := newBusChatRuntime(t, model, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("say hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	deltas := recorder.ofType(eventbus.EventClientOutputDelta)
	if len(deltas) != 3 {
		t.Fatalf("client_output_delta count = %d (%v), want one frame per upstream delta",
			len(deltas), streamContents(deltas))
	}
	wantContents := []string{"Hel", "lo", "!"}
	wantSnapshots := []string{"Hel", "Hello", "Hello!"}
	for i, delta := range deltas {
		content, _ := delta.Data["content"].(string)
		snapshot, _ := delta.Data["snapshot"].(string)
		if content != wantContents[i] || snapshot != wantSnapshots[i] {
			t.Fatalf("delta[%d] = content %q snapshot %q, want %q / %q",
				i, content, snapshot, wantContents[i], wantSnapshots[i])
		}
		if delta.Data["iteration"] != 1 {
			t.Fatalf("delta[%d] iteration = %#v, want 1", i, delta.Data["iteration"])
		}
		if delta.StreamID != "queen" || delta.NodeID == nil || *delta.NodeID != "queen" {
			t.Fatalf("delta[%d] stream/node = %q/%v, want queen/queen", i, delta.StreamID, delta.NodeID)
		}
	}

	// The persisted transcript keeps one assistant message, and the loop's
	// end-of-call snapshot must not republish it as a fourth frame.
	parts := waitForAssistantParts(t, runtime, 1)
	assistants := 0
	for _, part := range parts {
		if part["role"] == "assistant" {
			assistants++
			if part["content"] != "Hello!" {
				t.Fatalf("assistant part = %#v, want the full answer once", part)
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("assistant parts = %d, want 1", assistants)
	}
	if got := len(recorder.ofType(eventbus.EventLLMTurnComplete)); got != 1 {
		t.Fatalf("llm_turn_complete count = %d, want 1", got)
	}
	if streamed, plain := model.callCounts(); streamed != 1 || plain != 0 {
		t.Fatalf("calls streamed/plain = %d/%d, want 1/0", streamed, plain)
	}
}

// Parity: tests/test_event_loop_node.py::test_queen_stream_uses_client_output_delta
// Parity: core/tests/test_litellm_streaming.py::TestRealAPITextStreaming::test_text_stream
func TestChatRuntimeStreamsFromHTTPProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		flusher := writer.(http.Flusher)
		for _, frame := range []string{
			`data: {"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"Hi "},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"there"},"finish_reason":null}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			"data: [DONE]",
		} {
			if _, err := io.WriteString(writer, frame+"\n\n"); err != nil {
				t.Errorf("write SSE frame: %v", err)
			}
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := llm.NewHTTPProvider(llm.ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		APIBase: server.URL,
	})
	runtime, bus := newBusChatRuntime(t, provider, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	deltas := recorder.ofType(eventbus.EventClientOutputDelta)
	if got := strings.Join(streamContents(deltas), "|"); got != "Hi |there|!" {
		t.Fatalf("delta contents = %q, want the real SSE increments", got)
	}
	parts := waitForAssistantParts(t, runtime, 1)
	if len(parts) < 2 || parts[len(parts)-1]["role"] != "assistant" || parts[len(parts)-1]["content"] != "Hi there!" {
		t.Fatalf("parts = %#v, want one final assistant message", parts)
	}
}

// Parity: tests/test_event_loop_node.py::test_queen_stream_uses_client_output_delta
// Parity: tests/test_event_loop_node.py::test_lifecycle_events_published
func TestChatRuntimeStreamsToolCallTurns(t *testing.T) {
	model := &scriptedStreamLLM{steps: []streamScriptStep{
		{
			chunks: []string{"checking "},
			completion: llm.Completion{
				Content:    "checking ",
				StopReason: "tool_calls",
				Model:      "stream-fake-model",
				ToolCalls: []llm.ToolCall{{
					ID:        "tool-1",
					Name:      "echo_tool",
					Arguments: `{"value":"x"}`,
				}},
			},
		},
		{
			chunks:     []string{"final ", "answer"},
			completion: llm.Completion{Content: "final answer", StopReason: "stop", Model: "stream-fake-model"},
		},
	}}
	tools := []llm.ToolDef{{Name: "echo_tool", Description: "echoes"}}
	execute := func(_ context.Context, name string, input map[string]any) (string, bool, error) {
		if name != "echo_tool" || input["value"] != "x" {
			t.Fatalf("unexpected tool call %q %#v", name, input)
		}
		return "tool-ok", false, nil
	}
	runtime, bus := newBusChatRuntime(t, model, tools, execute)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("use the tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	if starts := recorder.ofType(eventbus.EventToolCallStarted); len(starts) != 1 {
		t.Fatalf("tool_call_started count = %d, want 1 (%v)", len(starts), recorder.types())
	}
	if completions := recorder.ofType(eventbus.EventToolCallCompleted); len(completions) != 1 {
		t.Fatalf("tool_call_completed count = %d, want 1 (%v)", len(completions), recorder.types())
	}
	deltas := recorder.ofType(eventbus.EventClientOutputDelta)
	if got := strings.Join(streamContents(deltas), "|"); got != "checking |final |answer" {
		t.Fatalf("delta contents = %q, want the two turns' increments", got)
	}
	if got := len(recorder.ofType(eventbus.EventLLMTurnComplete)); got != 2 {
		t.Fatalf("llm_turn_complete count = %d, want 2", got)
	}

	parts := waitForAssistantParts(t, runtime, 2)
	assistants := 0
	for _, part := range parts {
		if part["role"] == "assistant" {
			assistants++
		}
	}
	if assistants != 2 {
		t.Fatalf("assistant parts = %d, want one per LLM call (%#v)", assistants, parts)
	}
	if streamed, plain := model.callCounts(); streamed != 2 || plain != 0 {
		t.Fatalf("calls streamed/plain = %d/%d, want 2/0", streamed, plain)
	}
}

// Parity-exempt: the Go chat runtime falls back to AComplete when the provider
// declares streaming unsupported; no Python test drives that server adapter.
func TestChatRuntimeFallsBackWhenProviderCannotStream(t *testing.T) {
	model := &scriptedStreamLLM{
		steps:      []streamScriptStep{{err: llm.ErrStreamingUnsupported}},
		plainReply: "plain answer",
	}
	runtime, bus := newBusChatRuntime(t, model, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("first"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	deltas := recorder.ofType(eventbus.EventClientOutputDelta)
	if len(deltas) != 1 {
		t.Fatalf("client_output_delta count = %d, want the single non-streaming frame", len(deltas))
	}
	if content := deltas[0].Data["content"]; content != "plain answer" {
		t.Fatalf("delta content = %#v, want the AComplete text", content)
	}
	if streamed, plain := model.callCounts(); streamed != 1 || plain != 1 {
		t.Fatalf("calls streamed/plain = %d/%d, want 1/1", streamed, plain)
	}

	// The unsupported verdict is latched: the next turn must not retry it.
	if err := runtime.Deliver("second"); err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 2)
	if streamed, plain := model.callCounts(); streamed != 1 || plain != 2 {
		t.Fatalf("after fallback calls streamed/plain = %d/%d, want 1/2", streamed, plain)
	}
}

// Parity-exempt: a mid-stream upstream failure must surface as a failed turn
// without persisting the partial answer; no Python test covers this adapter.
func TestChatRuntimeStreamFailureDoesNotPersistPartialAnswer(t *testing.T) {
	model := &scriptedStreamLLM{steps: []streamScriptStep{{
		chunks: []string{"partial "},
		err:    errors.New("upstream disconnected"),
	}}}
	runtime, bus := newBusChatRuntime(t, model, nil, nil)
	recorder := recordChatEvents(bus)

	if err := runtime.Deliver("break mid stream"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	failed := waitForChatEvents(t, recorder, eventbus.EventExecutionFailed, 1)
	if !strings.Contains(failed[0].Data["error"].(string), "upstream disconnected") {
		t.Fatalf("execution_failed error = %#v", failed[0].Data["error"])
	}
	if recorder.ofType(eventbus.EventExecutionCompleted) != nil {
		t.Fatal("a failed stream must not publish execution_completed")
	}
	deltas := recorder.ofType(eventbus.EventClientOutputDelta)
	if len(deltas) != 1 || deltas[0].Data["content"] != "partial " {
		t.Fatalf("deltas = %#v, want the one partial frame", streamContents(deltas))
	}

	parts, err := runtime.Transcript()
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	for _, part := range parts {
		if part["role"] == "assistant" {
			t.Fatalf("partial answer was persisted: %#v", part)
		}
	}
}
