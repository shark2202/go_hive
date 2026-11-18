package eventbus

var eventTypeMembers = []struct {
	Name  string
	Value EventType
}{
	{"EXECUTION_STARTED", EventExecutionStarted},
	{"EXECUTION_COMPLETED", EventExecutionCompleted},
	{"EXECUTION_FAILED", EventExecutionFailed},
	{"EXECUTION_PAUSED", EventExecutionPaused},
	{"EXECUTION_RESUMED", EventExecutionResumed},
	{"PAYMENT_REQUIRED", EventPaymentRequired},
	{"STATE_CHANGED", EventStateChanged},
	{"STATE_CONFLICT", EventStateConflict},
	{"GOAL_PROGRESS", EventGoalProgress},
	{"GOAL_ACHIEVED", EventGoalAchieved},
	{"CONSTRAINT_VIOLATION", EventConstraintViolation},
	{"STREAM_STARTED", EventStreamStarted},
	{"STREAM_STOPPED", EventStreamStopped},
	{"NODE_LOOP_STARTED", EventNodeLoopStarted},
	{"NODE_LOOP_ITERATION", EventNodeLoopIteration},
	{"NODE_LOOP_COMPLETED", EventNodeLoopCompleted},
	{"LLM_TEXT_DELTA", EventLLMTextDelta},
	{"LLM_REASONING_DELTA", EventLLMReasoningDelta},
	{"LLM_TURN_COMPLETE", EventLLMTurnComplete},
	{"TOOL_CALL_STARTED", EventToolCallStarted},
	{"TOOL_CALL_COMPLETED", EventToolCallCompleted},
	{"CLIENT_OUTPUT_DELTA", EventClientOutputDelta},
	{"CLIENT_REASONING", EventClientReasoning},
	{"CLIENT_INPUT_REQUESTED", EventClientInputRequested},
	{"CLIENT_INPUT_RECEIVED", EventClientInputReceived},
	{"CLIENT_INPUT_COMMITTED", EventClientInputCommitted},
	{"CLIENT_CREDENTIAL_FORM_REQUESTED", EventClientCredentialFormRequested},
	{"COLONY_SUGGESTION_REQUESTED", EventColonySuggestionRequested},
	{"SKILL_INSTALL_REQUESTED", EventSkillInstallRequested},
	{"SKILL_INSTALL_DECIDED", EventSkillInstallDecided},
	{"NODE_INTERNAL_OUTPUT", EventNodeInternalOutput},
	{"NODE_INPUT_BLOCKED", EventNodeInputBlocked},
	{"NODE_STALLED", EventNodeStalled},
	{"NODE_TOOL_DOOM_LOOP", EventNodeToolDoomLoop},
	{"JUDGE_VERDICT", EventJudgeVerdict},
	{"NODE_RETRY", EventNodeRetry},
	{"STREAM_TTFT_EXCEEDED", EventStreamTTFTExceeded},
	{"STREAM_INACTIVE", EventStreamInactive},
	{"STREAM_NUDGE_SENT", EventStreamNudgeSent},
	{"LOOP_STATE_CHANGED", EventLoopStateChanged},
	{"REMINDER_INJECTED", EventReminderInjected},
	{"TOOL_CALL_REPLAY_DETECTED", EventToolCallReplayDetected},
	{"WORKER_COMPLETED", EventWorkerCompleted},
	{"WORKER_FAILED", EventWorkerFailed},
	{"CONTEXT_COMPACTION_STARTED", EventContextCompactionStarted},
	{"CONTEXT_COMPACTED", EventContextCompacted},
	{"CONTEXT_USAGE_UPDATED", EventContextUsageUpdated},
	{"WEBHOOK_RECEIVED", EventWebhookReceived},
	{"CUSTOM", EventCustom},
	{"ESCALATION_REQUESTED", EventEscalationRequested},
	{"EXECUTION_RESURRECTED", EventExecutionResurrected},
	{"WORKER_COLONY_LOADED", EventWorkerColonyLoaded},
	{"COLONY_CREATED", EventColonyCreated},
	{"CREDENTIALS_REQUIRED", EventCredentialsRequired},
	{"SESSION_FORKED", EventSessionForked},
	{"SESSION_TITLE_UPDATED", EventSessionTitleUpdated},
	{"QUEEN_PHASE_CHANGED", EventQueenPhaseChanged},
	{"QUEEN_IDENTITY_SELECTED", EventQueenIdentitySelected},
	{"SUBAGENT_REPORT", EventSubagentReport},
	{"TRIGGER_AVAILABLE", EventTriggerAvailable},
	{"TRIGGER_ACTIVATED", EventTriggerActivated},
	{"TRIGGER_DEACTIVATED", EventTriggerDeactivated},
	{"TRIGGER_FIRED", EventTriggerFired},
	{"TRIGGER_REMOVED", EventTriggerRemoved},
	{"TRIGGER_UPDATED", EventTriggerUpdated},
	{"MISSED_TRIGGERS", EventMissedTriggers},
	{"TASK_CREATED", EventTaskCreated},
	{"TASK_UPDATED", EventTaskUpdated},
	{"TASK_DELETED", EventTaskDeleted},
	{"TASK_LIST_RESET", EventTaskListReset},
	{"TASK_LIST_REATTACH_MISMATCH", EventTaskListReattachMismatch},
	{"SESSION_SNAPSHOT", EventSessionSnapshot},
	{"CREDENTIAL_PROVIDER_CONNECTED", EventCredentialProviderConnected},
	{"CREDENTIAL_PROVIDER_DISCONNECTED", EventCredentialProviderDisconnected},
	{"TOOL_CATALOG_REFRESHED", EventToolCatalogRefreshed},
	{"TOOLS_CONFIG_CHANGED", EventToolsConfigChanged},
	{"CRM_CHANGED", EventCRMChanged},
}

var (
	eventTypesByName = buildEventTypesByName()
	eventTypeValues  = buildEventTypeValues()
)

func buildEventTypesByName() map[string]EventType {
	m := make(map[string]EventType, len(eventTypeMembers))
	for _, member := range eventTypeMembers {
		m[member.Name] = member.Value
	}
	return m
}

func buildEventTypeValues() map[EventType]struct{} {
	m := make(map[EventType]struct{}, len(eventTypeMembers))
	for _, member := range eventTypeMembers {
		m[member.Value] = struct{}{}
	}
	return m
}

func (t EventType) String() string { return string(t) }

func (t EventType) Valid() bool {
	_, ok := eventTypeValues[t]
	return ok
}

func LookupEventType(value string) (EventType, bool) {
	t := EventType(value)
	if _, ok := eventTypeValues[t]; ok {
		return t, true
	}
	return "", false
}

func EventTypeByName(name string) (EventType, bool) {
	t, ok := eventTypesByName[name]
	return t, ok
}
