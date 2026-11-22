package agentloop

// Runtime-log wiring: the loop-side half of the three-level runtime logging
// system (L3 tool_logs.jsonl steps, L2 details.jsonl node completions).
//
// Parity: framework/agent_loop/agent_loop.py (ctx.runtime_logger.log_step /
// log_node_complete call sites)
//
// Upstream logs at the iteration boundaries of the event loop: the judge
// verdict branches (ACCEPT/RETRY/ESCALATE), the skip-judge short circuits
// (RETRY with empty feedback), the cadence skip (CONTINUE "Unjudged"), the
// worker stall policy (CONTINUE grace / FAIL auto-fail), the LLM error catch
// (partial step + failed node), and compaction (a COMPACTION step). This file
// mirrors those call sites and keeps the sink best-effort: a broken log file
// never fails a turn (upstream only guarantees that for end_run; Go extends it
// to every write — recorded as a deliberate divergence).

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
)

// logRuntimeVerdict records the iteration's L3 step plus, for terminal
// verdicts, the L2 node completion (ACCEPT -> success node, ESCALATE ->
// escalated node).
func (l *AgentLoop) logRuntimeVerdict(agentCtx *AgentContext, verdict JudgeVerdict, completion llm.Completion, calls []runtimelogs.ToolCallInput, stepIndex, latencyMS int) {
	l.logRuntimeTurnStep(agentCtx, completion, calls, stepIndex, latencyMS, string(verdict.Action), verdict.Feedback)
	switch verdict.Action {
	case JudgeAccept:
		l.logRuntimeJudgeAccept(agentCtx, stepIndex+1)
	case JudgeEscalate:
		l.logRuntimeJudgeEscalate(agentCtx, verdict, stepIndex+1)
	}
}

// logRuntimeNonJudgedStep records the iteration's L3 step when the judge did
// not evaluate it: the Level 0 short circuit (skip_judge / tool-continue)
// logs a constant RETRY, the cadence skip logs CONTINUE.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py (Level 0)
// Parity: framework/agent_loop/agent_loop.py::execute (Gap C)
func (l *AgentLoop) logRuntimeNonJudgedStep(agentCtx *AgentContext, completion llm.Completion, calls []runtimelogs.ToolCallInput, stepIndex, latencyMS int) {
	if l.runtimeJudgeShortCircuits(agentCtx) {
		l.logRuntimeTurnStep(agentCtx, completion, calls, stepIndex, latencyMS, string(JudgeRetry), "")
		return
	}
	l.logRuntimeTurnStep(agentCtx, completion, calls, stepIndex, latencyMS, "CONTINUE", "Unjudged (judge_every_n_turns skip)")
}

// logRuntimeTurnStep records one L3 step from the loop's judge branches.
func (l *AgentLoop) logRuntimeTurnStep(agentCtx *AgentContext, completion llm.Completion, calls []runtimelogs.ToolCallInput, stepIndex, latencyMS int, verdict, feedback string) {
	l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
		StepIndex:       stepIndex,
		LLMText:         completion.Content,
		ToolCalls:       calls,
		InputTokens:     completion.InputTokens,
		OutputTokens:    completion.OutputTokens,
		LatencyMS:       latencyMS,
		Verdict:         verdict,
		VerdictFeedback: feedback,
	})
}

// logRuntimeJudgeAccept records the ACCEPT step's node completion.
func (l *AgentLoop) logRuntimeJudgeAccept(agentCtx *AgentContext, totalSteps int) {
	l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
		Success:    true,
		TotalSteps: totalSteps,
		ExitStatus: "success",
	})
}

// logRuntimeJudgeEscalate records the ESCALATE step's failed node, with
// upstream's "Judge escalated: <feedback|no feedback>" error text.
func (l *AgentLoop) logRuntimeJudgeEscalate(agentCtx *AgentContext, verdict JudgeVerdict, totalSteps int) {
	feedback := verdict.Feedback
	if feedback == "" {
		feedback = "no feedback"
	}
	errMsg := "Judge escalated: " + feedback
	l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
		Success:    false,
		Error:      &errMsg,
		TotalSteps: totalSteps,
		ExitStatus: "escalated",
	})
}

// logRuntimeLLMFailure records the LLM-error catch: a partial step plus a
// failed node. A cancelled turn (ctx done) is host teardown, not a model
// failure, so it stays unlogged — upstream's TurnCancelled path skips the
// error branch too.
func (l *AgentLoop) logRuntimeLLMFailure(agentCtx *AgentContext, ctx context.Context, err error, iterStart time.Time, stepIndex, attempt int) {
	if ctx != nil && ctx.Err() != nil {
		return
	}
	errMsg := "LLM call failed: " + err.Error()
	l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
		StepIndex: stepIndex,
		Error:     errMsg,
		IsPartial: true,
		LatencyMS: stepLatencyMS(iterStart),
	})
	l.logRuntimeNodeComplete(agentCtx, runtimelogs.NodeCompleteInput{
		Success:    false,
		Error:      &errMsg,
		TotalSteps: stepIndex + 1,
		ExitStatus: "failure",
		Attempt:    attempt,
	})
}

// runTimedTool executes one admitted tool call and records its wall-clock
// measurement (upstream's _timed_execute).
func (l *AgentLoop) runTimedTool(ctx context.Context, agentCtx *AgentContext, item pendingToolCall, results []ToolResult, timings []toolTiming) {
	start := time.Now()
	startedAt := runtimelogs.NowISO()
	results[item.index] = l.executeTool(ctx, agentCtx, item.call)
	if item.index < len(timings) {
		timings[item.index] = toolTiming{
			startTimestamp: startedAt,
			durationS:      roundSeconds(time.Since(start)),
		}
	}
}

// logCompactionStep records the step upstream's log_compaction writes: the
// level label, the before/after usage percentages and absolute token counts.
//
// Parity: framework/agent_loop/internals/compaction.py::log_compaction
func (l *AgentLoop) logCompactionStep(agentCtx *AgentContext, conv *NodeConversation, ratioBefore float64, maxContext int) {
	if agentCtx == nil || conv == nil {
		return
	}
	ratioAfter := conv.UsageRatio()
	level := CompactionLevel(ratioBefore, ratioAfter)
	beforePct := int(ratioBefore*100 + 0.5)
	afterPct := int(ratioAfter*100 + 0.5)
	beforeK := 0
	if maxContext > 0 {
		beforeK = int(ratioBefore*float64(maxContext)/1000 + 0.5)
	}
	afterK := int(float64(conv.EstimateTokens())/1000 + 0.5)
	l.logRuntimeStep(agentCtx, runtimelogs.StepInput{
		NodeID:    agentCtx.AgentID,
		StepIndex: -1,
		LLMText: fmt.Sprintf("Context compacted (%s): %d%% (%dK) → %d%% (%dK)",
			level, beforePct, beforeK, afterPct, afterK),
		Verdict:         "COMPACTION",
		VerdictFeedback: fmt.Sprintf("level=%s before=%d%%/%dK after=%d%%/%dK", level, beforePct, beforeK, afterPct, afterK),
	})
}

// runtimeLogState accumulates the per-node counters and totals upstream keeps
// as `_accept_count`, `_retry_count`, `_escalate_count`, `_continue_count`,
// and the running token sums. Upstream counts per node execution; the Go loop
// has no node lifetime of its own (one host session runs many turns), so the
// counters live on the AgentLoop and cover its whole session — the closest
// equivalent of the upstream node.
type runtimeLogState struct {
	startedAt     time.Time
	inputTokens   int
	outputTokens  int
	acceptCount   int
	retryCount    int
	escalateCount int
	continueCount int
}

// runtimeLoggerFor returns the sink wired through AgentContext.RuntimeLogger,
// capturing the session start on first sight. It also lazily starts the
// session-scoped counter window.
func (l *AgentLoop) runtimeLoggerFor(agentCtx *AgentContext) *runtimelogs.RuntimeLogger {
	if agentCtx == nil || agentCtx.RuntimeLogger == nil {
		return nil
	}
	logger, ok := agentCtx.RuntimeLogger.(*runtimelogs.RuntimeLogger)
	if !ok || logger == nil {
		return nil
	}
	l.stateMu.Lock()
	if l.runtimeLogState.startedAt.IsZero() {
		l.runtimeLogState.startedAt = time.Now()
	}
	l.stateMu.Unlock()
	return logger
}

// runtimeLogNodeID is the node id upstream logs: the graph node's id ("queen"
// for the queen stream). The Go port keeps the agent id as that spelling and
// falls back to the stream id for hosts that only set one.
func runtimeLogNodeID(agentCtx *AgentContext) string {
	if agentCtx == nil {
		return ""
	}
	if agentCtx.AgentID != "" {
		return agentCtx.AgentID
	}
	return agentCtx.StreamID
}

// logRuntimeStep records one L3 step and folds its verdict into the node
// counters. Best-effort: write failures are swallowed so logging can never
// fail a turn.
func (l *AgentLoop) logRuntimeStep(agentCtx *AgentContext, in runtimelogs.StepInput) {
	logger := l.runtimeLoggerFor(agentCtx)
	if logger == nil {
		return
	}
	if in.NodeID == "" {
		in.NodeID = runtimeLogNodeID(agentCtx)
	}
	if in.NodeType == "" {
		in.NodeType = "event_loop"
	}
	_ = logger.LogStep(in)

	l.stateMu.Lock()
	l.runtimeLogState.inputTokens += in.InputTokens
	l.runtimeLogState.outputTokens += in.OutputTokens
	switch in.Verdict {
	case "ACCEPT":
		l.runtimeLogState.acceptCount++
	case "RETRY":
		l.runtimeLogState.retryCount++
	case "ESCALATE":
		l.runtimeLogState.escalateCount++
	case "CONTINUE":
		l.runtimeLogState.continueCount++
	}
	l.stateMu.Unlock()
}

// logRuntimeNodeComplete records one L2 node completion with the upstream
// attention flags and the session-scoped counters.
func (l *AgentLoop) logRuntimeNodeComplete(agentCtx *AgentContext, in runtimelogs.NodeCompleteInput) {
	logger := l.runtimeLoggerFor(agentCtx)
	if logger == nil {
		return
	}
	if in.NodeID == "" {
		in.NodeID = runtimeLogNodeID(agentCtx)
	}
	if in.NodeType == "" {
		in.NodeType = "event_loop"
	}
	if in.NodeName == "" {
		in.NodeName = agentCtx.AgentSpec.Name
	}
	l.stateMu.Lock()
	state := l.runtimeLogState
	l.stateMu.Unlock()
	if in.TotalSteps == 0 {
		in.TotalSteps = 0
	}
	in.InputTokens = state.inputTokens
	in.OutputTokens = state.outputTokens
	if in.TokensUsed == 0 {
		in.TokensUsed = state.inputTokens + state.outputTokens
	}
	if in.Attempt == 0 {
		in.Attempt = 1
	}
	if !state.startedAt.IsZero() {
		in.LatencyMS = int(time.Since(state.startedAt).Milliseconds())
	}
	in.AcceptCount = state.acceptCount
	in.RetryCount = state.retryCount
	in.EscalateCount = state.escalateCount
	in.ContinueCount = state.continueCount
	_ = logger.LogNodeComplete(in)
}

// runtimeJudgeShortCircuits reports whether the judge would short-circuit
// without an evaluation (upstream judge_pipeline Level 0): a skip_judge spec,
// a spec without success criteria, or no judge evaluator at all. For those the
// verdict is a constant RETRY with no feedback.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn (Level 0)
func (l *AgentLoop) runtimeJudgeShortCircuits(agentCtx *AgentContext) bool {
	if agentCtx == nil {
		return true
	}
	if l == nil || l.Judge == nil {
		return true
	}
	return agentCtx.AgentSpec.SkipJudge || !hasSuccessCriteria(agentCtx.AgentSpec)
}

// runtimeLogToolCalls converts the batch's stored records into the upstream
// log entry shape. Synthetic framework tools (ask_user, escalate) are excluded
// exactly as upstream excludes them from logged_tool_calls; timing comes from
// the execution measurement and stays zero for calls that never ran (grace
// skips, budget deferrals) — upstream's entries for those carry no timing keys
// either, and ToolCallLog fills the defaults.
func runtimeLogToolCalls(records []ToolCallRecord, timings []toolTiming) []runtimelogs.ToolCallInput {
	out := make([]runtimelogs.ToolCallInput, 0, len(records))
	for i, rec := range records {
		if rec.ToolName == AskUserToolName || rec.ToolName == EscalateToolName {
			continue
		}
		entry := runtimelogs.ToolCallInput{
			ToolUseID: rec.ToolUseID,
			ToolName:  rec.ToolName,
			ToolInput: rec.ToolInput,
			Content:   rec.Content,
			IsError:   rec.IsError,
		}
		if i < len(timings) {
			entry.StartTimestamp = timings[i].startTimestamp
			entry.DurationS = timings[i].durationS
		}
		out = append(out, entry)
	}
	return out
}

// toolTiming is the per-call wall-clock measurement upstream stores as
// start_timestamp ("ISO 8601 timestamp when tool execution started") and
// duration_s ("round(wall-clock, 3)").
type toolTiming struct {
	startTimestamp string
	durationS      float64
}

// roundSeconds rounds a duration to the millisecond upstream's
// round(time.time() - start, 3) keeps.
func roundSeconds(d time.Duration) float64 {
	return math.Round(d.Seconds()*1000) / 1000
}

// stepLatencyMS is the upstream iter_latency_ms computation:
// int((time.time() - iter_start) * 1000).
func stepLatencyMS(iterStart time.Time) int {
	return int(time.Since(iterStart).Milliseconds())
}
