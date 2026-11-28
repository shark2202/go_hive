package agentloop

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/internals/stall_detector.py::ngram_similarity
func TestNgramSimilarityMatchesPythonBigramJaccard(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want float64
	}{
		{"near-identical", "I am still stuck", "I am stuck", 0.6923076923076923},
		{"identical", "I am still stuck", "I am still stuck", 1},
		{"different-attempt-number", "attempt 1", "attempt 2", 0.7777777777777778},
		{"punctuation-only-change", "Done.", "Done!", 0.6},
		{"empty-left", "", "abc", 0},
		{"whitespace-only", "   ", "abc", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NgramSimilarity(tc.a, tc.b, 2)
			if math.Abs(got-tc.want) > 1e-12 {
				t.Fatalf("NgramSimilarity(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// Parity: framework/agent_loop/internals/stall_detector.py::is_stalled
func TestIsResponseStalledUsesConsecutivePairs(t *testing.T) {
	cases := []struct {
		name      string
		window    []string
		threshold int
		want      bool
	}{
		{
			name:      "three-identical-responses",
			window:    []string{"I am still stuck", "I am still stuck", "I am still stuck"},
			threshold: 3,
			want:      true,
		},
		{
			name:      "one-dissimilar-breaks-the-chain",
			window:    []string{"I am still stuck", "attempt 2", "I am still stuck"},
			threshold: 3,
			want:      false,
		},
		{
			name:      "window-too-short",
			window:    []string{"same", "same"},
			threshold: 3,
			want:      false,
		},
		{
			name:      "empty-first-response",
			window:    []string{"", "same", "same"},
			threshold: 3,
			want:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsResponseStalled(tc.window, tc.threshold, DefaultStallSimilarityThreshold); got != tc.want {
				t.Fatalf("IsResponseStalled(%q, %d) = %v, want %v", tc.window, tc.threshold, got, tc.want)
			}
		})
	}
}

// Parity: framework/agent_loop/agent_loop.py::execute (6f stall detection)
func TestResponseStallEndsTurnAndPublishesNodeStalled(t *testing.T) {
	const text = "I am still stuck"
	toolCall := llm.ToolCall{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: text, toolCalls: []llm.ToolCall{toolCall}},
		{content: text, toolCalls: []llm.ToolCall{toolCall}},
		{content: text, toolCalls: []llm.ToolCall{toolCall}},
	}}
	loop := NewAgentLoop(DefaultLoopConfig(), successToolExecutor)
	var reasons []string
	loop.NodeStalledSink = func(reason string) { reasons = append(reasons, reason) }

	result, err := loop.ExecuteTurn(
		context.Background(),
		midturnAgentContext(completer, "echo"),
		NewNodeConversation("system"),
		nil,
	)
	if err == nil {
		t.Fatal("ExecuteTurn returned nil, want ResponseStalledError")
	}
	var stalled *ResponseStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("error = %T %v, want *ResponseStalledError", err, err)
	}
	if !strings.Contains(err.Error(), "Node stalled: 3 similar responses (85+ threshold)") {
		t.Fatalf("error = %q, want the upstream stall text", err)
	}
	if result.Success {
		t.Fatalf("result = %#v, want Success=false", result)
	}
	if completer.callCount() != 3 {
		t.Fatalf("LLM calls = %d, want 3", completer.callCount())
	}
	if len(reasons) != 1 || reasons[0] != ResponseStallReason {
		t.Fatalf("node_stalled reasons = %q, want [%q]", reasons, ResponseStallReason)
	}
}

// Parity: framework/agent_loop/agent_loop.py::execute (6f stall detection)
func TestResponseStallDoesNotTripOnDissimilarText(t *testing.T) {
	toolCall := llm.ToolCall{ID: "call-1", Name: "echo", Arguments: `{"value":"x"}`}
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "step one", toolCalls: []llm.ToolCall{toolCall}},
		{content: "a completely different answer", toolCalls: []llm.ToolCall{toolCall}},
		{content: "done", toolCalls: nil},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 10}, successToolExecutor)
	var reasons []string
	loop.NodeStalledSink = func(reason string) { reasons = append(reasons, reason) }

	_, err := loop.ExecuteTurn(
		context.Background(),
		midturnAgentContext(completer, "echo"),
		NewNodeConversation("system"),
		nil,
	)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(reasons) != 0 {
		t.Fatalf("node_stalled reasons = %q, want none", reasons)
	}
}

// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func TestResponseStallCursorRoundTrip(t *testing.T) {
	store := NewMemoryConversationStore()
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt: "system",
		Store:        store,
	})
	loop := NewAgentLoop(DefaultLoopConfig(), successToolExecutor)
	window := []string{"still here", "still here", "still here"}
	loop.persistRecentResponses(conv, window)

	got := loop.restoreRecentResponses(conv)
	if len(got) != len(window) {
		t.Fatalf("restored window = %q, want %q", got, window)
	}
	for i := range window {
		if got[i] != window[i] {
			t.Fatalf("restored window = %q, want %q", got, window)
		}
	}

	cleared := loop.clearRecentResponses(conv, got)
	if len(cleared) != 0 {
		t.Fatalf("cleared window = %q, want empty", cleared)
	}
	if got := loop.restoreRecentResponses(conv); len(got) != 0 {
		t.Fatalf("restored after clear = %q, want empty", got)
	}
}
