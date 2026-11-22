package server

// Implicit judge verdict producer for the chat queen.
//
// Upstream's queen loop evaluates every iteration at step 6i, and the verdict
// for turn N is published after that turn's park unblocks: the message that
// ends the wait arrives, the park returns, and the loop evaluates before the
// next iteration starts. On the wire that puts judge_verdict right after
// client_input_received / EXECUTING and before the new message's
// client_input_committed boundary. The queen's AgentSpec sets skip_judge=True
// (queen_orchestrator.py), so the evaluation short-circuits to a constant
// RETRY with empty feedback — but the event is still published with
// judge_type "implicit".
//
// This port runs one bounded loop per turn, so the equivalent emission point
// is the start of the next turn: the runtime remembers the turn that just
// parked and publishes its verdict before committing the new message. A
// session whose spec carries success criteria runs the loop's LLM judge
// instead, which publishes its own "custom" verdicts per evaluation; the
// implicit verdict would duplicate those and is skipped there.
//
// Parity: framework/agent_loop/agent_loop.py::execute (step 6i)
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn (Level 0 skip_judge)
// Parity: framework/server/queen_orchestrator.py::_queen_loop (skip_judge=True spec)

import "github.com/shark2202/go_hive/go-hive/internal/agentloop"

// noteImplicitJudgeVerdictDue records that a loop turn finished and its
// implicit verdict is owed at the next turn start. executionID is the finished
// turn's execution id, which the verdict carries upstream (the queen loop's
// execution id is session-scoped, so its verdicts keep naming the session's
// own execution). The iteration is the session turn index: the opening turn
// is 0 and every later turn adds one, matching the permanent loop's
// iteration numbering for one-model-call turns.
func (r *ChatRuntime) noteImplicitJudgeVerdictDue(executionID string) {
	if r == nil {
		return
	}
	r.verdictMu.Lock()
	r.verdictPending = true
	r.verdictIteration = r.turnIndex
	r.verdictExecutionID = executionID
	r.turnIndex++
	r.verdictMu.Unlock()
}

// emitPendingImplicitJudgeVerdict publishes the verdict the previous turn owes
// and clears the ledger. It is a no-op when no turn has finished yet, when the
// runtime has no bus, or when a criteria judge is armed (that judge publishes
// its own verdicts from the loop).
func (r *ChatRuntime) emitPendingImplicitJudgeVerdict() {
	if r == nil || r.bus == nil {
		return
	}
	r.verdictMu.Lock()
	due := r.verdictPending
	iteration := r.verdictIteration
	executionID := r.verdictExecutionID
	r.verdictPending = false
	r.verdictMu.Unlock()
	if !due {
		return
	}
	if r.agentCtx != nil && agentloop.HasSuccessCriteria(r.agentCtx.AgentSpec) {
		return
	}
	var execution *string
	if executionID != "" {
		id := executionID
		execution = &id
	}
	r.bus.EmitJudgeVerdict(chatStreamID, chatNodeID,
		string(agentloop.JudgeRetry), "", "implicit", iteration, execution)
}
