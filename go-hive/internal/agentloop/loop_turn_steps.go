package agentloop

// Turn-step helpers split out of ExecuteTurn: admitting a tool batch and
// applying the post-batch guards that can end the turn. Both are pure
// extractions from the turn driver -- same order, same returns, same events.

import (
	"context"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// toolBatchAdmission is one batch's admission verdict: the results array
// indexed by call position, the calls cleared to execute this iteration, and
// the bookkeeping the skipped/deferred paths need to explain themselves.
type toolBatchAdmission struct {
	results          []ToolResult
	pending          []pendingToolCall
	batch            []ToolCallRequest
	started          []bool
	colonyPark       colonyDispatchOutcome
	toolCallCount    int
	limitHit         bool
	lifetimeLimitHit bool
	graceSkippedIdx  []int
	deferredIdx      []int
	coercedInputs    []map[string]any
	// replayPark carries the replay breaker's verdict when it refused the
	// batch: the turn parks as DOOM_LOOP after the stubbed results are stored.
	// Zero when the batch passed the breaker.
	replayPark doomLoopPark
}

// admitToolCalls walks the model's batch in call order and decides which
// calls execute this iteration. A grace iteration admits only the terminal
// trio; the per-turn hard stop and the lifetime budget defer everything past
// their ceilings to a neutral result (applyBatchSkips explains them to the
// model). It never runs a tool.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (grace-iteration
// filter, tool-call limit gate, schema-driven argument coercion)
func (l *AgentLoop) admitToolCalls(agentCtx *AgentContext, toolCalls []llm.ToolCall, tools []llm.ToolDef, inGrace bool, lifetimeBudget, hardLimit int) toolBatchAdmission {
	results := make([]ToolResult, len(toolCalls))

	toolCallCount := 0
	limitHit := false
	lifetimeLimitHit := false
	var graceSkippedIdx []int
	var deferredIdx []int
	pending := make([]pendingToolCall, 0, len(toolCalls))
	started := make([]bool, len(toolCalls))
	// batch is every call in the model response with its coerced input, in
	// call order. The replay detectors count duplicates across this whole
	// batch, not just the admitted prefix: upstream's batch_dup_count runs over
	// tool_calls before the grace/budget gates skip any of them.
	batch := make([]ToolCallRequest, len(toolCalls))
	// colonyPark is this batch's framework-level colony dispatch verdict;
	// a successful call ends the turn after the batch, as upstream's
	// user_input_requested does (see dispatchColonyCall).
	var colonyPark colonyDispatchOutcome

	// Schema-driven coercion of the model's arguments before dispatch —
	// the same point upstream heals them (see tool_coercion_wire.go).
	coercedInputs := coerceToolCallInputs(tools, toolCalls)

	for i, tc := range toolCalls {
		call := ToolCallRequest{ID: tc.ID, Name: tc.Name, Input: coercedInputs[i]}
		batch[i] = call
		if inGrace {
			if _, terminal := graceTerminalTools[tc.Name]; !terminal {
				graceSkippedIdx = append(graceSkippedIdx, i)
				continue
			}
		}
		toolCallCount++
		if hardLimit > 0 && toolCallCount > hardLimit {
			limitHit = true
			deferredIdx = append(deferredIdx, i)
			continue
		}
		l.stateMu.Lock()
		used := l.toolCallsUsed
		l.stateMu.Unlock()
		if lifetimeBudget > 0 && !inGrace && used >= lifetimeBudget {
			limitHit = true
			lifetimeLimitHit = true
			deferredIdx = append(deferredIdx, i)
			continue
		}
		l.stateMu.Lock()
		l.toolCallsUsed++
		l.stateMu.Unlock()

		// Parity: framework/agent_loop/agent_loop.py::_publish_tool_started.
		// Started events fire in call order even when the batch below runs
		// concurrently; completed events follow the same order.
		if agentCtx.Observer != nil {
			agentCtx.Observer.ToolCallStarted(call)
		}
		// Framework-level colony dispatch: suggest_colony and
		// task_create(new_colony=true) never reach the executor.
		if park, handled := l.dispatchColonyCall(agentCtx, call, i, results, started, colonyPark); handled {
			colonyPark = park
			continue
		}
		pending = append(pending, pendingToolCall{index: i, call: call})
	}
	return toolBatchAdmission{
		results:          results,
		pending:          pending,
		batch:            batch,
		started:          started,
		colonyPark:       colonyPark,
		toolCallCount:    toolCallCount,
		limitHit:         limitHit,
		lifetimeLimitHit: lifetimeLimitHit,
		graceSkippedIdx:  graceSkippedIdx,
		deferredIdx:      deferredIdx,
		coercedInputs:    coercedInputs,
	}
}

// finishToolBatchTurn applies the post-batch guards that can end the turn:
// the doom-loop park, the colony-dispatch park, batchStopsTurn, the queen
// guidance wait, the silent-tool nudge and the judge step. A non-nil result
// is the turn's final value; nil means the loop continues with another model
// call.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (post-batch
// park, wait, silent-worker and judge steps)
func (l *AgentLoop) finishToolBatchTurn(
	ctx context.Context,
	agentCtx *AgentContext,
	conv *NodeConversation,
	reminderCtx *ReminderAgentContext,
	completion llm.Completion,
	records []ToolCallRecord,
	timings []toolTiming,
	toolFailure bool,
	doomPark doomLoopPark,
	colonyPark colonyDispatchOutcome,
	pending []pendingToolCall,
	results []ToolResult,
	iterStart time.Time,
	inner int,
	result *TurnResult,
) (*TurnResult, error) {
	// A deterministic doom loop stops the turn here: the model has re-issued
	// the same call past the park threshold, so the park replaces another
	// advisory-only reminder. A turn-level window detection routes by stream
	// shape instead (worker escalates, queen parks, others warn).
	if doomPark.parked {
		if doomPark.window {
			settled, settleErr := l.settleDoomLoopWindow(conv, agentCtx, *result, inner, completion, doomPark)
			if settled != nil || settleErr != nil {
				return settled, settleErr
			}
			// A worker/other-stream window only warns; fall through so the
			// ordinary post-batch guards (stop-after-batch, ask_user) still run.
		} else {
			parked, err := l.parkOnDoomLoop(conv, *result, inner, completion, doomPark)
			return &parked, err
		}
	}

	// A successful framework-level colony dispatch parks the turn on the
	// user exactly like ask_user: the popup is open, and the next user
	// message (or the accept/dismiss route) drives the following turn.
	if parkTurnOnColonyDispatch(result, colonyPark, inner) {
		return result, nil
	}

	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop —
	// the batch can end the turn before another model call
	// (batchStopsTurn): a successful ask_user hands the park back to
	// the host as upstream's user_input_requested does, and a worker's
	// explicit report terminates the worker.
	if batchStopsTurn(agentCtx, pending, results) {
		result.InnerTurns = inner + 1
		return result, nil
	}

	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6h''
	// worker wait for queen guidance): a batch that escalated parks until
	// the queen injects a reply; the reply drives the next turn.
	if !l.settleQueenInputPark(ctx, agentCtx) {
		result.Success = false
		return result, ctx.Err()
	}

	// 6f'. Silent-tool nudge: consecutive turns that called tools but said
	// nothing to the user ask the model to narrate its next batch. It runs
	// after the doom-loop guard and before the judge, upstream's order.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6f'
	// silent worker detection)
	if err := l.observeSilentToolBatch(conv, completion.Content, len(pending)); err != nil {
		return &TurnResult{Success: false, InnerTurns: inner + 1, ToolResults: result.ToolResults}, err
	}

	loggedCalls := runtimeLogToolCalls(records, timings)
	iterLatencyMS := stepLatencyMS(iterStart)
	if stop, judgeErr := l.runJudgeStep(ctx, agentCtx, conv, completion, records, inner, toolFailure, loggedCalls, iterLatencyMS, result); stop {
		return result, judgeErr
	}

	if _, err := l.injectQueuedInput(reminderCtx, conv); err != nil {
		return &TurnResult{Success: false, InnerTurns: inner + 1, ToolResults: result.ToolResults}, err
	}
	return nil, nil
}

// prepareTurnTools renders the loop-owned tool surface for a turn: a caller's
// nil list becomes the agent's advertised tools (copied, never aliased), the
// search-tools loader joins when the agent carries one, and a configured
// background tool advertises collect_result so its handle stays collectable.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.execute
func (l *AgentLoop) prepareTurnTools(agentCtx *AgentContext, tools []llm.ToolDef) []llm.ToolDef {
	if tools == nil {
		tools = append([]llm.ToolDef(nil), agentCtx.AvailableTools...)
	}
	tools = l.ensureSearchToolsTool(agentCtx, tools)
	return l.ensureCollectResultTool(tools)
}
