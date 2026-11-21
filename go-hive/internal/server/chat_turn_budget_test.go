package server

// Tests for the queen turn's output budget: the main llm slot's max_tokens is
// read when a chat session is built and becomes the agent context's MaxTokens.

import (
	"os"
	"path/filepath"
	"testing"
)

// Parity: core/framework/config.py::get_max_tokens,
// core/framework/server/queen_orchestrator.py (AgentContext max_tokens)
func TestChatTurnMaxTokensFollowsTheMainSlot(t *testing.T) {
	home := newFeatureHome(t)

	// The configured file value wins.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "max_tokens": 4321,
	}})
	if got := chatTurnMaxTokens(home.Root); got != 4321 {
		t.Fatalf("chatTurnMaxTokens(file) = %d, want 4321", got)
	}

	// No file value: the model catalog's output cap.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5.5",
	}})
	if got := chatTurnMaxTokens(home.Root); got != 128000 {
		t.Fatalf("chatTurnMaxTokens(catalog) = %d, want the catalog cap", got)
	}

	// A non-integer is ignored, like get_max_tokens' `int(...)` guard failing
	// over to the catalog and then DEFAULT_MAX_TOKENS.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"max_tokens": "not-a-number",
	}})
	if got := chatTurnMaxTokens(home.Root); got != 8192 {
		t.Fatalf("chatTurnMaxTokens(string) = %d, want 8192", got)
	}

	// No home (a bare test runtime) and a home without configuration.json both
	// keep the 8192 default.
	if got := chatTurnMaxTokens(""); got != 8192 {
		t.Fatalf("chatTurnMaxTokens(no home) = %d, want 8192", got)
	}
	if got := chatTurnMaxTokens(filepath.Join(t.TempDir(), "missing")); got != 8192 {
		t.Fatalf("chatTurnMaxTokens(missing config) = %d, want 8192", got)
	}
}

// TestChatRuntimeBudgetsTheQueenTurnFromTheMainSlot is the consumption proof:
// a built session's agent context carries the configured max_tokens, so the
// value reaches the loop rather than only the REST readback.
//
// Parity: core/framework/server/queen_orchestrator.py (AgentContext max_tokens)
func TestChatRuntimeBudgetsTheQueenTurnFromTheMainSlot(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "max_tokens": 4321,
	}})
	liveRoot := filepath.Join(home.Root, "queens", "queen_ava", "sessions", "session_20260101_100000_budget1")
	if err := os.MkdirAll(liveRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      liveRoot,
		QueenName: "queen_ava",
		HomeRoot:  home.Root,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	if runtime.agentCtx == nil {
		t.Fatal("agent context not built")
	}
	if got := runtime.agentCtx.MaxTokens; got != 4321 {
		t.Fatalf("agentCtx.MaxTokens = %d, want the configured 4321", got)
	}
}
