package agentloop

// Worker text-only stall auto-fail.
//
// A worker that produces text without calling any tool is not making progress.
// After `worker_escalation_grace_turns` consecutive such turns the framework
// intervenes; the behavior diverges by worker type, exactly like upstream:
//
//   - Parallel workers (stream_id="worker:<id>", the run_worker shape)
//     synthesize a report_to_parent(status='failed') and terminate. The queen
//     reads it as a [WORKER_REPORT] and re-dispatches; NO escalation event,
//     NO synchronous wait. Per the BRD fail-fast model.
//   - Legacy primary workers (stream_id="worker", no colon) emit
//     ESCALATION_REQUESTED and park for queen guidance — the pre-BRD behavior
//     retained for flows that depend on credential/ambiguity handoffs.
//
// A turn that issues any tool call is progress (upstream resets the streak on
// real_tool_results/outputs_set), so the streak is "consecutive".
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6h worker stall)
// Parity: framework/agent_loop/internals/types.py::LoopConfig.worker_escalation_grace_turns
// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/runtimelogs"
)

const (
	// DefaultWorkerEscalationGraceTurns mirrors the upstream default: the
	// first text-only turn is fine (planning text), the second is tolerated
	// (rare-but-OK for genuinely-thinking models), the third consecutive
	// text-only turn triggers the auto-fail.
	DefaultWorkerEscalationGraceTurns = 2

	// ReportToParentToolName is the synthetic terminal tool a reporting
	// worker ends with; the stall auto-fail synthesizes a call to it.
	ReportToParentToolName = "report_to_parent"

	// stallAutoFailReason rides data["auto_fail_reason"] on the synthesized
	// report, so a stall failure is distinguishable from a model-authored
	// failed report (both reach the queen as status='failed').
	stallAutoFailReason = "stall_text_only"

	// stallEscalationReason is the upstream reason on the ESCALATION_REQUESTED
	// event the legacy primary worker path emits.
	stallEscalationReason = "Worker produced text-only turns without progress; auto-escalating"

	// stallEscalationContextLimit bounds the text excerpt on that event;
	// stallPreviewLimit bounds the "last text excerpt" in the auto-fail
	// summary. Both mirror the upstream slices.
	stallEscalationContextLimit = 2000
	stallPreviewLimit           = 1500

	stallGraceCounter            = "worker_stall_grace"
	stallAutoFailedCounter       = "worker_stall_auto_failed"
	stallEscalatedCounter        = "worker_stall_escalated"
	stallEscalationFailedCounter = "worker_stall_escalation_failed"
)

// workerStallKind classifies how (or whether) the 6h worker stall policy
// applies to the current stream.
type workerStallKind int

const (
	// workerStallNone: queen-stage or hostless stream; no stall policy.
	workerStallNone workerStallKind = iota
	// workerStallParallel: reports to a parent; auto-fail with a synthesized
	// report and terminate.
	workerStallParallel
	// workerStallLegacy: the legacy primary worker; escalate and park.
	workerStallLegacy
)

// workerStallKindFor mirrors upstream's _is_worker / _reports_to_parent gates:
// a worker is any non-queen stream without direct user I/O that has a host
// channel to act through (a report owner or an escalation bus). The overseer
// is a queen-stage node and stays out.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl
// (_is_worker / _reports_to_parent / _worker_no_tool_turn)
func (l *AgentLoop) workerStallKindFor(agentCtx *AgentContext) workerStallKind {
	if agentCtx == nil || agentCtx.SupportsDirectUserIO() || agentCtx.IsQueenStream() {
		return workerStallNone
	}
	switch agentCtx.StreamID {
	case "", "queen", "judge", "overseer":
		return workerStallNone
	}
	if strings.HasPrefix(agentCtx.StreamID, "worker:") {
		return workerStallParallel
	}
	// A host loop that terminates through a report-style terminal tool
	// (upstream's on_report) is held to the same fail-fast model; the colony
	// runtime is the only such host today.
	if agentCtx.StopAfterToolBatch != nil {
		return workerStallParallel
	}
	// The legacy primary worker is the one stream that escalates instead of
	// failing — when a bus is there to carry the question.
	if agentCtx.EscalationSink != nil && escalationStreamEligible(agentCtx.StreamID) {
		return workerStallLegacy
	}
	return workerStallNone
}

// settleTextOnlyTurn ends a turn that produced no tool calls. The worker stall
// policy runs first (upstream 6h continues before the judge, so a text-only
// worker turn never reaches it); everything else falls through to the judge
// exactly as before, or returns a plain natural stop.
//
// The turn's llm_turn_complete is published first: upstream emits it when
// _run_turn_loop returns, before the stall policy and the judge run.
//
// done=false means the inner loop continues (stall grace, or judge RETRY); a
// non-nil error ends the turn with that error.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6h then 6i)
// Parity: framework/agent_loop/agent_loop.py::execute (llm_turn_complete after _run_turn_loop)
func (l *AgentLoop) settleTextOnlyTurn(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, reminderCtx *ReminderAgentContext, messages []map[string]any, completion llm.Completion, inner int, tools []llm.ToolDef, result *TurnResult, iterStart time.Time) (done bool, err error) {
	if agentCtx.Observer != nil {
		agentCtx.Observer.LLMTurnComplete(completion, inner+1)
	}
	// A text-only model call is still a model call: log it (no tool results).
	l.logLLMTurn(agentCtx, conv, inner+1, messages, completion, nil, nil, tools)
	if kind := l.workerStallKindFor(agentCtx); kind != workerStallNone {
		stop, stallErr := l.handleWorkerTextOnlyTurn(ctx, agentCtx, conv, reminderCtx, kind, completion.Content, completion, inner, iterStart)
		if stallErr != nil {
			result.Success = false
			result.InnerTurns = inner + 1
			return true, stallErr
		}
		if stop {
			// The synthesized failed report is delivered; the worker
			// terminates cleanly (success=True), exactly like a model-issued
			// report_to_parent — its REPORT says failed.
			result.InnerTurns = inner + 1
			return true, nil
		}
		return false, nil
	}
	result.FinalText = completion.Content
	result.InnerTurns = inner + 1
	if l.shouldJudge(agentCtx, inner, true, false) {
		verdict := l.evaluateJudge(ctx, agentCtx, conv, completion.Content, result.ToolResults, inner)
		// Parity: framework/agent_loop/agent_loop.py::execute (6i judge
		// verdict branches for a text-only turn): log the step before acting.
		l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
			StepIndex:       inner,
			LLMText:         completion.Content,
			InputTokens:     completion.InputTokens,
			OutputTokens:    completion.OutputTokens,
			LatencyMS:       stepLatencyMS(iterStart),
			Verdict:         string(verdict.Action),
			VerdictFeedback: verdict.Feedback,
		})
		switch verdict.Action {
		case JudgeAccept:
			l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
				Success:    true,
				TotalSteps: inner + 1,
				ExitStatus: "success",
			})
			return true, nil
		case JudgeEscalate:
			feedback := verdict.Feedback
			if feedback == "" {
				feedback = "no feedback"
			}
			errMsg := "Judge escalated: " + feedback
			l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
				Success:    false,
				Error:      &errMsg,
				TotalSteps: inner + 1,
				ExitStatus: "escalated",
			})
			result.Success = false
			return true, fmt.Errorf("agent loop: judge escalated: %s", verdict.Feedback)
		default:
			if err := injectJudgeFeedback(conv, verdict); err != nil {
				result.Success = false
				return true, err
			}
			return false, nil
		}
	}
	return true, nil
}

// workerStallGrace is the effective grace bound. Zero (an unset field on a
// partially-built LoopConfig) or a negative value falls back to the upstream
// default, like the loop's other zero-means-default accessors; the minimum
// meaningful configured value is 1.
func (l *AgentLoop) workerStallGrace() int {
	if l.Config.WorkerEscalationGraceTurns > 0 {
		return l.Config.WorkerEscalationGraceTurns
	}
	return DefaultWorkerEscalationGraceTurns
}

// handleWorkerTextOnlyTurn applies the stall policy to one worker turn that
// produced no tool calls. stop=true means the turn-loop terminates cleanly
// (the synthesized report is already delivered); a non-nil error ends the
// turn with that error (the run was stopped while parked).
func (l *AgentLoop) handleWorkerTextOnlyTurn(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, reminderCtx *ReminderAgentContext, kind workerStallKind, text string, completion llm.Completion, inner int, iterStart time.Time) (stop bool, err error) {
	grace := l.workerStallGrace()
	l.workerTextOnlyStreak++
	streak := l.workerTextOnlyStreak
	if streak <= grace {
		// Grace: tolerate the turn and continue. The next model call carries
		// the same "[Continue working on your current task.]" nudge any
		// assistant-tailed conversation gets, exactly like upstream's
		// defensive continuation prompt.
		l.bumpBy(stallGraceCounter, 1)
		// Parity: framework/agent_loop/agent_loop.py::execute (worker stall
		// grace -> CONTINUE step).
		l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
			StepIndex:       inner,
			LLMText:         completion.Content,
			InputTokens:     completion.InputTokens,
			OutputTokens:    completion.OutputTokens,
			LatencyMS:       stepLatencyMS(iterStart),
			Verdict:         "CONTINUE",
			VerdictFeedback: fmt.Sprintf("Worker stall grace (%d/%d)", streak, grace),
		})
		return false, nil
	}

	if kind == workerStallParallel {
		// Fail fast: build the same payload the model would have built, run it
		// through the same handler, and let the worker terminate cleanly — its
		// REPORT says failed. The queen reads a [WORKER_REPORT] and
		// re-dispatches.
		input := map[string]any{
			"status":      "failed",
			"summary":     stallAutoFailSummary(streak, text),
			"data":        map[string]any{"auto_fail_reason": stallAutoFailReason},
			"tool_use_id": newAutoFailToolUseID(),
		}
		HandleReportToParent(input)
		l.deliverReportToParent(input)
		l.bumpBy(stallAutoFailedCounter, 1)
		// Parity: framework/agent_loop/agent_loop.py::execute (parallel-worker
		// auto-fail -> FAIL step).
		l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
			StepIndex:       inner,
			LLMText:         completion.Content,
			InputTokens:     completion.InputTokens,
			OutputTokens:    completion.OutputTokens,
			LatencyMS:       stepLatencyMS(iterStart),
			Verdict:         "FAIL",
			VerdictFeedback: fmt.Sprintf("Auto-failed: stall %d/%d", streak, grace),
		})
		return true, nil
	}

	// Legacy primary worker: emit ESCALATION_REQUESTED and park until the
	// queen answers. A sink error keeps the turn alive so a later text-only
	// turn can retry; the iteration budget bounds the run meanwhile.
	requestID, sinkErr := agentCtx.EscalationSink(EscalationRequest{
		StreamID:    agentCtx.StreamID,
		NodeID:      agentCtx.StreamID,
		ExecutionID: agentCtx.ExecutionID,
		Reason:      stallEscalationReason,
		Context:     truncateRunes(text, stallEscalationContextLimit),
	})
	if sinkErr != nil {
		l.bumpBy(stallEscalationFailedCounter, 1)
		return false, nil
	}
	l.bumpBy(stallEscalatedCounter, 1)
	l.markQueenInputRequested(requestID)
	if !l.settleQueenInputPark(ctx, agentCtx) {
		// Parity: framework/agent_loop/agent_loop.py::execute ("No queen
		// input received (shutdown during wait)" — a CONTINUE step).
		l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
			StepIndex:       inner,
			LLMText:         completion.Content,
			InputTokens:     completion.InputTokens,
			OutputTokens:    completion.OutputTokens,
			LatencyMS:       stepLatencyMS(iterStart),
			Verdict:         "CONTINUE",
			VerdictFeedback: "No queen input received (shutdown during wait)",
		})
		return false, ctx.Err()
	}
	// Guidance arrived: drain it into the conversation and reset the streak —
	// upstream resets when the queen's reply lands, so the next text-only turn
	// opens a fresh grace window instead of escalating again immediately.
	if _, err := l.injectQueuedInput(reminderCtx, conv); err != nil {
		return false, err
	}
	l.workerTextOnlyStreak = 0
	// Parity: framework/agent_loop/agent_loop.py::execute ("Blocked for queen
	// input (skip judge)" — a CONTINUE step after the park unblocks).
	l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
		StepIndex:       inner,
		LLMText:         completion.Content,
		InputTokens:     completion.InputTokens,
		OutputTokens:    completion.OutputTokens,
		LatencyMS:       stepLatencyMS(iterStart),
		Verdict:         "CONTINUE",
		VerdictFeedback: "Blocked for queen input (skip judge)",
	})
	return false, nil
}

// deliverReportToParent hands the synthesized report to the host exactly like
// a model-issued report_to_parent call: the loop's tool executor owns the side
// effects, so the colony runtime records it on the owning Worker and the
// worker's terminal event carries status='failed' to the queen.
func (l *AgentLoop) deliverReportToParent(input map[string]any) {
	if l == nil || l.ToolExecutor == nil {
		return
	}
	toolUseID, _ := input["tool_use_id"].(string)
	_, _ = l.ToolExecutor(ToolCallRequest{
		ID:    toolUseID,
		Name:  ReportToParentToolName,
		Input: input,
	})
}

// stallAutoFailSummary is the upstream auto-fail summary: what happened, that
// the worker stalled, and a bounded excerpt of its last words so the queen can
// judge whether a re-dispatch with different parameters is worth it.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (6h summary)
func stallAutoFailSummary(streak int, text string) string {
	summary := fmt.Sprintf(
		"Auto-failed: %d consecutive text-only turns (no tool calls, no set_output, no ask_user). "+
			"Worker stalled — re-dispatch with different parameters or take over.", streak)
	if excerpt := stallTextExcerpt(text); excerpt != "" {
		summary += " Last text excerpt: " + excerpt
	}
	return summary
}

func stallTextExcerpt(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	if len([]rune(trimmed)) > stallPreviewLimit {
		return truncateRunes(trimmed, stallPreviewLimit) + "…"
	}
	return trimmed
}

// newAutoFailToolUseID mirrors upstream's f"auto_fail_{uuid4().hex[:12]}".
func newAutoFailToolUseID() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "auto_fail_"
	}
	return "auto_fail_" + hex.EncodeToString(raw)
}
