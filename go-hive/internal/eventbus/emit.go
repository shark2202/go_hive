package eventbus

// Parity: framework/host/event_bus.py (CONVENIENCE PUBLISHERS)

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// pythonFloat renders a float64 the way Python's json encoder does, so a
// whole-number cost keeps its decimal point (0.0 rather than 0). Both
// literals are the same JSON number, but the session SSE frames are compared
// against the Python reference value-for-value, and encoding/json's float
// formatter drops the trailing .0. NaN/Infinity stay raw floats: Python
// writes non-standard bare literals for them, and preserving the float keeps
// encoding/json failing loudly instead of inventing a number.
func pythonFloat(value float64) any {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		// Python switches to exponent notation below 1e-4 and at/above 1e16;
		// Go's 'g' switches earlier (once the exponent reaches the shortest
		// digit count), so re-render those in fixed notation when Python
		// would.
		if exponent, err := strconv.Atoi(text[index+1:]); err == nil && exponent >= -4 && exponent < 16 {
			text = strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return json.Number(text)
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyMapStringString(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func orEmptyActivations(m []map[string]any) []map[string]any {
	if m == nil {
		return []map[string]any{}
	}
	return m
}

func stringOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func (b *EventBus) EmitExecutionStarted(streamID, executionID string, inputData map[string]any, correlationID, runID *string) {
	event := NewAgentEvent(EventExecutionStarted, streamID)
	event.ExecutionID = StrPtr(executionID)
	event.Data = map[string]any{"input": orEmptyMap(inputData)}
	event.CorrelationID = correlationID
	event.RunID = runID
	b.Publish(event)
}

func (b *EventBus) EmitExecutionCompleted(streamID, executionID string, output map[string]any, correlationID, runID *string) {
	event := NewAgentEvent(EventExecutionCompleted, streamID)
	event.ExecutionID = StrPtr(executionID)
	event.Data = map[string]any{"output": orEmptyMap(output)}
	event.CorrelationID = correlationID
	event.RunID = runID
	b.Publish(event)
}

func (b *EventBus) EmitExecutionFailed(streamID, executionID, errMsg string, correlationID, runID *string) {
	event := NewAgentEvent(EventExecutionFailed, streamID)
	event.ExecutionID = StrPtr(executionID)
	event.Data = map[string]any{"error": errMsg}
	event.CorrelationID = correlationID
	event.RunID = runID
	b.Publish(event)
}

func (b *EventBus) EmitPaymentRequired(streamID string, executionID *string, message *string, upstreamStatus ...*int) {
	status := any(402)
	if len(upstreamStatus) > 0 {
		if upstreamStatus[0] == nil {
			status = nil
		} else {
			status = *upstreamStatus[0]
		}
	}
	msg := "LLM provider returned payment required."
	if message != nil && *message != "" {
		msg = *message
	}
	event := NewAgentEvent(EventPaymentRequired, streamID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"message":         msg,
		"upstream_status": status,
	}
	b.Publish(event)
}

func (b *EventBus) EmitGoalProgress(streamID string, progress float64, criteriaStatus map[string]any) {
	event := NewAgentEvent(EventGoalProgress, streamID)
	event.Data = map[string]any{
		"progress":        progress,
		"criteria_status": criteriaStatus,
	}
	b.Publish(event)
}

func (b *EventBus) EmitConstraintViolation(streamID, executionID, constraintID, description string) {
	event := NewAgentEvent(EventConstraintViolation, streamID)
	event.ExecutionID = StrPtr(executionID)
	event.Data = map[string]any{
		"constraint_id": constraintID,
		"description":   description,
	}
	b.Publish(event)
}

func (b *EventBus) EmitStateChanged(streamID, executionID, key string, oldValue, newValue any, scope string) {
	event := NewAgentEvent(EventStateChanged, streamID)
	event.ExecutionID = StrPtr(executionID)
	event.Data = map[string]any{
		"key":       key,
		"old_value": oldValue,
		"new_value": newValue,
		"scope":     scope,
	}
	b.Publish(event)
}

func (b *EventBus) EmitNodeLoopStarted(streamID, nodeID string, executionID *string, maxIterations *int) {
	event := NewAgentEvent(EventNodeLoopStarted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"max_iterations": intOrNil(maxIterations)}
	b.Publish(event)
}

func (b *EventBus) EmitNodeLoopIteration(streamID, nodeID string, iteration int, executionID *string, extraData map[string]any) {
	data := map[string]any{"iteration": iteration}
	for k, v := range extraData {
		data[k] = v
	}
	event := NewAgentEvent(EventNodeLoopIteration, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitNodeLoopCompleted(streamID, nodeID string, iterations int, executionID *string) {
	event := NewAgentEvent(EventNodeLoopCompleted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"iterations": iterations}
	b.Publish(event)
}

func (b *EventBus) EmitLLMTextDelta(streamID, nodeID, content, snapshot string, executionID *string, innerTurn int) {
	event := NewAgentEvent(EventLLMTextDelta, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"content":    content,
		"snapshot":   snapshot,
		"inner_turn": innerTurn,
	}
	b.Publish(event)
}

func (b *EventBus) EmitLLMReasoningDelta(streamID, nodeID, content string, executionID *string, iteration *int, innerTurn int, snapshot *string) {
	data := map[string]any{"content": content, "inner_turn": innerTurn}
	if iteration != nil {
		data["iteration"] = *iteration
	}
	if snapshot != nil {
		data["snapshot"] = *snapshot
	}
	event := NewAgentEvent(EventLLMReasoningDelta, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitLLMTurnComplete(
	streamID, nodeID, stopReason, model string,
	inputTokens, outputTokens, cachedTokens, cacheCreationTokens int,
	costUSD float64,
	credits *float64,
	executionID *string,
	iteration *int,
	systemPrefixSHA, systemSuffixSHA *string,
	historyAnchorIdx *int,
	messageCount *int,
) {
	data := map[string]any{
		"stop_reason":           stopReason,
		"model":                 model,
		"input_tokens":          inputTokens,
		"output_tokens":         outputTokens,
		"cached_tokens":         cachedTokens,
		"cache_creation_tokens": cacheCreationTokens,
		"cost_usd":              pythonFloat(costUSD),
	}
	if credits != nil {
		data["credits"] = pythonFloat(*credits)
	}
	if iteration != nil {
		data["iteration"] = *iteration
	}
	if systemPrefixSHA != nil {
		data["system_prefix_sha"] = *systemPrefixSHA
	}
	if systemSuffixSHA != nil {
		data["system_suffix_sha"] = *systemSuffixSHA
	}
	if historyAnchorIdx != nil {
		data["history_anchor_idx"] = *historyAnchorIdx
	}
	if messageCount != nil {
		data["message_count"] = *messageCount
	}
	event := NewAgentEvent(EventLLMTurnComplete, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitToolCallStarted(streamID, nodeID, toolUseID, toolName string, toolInput map[string]any, executionID *string) {
	event := NewAgentEvent(EventToolCallStarted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"tool_input":  orEmptyMap(toolInput),
	}
	b.Publish(event)
}

func (b *EventBus) EmitToolCallCompleted(streamID, nodeID, toolUseID, toolName, result string, isError bool, executionID *string) {
	event := NewAgentEvent(EventToolCallCompleted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"tool_use_id": toolUseID,
		"tool_name":   toolName,
		"result":      result,
		"is_error":    isError,
	}
	b.Publish(event)
}

func (b *EventBus) EmitClientOutputDelta(streamID, nodeID, content, snapshot string, executionID *string, iteration *int, innerTurn int) {
	data := map[string]any{"content": content, "snapshot": snapshot, "inner_turn": innerTurn}
	if iteration != nil {
		data["iteration"] = *iteration
	}
	event := NewAgentEvent(EventClientOutputDelta, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitClientReasoning(streamID, nodeID, reasoning string, executionID *string, iteration *int, innerTurn int) {
	data := map[string]any{"reasoning": reasoning, "inner_turn": innerTurn}
	if iteration != nil {
		data["iteration"] = *iteration
	}
	event := NewAgentEvent(EventClientReasoning, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitClientInputRequested(streamID, nodeID string, executionID *string, questions []map[string]any, parkReason *string) {
	data := map[string]any{}
	if len(questions) > 0 {
		data["questions"] = questions
	}
	if parkReason != nil && *parkReason != "" {
		data["park_reason"] = *parkReason
	}
	event := NewAgentEvent(EventClientInputRequested, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitCredentialFormRequested(streamID, nodeID string, executionID *string, form map[string]any) {
	copied := make(map[string]any, len(form))
	for k, v := range form {
		copied[k] = v
	}
	event := NewAgentEvent(EventClientCredentialFormRequested, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = copied
	b.Publish(event)
}

func (b *EventBus) EmitClientInputCommitted(streamID, nodeID string, executionID *string, seq int, correlationID *string) {
	event := NewAgentEvent(EventClientInputCommitted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.CorrelationID = correlationID
	event.Data = map[string]any{"seq": seq}
	b.Publish(event)
}

func (b *EventBus) EmitColonySuggestionRequested(
	streamID, nodeID string,
	executionID *string,
	colonyID string,
	reason, sourceSessionID, sourcePhase, goal, handoff *string,
	taskCount *int,
) {
	data := map[string]any{"colony_id": colonyID}
	if reason != nil && *reason != "" {
		data["reason"] = *reason
	}
	if sourceSessionID != nil && *sourceSessionID != "" {
		data["source_session_id"] = *sourceSessionID
	}
	if sourcePhase != nil && *sourcePhase != "" {
		data["source_phase"] = *sourcePhase
	}
	if goal != nil && *goal != "" {
		data["goal"] = *goal
	}
	if handoff != nil && *handoff != "" {
		data["handoff"] = *handoff
	}
	if taskCount != nil {
		data["task_count"] = *taskCount
	}
	event := NewAgentEvent(EventColonySuggestionRequested, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitNodeInternalOutput(streamID, nodeID, content string, executionID *string) {
	event := NewAgentEvent(EventNodeInternalOutput, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"content": content}
	b.Publish(event)
}

func (b *EventBus) EmitNodeStalled(streamID, nodeID, reason string, executionID *string) {
	event := NewAgentEvent(EventNodeStalled, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"reason": reason}
	b.Publish(event)
}

func (b *EventBus) EmitToolDoomLoop(streamID, nodeID, description string, executionID *string) {
	event := NewAgentEvent(EventNodeToolDoomLoop, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"description": description}
	b.Publish(event)
}

func (b *EventBus) EmitNodeInputBlocked(streamID, nodeID, prompt string, executionID *string) {
	event := NewAgentEvent(EventNodeInputBlocked, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"prompt": prompt}
	b.Publish(event)
}

func (b *EventBus) EmitJudgeVerdict(streamID, nodeID, action, feedback, judgeType string, iteration int, executionID *string) {
	event := NewAgentEvent(EventJudgeVerdict, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"action":     action,
		"feedback":   feedback,
		"judge_type": judgeType,
		"iteration":  iteration,
	}
	b.Publish(event)
}

func (b *EventBus) EmitNodeRetry(streamID, nodeID string, retryCount, maxRetries int, errMsg string, executionID *string) {
	event := NewAgentEvent(EventNodeRetry, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"retry_count": retryCount,
		"max_retries": maxRetries,
		"error":       errMsg,
	}
	b.Publish(event)
}

func (b *EventBus) EmitStreamTTFTExceeded(streamID, nodeID string, ttftSeconds, limitSeconds float64, executionID *string) {
	event := NewAgentEvent(EventStreamTTFTExceeded, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"ttft_seconds":  ttftSeconds,
		"limit_seconds": limitSeconds,
	}
	b.Publish(event)
}

func (b *EventBus) EmitStreamInactive(streamID, nodeID string, idleSeconds, limitSeconds float64, executionID *string) {
	event := NewAgentEvent(EventStreamInactive, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"idle_seconds":  idleSeconds,
		"limit_seconds": limitSeconds,
	}
	b.Publish(event)
}

func (b *EventBus) EmitLoopStateChanged(streamID, nodeID, activity string, executionID *string, parkReason, interruptCause *string, questions []map[string]any) {
	var questionsValue any
	if len(questions) > 0 {
		questionsValue = questions
	}
	event := NewAgentEvent(EventLoopStateChanged, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"activity":        activity,
		"park_reason":     stringOrNil(parkReason),
		"interrupt_cause": stringOrNil(interruptCause),
		"questions":       questionsValue,
	}
	b.Publish(event)
}

func (b *EventBus) EmitStreamNudgeSent(streamID, nodeID, reason string, nudgeCount int, executionID *string) {
	event := NewAgentEvent(EventStreamNudgeSent, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"reason":      reason,
		"nudge_count": nudgeCount,
	}
	b.Publish(event)
}

func (b *EventBus) EmitReminderInjected(streamID, nodeID, source, detail string, meta map[string]any, executionID *string) {
	event := NewAgentEvent(EventReminderInjected, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"source": source,
		"detail": detail,
		"meta":   orEmptyMap(meta),
	}
	b.Publish(event)
}

func (b *EventBus) EmitToolCallReplayDetected(streamID, nodeID, toolName string, priorSeq int, executionID *string) {
	event := NewAgentEvent(EventToolCallReplayDetected, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"tool_name": toolName,
		"prior_seq": priorSeq,
	}
	b.Publish(event)
}

func (b *EventBus) EmitWorkerCompleted(streamID, nodeID, workerID string, success bool, output map[string]any, activations []map[string]any, executionID *string, extraData map[string]any) {
	data := map[string]any{
		"worker_id":   workerID,
		"success":     success,
		"output":      output,
		"activations": orEmptyActivations(activations),
	}
	for k, v := range extraData {
		data[k] = v
	}
	event := NewAgentEvent(EventWorkerCompleted, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = data
	b.Publish(event)
}

func (b *EventBus) EmitWorkerFailed(streamID, nodeID, workerID, errMsg string, executionID *string) {
	event := NewAgentEvent(EventWorkerFailed, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"worker_id": workerID,
		"error":     errMsg,
	}
	b.Publish(event)
}

func (b *EventBus) EmitExecutionPaused(streamID, nodeID, reason string, executionID *string) {
	event := NewAgentEvent(EventExecutionPaused, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{"reason": reason}
	b.Publish(event)
}

func (b *EventBus) EmitExecutionResumed(streamID, nodeID string, executionID *string) {
	event := NewAgentEvent(EventExecutionResumed, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{}
	b.Publish(event)
}

func (b *EventBus) EmitWebhookReceived(sourceID, path, method string, headers map[string]string, payload any, queryParams map[string]string) {
	event := NewAgentEvent(EventWebhookReceived, sourceID)
	event.Data = map[string]any{
		"path":         path,
		"method":       method,
		"headers":      orEmptyMapStringString(headers),
		"payload":      payload,
		"query_params": orEmptyMapStringString(queryParams),
	}
	b.Publish(event)
}

func (b *EventBus) EmitEscalationRequested(streamID, nodeID, reason, context string, executionID, requestID *string) {
	event := NewAgentEvent(EventEscalationRequested, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"request_id": stringOrNil(requestID),
		"reason":     reason,
		"context":    context,
	}
	b.Publish(event)
}

func (b *EventBus) EmitSubagentReport(streamID, nodeID, subagentID, message string, data map[string]any, executionID *string) {
	var dataValue any
	if data != nil {
		dataValue = data
	}
	event := NewAgentEvent(EventSubagentReport, streamID)
	event.NodeID = StrPtr(nodeID)
	event.ExecutionID = executionID
	event.Data = map[string]any{
		"subagent_id": subagentID,
		"message":     message,
		"data":        dataValue,
	}
	b.Publish(event)
}

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
