package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func noopToolGate(context.Context, string) error { return nil }

func sessionCostExecutor() ToolExecutor {
	return func(req ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: "ok " + req.Name}, nil
	}
}

// Parity-exempt: upstream has no session-level spend cap test — CostGuardStage
// only rejects a single request whose pre-flight estimate exceeds its budget.
func TestSessionCostLimitBlocksNextTurn(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "done", costUSD: 0.02},
		{content: "the blocked turn must not reach the model"},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:       2,
		SessionCostLimitUSD: 0.01,
		ToolCallGate:        noopToolGate,
	}, sessionCostExecutor())
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if got := loop.SessionCostUSD(); got != 0.02 {
		t.Fatalf("session cost = %v, want 0.02", got)
	}

	_, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	var limitErr *SessionCostLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("second turn error = %v, want *SessionCostLimitError", err)
	}
	if limitErr.SpentUSD != 0.02 || limitErr.LimitUSD != 0.01 {
		t.Fatalf("error amounts = %v/%v, want 0.02/0.01", limitErr.SpentUSD, limitErr.LimitUSD)
	}
	for _, want := range []string{"$0.0200", "$0.0100", "refusing the next turn", SessionCostLimitEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
	if calls := completer.callCount(); calls != 1 {
		t.Fatalf("model calls = %d, want 1 (the blocked turn must not call the model)", calls)
	}
	if got := loop.Counter(sessionCostBlockedCounter); got != 1 {
		t.Fatalf("%s = %d, want 1", sessionCostBlockedCounter, got)
	}
}

// Parity-exempt: Go-only session cap — spend accumulates across turns, not
// within one completion.
func TestSessionCostLimitAccumulatesAcrossTurns(t *testing.T) {
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "one", costUSD: 0.03},
		{content: "two", costUSD: 0.03},
		{content: "three", costUSD: 0.03},
	}}
	loop := NewAgentLoop(LoopConfig{
		MaxIterations:       2,
		SessionCostLimitUSD: 0.05,
		ToolCallGate:        noopToolGate,
	}, sessionCostExecutor())
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")

	for turn := 1; turn <= 2; turn++ {
		if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
			t.Fatalf("turn %d: %v", turn, err)
		}
	}
	if got := loop.SessionCostUSD(); got != 0.06 {
		t.Fatalf("session cost = %v, want 0.06", got)
	}
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); !errors.As(err, new(*SessionCostLimitError)) {
		t.Fatalf("third turn error = %v, want *SessionCostLimitError", err)
	}
	if calls := completer.callCount(); calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

// Parity-exempt: Go-only default — the cap ships disabled.
func TestSessionCostLimitDisabledByDefault(t *testing.T) {
	t.Setenv(SessionCostLimitEnv, "")
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{content: "one", costUSD: 100},
		{content: "two", costUSD: 100},
	}}
	loop := NewAgentLoop(LoopConfig{MaxIterations: 2, ToolCallGate: noopToolGate}, sessionCostExecutor())
	if loop.Config.SessionCostLimitUSD != 0 {
		t.Fatalf("limit = %v, want 0 when unset", loop.Config.SessionCostLimitUSD)
	}
	ctx := midturnAgentContext(completer)
	conv := NewNodeConversation("You are a test assistant.")
	for turn := 1; turn <= 2; turn++ {
		if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
			t.Fatalf("turn %d with the cap disabled: %v", turn, err)
		}
	}
	if got := loop.SessionCostUSD(); got != 200 {
		t.Fatalf("session cost = %v, want 200", got)
	}
}

// Parity-exempt: Go-only env parsing.
func TestSessionCostLimitFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{"", 0},
		{"0.5", 0.5},
		{" 2 ", 2},
		{"0", 0},
		{"-1", 0},
		{"not-a-number", 0},
		{"NaN", 0},
		{"+Inf", 0},
	}
	for _, tc := range cases {
		t.Setenv(SessionCostLimitEnv, tc.raw)
		if got := SessionCostLimitFromEnv(); got != tc.want {
			t.Fatalf("%s=%q => %v, want %v", SessionCostLimitEnv, tc.raw, got, tc.want)
		}
	}
}

// Parity-exempt: Go-only wiring — DefaultLoopConfig is what the chat runtime
// builds, so the env knob must reach it.
func TestDefaultLoopConfigReadsSessionCostLimit(t *testing.T) {
	t.Setenv(SessionCostLimitEnv, "0.75")
	if got := DefaultLoopConfig().SessionCostLimitUSD; got != 0.75 {
		t.Fatalf("DefaultLoopConfig().SessionCostLimitUSD = %v, want 0.75", got)
	}
	t.Setenv(SessionCostLimitEnv, "")
	if got := DefaultLoopConfig().SessionCostLimitUSD; got != 0 {
		t.Fatalf("DefaultLoopConfig().SessionCostLimitUSD = %v, want 0", got)
	}
}
