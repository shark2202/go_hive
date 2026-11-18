package eventbus

type EventType string

const (
	EventExecutionStarted     EventType = "execution_started"
	EventExecutionCompleted   EventType = "execution_completed"
	EventExecutionFailed      EventType = "execution_failed"
	EventExecutionPaused      EventType = "execution_paused"
	EventExecutionResumed     EventType = "execution_resumed"
	EventExecutionResurrected EventType = "execution_resurrected"
)

const EventPaymentRequired EventType = "payment_required"

const (
	EventStateChanged  EventType = "state_changed"
	EventStateConflict EventType = "state_conflict"
)

const (
	EventGoalProgress        EventType = "goal_progress"
	EventGoalAchieved        EventType = "goal_achieved"
	EventConstraintViolation EventType = "constraint_violation"
)

const (
	EventStreamStarted EventType = "stream_started"
	EventStreamStopped EventType = "stream_stopped"
)

const (
	EventNodeLoopStarted   EventType = "node_loop_started"
	EventNodeLoopIteration EventType = "node_loop_iteration"
	EventNodeLoopCompleted EventType = "node_loop_completed"
)

const (
	EventLLMTextDelta      EventType = "llm_text_delta"
	EventLLMReasoningDelta EventType = "llm_reasoning_delta"
	EventLLMTurnComplete   EventType = "llm_turn_complete"
)

const (
	EventToolCallStarted   EventType = "tool_call_started"
	EventToolCallCompleted EventType = "tool_call_completed"
)

const (
	EventClientOutputDelta             EventType = "client_output_delta"
	EventClientReasoning               EventType = "client_reasoning"
	EventClientInputRequested          EventType = "client_input_requested"
	EventClientInputReceived           EventType = "client_input_received"
	EventClientInputCommitted          EventType = "client_input_committed"
	EventClientCredentialFormRequested EventType = "client_credential_form_requested"
	EventColonySuggestionRequested     EventType = "colony_suggestion_requested"
	EventSkillInstallRequested         EventType = "skill_install_requested"
	EventSkillInstallDecided           EventType = "skill_install_decided"
)

const (
	EventNodeInternalOutput EventType = "node_internal_output"
	EventNodeInputBlocked   EventType = "node_input_blocked"
	EventNodeStalled        EventType = "node_stalled"
	EventNodeToolDoomLoop   EventType = "node_tool_doom_loop"
)

const EventJudgeVerdict EventType = "judge_verdict"

const EventNodeRetry EventType = "node_retry"

const (
	EventStreamTTFTExceeded EventType = "stream_ttft_exceeded"
	EventStreamInactive     EventType = "stream_inactive"
	EventStreamNudgeSent    EventType = "stream_nudge_sent"
)

const EventLoopStateChanged EventType = "loop_state_changed"

const EventReminderInjected EventType = "reminder_injected"

const EventToolCallReplayDetected EventType = "tool_call_replay_detected"

const (
	EventWorkerCompleted EventType = "worker_completed"
	EventWorkerFailed    EventType = "worker_failed"
)

const (
	EventContextCompactionStarted EventType = "context_compaction_started"
	EventContextCompacted         EventType = "context_compacted"
	EventContextUsageUpdated      EventType = "context_usage_updated"
)

const EventWebhookReceived EventType = "webhook_received"

const EventCustom EventType = "custom"

const EventEscalationRequested EventType = "escalation_requested"

const (
	EventWorkerColonyLoaded  EventType = "worker_colony_loaded"
	EventColonyCreated       EventType = "colony_created"
	EventCredentialsRequired EventType = "credentials_required"
)

const EventSessionForked EventType = "session_forked"

const EventSessionTitleUpdated EventType = "session_title_updated"

const EventQueenPhaseChanged EventType = "queen_phase_changed"

const EventQueenIdentitySelected EventType = "queen_identity_selected"

const EventSubagentReport EventType = "subagent_report"

const (
	EventTriggerAvailable   EventType = "trigger_available"
	EventTriggerActivated   EventType = "trigger_activated"
	EventTriggerDeactivated EventType = "trigger_deactivated"
	EventTriggerFired       EventType = "trigger_fired"
	EventTriggerRemoved     EventType = "trigger_removed"
	EventTriggerUpdated     EventType = "trigger_updated"
	EventMissedTriggers     EventType = "missed_triggers"
)

const (
	EventTaskCreated              EventType = "task_created"
	EventTaskUpdated              EventType = "task_updated"
	EventTaskDeleted              EventType = "task_deleted"
	EventTaskListReset            EventType = "task_list_reset"
	EventTaskListReattachMismatch EventType = "task_list_reattach_mismatch"
)

const EventSessionSnapshot EventType = "session_snapshot"

const (
	EventCredentialProviderConnected    EventType = "credential_provider_connected"
	EventCredentialProviderDisconnected EventType = "credential_provider_disconnected"
	EventToolCatalogRefreshed           EventType = "tool_catalog_refreshed"
	EventToolsConfigChanged             EventType = "tools_config_changed"
	EventCRMChanged                     EventType = "crm_changed"
)

var AllEventTypes = []EventType{
	EventExecutionStarted,
	EventExecutionCompleted,
	EventExecutionFailed,
	EventExecutionPaused,
	EventExecutionResumed,
	EventExecutionResurrected,
	EventPaymentRequired,
	EventStateChanged,
	EventStateConflict,
	EventGoalProgress,
	EventGoalAchieved,
	EventConstraintViolation,
	EventStreamStarted,
	EventStreamStopped,
	EventNodeLoopStarted,
	EventNodeLoopIteration,
	EventNodeLoopCompleted,
	EventLLMTextDelta,
	EventLLMReasoningDelta,
	EventLLMTurnComplete,
	EventToolCallStarted,
	EventToolCallCompleted,
	EventClientOutputDelta,
	EventClientReasoning,
	EventClientInputRequested,
	EventClientInputReceived,
	EventClientInputCommitted,
	EventClientCredentialFormRequested,
	EventColonySuggestionRequested,
	EventSkillInstallRequested,
	EventSkillInstallDecided,
	EventNodeInternalOutput,
	EventNodeInputBlocked,
	EventNodeStalled,
	EventNodeToolDoomLoop,
	EventJudgeVerdict,
	EventNodeRetry,
	EventStreamTTFTExceeded,
	EventStreamInactive,
	EventStreamNudgeSent,
	EventLoopStateChanged,
	EventReminderInjected,
	EventToolCallReplayDetected,
	EventWorkerCompleted,
	EventWorkerFailed,
	EventContextCompactionStarted,
	EventContextCompacted,
	EventContextUsageUpdated,
	EventWebhookReceived,
	EventCustom,
	EventEscalationRequested,
	EventWorkerColonyLoaded,
	EventColonyCreated,
	EventCredentialsRequired,
	EventSessionForked,
	EventSessionTitleUpdated,
	EventQueenPhaseChanged,
	EventQueenIdentitySelected,
	EventSubagentReport,
	EventTriggerAvailable,
	EventTriggerActivated,
	EventTriggerDeactivated,
	EventTriggerFired,
	EventTriggerRemoved,
	EventTriggerUpdated,
	EventMissedTriggers,
	EventTaskCreated,
	EventTaskUpdated,
	EventTaskDeleted,
	EventTaskListReset,
	EventTaskListReattachMismatch,
	EventSessionSnapshot,
	EventCredentialProviderConnected,
	EventCredentialProviderDisconnected,
	EventToolCatalogRefreshed,
	EventToolsConfigChanged,
	EventCRMChanged,
}
