package agentloop

import (
	"context"
	"strings"
	"testing"
)

// Parity-exempt: upstream builds its NodeConversation with the LoopConfig's
// max_context_tokens and hybrid compaction buffers inline in execute.
// test_node_conversation.py covers the trigger formula, but no upstream test
// pins the loop-config wiring this port had left unconnected (a fresh
// conversation kept the 180k window and the 0.8 multiplicative threshold
// even when the loop config said 32k + 8k/40% buffers). This test covers the
// wiring and its observable trigger.
func TestExecuteTurnAppliesLoopContextLimits(t *testing.T) {
	cfg := DefaultLoopConfig()
	cfg.MaxContextTokens = 32_000
	loop := NewAgentLoop(cfg, nil)
	conv := NewNodeConversation("You are a test assistant.")
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "ok"}}}
	ctx := midturnAgentContext(completer)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}

	if conv.maxContextTokens != 32_000 {
		t.Fatalf("conversation window = %d, want the loop config's 32000", conv.maxContextTokens)
	}
	if conv.compactionBufferTokens == nil || *conv.compactionBufferTokens != 8_000 {
		t.Fatalf("compaction buffer tokens = %v, want 8000", conv.compactionBufferTokens)
	}
	if conv.compactionBufferRatio == nil || *conv.compactionBufferRatio != 0.4 {
		t.Fatalf("compaction buffer ratio = %v, want 0.4", conv.compactionBufferRatio)
	}
	if conv.compactionWarningBuffer == nil || *conv.compactionWarningBuffer != 12_000 {
		t.Fatalf("compaction warning buffer = %v, want 12000", conv.compactionWarningBuffer)
	}

	// Trigger = 32000 - (8000 + 0.4*32000) = 11_200 tokens ≈ 33_600 chars.
	mustAddUser(t, conv, strings.Repeat("x", 20_000), UserMessageOptions{})
	if conv.NeedsCompaction() {
		t.Fatal("needs_compaction fired before the hybrid trigger")
	}
	mustAddUser(t, conv, strings.Repeat("x", 20_000), UserMessageOptions{})
	if !conv.NeedsCompaction() {
		t.Fatal("hybrid trigger did not fire past 11200 tokens")
	}
}
