package server

// Regression: wiring the generic worker prompt builder into the agent loop must
// not touch the queen's static prompt path. The queen owns her prompt
// (chatQueenStaticPrompt) and bypasses agentloop.BuildSystemPromptForContext;
// a chat session's context carries no dynamic system-prompt provider, so a loop
// turn leaves the golden byte-identical even when catalog/protocols fields are
// present.

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// queenPromptProbeLLM records the system prompt of the one completion it serves.
type queenPromptProbeLLM struct {
	requests []llm.CompletionRequest
}

func (m *queenPromptProbeLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.requests = append(m.requests, req)
	return llm.Completion{Content: "ok", StopReason: "stop"}, nil
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6b3 prompt
// refresh gate) + framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
// (the queen's static prompt bypasses the generic builder).
func TestQueenStaticPromptGoldenUnaffectedByLoopPromptRefresh(t *testing.T) {
	golden := readQueenPromptGolden(t)
	provider := &queenPromptProbeLLM{}
	ctx := &agentloop.AgentContext{
		LLM:       provider,
		AgentSpec: agentloop.AgentSpec{AgentType: "event_loop"},
		// A chat session's context can carry this data (the skills reminder
		// reads it), but the queen prompt is owned by the runtime: the loop
		// must not rebuild it from these fields.
		SkillsCatalogPrompt: "QUEEN-CATALOG-MUST-NOT-LEAK",
		ProtocolsPrompt:     "QUEEN-PROTOCOLS-MUST-NOT-LEAK",
	}
	conv := agentloop.NewNodeConversation(golden)
	loop := agentloop.NewAgentLoop(agentloop.DefaultLoopConfig(), nil)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("LLM requests = %d, want 1", len(provider.requests))
	}
	if got := provider.requests[0].System; got != golden {
		t.Fatalf("queen system prompt drifted from the golden: got %d chars, want %d%s",
			len(got), len(golden), firstDifference(got, golden))
	}
	if got := conv.SystemPrompt(); got != golden {
		t.Fatalf("queen conversation prompt drifted from the golden: got %d chars, want %d", len(got), len(golden))
	}
	for _, leaked := range []string{"QUEEN-CATALOG-MUST-NOT-LEAK", "QUEEN-PROTOCOLS-MUST-NOT-LEAK", "<available_skills>"} {
		if strings.Contains(provider.requests[0].System, leaked) {
			t.Fatalf("queen system prompt leaked %q", leaked)
		}
	}
}
