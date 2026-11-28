package agentloop

// Credential collect is a blocking synthetic tool, like ask_user: the loop must
// end the turn after a successful call so the host can park on the secure form.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)

import "testing"

func credentialBatch(action string, isError bool) ([]pendingToolCall, []ToolResult) {
	input := map[string]any{}
	if action != "" {
		input["action"] = action
	}
	return []pendingToolCall{{
			index: 0,
			call:  ToolCallRequest{Name: "credentials", Input: input},
		}},
		[]ToolResult{{Content: "result", IsError: isError}}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (credentials branch)
func TestBatchStopsTurnOnCredentialCollect(t *testing.T) {
	agentCtx := &AgentContext{}

	pending, results := credentialBatch("collect", false)
	if !batchStopsTurn(agentCtx, pending, results) {
		t.Fatal("successful credentials(collect) did not stop the turn")
	}

	// A failed collect stays an ordinary tool error: the model gets the
	// message and may correct itself in the same turn.
	pending, results = credentialBatch("collect", true)
	if batchStopsTurn(agentCtx, pending, results) {
		t.Fatal("failed credentials(collect) stopped the turn")
	}

	// attach/detach/help are non-blocking.
	for _, action := range []string{"attach", "detach", "help", ""} {
		pending, results = credentialBatch(action, false)
		if batchStopsTurn(agentCtx, pending, results) {
			t.Fatalf("credentials(%q) stopped the turn", action)
		}
	}

	// The action is matched case-insensitively, as the credentials dispatch is.
	pending, results = credentialBatch("COLLECT", false)
	if !batchStopsTurn(agentCtx, pending, results) {
		t.Fatal("case-insensitive collect did not stop the turn")
	}
}

// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (ask_user branch)
func TestBatchStopsTurnOnAskUserStillWins(t *testing.T) {
	agentCtx := &AgentContext{}
	pending := []pendingToolCall{{index: 0, call: ToolCallRequest{Name: AskUserToolName, Input: map[string]any{}}}}
	results := []ToolResult{{Content: "Waiting for user input..."}}
	if !batchStopsTurn(agentCtx, pending, results) {
		t.Fatal("successful ask_user no longer stops the turn")
	}
}
