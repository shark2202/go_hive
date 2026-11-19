package agentloop

// Judge evaluation shared by the loop hook and its llmjudge adapter.

import (
	"context"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
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
// criteria. It is the production entry point: a chat session (no criteria) or
// a nil completer leaves the loop's judge disabled, so the turn costs no extra
// LLM call. An already configured judge is left untouched.
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
// configured JudgeEveryNTurns cadence applies. A nil Judge, SkipJudge, or
// missing criteria always disables evaluation.
func (l *AgentLoop) shouldJudge(agentCtx *AgentContext, iteration int, naturalStop, toolFailure bool) bool {
	if l == nil || l.Judge == nil || agentCtx == nil {
		return false
	}
	if agentCtx.AgentSpec.SkipJudge || !hasSuccessCriteria(agentCtx.AgentSpec) {
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

// evaluateJudge calls the configured judge and normalizes its verdict.
func (l *AgentLoop) evaluateJudge(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, completion string, records []ToolCallRecord, iteration int) JudgeVerdict {
	request := JudgeRequest{
		AgentSpec:           agentCtx.AgentSpec,
		Criteria:            successCriteria(agentCtx.AgentSpec),
		AssistantText:       completion,
		ToolResults:         append([]ToolCallRecord(nil), records...),
		ConversationSummary: conv.ExportSummary(),
		Iteration:           iteration,
	}
	verdict, err := l.Judge.Evaluate(ctx, request)
	verdict = normalizeJudgeVerdict(verdict, err)
	l.bumpBy("judge_"+strings.ToLower(string(verdict.Action)), 1)
	l.emitJudgeVerdict(agentCtx, verdict, iteration)
	return verdict
}

// normalizeJudgeVerdict maps an evaluator result onto a known action and
// makes RETRY/ESCALATE verdicts carry feedback that is safe to log.
func normalizeJudgeVerdict(verdict JudgeVerdict, err error) JudgeVerdict {
	if err != nil {
		return JudgeVerdict{
			Action:   JudgeRetry,
			Feedback: "Judge evaluation failed: " + err.Error(),
		}
	}
	switch JudgeAction(strings.ToUpper(strings.TrimSpace(string(verdict.Action)))) {
	case JudgeAccept:
		verdict.Action = JudgeAccept
	case JudgeEscalate:
		verdict.Action = JudgeEscalate
	default:
		verdict.Action = JudgeRetry
	}
	if verdict.Action == JudgeRetry && strings.TrimSpace(verdict.Feedback) == "" {
		verdict.Feedback = "Judge returned RETRY without feedback."
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
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop publishes the
	// first 200 characters of the feedback as the event preview.
	l.JudgeEvents.EmitJudgeVerdict(
		agentCtx.StreamID,
		nodeID,
		string(verdict.Action),
		truncateRunes(verdict.Feedback, 200),
		"custom",
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
