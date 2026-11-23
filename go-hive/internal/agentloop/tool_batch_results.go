package agentloop

// applyBatchSkips and toolBatchTail are the two result-shaping steps ExecuteTurn
// runs between the tool batch and storeToolBatch. They were extracted from the
// turn body (2026-11-23) to bring ExecuteTurn back under its frozen function
// budget; the statements and their order are unchanged.

// applyBatchSkips fills the results of the calls the batch did not execute:
// grace-window calls get the grace notice, budget-deferred calls the defer
// notice (and the hard-stop advisory rides the next model call).
func (l *AgentLoop) applyBatchSkips(reminderCtx *ReminderAgentContext, conv *NodeConversation, results []ToolResult, graceSkippedIdx, deferredIdx []int, limitHit, lifetimeLimitHit bool, lifetimeBudget, hardLimit int, names []string) {
	if len(graceSkippedIdx) > 0 {
		l.bumpBy("grace_iteration_skipped", len(graceSkippedIdx))
		for _, i := range graceSkippedIdx {
			results[i] = ToolResult{Content: graceSkipMessage}
		}
	}
	if !limitHit {
		return
	}
	deferMessage := hardStopDeferMessage
	if lifetimeLimitHit {
		deferMessage = lifetimeBudgetDeferMessage
	}
	l.bumpBy("tool_budget_deferred", len(deferredIdx))
	for _, i := range deferredIdx {
		results[i] = ToolResult{Content: deferMessage}
	}
	l.injectToolBudgetStop(reminderCtx, conv, len(deferredIdx), lifetimeBudget, hardLimit, lifetimeLimitHit, names)
}

// toolBatchTail assembles the reminder tail one finished tool batch carries
// (the POST_TOOL_USE hub block, the soft budget checkpoint, the doom-loop
// advisory) and returns it with the updated soft-checkpoint count.
//
// Parity: framework/agent_loop/agent_loop.py::_render_tool_budget_checkpoint
// (the soft checkpoint rides the batch tail and never defers work).
func (l *AgentLoop) toolBatchTail(reminderCtx *ReminderAgentContext, names []string, toolCallCount, softInterval, hardLimit, softBudgetReminders int, doomLoopReminder string) (string, int) {
	tail := ""
	if l.Hub != nil {
		tail = l.Hub.Fire(POST_TOOL_USE, reminderCtx, names, "")
	}
	var checkpoint string
	checkpoint, softBudgetReminders = l.softBudgetCheckpointBlock(
		reminderCtx, names, toolCallCount, softInterval, hardLimit, softBudgetReminders)
	if checkpoint != "" {
		if tail == "" {
			tail = checkpoint
		} else {
			tail = tail + "\n\n" + checkpoint
		}
	}
	if doomLoopReminder != "" {
		if tail == "" {
			tail = doomLoopReminder
		} else {
			tail = tail + "\n\n" + doomLoopReminder
		}
	}
	return tail, softBudgetReminders
}
