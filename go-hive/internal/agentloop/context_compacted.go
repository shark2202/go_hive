package agentloop

import "math"

// ContextCompactionStarted carries the data payload of the
// CONTEXT_COMPACTION_STARTED event upstream publishes at the start of every
// compaction pass. The debug panel and any "agent is busy" indicator use it so
// a multi-minute compaction window is not mistaken for a frozen agent.
//
// Parity: framework/agent_loop/internals/compaction.py::compact
type ContextCompactionStarted struct {
	// UsageBefore is the pre-compaction usage percentage (round(ratio*100)).
	UsageBefore int
	// MessageCount is len(conversation.messages) at pass start.
	MessageCount int
	// LLMCompactionSkipped reports that the failure circuit breaker is open,
	// so this pass will not spend an LLM summary call.
	LLMCompactionSkipped bool
}

// ContextCompacted carries the data payload of the CONTEXT_COMPACTED event
// upstream publishes once a compaction pass finishes. The chat runtime maps it
// onto a session event; the queen's reflection subscriber turns that event into
// one long memory pass.
//
// Parity: framework/agent_loop/internals/compaction.py::log_compaction
type ContextCompacted struct {
	Level         string
	UsageBefore   int
	UsageAfter    int
	TokensBeforeK int
	TokensAfterK  int
	// MessageInventory is the pre-compaction message manifest. It is attached
	// only when the pre-compaction usage ratio reached 1.0 — upstream builds
	// pre_inventory under exactly that gate and includes it in the event only
	// when it is not nil.
	MessageInventory []map[string]any
}

// BuildContextCompacted computes the event payload from the conversation state
// captured around a completed compaction.
func BuildContextCompacted(ratioBefore, ratioAfter float64, tokensAfter, maxContext int, inventory []map[string]any) ContextCompacted {
	beforeK := 0
	if maxContext > 0 {
		// ratio_before is a fraction of max_context_tokens captured
		// pre-compaction; multiply back to recover the absolute token count.
		beforeK = compactionTokensK(ratioBefore * float64(maxContext))
	}
	return ContextCompacted{
		Level:            CompactionLevel(ratioBefore, ratioAfter),
		UsageBefore:      compactionPercent(ratioBefore),
		UsageAfter:       compactionPercent(ratioAfter),
		TokensBeforeK:    beforeK,
		TokensAfterK:     compactionTokensK(float64(tokensAfter)),
		MessageInventory: inventory,
	}
}

// compactionPercent matches Python round(ratio * 100): round half to even.
// Go's int(x*100+0.5) is round-half-up and diverges on exact halves.
func compactionPercent(ratio float64) int {
	return int(math.RoundToEven(ratio * 100))
}

// compactionTokensK matches Python round(tokens / 1000) (1K = 1000).
func compactionTokensK(tokens float64) int {
	return int(math.RoundToEven(tokens / 1000))
}
