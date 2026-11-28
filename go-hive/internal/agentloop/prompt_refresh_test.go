package agentloop

// Per-iteration system-prompt refresh (upstream agent_loop.py step 6b3). The
// loop rebuilds the conversation prompt from the context only when the context
// supplies a rebuild hook, and only swaps it in when the text changed — a
// stable prompt keeps the cached prefix warm, a reloaded catalog reaches the
// next model call.

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6b3 dynamic
// prompt refresh -> conversation.update_system_prompt on change).
func TestExecuteTurnRefreshesSystemPromptFromProvider(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: []llm.ToolCall{{ID: "t1", Name: "ping", Arguments: `{}`}}},
		{content: "done"},
	}}
	prompts := []struct{ static, suffix string }{
		{static: "STATIC ONE", suffix: "NARRATIVE ONE"},
		{static: "STATIC TWO", suffix: ""},
	}
	providerCalls := 0
	ctx := midturnAgentContext(completer, "ping")
	ctx.DynamicSystemPromptProvider = func() (string, string) {
		index := providerCalls
		providerCalls++
		if index >= len(prompts) {
			index = len(prompts) - 1
		}
		return prompts[index].static, prompts[index].suffix
	}

	conv := NewNodeConversation("STALE PROMPT")
	loop := NewAgentLoop(DefaultLoopConfig(), nil)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, ctx.AvailableTools); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if completer.callCount() != 2 {
		t.Fatalf("LLM calls = %d, want 2", completer.callCount())
	}
	// Each model call sees the prompt the provider rendered for that
	// iteration, not the conversation's original one.
	if got := completer.request(0).System; got != "STATIC ONE\n\nNARRATIVE ONE" {
		t.Fatalf("first request system = %q, want the static+suffix pair", got)
	}
	if got := completer.request(1).System; got != "STATIC TWO" {
		t.Fatalf("second request system = %q, want the refreshed static prompt", got)
	}
	// The split survives on the conversation: static and suffix are tracked
	// separately so the transport can emit them as two cache-aware blocks.
	if got := conv.SystemPromptStatic(); got != "STATIC TWO" {
		t.Fatalf("conversation static = %q, want STATIC TWO", got)
	}
	if got := conv.SystemPromptDynamicSuffix(); got != "" {
		t.Fatalf("conversation suffix = %q, want empty", got)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d, want one per iteration", providerCalls)
	}
}

// Parity-exempt: the explicit opt-in is the Go seam (upstream gates the refresh
// on its provider trio); this pins that a context without the hook keeps the
// caller-owned prompt byte-identical — the queen's static-prompt path.
func TestExecuteTurnLeavesSystemPromptAloneWithoutProvider(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	ctx := midturnAgentContext(completer)
	// The fields a worker would carry must not leak into a prompt the loop was
	// not asked to rebuild.
	ctx.SkillsCatalogPrompt = "LEAKED CATALOG"
	ctx.ProtocolsPrompt = "LEAKED PROTOCOLS"

	conv := NewNodeConversation("OWNED SYSTEM PROMPT")
	loop := NewAgentLoop(DefaultLoopConfig(), nil)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := completer.request(0).System; got != "OWNED SYSTEM PROMPT" {
		t.Fatalf("request system = %q, want the caller-owned prompt unchanged", got)
	}
	if got := conv.SystemPrompt(); got != "OWNED SYSTEM PROMPT" {
		t.Fatalf("conversation system = %q, want the caller-owned prompt unchanged", got)
	}
}

// Parity-exempt: upstream catches provider exceptions; the Go provider is
// recovered so a broken rebuild leaves the prompt untouched instead of wiping
// it with an empty render.
func TestExecuteTurnKeepsSystemPromptWhenProviderPanics(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{{content: "done"}}}
	ctx := midturnAgentContext(completer)
	ctx.DynamicSystemPromptProvider = func() (string, string) {
		panic("catalog render failed")
	}
	conv := NewNodeConversation("OWNED SYSTEM PROMPT")
	loop := NewAgentLoop(DefaultLoopConfig(), nil)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if got := completer.request(0).System; got != "OWNED SYSTEM PROMPT" {
		t.Fatalf("request system = %q, want the prompt kept after a provider panic", got)
	}
}

// Parity: framework/agent_loop/prompting.py::build_system_prompt_parts_for_context
// (the Go builder's split, recombined through JoinSystemPromptParts).
func TestJoinSystemPromptPartsMatchesUpstreamAssembly(t *testing.T) {
	cases := []struct{ static, suffix, want string }{
		{static: "base", suffix: "", want: "base"},
		{static: "", suffix: "narrative", want: "narrative"},
		{static: "base", suffix: "narrative", want: "base\n\nnarrative"},
		{static: "", suffix: "", want: ""},
	}
	for _, tc := range cases {
		if got := JoinSystemPromptParts(tc.static, tc.suffix); got != tc.want {
			t.Fatalf("JoinSystemPromptParts(%q, %q) = %q, want %q", tc.static, tc.suffix, got, tc.want)
		}
	}
	// Empty catalog + empty protocols render the base prompt with no stray
	// separator (the "absent catalog" shape). Upstream prefixes the focus block
	// with "\n"; with nothing above it that leading separator is the whole
	// difference from the raw base text.
	base := "WORKER BASE"
	ctx := &AgentContext{AgentSpec: AgentSpec{SystemPrompt: &base}}
	static, suffix := BuildSystemPromptPartsForContext(ctx, PromptOverrides{})
	if static != "\n"+base || suffix != "" {
		t.Fatalf("parts = (%q, %q), want (%q, \"\")", static, suffix, "\n"+base)
	}
	if strings.Contains(static, "<available_skills>") {
		t.Fatalf("absent catalog still rendered a skills section: %q", static)
	}
}
