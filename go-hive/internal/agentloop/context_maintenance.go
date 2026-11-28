package agentloop

// Post-batch context maintenance.
//
// Upstream reclaims context twice after every tool batch, before it publishes
// the post_tool_results readout:
//
//  1. Screenshot eviction: hive-browser screenshots ride the conversation as
//     base64 data URLs in Message.ImageContent, and each costs ~250k tokens
//     when the provider counts base64 as text. The char-based usage estimate
//     counts every image as ~2000 tokens, so it cannot gate this — the newest
//     `max_retained_screenshots` messages keep their images and older ones are
//     stripped, leaving the text metadata (\"I took a screenshot at step N\").
//  2. Mid-turn pruning: once the conversation reaches 60% of its window, old
//     recoverable tool results are replaced with a spillover pointer, because
//     the standard compaction check may not fire in time inside one turn.
//
// Both are best-effort: a store failure leaves the conversation usable.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (image eviction;
// mid-turn pruning at usage_ratio >= 0.6)

const (
	// DefaultMaxRetainedScreenshots is the upstream default number of
	// screenshot-bearing messages that keep their image content.
	DefaultMaxRetainedScreenshots = 2

	screenshotsEvictedCounter  = "screenshots_evicted"
	midTurnPrunedCounter       = "mid_turn_pruned"
	midTurnPruneUsageRatio     = 0.6
	midTurnPruneProtectFloor   = 2000
	midTurnPruneMinTokensFloor = 1000
)

// maxRetainedScreenshots resolves the eviction bound. A negative value disables
// eviction (upstream's `>= 0` gate); zero — unset on a partially-built config —
// falls back to the upstream default.
func (l *AgentLoop) maxRetainedScreenshots() int {
	if l == nil || l.Config.MaxRetainedScreenshots == 0 {
		return DefaultMaxRetainedScreenshots
	}
	return l.Config.MaxRetainedScreenshots
}

// maintainContextAfterBatch runs the two reclamations above. It is called by
// the turn loop after a tool batch is stored and before the batch-end context
// readout is published, so the readout reports the reclaimed size.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (image eviction;
// mid-turn pruning at usage_ratio >= 0.6)
func (l *AgentLoop) maintainContextAfterBatch(conv *NodeConversation) {
	if l == nil || conv == nil {
		return
	}
	if keep := l.maxRetainedScreenshots(); keep >= 0 {
		if evicted, err := conv.EvictOldImages(keep); err == nil && evicted > 0 {
			l.bumpBy(screenshotsEvictedCounter, evicted)
		}
	}
	if conv.UsageRatio() < midTurnPruneUsageRatio {
		return
	}
	protect, minPrune := l.midTurnPruneBudget()
	if pruned, err := conv.PruneOldToolResults(protect, minPrune); err == nil && pruned > 0 {
		l.bumpBy(midTurnPrunedCounter, pruned)
	}
}

// midTurnPruneBudget is the mid-turn prune pair upstream derives from the
// context window: protect the newest max(2000, window/12) tokens, and only
// prune when the reclaimable tail is at least max(1000, protect/3) tokens.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (prune budget)
func (l *AgentLoop) midTurnPruneBudget() (protect, minPrune int) {
	maxContext := l.Config.MaxContextTokens
	if maxContext <= 0 {
		maxContext = DefaultMaxContextTokens
	}
	protect = maxInt(midTurnPruneProtectFloor, maxContext/12)
	minPrune = maxInt(midTurnPruneMinTokensFloor, protect/3)
	return protect, minPrune
}
