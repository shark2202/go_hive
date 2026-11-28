package agentloop

import "github.com/shark2202/go_hive/go-hive/internal/llm"

// recordPromptTokenCount feeds the provider-reported size of THIS request's
// prompt into the conversation's token estimate. Upstream's token_counts
// ["input"] is a billing sum across every inner LLM call in a turn; using that
// sum for usage_ratio would compare billing to a per-request budget and report
// fictional ratios. Only a positive per-call count replaces the estimate, so a
// provider that omits usage keeps the char-based fallback.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (FinishEvent -> conversation.update_token_count)
func (l *AgentLoop) recordPromptTokenCount(conv *NodeConversation, completion llm.Completion) {
	if conv == nil || completion.InputTokens <= 0 {
		return
	}
	conv.UpdateTokenCount(completion.InputTokens)
}
