package agentloop

// Replay circuit breaker and the replay-exemption table.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (Phase 1
// replay detector, both tiers) and framework/agent_loop/internals/types.py::
// LoopConfig.replay_exempt_tools / replay_detector_enabled /
// replay_detector_within_last_turns.
//
// The gap this closes: the port only had the post-execution doom-loop
// detector, which fires *after* a repeated call has already run. Upstream runs
// a two-tier guard *before* dispatch:
//
//   - below ``tool_doom_loop_threshold`` consecutive identical calls: execute,
//     but prepend a "[Replay detected: ...]" steer onto the stored result so
//     the model can see it is repeating itself;
//   - at/above the threshold: trip the breaker — refuse the offending call,
//     stub it and every sibling call of the same batch, and park the turn as
//     ParkReasonDoomLoop. A single response carrying N identical calls thus
//     collapses to zero executions instead of N.
//
// The streak is ``prior_streak + in_batch_dup``: prior turns count only
// *successful* completed calls (a failing side effect is the doom-loop
// detector's business, not the breaker's), while duplicates inside the batch
// count every occurrence — they have not run yet, so success is unknown.
//
// Scope note (see applyReplayBreaker): both tiers are exactly upstream's. The
// breaker refuses a repeated identical call across turns as well as inside one
// response (upstream refuses at streak >= threshold unconditionally), so a
// model that re-issues the same call on three consecutive turns has the third
// refused before dispatch and the turn parked as ParkReasonDoomLoop. The streak
// has upstream's reset boundary: a text-only assistant turn breaks it, because
// count_consecutive_completed_tool_calls stops walking at a turn with no tool
// calls. The deterministic doom-loop detector in doom_loop.go still owns the
// non-identical/repeated-batch shapes and keeps its own escalation contract.
// This is the old "cross-turn repeats stay with the detector" divergence
// removed; TestCrossTurnReplayBreakerRefusesWorkerThirdIdenticalCall pins it.
//
// The soft tier additionally publishes tool_call_replay_detected, exactly when
// upstream does: a cross-turn prior success exists (not for a purely in-batch
// duplicate), with that prior call's seq. The writer is an interface so this
// package stays free of the event bus; eventbus.EventBus satisfies it.
//
// Both halves — and the deterministic doom-loop detector in doom_loop.go —
// consult one exemption table. Polls (``terminal_job_logs`` and friends) and
// synthetic control tools repeat identical calls by design; counting them
// parks a healthy 3-minute image/job poll at five lookups. Mutating tools
// stay guarded.

import "fmt"

// replayBreakerStub is the result stored for the call the breaker refused. It
// says the call did NOT execute, so the model cannot mistake it for output.
const replayBreakerStub = "[Breaker tripped: %s has been called %d times in a row with " +
	"identical arguments. This call was NOT executed. Take a different action or " +
	"produce a text-only turn to clear the streak.]"

// replaySiblingStub is the result stored for every other call of a batch whose
// breaker tripped: upstream cancels the whole batch so no dangling tool_use
// block survives and no side effect of the same response slips through.
const replaySiblingStub = "[Tool call cancelled — breaker tripped on a sibling tool call " +
	"in the same response. Re-issue with a different argument or a different approach.]"

// replaySteerPrefix is the soft-tier advisory prepended onto a stored result
// when the repetition was noticed but stayed below the breaker threshold.
const replaySteerPrefix = "[Replay detected: %s matches %d prior consecutive turn(s) + " +
	"%d duplicate(s) in this batch. Result still produced below — consider whether " +
	"the retry was necessary.]\n"

// DefaultReplayDetectorWithinLastTurns is the upstream window for the
// cross-turn half of the streak.
const DefaultReplayDetectorWithinLastTurns = 3

// defaultReplayExemptToolNames is the upstream exemption table verbatim
// (LoopConfig.replay_exempt_tools). A tool listed here is invisible to BOTH
// the replay breaker and the doom-loop fingerprint guard.
var defaultReplayExemptToolNames = []string{
	// Synthetic control / poll tools.
	"ask_user",
	"escalate",
	"collect_result",
	// Async-job polling (terminal jobs).
	"terminal_job_logs",
	"terminal_job_manage",
	"terminal_output_get",
	// Lookups / context reads.
	"search_tools",
	"search_messages",
	"get_current_time",
	"get_account_info",
}

// portReplayBreakerExemptToolNames exempts the read-only task tools from the
// PRE-DISPATCH breaker only. The Go port binds a task reminder per worker turn;
// the reminder tells the worker to consult its own list, and a worker that reads
// task_list (or one task through task_get) on each reminder is doing what the
// reminder asked. Refusing that read would break the reminder path, and a read
// cannot cause the side effect the breaker exists to stop. Task writes
// (task_create/task_update) stay guarded.
//
// They are deliberately NOT in the shared exemption table: the deterministic
// doom-loop detector still counts them, so a worker that really is stuck
// polling keeps the colony's warn/escalate/park contract. Pinned by
// TestReplayExemptionTableMirrorsUpstream,
// TestWorkerTaskReadsSurviveRepeatedReminderTurns and the colony escalation
// tests.
//
// Parity-exempt: upstream's table predates Hive's session task tools; the split
// follows its documented intent (the breaker catches repeated FAILING actions,
// idempotent reads repeat by design) without blinding the detector.
var portReplayBreakerExemptToolNames = []string{
	"task_list",
	"task_get",
}

// DefaultReplayExemptTools returns a fresh copy of the upstream exemption
// table. A fresh map per call keeps a host that mutates its config from
// mutating every other loop's table.
func DefaultReplayExemptTools() map[string]struct{} {
	tools := make(map[string]struct{}, len(defaultReplayExemptToolNames))
	for _, name := range defaultReplayExemptToolNames {
		tools[name] = struct{}{}
	}
	return tools
}

// ReplayExempt reports whether toolName is fully exempt from the replay
// breaker and from the doom-loop fingerprint guard.
//
// A nil table means "the upstream defaults": a partially-built LoopConfig must
// not silently lose the poll exemptions (the bug this file fixes). An explicit
// empty table (non-nil, len 0) disables the exemptions.
func (c LoopConfig) ReplayExempt(toolName string) bool {
	if c.ReplayExemptTools == nil {
		for _, name := range defaultReplayExemptToolNames {
			if name == toolName {
				return true
			}
		}
		return false
	}
	_, exempt := c.ReplayExemptTools[toolName]
	return exempt
}

// ReplayBreakerExempt reports whether toolName is invisible to the two replay
// breaker tiers (the pre-dispatch refusal and the soft steer). It is
// ReplayExempt plus the port's read-only task tools, which the reminder path
// asks the model to poll; those stay visible to the doom-loop detector. The
// task-read addition applies to every table, including an explicit one: a read
// cannot cause the side effect the breaker exists to stop.
func (c LoopConfig) ReplayBreakerExempt(toolName string) bool {
	if c.ReplayExempt(toolName) {
		return true
	}
	for _, name := range portReplayBreakerExemptToolNames {
		if name == toolName {
			return true
		}
	}
	return false
}

// replayDetectorOn reports whether the Phase-1 replay detector (soft steer and
// hard breaker) runs. The table above still filters the doom-loop guard when
// this is off, exactly as upstream's 6f' filter is independent of
// replay_detector_enabled.
func (l *AgentLoop) replayDetectorOn() bool {
	return l != nil && l.Config.ReplayDetectorEnabled
}

// replayWithinLastTurns resolves the cross-turn window; 0 means the upstream
// default.
func (c LoopConfig) replayWithinLastTurns() int {
	if c.ReplayDetectorWithinLastTurns > 0 {
		return c.ReplayDetectorWithinLastTurns
	}
	return DefaultReplayDetectorWithinLastTurns
}

// replayThreshold is the streak at which the breaker trips. Upstream shares
// tool_doom_loop_threshold with the turn-level fingerprint guard, so a host
// that tightened the detector tightens the breaker too.
func (l *AgentLoop) replayThreshold() int {
	return l.DoomLoop.threshold()
}

// replayDuplicateCounts counts how many times each (tool, canonical-args)
// fingerprint appears inside one model response. The count is batch-wide, not
// positional or admission-scoped: three identical calls give every one of them
// dup=3, which is why the very first call can trip a threshold-3 breaker before
// anything executes. Calls that a grace/budget gate later skips still count,
// matching upstream's Counter over the full tool_calls list.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (batch_dup_count = Counter(...))
func replayDuplicateCounts(batch []ToolCallRequest) map[string]int {
	counts := make(map[string]int, len(batch))
	for _, call := range batch {
		counts[ToolCallFingerprint(call.Name, call.Input)]++
	}
	return counts
}

// applyReplayBreaker runs the hard tier over one admitted batch, before any
// call executes. A call trips it when its prior successful streak plus its
// in-batch duplicates reaches the tool_doom_loop_threshold — the same rule
// upstream applies, with no stream-shape exemption: a third identical call
// across turns and a single response carrying N identical calls are both
// refused.
//
// On a trip the offending call and every sibling of the batch are stubbed,
// pending is cleared and the park is recorded on the admission; the caller
// still stores the stubs so the conversation keeps one tool result per
// tool_use, then parks the turn.
//
// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker
func (l *AgentLoop) applyReplayBreaker(conv *NodeConversation, batch *toolBatchAdmission) {
	if batch == nil || len(batch.pending) == 0 || !l.replayDetectorOn() {
		return
	}
	dups := replayDuplicateCounts(batch.batch)
	within := l.Config.replayWithinLastTurns()
	threshold := l.replayThreshold()
	for _, item := range batch.pending {
		if l.Config.ReplayBreakerExempt(item.call.Name) {
			continue
		}
		fingerprint := ToolCallFingerprint(item.call.Name, item.call.Input)
		dup := dups[fingerprint]
		prior := conv.CountConsecutiveCompletedToolCalls(item.call.Name, item.call.Input, within, true)
		streak := prior + dup
		if dup < threshold && streak < threshold {
			continue
		}
		l.tripReplayBreaker(batch, item, streak, prior, dup)
		return
	}
}

// tripReplayBreaker is the refusal itself: every pending call of the batch gets
// an is_error stub (the offending one names the streak), the queue is cleared
// so nothing executes, and the park rides back on the admission. The started
// flag is set for the stubs so the store step publishes their completions —
// the calls were announced as started at admission, so leaving them without a
// completion would strand the host's tool view.
func (l *AgentLoop) tripReplayBreaker(batch *toolBatchAdmission, offending pendingToolCall, streak, prior, inBatchDup int) {
	for _, item := range batch.pending {
		content := replaySiblingStub
		if item.index == offending.index {
			content = fmt.Sprintf(replayBreakerStub, item.call.Name, streak)
		}
		batch.results[item.index] = ToolResult{Content: content, IsError: true}
		if item.index < len(batch.started) {
			batch.started[item.index] = true
		}
	}
	batch.pending = nil
	batch.replayPark = doomLoopPark{
		tool:    offending.call.Name,
		streak:  streak,
		parked:  true,
		breaker: true,
		prior:   prior,
		inBatch: inBatchDup,
	}
	l.bumpBy("replay_breaker_tripped", 1)
	// Upstream reuses the turn-level doom-loop event so the UI banner is
	// identical for both guards.
	l.reportDoomLoop(offending.call.Name, streak, replayBreakerDetail(offending.call.Name, streak, prior, inBatchDup))
}

// replayBreakerDetail is the park detail for a breaker trip, worded like
// upstream's description so the event and the park name the same numbers.
func replayBreakerDetail(tool string, streak, prior, inBatch int) string {
	return fmt.Sprintf(
		"Tool breaker tripped: %s reached %d consecutive identical call(s) (prior_streak=%d, in_batch_dup=%d)",
		tool, streak, prior, inBatch)
}

// ReplayEventWriter publishes the upstream tool_call_replay_detected event.
// eventbus.EventBus satisfies it; nil disables event publication.
//
// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
type ReplayEventWriter interface {
	EmitToolCallReplayDetected(streamID, nodeID, toolName string, priorSeq int, executionID *string)
}

// applyReplaySteers is the soft tier: a repetition that stayed below the
// breaker still executes, but its stored result carries the "[Replay detected]"
// steer so the model can see the repetition. Exempt tools and a disabled
// detector are skipped, matching the breaker's gate. A cross-turn prior also
// publishes tool_call_replay_detected, with that prior call's seq — upstream
// emits only for the prior-success case, not for a purely in-batch duplicate.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (replay_prefixes_by_id and emit_tool_call_replay_detected)
func (l *AgentLoop) applyReplaySteers(conv *NodeConversation, agentCtx *AgentContext, pending []pendingToolCall, batch []ToolCallRequest, results []ToolResult) {
	if len(pending) == 0 || !l.replayDetectorOn() {
		return
	}
	dups := replayDuplicateCounts(batch)
	within := l.Config.replayWithinLastTurns()
	for _, item := range pending {
		if l.Config.ReplayBreakerExempt(item.call.Name) {
			continue
		}
		dup := dups[ToolCallFingerprint(item.call.Name, item.call.Input)]
		prior := conv.CountConsecutiveCompletedToolCalls(item.call.Name, item.call.Input, within, true)
		if prior == 0 && dup < 2 {
			continue
		}
		// A batch that tripped the breaker never reaches this point (its
		// pending list was cleared), so everything seen here executes and
		// earns the steer. Upstream only prepends it to a non-error result —
		// an error already breaks the replay chain — but still counts and
		// publishes the detection.
		prefix := fmt.Sprintf(replaySteerPrefix, item.call.Name, prior, maxInt(0, dup-1))
		if item.index < len(results) && !results[item.index].IsError {
			results[item.index].Content = prefix + results[item.index].Content
		}
		l.bumpBy("tool_call_replay_detected", 1)
		if prior > 0 {
			priorSeq := 0
			if priorCall := conv.FindCompletedToolCall(item.call.Name, item.call.Input, within); priorCall != nil {
				priorSeq = priorCall.Seq
			}
			l.emitReplayDetected(agentCtx, item.call.Name, priorSeq)
		}
	}
}

// emitReplayDetected hands one cross-turn replay to the configured writer.
// The stream/node/execution stamps follow the loop's other events; a nil
// writer keeps the loop silent, never stuck.
func (l *AgentLoop) emitReplayDetected(agentCtx *AgentContext, toolName string, priorSeq int) {
	if l == nil || l.ReplayEvents == nil || agentCtx == nil {
		return
	}
	nodeID := agentCtx.AgentID
	if nodeID == "" {
		nodeID = agentCtx.AgentSpec.ID
	}
	var executionID *string
	if agentCtx.ExecutionID != "" {
		id := agentCtx.ExecutionID
		executionID = &id
	}
	l.ReplayEvents.EmitToolCallReplayDetected(agentCtx.StreamID, nodeID, toolName, priorSeq, executionID)
	l.bumpBy("tool_call_replay_event", 1)
}
