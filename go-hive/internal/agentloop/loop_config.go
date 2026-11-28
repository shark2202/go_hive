package agentloop

import "context"

// Parity: framework/agent_loop/agent_loop.py LoopConfig (defaults mirror
type LoopConfig struct {
	MaxIterations        int
	MaxContextTokens     int
	ToolWaitLimitSeconds float64
	// CompactionBufferTokens and CompactionBufferRatio are the hybrid
	// compaction headroom: a compaction fires once the conversation estimate
	// reaches MaxContextTokens - (CompactionBufferTokens +
	// CompactionBufferRatio*MaxContextTokens). The defaults mirror upstream's
	// LoopConfig (8k absolute + 40% of the window, i.e. a ~100k trigger on the
	// 180k default). Setting both to zero keeps the legacy multiplicative
	// CompactionThreshold rule.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.compaction_buffer_tokens,
	// ::LoopConfig.compaction_buffer_ratio
	CompactionBufferTokens int
	CompactionBufferRatio  float64
	// CompactionWarningBufferTokens emits the "context getting tight" warning
	// one buffer before the hard trigger; zero disables the warning.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.compaction_warning_buffer_tokens
	CompactionWarningBufferTokens int
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
	// JudgeEveryNTurns is the cadence for judge evaluation.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.judge_every_n_turns.
	JudgeEveryNTurns int
	// StallDetectionThreshold is the number of consecutive assistant responses
	// considered for the response-text stall guard. Zero falls back to the
	// upstream default (3).
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.stall_detection_threshold.
	StallDetectionThreshold int
	// StallSimilarityThreshold is the character-bigram Jaccard floor for the
	// response-text stall guard. Zero falls back to the upstream default
	// (0.85).
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.stall_similarity_threshold.
	StallSimilarityThreshold float64
	// SilentToolStreakThreshold is the number of consecutive tool-only turns
	// (tool calls, no assistant text) after which the loop injects the
	// "explain what you are doing" nudge. Each multiple of the threshold
	// nudges again. Zero falls back to the upstream default (5).
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.silent_tool_streak_threshold.
	SilentToolStreakThreshold int
	// MaxRetainedScreenshots is how many of the newest screenshot-bearing
	// messages keep their image content after a tool batch; older ones are
	// evicted (text metadata stays). A negative value disables eviction, as
	// upstream's `max_retained_screenshots >= 0` gate does. Zero (an unset
	// field on a partially-built config) falls back to the upstream default
	// (2), like the loop's other zero-means-default accessors.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.max_retained_screenshots.
	MaxRetainedScreenshots int

	// ReplayDetectorEnabled turns on the two-tier replay guard that runs
	// BEFORE dispatch: a soft "[Replay detected]" steer below the threshold,
	// and the hard breaker at it (refuse the call, stub the batch, park as
	// DOOM_LOOP). DefaultLoopConfig enables it, matching upstream; a
	// zero-value LoopConfig leaves it off, like the loop's other default-on
	// booleans.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_detector_enabled.
	ReplayDetectorEnabled bool
	// ReplayDetectorWithinLastTurns is how many recent assistant turns the
	// cross-turn half of the streak walks. Zero falls back to the upstream
	// default (3).
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_detector_within_last_turns.
	ReplayDetectorWithinLastTurns int
	// ReplayExemptTools is the exemption table shared by the replay breaker
	// and the doom-loop fingerprint guard: tools the agent legitimately calls
	// repeatedly with identical args — async-job polls, idempotent reads,
	// status/observe calls and synthetic control tools. Counting them is a
	// false positive (a healthy job poll would park the turn at five reads).
	// Mutating tools (edits, sends, terminal_exec) deliberately stay guarded.
	// Nil falls back to DefaultReplayExemptTools(); an explicit empty map
	// disables the exemptions.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.replay_exempt_tools.
	ReplayExemptTools map[string]struct{}

	// MaxStreamRetries, StreamRetryBackoffBase and StreamRetryMaxDelay mirror
	// the upstream LoopConfig fields of the same names. Go's stream path
	// recovers stalls with the continue-nudge loop instead of a retry count,
	// so today they drive the judge seam's retry budget (upstream
	// _judge_turn): max(1, MaxStreamRetries) attempts with
	// min(base*2**attempt, max delay) waits, then the fail-open ACCEPT.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.max_stream_retries,
	// ::LoopConfig.stream_retry_backoff_base, ::LoopConfig.stream_retry_max_delay
	MaxStreamRetries       int
	StreamRetryBackoffBase float64
	StreamRetryMaxDelay    float64
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
	// BackgroundTools names the tools the loop runs in the BACKGROUND: the
	// call returns a bg_* handle immediately and the agent collects the real
	// result later via the synthetic collect_result tool. For tools whose work
	// legitimately exceeds the per-call timeout (image generation can take
	// minutes) this keeps the loop — and any shared MCP server behind the
	// executor — unblocked. A nil table means the upstream default set
	// (image_generate, terminal_exec); an explicit empty table disables
	// backgrounding, mirroring ReplayExemptTools.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.background_tools
	BackgroundTools map[string]struct{}
	// BackgroundToolTimeoutSeconds is the real-call budget for a backgrounded
	// tool. Must stay below tools.MCPCallResultTimeout, the same invariant as
	// ToolTimeoutOverrides. Zero falls back to the upstream 235s default.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.background_tool_timeout_seconds
	BackgroundToolTimeoutSeconds float64
	// BackgroundToolGraceSeconds is the inline window before a background
	// tool actually backgrounds: work that finishes inside it returns on the
	// original call and never mints a handle, so quick work pays nothing.
	// Zero (the struct zero value) backgrounds immediately; DefaultLoopConfig
	// ships the upstream 5s window.
	//
	// Parity: framework/agent_loop/internals/types.py::LoopConfig.background_tool_grace_seconds
	BackgroundToolGraceSeconds float64

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
		MaxIterations:                 50,
		MaxContextTokens:              180_000,
		CompactionBufferTokens:        8_000,
		CompactionBufferRatio:         0.4,
		CompactionWarningBufferTokens: 12_000,
		ToolWaitLimitSeconds:          600,
		GraceIterations:               0,
		// Worker stall grace (upstream default): two text-only turns are
		// tolerated, the third consecutive one auto-fails the worker.
		WorkerEscalationGraceTurns: DefaultWorkerEscalationGraceTurns,
		// Generic tool-call envelope: a soft checkpoint every 30 calls in
		// one turn-loop, a hard stop at 50. Role profiles override it:
		// workers run 30/90 (BuildWorkerLoopConfig), the queen 30/150
		// (QueenLoopConfig) and the colony queen 50/500
		// (ColonyQueenLoopConfig).
		ToolCallBudget:            30,
		ToolCallHardLimit:         50,
		ToolCallHardMultiple:      5,
		ToolCallLifetimeBudget:    0,
		JudgeEveryNTurns:          1,
		StallDetectionThreshold:   DefaultStallDetectionThreshold,
		StallSimilarityThreshold:  DefaultStallSimilarityThreshold,
		SilentToolStreakThreshold: DefaultSilentToolStreakThreshold,
		MaxRetainedScreenshots:    DefaultMaxRetainedScreenshots,
		// Replay guard (upstream defaults): the breaker trips at the same
		// tool_doom_loop_threshold the turn-level detector warns at, and the
		// exemption table keeps polls/reads out of both guards.
		ReplayDetectorEnabled:         true,
		ReplayDetectorWithinLastTurns: DefaultReplayDetectorWithinLastTurns,
		ReplayExemptTools:             DefaultReplayExemptTools(),
		// Upstream LoopConfig's stream-retry trio; the judge seam consumes
		// them for its attempt budget and backoff.
		MaxStreamRetries:                5,
		StreamRetryBackoffBase:          2.0,
		StreamRetryMaxDelay:             60.0,
		SessionIdleNudgeSeconds:         120.0,
		SessionIdleNudgeMaxPerSession:   3,
		SessionIdleNudgeAwaitingSeconds: 120.0,
		SessionIdleNudgeBrokenSeconds:   30.0,
		ContinueNudgeEnabled:            true,
		ContinueNudgeMaxPerTurn:         3,
		// Split inactivity watchdog: a slow first token is not a dead
		// connection, so the TTFT budget is the looser of the two.
		LLMStreamTTFTTimeoutSeconds:       DefaultLLMStreamTTFTTimeoutSeconds,
		LLMStreamInterEventIdleSeconds:    DefaultLLMStreamInterEventIdleSeconds,
		LLMStreamInactivityTimeoutSeconds: DefaultLLMStreamInactivityTimeoutSeconds,
		ToolCallTimeoutSeconds:            60.0,
		// Background tools run off the turn's critical path: image_generate
		// and terminal_exec return a bg_* handle after
		// BackgroundToolGraceSeconds, and collect_result waits for the real
		// result. The real call keeps the upstream 235s budget, below the MCP
		// call-result ceiling.
		//
		// Parity: framework/agent_loop/internals/types.py::LoopConfig.background_tools,
		// ::LoopConfig.background_tool_timeout_seconds,
		// ::LoopConfig.background_tool_grace_seconds
		BackgroundTools:              DefaultBackgroundTools(),
		BackgroundToolTimeoutSeconds: DefaultBackgroundToolTimeoutSeconds,
		BackgroundToolGraceSeconds:   DefaultBackgroundToolGraceSeconds,
		ToolTimeoutOverrides:         map[string]float64{"browser_": 180.0},
		// Spend cap: opt-in, read from HIVE_SESSION_COST_LIMIT_USD once per
		// config build. Unset means 0 (disabled), matching the shipped default.
		SessionCostLimitUSD: SessionCostLimitFromEnv(),
		// Match upstream: safe (explicitly marked) tools may run together.
		// The loop still keeps a batch sequential when any call is unmarked.
		ParallelReadOnlyTools: true,
		MaxToolResultChars:    30_000,
	}
}

// QueenLoopConfig is the independent queen's loop profile: an effectively
// unbounded autonomous loop with upstream's 30/150 tool-call envelope
// (budget 30, hard stop 30 * 5).
//
// Parity: core/framework/agents/queen/agent.py::queen_loop_config
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func QueenLoopConfig() LoopConfig {
	config := DefaultLoopConfig()
	config.MaxIterations = 999_999
	config.ToolCallBudget = 30
	// Zero keeps the upstream multiple rule: the hard stop is
	// ToolCallBudget * ToolCallHardMultiple = 150.
	config.ToolCallHardLimit = 0
	config.ToolCallHardMultiple = 5
	config.GraceIterations = 0
	config.ToolCallLifetimeBudget = 0
	return config
}

// ColonyQueenLoopConfig is the colony queen's (overseer) loop profile: the
// same autonomous loop as an independent queen, but with the wider 50/500
// tool-call envelope upstream selects when effective_phase == "colony".
//
// Parity: core/framework/agents/queen/agent.py::queen_colony_loop_config
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func ColonyQueenLoopConfig() LoopConfig {
	config := QueenLoopConfig()
	config.ToolCallBudget = 50
	config.ToolCallHardMultiple = 10
	return config
}
