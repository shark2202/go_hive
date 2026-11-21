package agentloop

// Conversation-level limits pushed from the loop config. Upstream builds its
// NodeConversation with the LoopConfig's max_context_tokens and hybrid
// compaction buffers on every execute; the port's conversations can be
// restored from disk with an older window in their meta, so the loop
// re-asserts its config before every turn.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (NodeConversation
// built with self._config.max_context_tokens / compaction buffers)

// applyConversationLimits pushes the loop's context budget and hybrid
// compaction buffers onto its conversation.
func (l *AgentLoop) applyConversationLimits(conv *NodeConversation) {
	if l == nil || conv == nil {
		return
	}
	if l.Config.MaxContextTokens > 0 {
		conv.maxContextTokens = l.Config.MaxContextTokens
	}
	// Upstream's LoopConfig always carries the hybrid buffers (8k + 40% +
	// 12k warning), so the pointer pair is installed unless a caller zeroes
	// both components to keep the legacy threshold rule.
	if l.Config.CompactionBufferTokens != 0 || l.Config.CompactionBufferRatio != 0 {
		tokens := l.Config.CompactionBufferTokens
		ratio := l.Config.CompactionBufferRatio
		conv.compactionBufferTokens = &tokens
		conv.compactionBufferRatio = &ratio
	}
	if l.Config.CompactionWarningBufferTokens > 0 {
		warning := l.Config.CompactionWarningBufferTokens
		conv.compactionWarningBuffer = &warning
	}
}
