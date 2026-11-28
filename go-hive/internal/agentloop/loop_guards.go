package agentloop

// Loop-level guards kept out of loop.go: the tool-call budget pair (the queen's
// 30 soft / 50 hard profile) and the deterministic doom-loop park. They share
// one property the turn loop relies on: neither needs a model call, so a stuck
// turn is bounded without spending tokens.

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// toolBudgetLimits resolves the turn-loop tool-call budget pair: the soft
// checkpoint interval and the absolute hard stop. An explicit ToolCallHardLimit
// wins over the multiple rule so a role profile can pin an exact cap (queen
// 30/50); ToolCallBudget 0 disables both.
func (l *AgentLoop) toolBudgetLimits() (int, int) {
	soft := l.Config.ToolCallBudget
	if soft <= 0 {
		return 0, 0
	}
	if l.Config.ToolCallHardLimit > 0 {
		return soft, l.Config.ToolCallHardLimit
	}
	return soft, soft * maxInt(1, l.Config.ToolCallHardMultiple)
}

// softBudgetCheckpointBlock returns one soft checkpoint block when the running
// tool-call count has crossed a new budget multiple below the hard stop, plus
// the new emitted count. `crossed` is capped so the soft checkpoint never
// doubles up with the hard-stop advisory: hard_multiple-1 under the multiple
// rule, or the last checkpoint strictly below an explicit hard limit.
//
// Parity: framework/agent_loop/agent_loop.py::_render_tool_budget_checkpoint
func (l *AgentLoop) softBudgetCheckpointBlock(reminderCtx *ReminderAgentContext, names []string, count, softInterval, hardLimit, emitted int) (string, int) {
	if softInterval <= 0 || hardLimit <= 0 {
		return "", emitted
	}
	crossed := count / softInterval
	maxCrossed := maxInt(1, l.Config.ToolCallHardMultiple) - 1
	if l.Config.ToolCallHardLimit > 0 {
		// An explicit hard stop changes where the last checkpoint may sit: only
		// checkpoints strictly below it are allowed, so 30/50 gives exactly one
		// (at 30) instead of the multiple rule's four.
		maxCrossed = maxInt(1, l.Config.ToolCallHardLimit/softInterval)
	}
	if crossed > maxCrossed {
		crossed = maxCrossed
	}
	if crossed <= emitted {
		return "", emitted
	}
	l.bumpBy("tool_budget_soft_reminder", 1)
	bodies := []string{renderToolBudgetCheckpoint(count, hardLimit)}
	if l.Hub != nil {
		for _, reminder := range l.Hub.Collect(TOOL_BUDGET_CHECKPOINT, reminderCtx, names, "") {
			if reminder.Body != "" {
				bodies = append(bodies, reminder.Body)
			}
		}
	}
	return WrapReminder(bodies), crossed
}

// Parity: framework/agent_loop/agent_loop.py::_render_tool_budget_checkpoint
func renderToolBudgetCheckpoint(count, hardLimit int) string {
	return fmt.Sprintf(
		"Tool-call checkpoint: you've made %d tool calls in this stretch "+
			"without yielding the turn (hard stop at %d).\n\n"+
			"This is a checkpoint, not a stop. If you're deliberately working a "+
			"long, multi-step mission and you know where you are, keep going.\n\n"+
			"But take one honest beat first. If you've been repeating similar "+
			"calls, retrying a failing approach, or have lost the thread: "+
			"step back and either (a) try a genuinely different approach, or "+
			"(b) if you've hit real difficulty, pause and escalate instead of "+
			"grinding on.",
		count, hardLimit)
}

// doomLoopPark is one batch's verdict from the deterministic guard: the call
// and streak that crossed the park threshold, when they did. breaker marks the
// replay breaker's variant (the offending call was refused BEFORE it executed,
// so the park must not claim the call ran N times). prior/inBatch keep the
// streak split for the park detail and diagnostics.
type doomLoopPark struct {
	tool    string
	streak  int
	parked  bool
	breaker bool
	prior   int
	inBatch int
	// window marks a turn-level fingerprint-window detection (A5) rather than
	// the per-call streak. Its routing differs: a worker escalates and keeps
	// working, the queen parks on the user, and any other stream only warns.
	window      bool
	description string
}

func (p doomLoopPark) detail() string {
	if p.breaker {
		return replayBreakerDetail(p.tool, p.streak, p.prior, p.inBatch)
	}
	if p.description != "" {
		return p.description
	}
	return doomLoopDescription(p.tool, p.streak)
}

// emitPark hands a loop-raised park to the configured sink (the server's
// session bus publisher). A nil sink keeps the loop silent, never stuck.
func (l *AgentLoop) emitPark(reason ParkReason, detail string) {
	if l == nil || l.ParkSink == nil {
		return
	}
	l.ParkSink(reason, detail)
}

// reportDoomLoop hands the turn's first tool doom-loop detection to the
// configured sink (the colony runtime's node_tool_doom_loop publisher). The
// latch is per turn-loop, not per streak: the event marks that this turn hit a
// doom loop, and a worker that thrashes through several streaks must not
// publish one event per streak. description is the detection's own wording, so
// a window detection publishes the batch description its park/escalation text
// already uses. A nil sink keeps the loop silent.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_tool_doom_loop)
func (l *AgentLoop) reportDoomLoop(tool string, streak int, description string) {
	if l == nil || l.DoomLoopSink == nil {
		return
	}
	l.mu.Lock()
	if l.doomLoopReported {
		l.mu.Unlock()
		return
	}
	l.doomLoopReported = true
	l.mu.Unlock()
	l.bumpBy("doom_loop_reported", 1)
	l.DoomLoopSink(tool, streak, description)
}

// announceGrace opens the grace phase with one reminder: the budget (or the
// iteration limit) is spent, so the next turns are wind-down work. The
// announcement happens once per turn-loop.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (grace reminder)
func (l *AgentLoop) announceGrace(conv *NodeConversation, budgetExhausted bool) error {
	l.graceAnnounced = true
	l.bumpBy("grace_iteration_entered", 1)
	body := graceReminderBody
	if budgetExhausted {
		l.bumpBy("tool_lifetime_budget_grace", 1)
		body = toolBudgetGraceReminderBody
	}
	block := WrapReminder([]string{body})
	_, err := conv.AddUserMessage(block, UserMessageOptions{IsSystemReminder: true})
	return err
}

// parkOnDoomLoop ends the turn on a repeated identical tool call. The tool
// results (including the advisory reminder where the streak tripped it) are
// already stored, so the transcript explains the stop; the park is published
// through ParkSink and returned to the caller as a typed error.
//
// A breaker trip (park.breaker) refused the call before it ran, so it also
// appends the upstream [SYSTEM] explanation: the model must see on resume that
// the call was not executed and that repeating it will not help.
//
// Parity: framework/agent_loop/agent_loop.py::_trip_tool_breaker (system
// message + park) for the breaker branch.
// Parity-exempt: upstream has no deterministic doom-loop park; its stall guard
// is the judge-based stall check and needs a model call.
func (l *AgentLoop) parkOnDoomLoop(conv *NodeConversation, result TurnResult, inner int, completion llm.Completion, park doomLoopPark) (TurnResult, error) {
	reason := ParkReasonDoomLoop
	result.Success = false
	result.FinalText = completion.Content
	result.InnerTurns = inner + 1
	result.ParkReason = &reason
	l.bumpBy("doom_loop_parked", 1)
	l.emitPark(reason, park.detail())
	if park.breaker && conv != nil {
		notice := fmt.Sprintf(
			"[SYSTEM] %s. You are repeating the same tool calls with identical "+
				"arguments. Try a different approach or different arguments.",
			park.detail())
		if _, err := conv.AddUserMessage(notice, UserMessageOptions{IsSystemReminder: true}); err != nil {
			return result, err
		}
	}
	return result, &DoomLoopParkedError{Tool: park.tool, Streak: park.streak, Breaker: park.breaker}
}

// settleDoomLoopWindow routes a turn-level fingerprint-window detection the way
// upstream's 6f' branch does:
//
//   - a colony worker (an escalation channel is live) is told once, the queen
//     is informed, and the worker keeps working — its terminal bound stays the
//     per-call park at five;
//   - a stream with direct user I/O (the queen) parks on the user; the warning
//     user message is already in the conversation, so the resume shows why;
//   - any other stream (event-triggered, judge, no escalation channel) only
//     gets the warning and continues.
//
// The caller stored the tool results first, so the warning lands after the
// batch instead of orphaning its tool_use blocks.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom loop
// branch order: escalate / await user / warn)
func (l *AgentLoop) settleDoomLoopWindow(conv *NodeConversation, agentCtx *AgentContext, result TurnResult, inner int, completion llm.Completion, park doomLoopPark) (*TurnResult, error) {
	warning := fmt.Sprintf(
		"[SYSTEM] %s. You are repeating the same tool calls with identical "+
			"arguments. Try a different approach or different arguments.",
		park.detail())
	if conv != nil {
		if _, err := conv.AddUserMessage(warning, UserMessageOptions{}); err != nil {
			result.Success = false
			return &result, err
		}
	}
	l.clearDoomWindow(conv)
	if doomLoopEscalationEligible(agentCtx) {
		notice := l.escalateDoomLoopDescription(agentCtx, park.tool, park.streak, park.detail())
		if notice != "" && conv != nil {
			if _, err := conv.AddUserMessage(notice, UserMessageOptions{}); err != nil {
				result.Success = false
				return &result, err
			}
		}
		return nil, nil
	}
	if agentCtx != nil && agentCtx.SupportsDirectUserIO() {
		parked, err := l.parkOnDoomLoop(conv, result, inner, completion, park)
		return &parked, err
	}
	return nil, nil
}
