package agentloop

import "context"

// Parity: framework/agent_loop/agent_loop.py LoopConfig (defaults mirror
type LoopConfig struct {
	MaxIterations        int
	MaxContextTokens     int
	ToolWaitLimitSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.grace_iterations.
	GraceIterations int
	// WorkerEscalationGraceTurns is the number of consecutive text-only turns
	// (no tool calls) a worker may produce before the loop auto-fails it: a
	// parallel worker gets a synthesized report_to_parent(status='failed') and
	// terminates, the legacy primary worker escalates and parks. Zero (an
	// unset field on a partially-built config) or a negative value falls back
	// to DefaultWorkerEscalationGraceTurns (2, the upstream default); the
	// minimum meaningful configured value is 1.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.worker_escalation_grace_turns.
	WorkerEscalationGraceTurns int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_call_budget.
	ToolCallBudget int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_call_hard_multiple.
	ToolCallHardMultiple int
	// ToolCallHardLimit, when > 0, is the absolute per-turn-loop hard stop. It
	// overrides ToolCallBudget*ToolCallHardMultiple so a role profile can pin an
	// exact cap (the queen's 30 soft / 50 hard, the audit's numbers); zero keeps
	// the upstream multiple rule. Ignored when ToolCallBudget is 0.
	ToolCallHardLimit int
	// Parity: framework/agent_loop/internals/types.py
	ToolCallLifetimeBudget int
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.judge_every_n_turns.
	JudgeEveryNTurns int
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeSeconds float64
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeMaxPerSession int
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeAwaitingSeconds float64
	// Parity: framework/agent_loop/internals/types.py
	SessionIdleNudgeBrokenSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.continue_nudge_enabled.
	ContinueNudgeEnabled bool
	// Parity: framework/agent_loop/internals/types.py
	ContinueNudgeMaxPerTurn int
	// LLMStreamTTFTTimeoutSeconds bounds the wait for the first stream event of
	// a completion call. Zero disables this budget.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.llm_stream_ttft_timeout_seconds.
	LLMStreamTTFTTimeoutSeconds float64
	// LLMStreamInterEventIdleSeconds bounds the silence between stream events
	// once the first one has arrived. Zero disables this budget.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.llm_stream_inter_event_idle_seconds.
	LLMStreamInterEventIdleSeconds float64
	// LLMStreamInactivityTimeoutSeconds is the deprecated alias kept so old
	// configs keep working: a non-zero value below the inter-event budget
	// overrides it (historical behaviour).
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.llm_stream_inactivity_timeout_seconds.
	LLMStreamInactivityTimeoutSeconds float64
	// Parity: framework/agent_loop/internals/types.py
	ToolCallTimeoutSeconds float64
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.tool_timeout_overrides.
	// ToolTimeoutOverrides maps a tool-name prefix to its timeout.
	ToolTimeoutOverrides map[string]float64
	// ToolCallGate paces each tool execution against the configured call
	// limits (rate_limits.tools.* in hive.config.json). NewAgentLoop installs
	// the process-wide limiter when this is nil; it is a no-op until a bucket
	// is configured. A non-nil no-op disables pacing for a loop.
	ToolCallGate func(ctx context.Context, toolName string) error
	// SessionCostLimitUSD is the optional hard cap on cumulative session
	// spend. Zero (the default) disables it. Once the loop's recorded spend
	// reaches the cap, the next ExecuteTurn returns *SessionCostLimitError
	// before any model call.
	SessionCostLimitUSD float64
	// ParallelReadOnlyTools runs a tool batch concurrently when every call in
	// it is explicitly marked ConcurrencySafe (llm.ToolDef.ConcurrencySafe).
	// DefaultLoopConfig enables it, matching upstream; an unsafe or unmarked
	// call keeps the whole batch sequential either way.
	ParallelReadOnlyTools bool
	// MaxToolResultChars caps the LLM-visible tool result. A result above it is
	// replaced with a preview while the full payload is written to
	// SpilloverDir. Zero disables the cap.
	MaxToolResultChars int
	// SpilloverDir, when set, stores an oversized non-error tool result on disk
	// under a monotonic filename; the model-visible result then cites the file
	// so the full payload can be read back.
	SpilloverDir string
}

// Parity: framework/agent_loop/internals/types.py LoopConfig defaults.
func DefaultLoopConfig() LoopConfig {
	return LoopConfig{
		MaxIterations:        50,
		MaxContextTokens:     180_000,
		ToolWaitLimitSeconds: 600,
		GraceIterations:      0,
		// Worker stall grace (upstream default): two text-only turns are
		// tolerated, the third consecutive one auto-fails the worker.
		WorkerEscalationGraceTurns: DefaultWorkerEscalationGraceTurns,
		// The queen profile: a soft checkpoint every 30 tool calls in one
		// turn-loop, a hard stop at 50. Upstream's queen runs the neutral
		// 0/unbounded default, so this is the port's fix for the audit gap
		// ("Queen 30/50 软检查点"): without it softBudgetCheckpointBlock never
		// fires in a chat session. Worker profiles override both fields.
		ToolCallBudget:                  30,
		ToolCallHardLimit:               50,
		ToolCallHardMultiple:            5,
		ToolCallLifetimeBudget:          0,
		JudgeEveryNTurns:                1,
		SessionIdleNudgeSeconds:         120.0,
		SessionIdleNudgeMaxPerSession:   3,
		SessionIdleNudgeAwaitingSeconds: 120.0,
		SessionIdleNudgeBrokenSeconds:   30.0,
		ContinueNudgeEnabled:            true,
		ContinueNudgeMaxPerTurn:         3,
		// Split inactivity watchdog: a slow first token is not a dead
		// connection, so the TTFT budget is the looser of the two.
		LLMStreamTTFTTimeoutSeconds:       600.0,
		LLMStreamInterEventIdleSeconds:    120.0,
		LLMStreamInactivityTimeoutSeconds: 120.0,
		ToolCallTimeoutSeconds:            60.0,
		ToolTimeoutOverrides:              map[string]float64{"browser_": 180.0},
		// Spend cap: opt-in, read from HIVE_SESSION_COST_LIMIT_USD once per
		// config build. Unset means 0 (disabled), matching the shipped default.
		SessionCostLimitUSD: SessionCostLimitFromEnv(),
		// Match upstream: safe (explicitly marked) tools may run together.
		// The loop still keeps a batch sequential when any call is unmarked.
		ParallelReadOnlyTools: true,
		MaxToolResultChars:    30_000,
	}
}
