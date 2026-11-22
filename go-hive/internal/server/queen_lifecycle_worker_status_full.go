package server

// get_worker_status detail sections: tools, issues, progress and the
// focus='full' JSON dump.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_tools
// Parity: framework/tools/queen_lifecycle_tools.py::_format_issues
// Parity: framework/tools/queen_lifecycle_tools.py::_format_progress
// Parity: framework/tools/queen_lifecycle_tools.py::_build_full_json

import (
	"fmt"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// formatWorkerTools lists running and recent tool calls.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_tools
func formatWorkerTools(bus *eventbus.EventBus, lastN int) string {
	lines := []string{}
	started := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventToolCallStarted), eventbus.WithHistoryLimit(lastN*2))
	completed := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventToolCallCompleted), eventbus.WithHistoryLimit(lastN*2))
	completedIDs := map[string]struct{}{}
	for _, event := range completed {
		if id := stringValue(event.Data["tool_use_id"]); id != "" {
			completedIDs[id] = struct{}{}
		}
	}
	running := make([]*eventbus.AgentEvent, 0)
	for _, event := range started {
		id := stringValue(event.Data["tool_use_id"])
		if id == "" {
			continue
		}
		if _, done := completedIDs[id]; !done {
			running = append(running, event)
		}
	}
	if len(running) > 0 {
		names := make([]string, 0, len(running))
		for _, event := range running {
			names = append(names, stringValue(event.Data["tool_name"]))
		}
		lines = append(lines, fmt.Sprintf("%d tool(s) running: %s.", len(running), strings.Join(names, ", ")))
		for _, event := range running {
			name := stringValue(event.Data["tool_name"])
			node := "?"
			if event.NodeID != nil {
				node = *event.NodeID
			}
			lines = append(lines, fmt.Sprintf("  %s (%s, started %s)", name, node, formatTimeAgo(event.Timestamp)))
			if input := charPrefix(fmt.Sprint(event.Data["tool_input"]), 150); input != "" && input != "<nil>" {
				lines = append(lines, "    Input: "+input)
			}
		}
	} else {
		lines = append(lines, "No tools currently running.")
	}

	if len(completed) > 0 {
		limit := lastN
		if len(completed) < limit {
			limit = len(completed)
		}
		lines = append(lines, "", fmt.Sprintf("Recent calls (last %d):", limit))
		for _, event := range completed[:limit] {
			name := stringValue(event.Data["tool_name"])
			node := "?"
			if event.NodeID != nil {
				node = *event.NodeID
			}
			status := "ok"
			if pyTruthy(event.Data["is_error"]) {
				status = "error"
			}
			duration := ""
			if seconds, ok := floatValue(event.Data["duration_s"]); ok && seconds > 0 {
				duration = fmt.Sprintf(", %.1fs", seconds)
			}
			lines = append(lines, fmt.Sprintf("  %s (%s) — %s%s", name, node, status, duration))
			if result := charPrefix(strings.ReplaceAll(stringValue(event.Data["result"]), "\n", " "), 300); result != "" {
				lines = append(lines, "    Result: "+result)
			}
		}
	} else {
		lines = append(lines, "No recent tool calls.")
	}
	return strings.Join(lines, "\n")
}

// formatWorkerIssues lists retries, stalls, doom loops and constraint
// violations.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_issues
func formatWorkerIssues(bus *eventbus.EventBus) string {
	lines := []string{}
	total := 0

	retries := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeRetry), eventbus.WithHistoryLimit(20))
	if len(retries) > 0 {
		total += len(retries)
		lines = append(lines, fmt.Sprintf("%d retry event(s):", len(retries)))
		limit := len(retries)
		if limit > 5 {
			limit = 5
		}
		for _, event := range retries[:limit] {
			node := "?"
			if event.NodeID != nil {
				node = *event.NodeID
			}
			lines = append(lines, fmt.Sprintf("  %s (attempt %v, %s): %s",
				node, event.Data["retry_count"], formatTimeAgo(event.Timestamp),
				charPrefix(stringValue(event.Data["error"]), 120)))
		}
	}

	stalls := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeStalled), eventbus.WithHistoryLimit(5))
	if len(stalls) > 0 {
		total += len(stalls)
		lines = append(lines, fmt.Sprintf("%d stall(s):", len(stalls)))
		for _, event := range stalls {
			node := "?"
			if event.NodeID != nil {
				node = *event.NodeID
			}
			lines = append(lines, fmt.Sprintf("  %s (%s): %s", node, formatTimeAgo(event.Timestamp), charPrefix(stringValue(event.Data["reason"]), 150)))
		}
	}

	doomLoops := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeToolDoomLoop), eventbus.WithHistoryLimit(5))
	if len(doomLoops) > 0 {
		total += len(doomLoops)
		lines = append(lines, fmt.Sprintf("%d tool doom loop(s):", len(doomLoops)))
		for _, event := range doomLoops {
			node := "?"
			if event.NodeID != nil {
				node = *event.NodeID
			}
			lines = append(lines, fmt.Sprintf("  %s (%s): %s", node, formatTimeAgo(event.Timestamp), charPrefix(stringValue(event.Data["description"]), 150)))
		}
	}

	violations := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventConstraintViolation), eventbus.WithHistoryLimit(5))
	if len(violations) > 0 {
		total += len(violations)
		lines = append(lines, fmt.Sprintf("%d constraint violation(s):", len(violations)))
		for _, event := range violations {
			lines = append(lines, fmt.Sprintf("  %v (%s): %s",
				event.Data["constraint_id"], formatTimeAgo(event.Timestamp), charPrefix(stringValue(event.Data["description"]), 150)))
		}
	}

	if total == 0 {
		return "No issues detected. No retries, stalls, or constraint violations."
	}
	return fmt.Sprintf("%d issue(s) detected.", total) + "\n\n" + strings.Join(lines, "\n")
}

// formatProgress reports goal criteria, token consumption and execution
// outcomes.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_format_progress
func (b *QueenLifecycleBinding) formatProgress(runtime *colony.ColonyRuntime, bus *eventbus.EventBus) string {
	lines := []string{}

	if goal := runtime.Goal(); goal != nil {
		if name := strings.TrimSpace(goal.Name); name != "" {
			lines = append(lines, "Goal: "+name+".")
		}
		if len(goal.SuccessCriteria) > 0 {
			lines = append(lines, fmt.Sprintf("Success criteria: %d declared.", len(goal.SuccessCriteria)))
		}
	}

	llmEvents := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventLLMTurnComplete), eventbus.WithHistoryLimit(200))
	if len(llmEvents) > 0 {
		inputTokens, outputTokens := 0, 0
		for _, event := range llmEvents {
			if value, ok := pythonIntValue(event.Data["input_tokens"]); ok {
				inputTokens += value
			}
			if value, ok := pythonIntValue(event.Data["output_tokens"]); ok {
				outputTokens += value
			}
		}
		lines = append(lines, "")
		lines = append(lines, fmt.Sprintf("Tokens: %d LLM turns, %d total (%d in + %d out).",
			len(llmEvents), inputTokens+outputTokens, inputTokens, outputTokens))
	}

	completed := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventExecutionCompleted), eventbus.WithHistoryLimit(5))
	failed := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventExecutionFailed), eventbus.WithHistoryLimit(5))
	lines = append(lines, fmt.Sprintf("Executions: %d completed, %d failed.", len(completed), len(failed)))
	for _, event := range failed {
		var executionID string
		if event.ExecutionID != nil {
			executionID = *event.ExecutionID
		}
		lines = append(lines, fmt.Sprintf("  Failed (%s) %s: %s",
			formatTimeAgo(event.Timestamp), executionID, charPrefix(stringValue(event.Data["error"]), 150)))
	}
	return strings.Join(lines, "\n")
}

// buildWorkerStatusJSON is the focus='full' dump. The keys mirror the upstream
// object; a "workers" section carries this port's worker-registry rows where
// upstream reads a single graph's executions.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_build_full_json
func (b *QueenLifecycleBinding) buildWorkerStatusJSON(runtime *colony.ColonyRuntime, bus *eventbus.EventBus, preamble queenWorkerPreamble, lastN int) string {
	colonyID := runtime.ColonyID()
	goalName := colonyID
	if goal := runtime.Goal(); goal != nil && strings.TrimSpace(goal.Name) != "" {
		goalName = goal.Name
	}
	result := map[string]any{
		"worker_colony_id": colonyID,
		"worker_goal":      goalName,
		"status":           preamble.status,
		// This port has no idle clock per agent; -1 is upstream's "unknown".
		"agent_idle_seconds": -1,
	}

	activeExecutions := make([]map[string]any, 0, len(preamble.active))
	workers := make([]map[string]any, 0)
	for _, worker := range preamble.active {
		activeExecutions = append(activeExecutions, map[string]any{
			"execution_id":    worker.ID,
			"entry_point":     worker.Status,
			"elapsed_seconds": roundTo1(worker.Elapsed),
		})
	}
	for _, info := range runtime.ListWorkers() {
		if worker := runtime.GetWorker(info.WorkerID); worker != nil && worker.IsPersistent() {
			continue
		}
		row := map[string]any{
			"worker_id": info.WorkerID,
			"status":    string(info.Status),
			"task":      charPrefix(info.Task, 200),
		}
		if info.Goal != "" {
			row["goal"] = charPrefix(info.Goal, 200)
		}
		if info.BatchID != "" {
			row["batch_id"] = info.BatchID
		}
		if info.Result != nil {
			row["report_status"] = info.Result.ReportStatus
			row["summary"] = info.Result.Summary
		}
		workers = append(workers, row)
	}
	if len(activeExecutions) > 0 {
		result["active_executions"] = activeExecutions
	}
	result["workers"] = workers
	if pending := b.session.pendingQuestionEntries(); len(pending) > 0 {
		result["pending_question"] = charPrefix(stringValue(pending[0]["reason"]), 200)
	}

	queenStatusToolSection(result, bus, lastN)
	queenStatusRetrySection(result, bus, lastN)
	queenStatusIssueSection(result, bus, lastN)
	queenStatusUsageSection(result, bus)

	return queenLifecycleJSON(result)
}

// queenStatusToolSection copies running + recent tool calls into the dump.
func queenStatusToolSection(result map[string]any, bus *eventbus.EventBus, lastN int) {
	toolStarted := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventToolCallStarted), eventbus.WithHistoryLimit(lastN*2))
	toolCompleted := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventToolCallCompleted), eventbus.WithHistoryLimit(lastN*2))
	completedIDs := map[string]struct{}{}
	for _, event := range toolCompleted {
		if id := stringValue(event.Data["tool_use_id"]); id != "" {
			completedIDs[id] = struct{}{}
		}
	}
	running := []map[string]any{}
	for _, event := range toolStarted {
		id := stringValue(event.Data["tool_use_id"])
		if id == "" {
			continue
		}
		if _, done := completedIDs[id]; done {
			continue
		}
		running = append(running, map[string]any{
			"tool":          stringValue(event.Data["tool_name"]),
			"node":          event.NodeID,
			"started_at":    event.Timestamp.UTC().Format(time.RFC3339),
			"input_preview": charPrefix(fmt.Sprint(event.Data["tool_input"]), 200),
		})
	}
	if len(running) > 0 {
		result["running_tools"] = running
	}
	if len(toolCompleted) == 0 {
		return
	}
	limit := lastN
	if len(toolCompleted) < limit {
		limit = len(toolCompleted)
	}
	recent := make([]map[string]any, 0, limit)
	for _, event := range toolCompleted[:limit] {
		entry := map[string]any{
			"tool":  stringValue(event.Data["tool_name"]),
			"error": pyTruthy(event.Data["is_error"]),
			"node":  event.NodeID,
			"time":  event.Timestamp.UTC().Format(time.RFC3339),
		}
		if text := stringValue(event.Data["result"]); text != "" {
			entry["result_preview"] = charPrefix(text, 300)
		}
		recent = append(recent, entry)
	}
	result["recent_tool_calls"] = recent
}

// queenStatusRetrySection copies node transitions and retries into the dump.
func queenStatusRetrySection(result map[string]any, bus *eventbus.EventBus, lastN int) {
	retries := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeRetry), eventbus.WithHistoryLimit(lastN))
	if len(retries) == 0 {
		return
	}
	transitions := make([]map[string]any, 0, len(retries))
	rows := make([]map[string]any, 0, len(retries))
	for _, event := range retries {
		transitions = append(transitions, map[string]any{
			"from":      event.Data["source_node"],
			"to":        event.Data["target_node"],
			"condition": event.Data["edge_condition"],
			"time":      event.Timestamp.UTC().Format(time.RFC3339),
		})
		rows = append(rows, map[string]any{
			"node":        event.NodeID,
			"retry_count": event.Data["retry_count"],
			"error":       charPrefix(stringValue(event.Data["error"]), 200),
			"time":        event.Timestamp.UTC().Format(time.RFC3339),
		})
	}
	result["node_transitions"] = transitions
	result["retries"] = rows
}

// queenStatusIssueSection copies stalls, doom loops, subagent reports and
// constraint violations into the dump.
func queenStatusIssueSection(result map[string]any, bus *eventbus.EventBus, lastN int) {
	issues := []map[string]any{}
	for _, event := range bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeStalled), eventbus.WithHistoryLimit(5)) {
		issues = append(issues, map[string]any{
			"type":   "stall",
			"node":   event.NodeID,
			"reason": charPrefix(stringValue(event.Data["reason"]), 200),
			"time":   event.Timestamp.UTC().Format(time.RFC3339),
		})
	}
	for _, event := range bus.GetHistory(eventbus.WithHistoryType(eventbus.EventNodeToolDoomLoop), eventbus.WithHistoryLimit(5)) {
		issues = append(issues, map[string]any{
			"type":        "tool_doom_loop",
			"node":        event.NodeID,
			"description": charPrefix(stringValue(event.Data["description"]), 200),
			"time":        event.Timestamp.UTC().Format(time.RFC3339),
		})
	}
	if len(issues) > 0 {
		result["issues"] = issues
	}

	reports := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventSubagentReport), eventbus.WithHistoryLimit(lastN))
	if len(reports) > 0 {
		activity := make([]map[string]any, 0, len(reports))
		for _, event := range reports {
			activity = append(activity, map[string]any{
				"subagent": event.Data["worker_id"],
				"message":  charPrefix(stringValue(event.Data["message"]), 300),
				"time":     event.Timestamp.UTC().Format(time.RFC3339),
			})
		}
		result["subagent_activity"] = activity
	}

	violations := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventConstraintViolation), eventbus.WithHistoryLimit(5))
	if len(violations) > 0 {
		rows := make([]map[string]any, 0, len(violations))
		for _, event := range violations {
			rows = append(rows, map[string]any{
				"constraint":  event.Data["constraint_id"],
				"description": charPrefix(stringValue(event.Data["description"]), 200),
				"time":        event.Timestamp.UTC().Format(time.RFC3339),
			})
		}
		result["constraint_violations"] = rows
	}
}

// queenStatusUsageSection copies the token summary and execution outcomes into
// the dump.
func queenStatusUsageSection(result map[string]any, bus *eventbus.EventBus) {
	llmEvents := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventLLMTurnComplete), eventbus.WithHistoryLimit(200))
	if len(llmEvents) > 0 {
		inputTokens, outputTokens := 0, 0
		for _, event := range llmEvents {
			if value, ok := pythonIntValue(event.Data["input_tokens"]); ok {
				inputTokens += value
			}
			if value, ok := pythonIntValue(event.Data["output_tokens"]); ok {
				outputTokens += value
			}
		}
		result["token_summary"] = map[string]any{
			"llm_turns":     len(llmEvents),
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
			"total_tokens":  inputTokens + outputTokens,
		}
	}

	execCompleted := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventExecutionCompleted), eventbus.WithHistoryLimit(5))
	execFailed := bus.GetHistory(eventbus.WithHistoryType(eventbus.EventExecutionFailed), eventbus.WithHistoryLimit(5))
	if len(execCompleted) == 0 && len(execFailed) == 0 {
		return
	}
	outcomes := []map[string]any{}
	for _, event := range execCompleted {
		outcomes = append(outcomes, map[string]any{
			"outcome":      "completed",
			"execution_id": event.ExecutionID,
			"time":         event.Timestamp.UTC().Format(time.RFC3339),
		})
	}
	for _, event := range execFailed {
		outcomes = append(outcomes, map[string]any{
			"outcome":      "failed",
			"execution_id": event.ExecutionID,
			"error":        charPrefix(stringValue(event.Data["error"]), 200),
			"time":         event.Timestamp.UTC().Format(time.RFC3339),
		})
	}
	result["execution_outcomes"] = outcomes
}
