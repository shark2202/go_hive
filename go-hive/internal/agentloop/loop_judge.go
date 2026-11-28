package agentloop

// Judge-verdict application, split out of ExecuteTurn so the turn loop stays
// within the repo's function-size budget.
//
// Parity: framework/agent_loop/agent_loop.py::execute (6i verdict branches)

import (
	"context"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
)

// runJudgeStep 执行一轮的 judge 钩子：未触发时仅记 runtime 日志；
// 非瞬态 judge 失败上抛（对齐上游 re-raise），否则按 verdict 收束回合。
// 从 ExecuteTurn 抽出以守住函数规模预算。
// Parity: framework/agent_loop/agent_loop.py::_judge_turn
func (l *AgentLoop) runJudgeStep(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, completion llm.Completion, records []ToolCallRecord, inner int, toolFailure bool, loggedCalls []runtimelogs.ToolCallInput, iterLatencyMS int, result *TurnResult) (bool, error) {
	if !l.shouldJudge(agentCtx, inner, false, toolFailure) {
		l.logRuntimeNonJudgedStep(agentCtx, completion, loggedCalls, inner, iterLatencyMS)
		return false, nil
	}
	verdict, judgeErr := l.evaluateJudge(ctx, agentCtx, conv, completion.Content, records, inner)
	if judgeErr != nil {
		// Parity: framework/agent_loop/agent_loop.py::_judge_turn —
		// a non-transient judge failure re-raises, ending the turn.
		result.Success = false
		return true, judgeErr
	}
	l.logRuntimeVerdict(agentCtx, verdict, completion, loggedCalls, inner, iterLatencyMS)
	return applyJudgeVerdict(conv, verdict, completion, inner, result)
}

// applyJudgeVerdict applies one normalized judge verdict to the turn:
// ACCEPT/ESCALATE finalize the result and stop the loop (an ESCALATE carries
// the loop's error), RETRY injects the feedback and continues.
func applyJudgeVerdict(conv *NodeConversation, verdict JudgeVerdict, completion llm.Completion, iteration int, result *TurnResult) (stop bool, err error) {
	switch verdict.Action {
	case JudgeAccept:
		result.FinalText = completion.Content
		result.InnerTurns = iteration + 1
		return true, nil
	case JudgeEscalate:
		result.Success = false
		result.FinalText = completion.Content
		result.InnerTurns = iteration + 1
		return true, fmt.Errorf("agent loop: judge escalated: %s", verdict.Feedback)
	default:
		if verdict.Silent {
			return false, nil
		}
		if feedbackErr := injectJudgeFeedback(conv, verdict); feedbackErr != nil {
			result.Success = false
			return true, feedbackErr
		}
		return false, nil
	}
}
