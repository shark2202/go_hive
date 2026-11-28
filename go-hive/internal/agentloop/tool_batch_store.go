package agentloop

// storeToolBatch 及其逐调用落库逻辑从 loop.go 抽出（2026-11-22）：接线
// tool input coercion 后 loop.go 越过 1500 行冻结上限，按仓库规则拆函数而
// 不是上调 waiver。行为不变。

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// storeToolBatch rebuilds the per-call records for a finished tool batch and
// adds every result to the conversation, appending the reminder tail to the
// last non-parking call. Extracted from ExecuteTurn so the turn body stays
// within the repo's function-size budget.
//
// It also publishes, in call order, the completion and per-call context
// readouts upstream emits while its tool loop drains: for every executed call
// tool_call_completed immediately followed by context_usage_updated
// (trigger=post_tool_call). Only calls that started (started[i]) are announced:
// a deferred or grace-skipped call never left a started frame to complete. The
// batch-end readout (trigger=post_tool_results) is NOT published here: the
// caller runs the post-batch context maintenance first, so the readout reports
// the reclaimed size exactly as upstream's publish_context_usage does.
//
// coercedInputs, when present at index i, is the schema-healed input the call
// was dispatched with; upstream stores tc.tool_input, which its pre-dispatch
// coercion already mutated in place, so records and the vision intent read the
// healed values too. nil falls back to parsing the raw arguments (tests, and
// any caller that never coerced).
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (per-tool publication)
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
func (l *AgentLoop) storeToolBatch(ctx context.Context, agentCtx *AgentContext, conv *NodeConversation, toolCalls []llm.ToolCall, results []ToolResult, tail string, started []bool, timings []toolTiming, coercedInputs []map[string]any) ([]ToolCallRecord, error) {
	records := make([]ToolCallRecord, 0, len(toolCalls))
	for i, tc := range toolCalls {
		input := parseToolInput(tc.Arguments)
		if i < len(coercedInputs) && coercedInputs[i] != nil {
			input = coercedInputs[i]
		}
		record := ToolCallRecord{ToolUseID: tc.ID, ToolName: tc.Name, ToolInput: input, Content: results[i].Content, IsError: results[i].IsError}
		if i < len(timings) {
			record.StartTimestamp, record.DurationS = timings[i].startTimestamp, timings[i].durationS
		}
		records = append(records, record)
	}
	targetID := ""
	if tail != "" {
		for i := len(toolCalls) - 1; i >= 0; i-- {
			if toolCalls[i].Name != "ask_user" && toolCalls[i].Name != "escalate" {
				targetID = toolCalls[i].ID
				break
			}
		}
	}
	// A text-only main model cannot receive image blocks: with a captioner
	// configured its images are captioned and dropped, exactly as upstream's
	// vision-fallback chain does.
	captionImages := l.captionToolImagesEnabled(agentCtx)
	for i, tc := range toolCalls {
		stored := results[i].Content
		opts := ToolResultOptions{
			IsError:        results[i].IsError,
			IsSkillContent: results[i].IsSkillContent,
			ImageContent:   results[i].ImageContent,
		}
		// The caption is spliced before the reminder tail, matching upstream's
		// order: prefix, caption, tail.
		if captionImages && len(results[i].ImageContent) > 0 {
			stored = stored + "\n\n" + l.captionToolImages(ctx, conv, tc.Name, records[i].ToolInput, results[i])
			opts.ImageContent = nil
		}
		if tail != "" && tc.ID == targetID {
			stored = stored + "\n\n" + tail
		}
		if results[i].SpilloverPath != "" {
			spillover := results[i].SpilloverPath
			opts.SpilloverPath = &spillover
		}
		if _, err := conv.AddToolResult(tc.ID, stored, opts); err != nil {
			return records, err
		}
		records[i].Content = stored
		if agentCtx.Observer != nil && i < len(started) && started[i] {
			agentCtx.Observer.ToolCallCompleted(
				ToolCallRequest{ID: tc.ID, Name: tc.Name, Input: records[i].ToolInput},
				results[i],
			)
			if observer, ok := agentCtx.Observer.(ToolBatchContextObserver); ok {
				observer.ToolResultStored()
			}
		}
	}
	return records, nil
}
