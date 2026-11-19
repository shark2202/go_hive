package agentloop

// Loop-level guards kept out of loop.go: the tool-call budget pair (the queen's
// 30 soft / 50 hard profile) and the deterministic doom-loop park. They share
// one property the turn loop relies on: neither needs a model call, so a stuck
// turn is bounded without spending tokens.

import (
	"fmt"

	"github.com/aden-hive/hive-go/internal/llm"
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
// and streak that crossed the park threshold, when they did.
type doomLoopPark struct {
	tool   string
	streak int
	parked bool
}

func (p doomLoopPark) detail() string {
	return fmt.Sprintf("`%s` called %d times in a row with identical arguments and the same result", p.tool, p.streak)
}

// emitPark hands a loop-raised park to the configured sink (the server's
// session bus publisher). A nil sink keeps the loop silent, never stuck.
func (l *AgentLoop) emitPark(reason ParkReason, detail string) {
	if l == nil || l.ParkSink == nil {
		return
	}
	l.ParkSink(reason, detail)
}

// parkOnDoomLoop ends the turn on a repeated identical tool call. The tool
// results (including the advisory reminder where the streak tripped it) are
// already stored, so the transcript explains the stop; the park is published
// through ParkSink and returned to the caller as a typed error.
//
// Parity-exempt: upstream has no deterministic doom-loop park; its stall guard
// is the judge-based stall check and needs a model call.
func (l *AgentLoop) parkOnDoomLoop(result TurnResult, inner int, completion llm.Completion, park doomLoopPark) (TurnResult, error) {
	reason := ParkReasonDoomLoop
	result.Success = false
	result.FinalText = completion.Content
	result.InnerTurns = inner + 1
	result.ParkReason = &reason
	l.bumpBy("doom_loop_parked", 1)
	l.emitPark(reason, park.detail())
	return result, &DoomLoopParkedError{Tool: park.tool, Streak: park.streak}
}
