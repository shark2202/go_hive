package agentloop

// Judge evaluation shared by the loop hook and its llmjudge adapter.
//
// This is the generic turn-quality seam inherited from the graph-era agent
// design. Callers opt in explicitly through ConfigureJudge; the Hive 1.0.15
// queen and colony production paths do not (queen skip_judge=True, colony
// worker/overseer loops built with judge=None), so a colony natural stop is
// accepted without an extra model call or a [Judge feedback] injection.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// JudgeAction is the verdict an evaluator returns for a turn.
//
// Parity: framework/agent_loop/internals/types.py::JudgeVerdict
type JudgeAction string

const (
	// JudgeAccept ends the turn successfully.
	JudgeAccept JudgeAction = "ACCEPT"
	// JudgeRetry asks the model to keep working; feedback is injected as a
	// user message and the loop runs another inner turn.
	JudgeRetry JudgeAction = "RETRY"
	// JudgeEscalate stops the turn as a failure and publishes a verdict event.
	JudgeEscalate JudgeAction = "ESCALATE"
)

// JudgeVerdict is the result of evaluating a turn.
//
// Parity: framework/agent_loop/internals/types.py::JudgeVerdict
type JudgeVerdict struct {
	Action   JudgeAction
	Feedback string
	// Silent marks a RETRY that came from the "tool calls mean keep working"
	// short-circuit: upstream's feedback=None. The loop continues without
	// injecting a [Judge feedback] message, and normalizeJudgeVerdict must not
	// invent one.
	Silent bool
	// Implicit marks a verdict produced by the local output-key gate instead of
	// a configured judge; the published judge_type is "implicit".
	Implicit bool
}

// JudgeRequest carries the evidence a judge evaluates. Criteria is always the
// non-empty AgentSpec.SuccessCriteria string; the loop never calls a judge
// without criteria.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
type JudgeRequest struct {
	AgentSpec           AgentSpec
	Criteria            string
	AssistantText       string
	ToolResults         []ToolCallRecord
	ConversationSummary string
	Iteration           int
	// OutputAccumulator, OutputKeys and MissingOutputKeys mirror the context
	// upstream hands a custom judge (judge_pipeline.judge_turn Level 1).
	OutputAccumulator map[string]any
	OutputKeys        []string
	NullableKeys      []string
	MissingOutputKeys []string
}

// Judge evaluates whether a turn satisfies the agent's success criteria.
//
// Parity: framework/agent_loop/internals/types.py::JudgeProtocol
type Judge interface {
	Evaluate(ctx context.Context, request JudgeRequest) (JudgeVerdict, error)
}

// JudgeEventWriter receives one verdict event per evaluation. The eventbus
// EventBus satisfies it; nil disables event publication.
//
// Parity: framework/agent_loop/internals/event_publishing.py::publish_judge_verdict
type JudgeEventWriter interface {
	EmitJudgeVerdict(streamID, nodeID, action, feedback, judgeType string, iteration int, executionID *string)
}

// successCriteria returns the configured criteria verbatim.
func successCriteria(spec AgentSpec) string {
	if spec.SuccessCriteria == nil {
		return ""
	}
	return *spec.SuccessCriteria
}

func hasSuccessCriteria(spec AgentSpec) bool {
	return strings.TrimSpace(successCriteria(spec)) != ""
}

// HasSuccessCriteria reports whether the spec declares non-blank success
// criteria. Callers use it to decide whether a judge is worth constructing:
// no criteria means no evaluation and no extra LLM call.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
func HasSuccessCriteria(spec AgentSpec) bool {
	return hasSuccessCriteria(spec)
}

// ConfigureJudge wires the default LLM judge for a spec that declares success
// criteria. It is the explicit-caller entry point for the generic judgment
// seam: a spec with no criteria, skip_judge set, or a nil completer leaves the
// loop's judge disabled, so the turn costs no extra LLM call. An already
// configured judge is left untouched.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn (Level 2
// conversation judge) and framework/testing/llm_judge.py::LLMJudge.evaluate
func (l *AgentLoop) ConfigureJudge(spec AgentSpec, completer llm.Completer, events JudgeEventWriter) bool {
	if l == nil || completer == nil || spec.SkipJudge || !hasSuccessCriteria(spec) {
		return false
	}
	if l.Judge == nil {
		l.Judge = NewLLMJudgeAdapterFromCompleter(completer)
	}
	if l.JudgeEvents == nil {
		l.JudgeEvents = events
	}
	return true
}

// shouldJudge reports whether this iteration owes a judge evaluation.
// Natural stops always judge; tool failures judge immediately; otherwise the
// configured JudgeEveryNTurns cadence applies. A nil Judge disables the LLM
// quality gate, but a spec that declares output keys still owes the local
// implicit gate (upstream judge_pipeline Level 2 runs with no custom judge).
// SkipJudge and missing criteria with no output keys disable evaluation.
func (l *AgentLoop) shouldJudge(agentCtx *AgentContext, iteration int, naturalStop, toolFailure bool) bool {
	if l == nil || agentCtx == nil {
		return false
	}
	if agentCtx.AgentSpec.SkipJudge {
		return false
	}
	hasOutputKeys := len(agentCtx.AgentSpec.OutputKeys) > 0
	hasCriteria := hasSuccessCriteria(agentCtx.AgentSpec)
	if !hasOutputKeys && (!hasCriteria || l.Judge == nil) {
		return false
	}
	if naturalStop || toolFailure {
		return true
	}
	every := l.Config.JudgeEveryNTurns
	if every <= 0 {
		return false
	}
	return (iteration+1)%every == 0
}

// evaluateJudge calls the configured judge and applies the _judge_turn
// retry/fallback contract: a transient evaluator failure is retried, an
// exhausted transient budget accepts the turn with the upstream
// unavailability feedback, and any other failure is returned so the caller
// ends the turn, mirroring the upstream re-raise.
//
// Before any configured judge runs, a spec that declares output keys passes
// through upstream's implicit Level 2 gate: a turn that produced tool calls
// keeps working silently, a natural stop with missing keys gets the
// "Task incomplete" RETRY feedback, and a fully-nullable spec with nothing set
// gets the set_output hint. The gate is local — it never spends an LLM call.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
// Parity: framework/agent_loop/agent_loop.py::_judge_turn
func (l *AgentLoop) evaluateJudge(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, completion string, records []ToolCallRecord, iteration int) (JudgeVerdict, error) {
	accumulator := conv.OutputAccumulator(agentCtx.AgentSpec.OutputKeys)
	missing := missingOutputKeys(accumulator, agentCtx.AgentSpec.OutputKeys, agentCtx.AgentSpec.NullableOutputKeys)
	if verdict, ok := implicitOutputKeyVerdict(agentCtx.AgentSpec, accumulator, missing, records, l.Judge); ok {
		l.bumpBy("judge_"+strings.ToLower(string(verdict.Action)), 1)
		l.emitJudgeVerdict(agentCtx, verdict, iteration)
		if l.OnJudgeVerdict != nil {
			l.OnJudgeVerdict(verdict, iteration)
		}
		return verdict, nil
	}
	request := JudgeRequest{
		AgentSpec:           agentCtx.AgentSpec,
		Criteria:            successCriteria(agentCtx.AgentSpec),
		AssistantText:       completion,
		ToolResults:         append([]ToolCallRecord(nil), records...),
		ConversationSummary: conv.ExportSummary(),
		Iteration:           iteration,
		OutputAccumulator:   accumulator,
		OutputKeys:          append([]string(nil), agentCtx.AgentSpec.OutputKeys...),
		NullableKeys:        append([]string(nil), agentCtx.AgentSpec.NullableOutputKeys...),
		MissingOutputKeys:   append([]string(nil), missing...),
	}
	verdict, err := l.evaluateJudgeWithRetry(ctx, request)
	if err != nil {
		return JudgeVerdict{}, err
	}
	l.bumpBy("judge_"+strings.ToLower(string(verdict.Action)), 1)
	l.emitJudgeVerdict(agentCtx, verdict, iteration)
	if l.OnJudgeVerdict != nil {
		l.OnJudgeVerdict(verdict, iteration)
	}
	return verdict, nil
}

// implicitOutputKeyVerdict is upstream judge_pipeline Level 2's local
// output-key gate. It reports ok=false when the spec declares no output keys or
// when the keys are satisfied and a configured criteria judge should run next.
//
// Order matters and follows upstream exactly:
//  1. tool results in this turn -> silent RETRY (keep working);
//  2. missing required keys -> RETRY with the "Task incomplete" feedback;
//  3. every key nullable and none set -> RETRY with the set_output hint;
//  4. otherwise ACCEPT when no criteria judge is armed.
func implicitOutputKeyVerdict(spec AgentSpec, accumulator map[string]any, missing []string, records []ToolCallRecord, judge Judge) (JudgeVerdict, bool) {
	if len(spec.OutputKeys) == 0 {
		return JudgeVerdict{}, false
	}
	if len(records) > 0 {
		return JudgeVerdict{Action: JudgeRetry, Silent: true, Implicit: true}, true
	}
	if len(missing) > 0 {
		return JudgeVerdict{
			Action:   JudgeRetry,
			Implicit: true,
			Feedback: "Task incomplete. Required outputs not yet produced: " +
				pyStringListRepr(missing) +
				". Follow your system prompt instructions to complete the work.",
		}, true
	}
	allNullable := len(spec.OutputKeys) > 0
	nullable := make(map[string]struct{}, len(spec.NullableOutputKeys))
	for _, key := range spec.NullableOutputKeys {
		nullable[key] = struct{}{}
	}
	for _, key := range spec.OutputKeys {
		if _, ok := nullable[key]; !ok {
			allNullable = false
			break
		}
	}
	noneSet := true
	for _, key := range spec.OutputKeys {
		if value, ok := accumulator[key]; ok && value != nil {
			noneSet = false
			break
		}
	}
	if allNullable && noneSet {
		return JudgeVerdict{
			Action:   JudgeRetry,
			Implicit: true,
			Feedback: "No output keys have been set yet. Use set_output to set at least one of: " +
				pyStringListRepr(spec.OutputKeys),
		}, true
	}
	if !hasSuccessCriteria(spec) || judge == nil {
		return JudgeVerdict{Action: JudgeAccept, Implicit: true}, true
	}
	return JudgeVerdict{}, false
}

// missingOutputKeys returns the declared keys that are still unset, excluding
// nullable keys. Upstream's _get_missing_output_keys.
func missingOutputKeys(accumulator map[string]any, outputKeys, nullableKeys []string) []string {
	if len(outputKeys) == 0 {
		return nil
	}
	skip := make(map[string]struct{}, len(nullableKeys))
	for _, key := range nullableKeys {
		skip[key] = struct{}{}
	}
	missing := make([]string, 0, len(outputKeys))
	for _, key := range outputKeys {
		if _, nullable := skip[key]; nullable {
			continue
		}
		if value, ok := accumulator[key]; !ok || value == nil {
			missing = append(missing, key)
		}
	}
	return missing
}

// evaluateJudgeWithRetry mirrors AgentLoop._judge_turn (agent_loop.py:6202-6270):
// a transient judge failure is retried up to max(1, MaxStreamRetries) attempts
// with the exponential backoff min(base*2**attempt, max_delay); when the last
// attempt is still transient the turn is accepted with upstream's
// "[judge unavailable after N attempts: <type>; accepting to avoid stalling
// the loop]" feedback; any other failure is returned to the caller, mirroring
// the upstream re-raise.
//
// Parity: framework/agent_loop/agent_loop.py::_judge_turn
func (l *AgentLoop) evaluateJudgeWithRetry(ctx context.Context, request JudgeRequest) (JudgeVerdict, error) {
	attempts := l.judgeMaxAttempts()
	for attempt := 0; attempt < attempts; attempt++ {
		verdict, err := l.Judge.Evaluate(ctx, request)
		if err == nil {
			return normalizeJudgeVerdict(verdict), nil
		}
		transient := isTransientJudgeError(err)
		last := attempt == attempts-1
		if !transient || last {
			if last && transient {
				l.bumpBy("judge_fallback_accept", 1)
				return JudgeVerdict{
					Action: JudgeAccept,
					Feedback: fmt.Sprintf(
						"[judge unavailable after %d attempts: %s; accepting to avoid stalling the loop]",
						attempts, judgeErrorTypeName(err)),
				}, nil
			}
			// Non-transient: propagate so the caller ends the turn, exactly
			// like the upstream _judge_turn re-raise.
			return JudgeVerdict{}, err
		}
		l.bumpBy("judge_transient_retry", 1)
		if err := sleepBeforeJudgeRetry(ctx, l.judgeRetryDelay(attempt)); err != nil {
			return JudgeVerdict{}, err
		}
	}
	// Unreachable — attempts is at least 1, so the loop always returns.
	return JudgeVerdict{}, errors.New("agent loop: _judge_turn retry loop exited unexpectedly")
}

// judgeMaxAttempts mirrors upstream's max_attempts = max(1, max_stream_retries).
//
// Parity: framework/agent_loop/agent_loop.py::_judge_turn
func (l *AgentLoop) judgeMaxAttempts() int {
	attempts := l.Config.MaxStreamRetries
	if attempts < 1 {
		attempts = 1
	}
	return attempts
}

// judgeRetryDelay mirrors upstream's min(base * 2**attempt, max_delay) wait
// between judge attempts.
//
// Parity: framework/agent_loop/agent_loop.py::_judge_turn
func (l *AgentLoop) judgeRetryDelay(attempt int) time.Duration {
	delay := l.Config.StreamRetryBackoffBase * math.Pow(2, float64(attempt))
	if maxDelay := l.Config.StreamRetryMaxDelay; maxDelay < delay {
		delay = maxDelay
	}
	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay * float64(time.Second))
}

// sleepBeforeJudgeRetry waits out the retry backoff; a cancelled context ends
// the wait with its error, like the upstream asyncio.sleep raising
// CancelledError.
func sleepBeforeJudgeRetry(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// transientJudgeErrorKeywords is upstream's RuntimeError keyword list,
// reproduced verbatim.
//
// Parity: framework/agent_loop/internals/tool_result_handler.py::is_transient_error
var transientJudgeErrorKeywords = []string{
	"rate limit",
	"429",
	"timeout",
	"connection",
	"internal server",
	"502",
	"503",
	"504",
	"service unavailable",
	"bad gateway",
	"overloaded",
	"failed to parse tool call",
}

// isTransientJudgeError ports upstream's is_transient_error classification —
// the helper _judge_turn consults — to Go errors. The exception-class rules
// map onto Go error values: TimeoutError/OSError onto context deadlines,
// net errors and connection syscall errors; the RuntimeError keyword list
// onto substring matching. A cancelled context is a deliberate stop and stays
// non-transient, like upstream's CancelledError.
//
// Parity: framework/agent_loop/internals/tool_result_handler.py::is_transient_error
func isTransientJudgeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if isConnectionSyscallError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, keyword := range transientJudgeErrorKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

// isConnectionSyscallError covers the raw socket failures upstream's OSError
// arm absorbs (a net.OpError already satisfies net.Error, so this catches the
// bare syscall errors).
func isConnectionSyscallError(err error) bool {
	for _, errno := range []error{
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
		syscall.EHOSTUNREACH,
		syscall.ENETUNREACH,
		syscall.EHOSTDOWN,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}

// judgeErrorTypeName renders the upstream-style exception name for the
// fallback feedback, so the sentence reads like Python's
// f"...: {type(e).__name__}; ...".
func judgeErrorTypeName(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutError"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "TimeoutError"
		}
		return "ConnectionError"
	}
	if isConnectionSyscallError(err) {
		return "ConnectionError"
	}
	return "RuntimeError"
}

// normalizeJudgeVerdict maps a judge verdict onto a known action and makes
// RETRY/ESCALATE verdicts carry feedback that is safe to log. Judge failures
// never reach here: evaluateJudgeWithRetry owns them (retry, then the
// fail-open ACCEPT or propagation), mirroring _judge_turn.
func normalizeJudgeVerdict(verdict JudgeVerdict) JudgeVerdict {
	switch JudgeAction(strings.ToUpper(strings.TrimSpace(string(verdict.Action)))) {
	case JudgeAccept:
		verdict.Action = JudgeAccept
	case JudgeEscalate:
		verdict.Action = JudgeEscalate
	default:
		verdict.Action = JudgeRetry
	}
	if verdict.Action == JudgeRetry && !verdict.Silent && strings.TrimSpace(verdict.Feedback) == "" {
		// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
		// (Level 1: "Custom judge returned RETRY.")
		verdict.Feedback = "Custom judge returned RETRY."
	}
	if verdict.Action == JudgeEscalate && strings.TrimSpace(verdict.Feedback) == "" {
		verdict.Feedback = "Judge escalated without feedback."
	}
	return verdict
}

func (l *AgentLoop) emitJudgeVerdict(agentCtx *AgentContext, verdict JudgeVerdict, iteration int) {
	if l.JudgeEvents == nil || agentCtx == nil {
		return
	}
	var executionID *string
	if agentCtx.ExecutionID != "" {
		id := agentCtx.ExecutionID
		executionID = &id
	}
	nodeID := agentCtx.AgentID
	if nodeID == "" {
		nodeID = agentCtx.AgentSpec.ID
	}
	judgeType := "custom"
	if verdict.Implicit {
		judgeType = "implicit"
	}
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop publishes the
	// first 200 characters of the feedback as the event preview.
	l.JudgeEvents.EmitJudgeVerdict(
		agentCtx.StreamID,
		nodeID,
		string(verdict.Action),
		truncateRunes(verdict.Feedback, 200),
		judgeType,
		iteration,
		executionID,
	)
}

// injectJudgeFeedback adds a RETRY critique to the conversation.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (RETRY branch)
func injectJudgeFeedback(conv *NodeConversation, verdict JudgeVerdict) error {
	_, err := conv.AddUserMessage("[Judge feedback]: "+verdict.Feedback, UserMessageOptions{})
	return err
}
