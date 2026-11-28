package agentloop

// Compaction pass orchestration.
//
// Upstream's compact() is deliberately cheapest-first: truncate oversized user
// messages, clear old compactable tool results (microcompact), prune
// recoverable tool results, and only then spend an LLM call on a summary. The
// budget is re-checked after each free stage, so a conversation a free stage
// rescued never pays for a summarizer call.
//
// Two event boundaries are observable:
//   - CONTEXT_COMPACTION_STARTED is published before any stage runs, carrying
//     the pre-compaction usage, message count and circuit-breaker flag.
//   - CONTEXT_COMPACTED (log_compaction) is published on every exit path —
//     including a free-stage success, a failed summary and a circuit-open skip
//     — so the UI sees the pass finish.
//
// Parity: framework/agent_loop/internals/compaction.py::compact
// Parity: framework/agent_loop/agent_loop.py::_compact
//
// The circuit breaker only suppresses the LLM stage; the free stages still run
// so a provider outage cannot leave the conversation growing past the window.
// A summary that cannot bring usage under budget leaves the conversation
// intact — upstream removed the destructive deterministic "emergency" summary.

import (
	"context"
	"log"
	"strings"
)

const (
	compactionMicrocompactedCounter = "compaction_microcompacted"
	compactionPrunedCounter         = "compaction_pruned"
)

// compactConversationIfNeeded runs one compaction pass when the conversation
// has crossed its trigger, and fires the reminder points around it the way
// upstream _compact does. Upstream's call sites enter _compact only when
// needs_compaction() (or the pre-send ratio >= 1.0) is true; the Go call site
// runs every iteration, so the gate lives here.
func (l *AgentLoop) compactConversationIfNeeded(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, reminderCtx *ReminderAgentContext) {
	if conv == nil || agentCtx == nil {
		return
	}
	// The conversation's window/buffers always follow the live loop config,
	// including for a conversation restored with an older window in its meta.
	l.applyConversationLimits(conv)
	if !conv.NeedsCompaction() {
		return
	}
	l.fireReminder(PRE_COMPACT, reminderCtx, conv, "")
	l.runCompactionPass(ctx, agentCtx, conv)
	// log_compaction (inside runCompactionPass) runs before _compact fires
	// POST_COMPACT: the event's after-values describe the post-stage state,
	// not the re-announced reminders.
	l.fireReminder(POST_COMPACT, reminderCtx, conv, "")
}

// runCompactionPass is upstream compact()'s stage sequence. Every return path
// ends in finishCompactionPass so CONTEXT_COMPACTED fires exactly once per
// pass.
func (l *AgentLoop) runCompactionPass(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation) {
	maxContext := l.Config.MaxContextTokens
	if maxContext <= 0 {
		maxContext = DefaultMaxContextTokens
	}
	ratioBefore := conv.UsageRatio()
	// The event needs the pre-compaction state; the debug report uses the
	// same manifest. Upstream builds it only once the conversation is over
	// the window (ratio_before >= 1.0), and only then attaches it to the
	// CONTEXT_COMPACTED payload.
	//
	// Parity: framework/agent_loop/internals/compaction.py::compact (pre_inventory gate)
	var preInventory []map[string]any
	if ratioBefore >= 1.0 {
		preInventory = BuildMessageInventory(conv)
	}
	// Circuit breaker: stop LLM-based compaction after repeated failures, but
	// still run the free stages so the conversation does not silently grow
	// past the window.
	llmSkipped := l.compactionCircuitOpen()
	if l.OnContextCompactionStarted != nil {
		l.OnContextCompactionStarted(ContextCompactionStarted{
			UsageBefore:          compactionPercent(ratioBefore),
			MessageCount:         conv.MessageCount(),
			LLMCompactionSkipped: llmSkipped,
		})
	}

	// --- Step 0a: defensive truncation of oversized historical user messages.
	// Runs before microcompaction so a 2 MB message cannot make the summarizer
	// prompt itself exceed the model window.
	if clipped := TruncateOversizedUserMessages(conv); clipped > 0 {
		l.bumpBy(oversizedUserMessageClippedStat, clipped)
	}

	// --- Step 0: microcompaction (count-based, cheapest).
	if cleared := Microcompact(conv); cleared > 0 {
		l.bumpBy(compactionMicrocompactedCounter, cleared)
	}
	if !conv.NeedsCompaction() {
		l.recordCompactionSuccess()
		l.finishCompactionPass(agentCtx, conv, ratioBefore, maxContext, preInventory)
		return
	}

	// --- Step 1: prune old tool results (free, token-budget based).
	protect, minPrune := l.midTurnPruneBudget()
	if pruned, err := conv.PruneOldToolResults(protect, minPrune); err == nil && pruned > 0 {
		l.bumpBy(compactionPrunedCounter, pruned)
	}
	if !conv.NeedsCompaction() {
		l.recordCompactionSuccess()
		l.finishCompactionPass(agentCtx, conv, ratioBefore, maxContext, preInventory)
		return
	}

	// --- Step 2: LLM summary compaction. The circuit breaker (or a missing
	// LLM) skips only this stage; the pass still finishes below.
	if agentCtx.LLM != nil && !llmSkipped {
		spec := agentCtx.AgentSpec
		summary, err := LlmCompact(ctx, agentCtx.LLM, conv.Messages(), LlmCompactOptions{
			Spec:             &spec,
			MaxContextTokens: maxContext,
		})
		if err != nil || strings.TrimSpace(summary) == "" {
			l.recordCompactionFailure()
		} else {
			if l.recompactionChainDetected() {
				l.bumpBy(compactionRecompactionCounter, 1)
			}
			if err := conv.Compact(summary, CompactOptions{}); err != nil {
				l.recordCompactionFailure()
			}
		}
	} else if llmSkipped {
		l.bumpBy(compactionCircuitOpenCounter, 1)
	}

	if !conv.NeedsCompaction() {
		l.recordCompactionSuccess()
	} else {
		// Upstream warns and leaves the conversation intact; it never falls
		// back to a destructive deterministic summary.
		log.Printf("[compaction] usage still at %.0f%% after compaction; conversation left intact (LLM summary unavailable or insufficient)", conv.UsageRatio()*100)
	}
	l.finishCompactionPass(agentCtx, conv, ratioBefore, maxContext, preInventory)
}

// finishCompactionPass is upstream log_compaction: the runtime-log step, the
// CONTEXT_COMPACTED event and the opt-in debug report, all against the
// post-stage state.
func (l *AgentLoop) finishCompactionPass(agentCtx *AgentContext, conv *NodeConversation, ratioBefore float64, maxContext int, preInventory []map[string]any) {
	ratioAfter := conv.UsageRatio()
	afterTokens := conv.EstimateTokens()
	// Parity: framework/agent_loop/internals/compaction.py::log_compaction.
	l.logCompactionStep(agentCtx, conv, ratioBefore, maxContext)
	if l.OnContextCompacted != nil {
		l.OnContextCompacted(BuildContextCompacted(
			ratioBefore, ratioAfter, afterTokens, conv.maxContextTokens, preInventory))
	}
	if compactionDebugEnabled() {
		writeCompactionReport(agentCtx, ratioBefore, ratioAfter, preInventory)
	}
}
