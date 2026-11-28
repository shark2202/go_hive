package agentloop

// Guards for turns that carry no user-facing text.
//
// Two upstream guards live here, both keyed on the same signal (a model call
// that produced no assistant text):
//
//  1. The empty-response guard: a ghost stream — no text, no tool calls, no
//     output — must never be mistaken for a finished turn. Upstream first
//     accepts the turn when the node declares output_keys and every required
//     key is already set (its OutputAccumulator is complete); `missing` is
//     trivially empty when no keys are declared, so the accepted branch also
//     requires real output_keys — a forever-alive node (the queen) is never
//     ended by a ghost stream. Otherwise upstream nudges the model once per
//     consecutive empty turn and parks the stream on the EMPTY_RESPONSES
//     reason at the stall-detection threshold. The port reconstructs the
//     accumulator from the conversation's successful `set_output` calls; every
//     non-accepted empty turn keeps upstream's nudge-then-park behavior. The
//     park ends the turn with *EmptyResponseParkedError, and the host's
//     existing park path (TurnResult.ParkReason + ParkSink) keeps
//     EMPTY_RESPONSES a live reason instead of a constant with no producer.
//  2. The silent-tool nudge: consecutive turns that call tools but say nothing
//     to the user read as a runaway process. Every SilentToolStreakThreshold
//     consecutive silent turns the loop injects a user message asking the
//     model to narrate its next batch.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e''' empty
// response guard; 6f' silent worker detection)
// Parity: framework/agent_loop/internals/types.py::LoopConfig.silent_tool_streak_threshold

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	// DefaultSilentToolStreakThreshold mirrors the upstream default: five
	// consecutive tool-only turns, then a nudge; every further multiple of
	// five nudges again.
	DefaultSilentToolStreakThreshold = 5

	// emptyResponseNudge is injected after an empty turn that did not yet
	// reach the park threshold. Upstream's no-output branch text verbatim.
	emptyResponseNudge = "[System: Your response was empty. Review the conversation and respond to the user or take action with your tools.]"

	// silentToolNudge is injected every SilentToolStreakThreshold consecutive
	// silent turns. The %d is the running streak. Upstream's text verbatim.
	silentToolNudge = "[SYSTEM] You have been calling tools for %d consecutive turns without any text output. Continue working, but include a brief explanation alongside your next tool calls so the user can see what you are doing."

	emptyResponseCounter     = "empty_response"
	emptyResponseParkCounter = "empty_response_parked"
	silentToolNudgedCounter  = "silent_tool_nudged"
)

// EmptyResponseParkedError ends a turn whose model call produced nothing at all
// for the configured number of consecutive turns. It is the port's counterpart
// of upstream's `_await_user_input(reason=ParkReason.EMPTY_RESPONSES)`: the
// host parks the session, and the next user message starts a fresh turn.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' ghost
// stream park)
type EmptyResponseParkedError struct {
	Consecutive int
	Threshold   int
}

func (e *EmptyResponseParkedError) Error() string {
	if e == nil {
		return "agent loop: parked on consecutive empty responses"
	}
	return fmt.Sprintf(
		"agent loop: parked on empty responses — %d consecutive model calls returned no text, no tool calls and no output (threshold %d)",
		e.Consecutive, e.Threshold)
}

// ParkReason names the park this error produced.
func (e *EmptyResponseParkedError) ParkReason() ParkReason { return ParkReasonEmptyResponses }

// silentToolStreakThreshold resolves the nudge cadence, with zero falling back
// to the upstream default like the loop's other zero-means-default accessors.
func (l *AgentLoop) silentToolStreakThreshold() int {
	if l != nil && l.Config.SilentToolStreakThreshold > 0 {
		return l.Config.SilentToolStreakThreshold
	}
	return DefaultSilentToolStreakThreshold
}

// settleEmptyTurn applies the empty-response guard to one turn that produced no
// text and no tool calls. A node that declares output keys accepts immediately
// when every required key is already set — upstream's "empty response but all
// outputs set" branch — returning success with no nudge and no park. Missing
// keys (or no declared keys at all) keep the existing guard: below the
// threshold it nudges the model and reports done=false so the inner loop
// streams again; at the threshold it parks the turn: the result carries
// ParkReasonEmptyResponses, the park is published through ParkSink, and the
// typed error ends the turn the way a doom-loop park does.
//
// done=false means the loop continues; a non-nil error ends the turn.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e”' empty
// response guard: all-outputs-set accept, then missing-outputs nudge/park)
func (l *AgentLoop) settleEmptyTurn(agentCtx *AgentContext, conv *NodeConversation, completion llm.Completion, inner int, tools []llm.ToolDef, messages []map[string]any, result *TurnResult) (done bool, err error) {
	// The model call still happened: publish it exactly like any text-only
	// turn, before the guard decides what the turn means.
	if agentCtx.Observer != nil {
		agentCtx.Observer.LLMTurnComplete(completion, inner+1)
	}
	l.logLLMTurn(agentCtx, conv, inner+1, messages, completion, nil, nil, tools)

	// Upstream accepts an empty turn outright when the node declares
	// output_keys and the accumulator already satisfies all of them, e.g.
	// after set_output landed on a previous turn. The declared-keys check is
	// load-bearing: with no output_keys `missing` is trivially empty, and
	// accepting there would end a forever-alive node (the queen) on a ghost
	// stream instead of nudging it.
	//
	// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6e''' —
	// `not missing and has_real_outputs` → accept)
	missing := missingOutputKeys(
		conv.OutputAccumulator(agentCtx.AgentSpec.OutputKeys),
		agentCtx.AgentSpec.OutputKeys,
		agentCtx.AgentSpec.NullableOutputKeys,
	)
	if len(agentCtx.AgentSpec.OutputKeys) > 0 && len(missing) == 0 {
		result.Success = true
		result.FinalText = ""
		result.InnerTurns = inner + 1
		return true, nil
	}

	l.consecutiveEmptyTurns++
	streak := l.consecutiveEmptyTurns
	threshold := l.stallDetectionThreshold()
	l.bumpBy(emptyResponseCounter, 1)

	if streak >= threshold {
		reason := ParkReasonEmptyResponses
		result.Success = false
		result.FinalText = ""
		result.InnerTurns = inner + 1
		result.ParkReason = &reason
		l.bumpBy(emptyResponseParkCounter, 1)
		l.consecutiveEmptyTurns = 0
		l.emitPark(reason, fmt.Sprintf(
			"%d consecutive empty responses", streak))
		return true, &EmptyResponseParkedError{Consecutive: streak, Threshold: threshold}
	}

	if _, err := conv.AddUserMessage(emptyResponseNudge, UserMessageOptions{}); err != nil {
		return true, err
	}
	return false, nil
}

// observeSilentToolBatch advances the silent-tool streak after a tool batch. A
// batch that ran at least one call with no assistant text is a silent turn; any
// assistant text resets the streak. Every multiple of the threshold injects the
// nudge as a plain user message, exactly where upstream 6f' does (after the
// doom-loop guard, before the judge).
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6f' silent
// worker detection)
func (l *AgentLoop) observeSilentToolBatch(conv *NodeConversation, assistantText string, toolCallsRan int) error {
	if toolCallsRan > 0 && assistantText == "" {
		l.silentToolStreak++
		if l.silentToolStreak%l.silentToolStreakThreshold() != 0 {
			return nil
		}
		l.bumpBy(silentToolNudgedCounter, 1)
		_, err := conv.AddUserMessage(fmt.Sprintf(silentToolNudge, l.silentToolStreak), UserMessageOptions{})
		return err
	}
	l.silentToolStreak = 0
	return nil
}
