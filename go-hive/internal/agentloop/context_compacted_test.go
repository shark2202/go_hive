package agentloop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func TestBuildContextCompactedFieldsMatchUpstream(t *testing.T) {
	inventory := []map[string]any{{"seq": 1, "role": "user", "content_chars": 42}}
	cases := []struct {
		name        string
		ratioBefore float64
		ratioAfter  float64
		tokensAfter int
		maxContext  int
		inventory   []map[string]any
		want        ContextCompacted
	}{
		{
			// ratio_after <= 0.6 with a real drop: the LLM summary label.
			name:        "llm summary",
			ratioBefore: 0.95,
			ratioAfter:  0.40,
			tokensAfter: 40_000,
			maxContext:  200_000,
			want:        ContextCompacted{Level: "llm", UsageBefore: 95, UsageAfter: 40, TokensBeforeK: 190, TokensAfterK: 40},
		},
		{
			// after_pct >= before_pct - 1: prune_only even after the LLM pass.
			name:        "prune only",
			ratioBefore: 0.90,
			ratioAfter:  0.894,
			tokensAfter: 179_000,
			maxContext:  200_000,
			want:        ContextCompacted{Level: "prune_only", UsageBefore: 90, UsageAfter: 89, TokensBeforeK: 180, TokensAfterK: 179},
		},
		{
			// A drop over one point that stops above 60%: structural.
			name:        "structural",
			ratioBefore: 0.95,
			ratioAfter:  0.80,
			tokensAfter: 160_000,
			maxContext:  200_000,
			want:        ContextCompacted{Level: "structural", UsageBefore: 95, UsageAfter: 80, TokensBeforeK: 190, TokensAfterK: 160},
		},
		{
			// Python round() is half-to-even: 12.5 -> 12 and 62.5 -> 62,
			// while Go's int(x+0.5) would say 13 and 63.
			name:        "python half even rounding",
			ratioBefore: 0.125,
			ratioAfter:  0.625,
			tokensAfter: 100_000,
			maxContext:  160_000,
			want:        ContextCompacted{Level: "prune_only", UsageBefore: 12, UsageAfter: 62, TokensBeforeK: 20, TokensAfterK: 100},
		},
		{
			// The pre-compaction manifest rides along when supplied.
			name:        "inventory attached",
			ratioBefore: 1.20,
			ratioAfter:  0.30,
			tokensAfter: 60_000,
			maxContext:  200_000,
			inventory:   inventory,
			want:        ContextCompacted{Level: "llm", UsageBefore: 120, UsageAfter: 30, TokensBeforeK: 240, TokensAfterK: 60, MessageInventory: inventory},
		},
		{
			// No window: tokens_before_k is 0 (upstream's max_ctx <= 0 guard).
			name:        "no context window",
			ratioBefore: 0.5,
			ratioAfter:  0.2,
			tokensAfter: 1_500,
			maxContext:  0,
			want:        ContextCompacted{Level: "llm", UsageBefore: 50, UsageAfter: 20, TokensBeforeK: 0, TokensAfterK: 2},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := BuildContextCompacted(testCase.ratioBefore, testCase.ratioAfter,
				testCase.tokensAfter, testCase.maxContext, testCase.inventory)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("payload = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

// Parity: framework/agent_loop/conversation.py::add_tool_result (proactive
// microcompaction). Upstream's proactive microcompaction inside add_tool_result
// publishes no event; only a compact() pass that reaches log_compaction does.
// The Go port keeps the same split: compaction.go's proactive path never calls
// OnContextCompacted, and compactConversationIfNeeded fires once per pass it
// actually runs (any stage).
func TestProactiveMicrocompactionDoesNotPublishContextCompacted(t *testing.T) {
	conv := NewNodeConversation("system")
	for i := 0; i < MicrocompactKeepRecent; i++ {
		id := "seed_" + string(rune('a'+i))
		toolCalls := []map[string]any{{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      "terminal_exec",
				"arguments": `{}`,
			},
		}}
		if _, err := conv.AddAssistantMessage("", toolCalls, AssistantMessageOptions{}); err != nil {
			t.Fatalf("add assistant: %v", err)
		}
		path := "/tmp/spill-" + id
		if _, err := conv.AddToolResult(id, strings.Repeat("x", 200), ToolResultOptions{SpilloverPath: &path}); err != nil {
			t.Fatalf("add tool result: %v", err)
		}
	}

	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "tc_new", Name: "terminal_exec", Arguments: "{}"}}},
		{content: "done"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:      2,
		MaxToolResultChars: 50,
		SpilloverDir:       t.TempDir(),
	}, func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: strings.Repeat("y", 200)}, nil
	})
	var payloads []ContextCompacted
	loop.OnContextCompacted = func(payload ContextCompacted) {
		payloads = append(payloads, payload)
	}
	var started []ContextCompactionStarted
	loop.OnContextCompactionStarted = func(payload ContextCompactionStarted) {
		started = append(started, payload)
	}

	if _, err := loop.ExecuteTurn(context.Background(), midturnAgentContext(completer, "terminal_exec"), conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}

	cleared := false
	for _, message := range conv.Messages() {
		if strings.HasPrefix(message.Content, "Old tool result") {
			cleared = true
			break
		}
	}
	if !cleared {
		t.Fatal("test setup: the turn never exercised proactive microcompaction")
	}
	if len(payloads) != 0 {
		t.Fatalf("OnContextCompacted calls = %d, want 0 on the proactive-only path", len(payloads))
	}
	if len(started) != 0 {
		t.Fatalf("OnContextCompactionStarted calls = %d, want 0 on the proactive-only path", len(started))
	}
}

// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func TestCompactionPublishesContextCompactedOnce(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "the summary"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 200}, nil)
	agentCtx := midturnAgentContext(completer)
	agentCtx.StreamID = "queen"
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    200,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage(strings.Repeat("x", 1000), UserMessageOptions{}); err != nil {
		t.Fatalf("add message: %v", err)
	}
	ratioBefore := conv.UsageRatio()
	if ratioBefore < 1.0 {
		t.Fatalf("test setup: ratio_before = %v, want >= 1.0 for the inventory gate", ratioBefore)
	}

	var payloads []ContextCompacted
	loop.OnContextCompacted = func(payload ContextCompacted) {
		payloads = append(payloads, payload)
	}
	loop.compactConversationIfNeeded(context.Background(), agentCtx, conv, nil)

	if len(payloads) != 1 {
		t.Fatalf("OnContextCompacted calls = %d, want 1", len(payloads))
	}
	payload := payloads[0]
	if payload.UsageBefore != compactionPercent(ratioBefore) {
		t.Fatalf("usage_before = %d, want %d", payload.UsageBefore, compactionPercent(ratioBefore))
	}
	if payload.UsageAfter != compactionPercent(conv.UsageRatio()) {
		t.Fatalf("usage_after = %d, want %d", payload.UsageAfter, compactionPercent(conv.UsageRatio()))
	}
	if payload.TokensBeforeK != compactionTokensK(ratioBefore*200) {
		t.Fatalf("tokens_before_k = %d, want %d", payload.TokensBeforeK, compactionTokensK(ratioBefore*200))
	}
	if payload.TokensAfterK != compactionTokensK(float64(conv.EstimateTokens())) {
		t.Fatalf("tokens_after_k = %d, want %d", payload.TokensAfterK, compactionTokensK(float64(conv.EstimateTokens())))
	}
	if want := CompactionLevel(ratioBefore, conv.UsageRatio()); payload.Level != want {
		t.Fatalf("level = %q, want %q", payload.Level, want)
	}
	// ratio_before >= 1.0 is the only gate for the manifest.
	if payload.MessageInventory == nil {
		t.Fatal("message_inventory missing for a ratio_before >= 1.0 compaction")
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (pre_inventory gate)
func TestCompactionOmitsInventoryBelowFullWindow(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "the summary"}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 1000}, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    1000,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage(strings.Repeat("x", 1500), UserMessageOptions{}); err != nil {
		t.Fatalf("add message: %v", err)
	}
	ratioBefore := conv.UsageRatio()
	if ratioBefore < 0.5 || ratioBefore >= 1.0 {
		t.Fatalf("test setup: ratio_before = %v, want [0.5, 1.0)", ratioBefore)
	}

	var payloads []ContextCompacted
	loop.OnContextCompacted = func(payload ContextCompacted) {
		payloads = append(payloads, payload)
	}
	loop.compactConversationIfNeeded(context.Background(), midturnAgentContext(completer), conv, nil)

	if len(payloads) != 1 {
		t.Fatalf("OnContextCompacted calls = %d, want 1", len(payloads))
	}
	if payloads[0].MessageInventory != nil {
		t.Fatalf("message_inventory = %v, want nil below a full window", payloads[0].MessageInventory)
	}
}

// Parity: framework/agent_loop/internals/compaction.py::compact (log_compaction
// runs on every exit path, including a failed/unavailable summarizer)
func TestCompactionFailureStillPublishesContextCompacted(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{err: errors.New("provider down")}}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, MaxContextTokens: 200}, nil)
	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:        "system",
		MaxContextTokens:    200,
		CompactionThreshold: 0.5,
	})
	if _, err := conv.AddUserMessage(strings.Repeat("x", 1000), UserMessageOptions{}); err != nil {
		t.Fatalf("add message: %v", err)
	}

	compactedCalls := 0
	loop.OnContextCompacted = func(ContextCompacted) { compactedCalls++ }
	startedCalls := 0
	loop.OnContextCompactionStarted = func(ContextCompactionStarted) { startedCalls++ }
	loop.compactConversationIfNeeded(context.Background(), midturnAgentContext(completer), conv, nil)

	if startedCalls != 1 {
		t.Fatalf("OnContextCompactionStarted calls = %d, want 1", startedCalls)
	}
	// The pass finished even though no summary landed: upstream logs the
	// attempt and leaves the conversation intact rather than hiding the pass.
	if compactedCalls != 1 {
		t.Fatalf("OnContextCompacted calls = %d, want 1 when the summary failed", compactedCalls)
	}
}
