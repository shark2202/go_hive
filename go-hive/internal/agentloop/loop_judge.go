package agentloop

// Judge-verdict application, split out of ExecuteTurn so the turn loop stays
// within the repo's function-size budget.
//
// Parity: framework/agent_loop/agent_loop.py::execute (6i verdict branches)

import (
	"fmt"

	"github.com/aden-hive/hive-go/internal/llm"
)

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
		if feedbackErr := injectJudgeFeedback(conv, verdict); feedbackErr != nil {
			result.Success = false
			return true, feedbackErr
		}
		return false, nil
	}
}
