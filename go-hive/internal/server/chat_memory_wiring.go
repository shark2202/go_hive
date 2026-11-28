package server

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
	"github.com/shark2202/go_hive/go-hive/internal/soul"
)

// chatShutdownGrace is the one budget a session teardown gives the work
// stopping a session starts: the final short reflection and the timeline flush.
// Upstream starts both fire-and-forget from stop_session and spends exactly this
// long at server exit before cancelling what is left
// (loader/cli.py::_SHUTDOWN_GRACE_S = 6.0,
// session_manager.finish_background_tasks(6s)); the port keeps the same single
// budget for one session, so a stop cannot take longer than the whole server
// used to get.
const chatShutdownGrace = 6 * time.Second

// chatTurnMaxTokens is the session's output budget: the main llm slot's
// max_tokens (config, then model catalog, then 8192). Without a home the
// 8192 default stands, so a bare test runtime keeps its old behaviour.
//
// Parity: framework/config.py::get_max_tokens,
// framework/server/queen_orchestrator.py (AgentContext max_tokens)
func chatTurnMaxTokens(homeRoot string) int {
	if root := strings.TrimSpace(homeRoot); root != "" {
		if home, err := hivehome.ResolveAt(root); err == nil {
			return LLMMaxTokens(home)
		}
	}
	return 8192
}

// chatLoopConfig is the queen loop's configuration: the queen profile (a
// 999_999-iteration autonomous loop with the 30/150 tool-call envelope), the
// session-scoped spillover directory large tool results are written to, the
// global loop.max_tool_result_chars override, and — for a queen with a model of
// her own — her max_context_tokens window.
//
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func chatLoopConfig(root, homeRoot, queenID string) agentloop.LoopConfig {
	config := agentloop.QueenLoopConfig()
	if root != "" {
		config.SpilloverDir = filepath.Join(root, "data")
	}
	if homeRoot != "" {
		if home, err := hivehome.ResolveAt(homeRoot); err == nil {
			config.MaxToolResultChars = LoopMaxToolResultChars(home, config.MaxToolResultChars)
			// A queen with her own section budgets by her model's window: a 32k
			// local model must not run with the 180k queen default.
			config.MaxContextTokens = QueenMaxContextTokens(home, queenID, config.MaxContextTokens)
		}
	}
	// HIVE_MAX_ITER caps the queen's reply loop for single-reply chat
	// deployments (upstream reads it in place of the 999_999 autonomous
	// default). Only a positive integer counts; anything else keeps the
	// queen profile's default.
	//
	// Parity: core/framework/server/queen_orchestrator.py (HIVE_MAX_ITER)
	if cap, ok := envPositiveInt("HIVE_MAX_ITER"); ok {
		config.MaxIterations = cap
	}
	// The LLM stream watchdog and the continue-nudge cap are operator knobs
	// upstream feeds from hive.config.json's `loop` section. The Go port keeps
	// its config surface env-based, so the same knobs can be tightened without
	// a rebuild (and end-to-end tests can use sub-second budgets).
	if value, ok := envFloat("HIVE_LLM_STREAM_TTFT_TIMEOUT_SECONDS"); ok {
		config.LLMStreamTTFTTimeoutSeconds = value
	}
	if value, ok := envFloat("HIVE_LLM_STREAM_INTER_EVENT_IDLE_SECONDS"); ok {
		config.LLMStreamInterEventIdleSeconds = value
		// The legacy alias defaults to the same 120s; keep it from overriding
		// an explicit inter-event budget.
		config.LLMStreamInactivityTimeoutSeconds = value
	}
	if value, ok := envIntValue("HIVE_CONTINUE_NUDGE_MAX_PER_TURN"); ok {
		config.ContinueNudgeMaxPerTurn = value
	}
	if value, ok := envBool("HIVE_CONTINUE_NUDGE_ENABLED"); ok {
		config.ContinueNudgeEnabled = value
	}
	return config
}

// envPositiveInt reads a positive integer environment variable. Unset, empty,
// non-numeric and non-positive values all report absent.
func envPositiveInt(name string) (int, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

// envFloat reads a positive float knob; an unset or malformed value leaves the
// caller's default in place.
func envFloat(name string) (float64, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

// envIntValue reads an int knob; zero is valid (it disables the feature), so
// only an unset or malformed value is rejected.
func envIntValue(name string) (int, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// envBool reads a boolean knob, accepting the spellings operators expect.
func envBool(name string) (bool, bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

// pastConversationRecall returns the dated excerpts of earlier conversations
// that match the user's message, or "" when nothing clears the relevance bar.
// It is the provider behind PastConversationsReminderSource, which the hub
// consults before the user message joins the conversation — so related
// history is in front of the model even when it does not call
// search_messages.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_past_conversations
// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource
func (r *ChatRuntime) pastConversationRecall(query string) string {
	if r == nil || r.deps.HomeRoot == "" || r.deps.Root == "" {
		return ""
	}
	text := strings.TrimSpace(query)
	if len([]rune(text)) < memory.PastRecallMinMessageChars {
		return ""
	}
	ref := memory.ScopeRef{Scope: memory.SearchScopeQueens, Owner: r.queenName}
	return memory.PastConversationRecallBlock(
		r.deps.HomeRoot,
		ref,
		text,
		filepath.Base(r.deps.Root),
	)
}

// startTimeline enables the per-session timeline writer when the feature flag
// is on and arms the startup backfill for earlier sessions.
//
// Parity: core/framework/agents/queen/timeline.py::subscribe_timeline_triggers
func (r *ChatRuntime) startTimeline() {
	if r == nil || r.deps.Root == "" || r.memoryWorkLLM() == nil {
		return
	}
	enabled := memory.MemoryTimelineEnabled()
	if r.deps.HomeRoot != "" {
		if home, err := hivehome.ResolveAt(r.deps.HomeRoot); err == nil {
			enabled = MemoryTimelineEnabled(home)
		}
	}
	if !enabled {
		return
	}
	r.timelineEnabled = true
	sessionsDir := filepath.Dir(r.deps.Root)
	live := filepath.Base(r.deps.Root)
	r.timelineWG.Add(1)
	go func() {
		defer r.timelineWG.Done()
		_, _ = memory.BackfillTimelines(r.ctx, r.memoryWorkLLM(), sessionsDir, memory.BackfillOptions{
			Limit:       10,
			Exclude:     map[string]struct{}{live: {}},
			Concurrency: 2,
		})
	}()
}

// timelineAfterTurn extracts the user turns added since the last timeline
// write. The batch is coalesced the same way the upstream trigger does: a
// single new message waits for the cooldown, a fuller batch runs at once.
//
// Parity: core/framework/agents/queen/timeline.py::update_session_timeline
func (r *ChatRuntime) timelineAfterTurn() {
	if r == nil || !r.timelineEnabled || r.deps.Root == "" {
		return
	}
	pending, _ := memory.TimelinePending(r.deps.Root)
	if pending == nil {
		return
	}
	r.timelineMu.Lock()
	// time.Since on the zero lastTimelineRun saturates at the maximum
	// duration, so the first completed turn always clears the cooldown guard
	// — the same "last_run starts at 0.0" behaviour upstream gets from
	// time.monotonic().
	waited := time.Since(r.lastTimelineRun)
	if len(pending.Messages) < memory.TimelineLiveBatchMessages && waited < memory.TimelineLiveCooldown {
		r.timelineMu.Unlock()
		return
	}
	r.lastTimelineRun = time.Now()
	r.timelineMu.Unlock()
	sessionDir := r.deps.Root
	r.timelineWG.Add(1)
	go func() {
		defer r.timelineWG.Done()
		_, _ = memory.UpdateSessionTimeline(r.ctx, r.memoryWorkLLM(), sessionDir)
	}()
}

// Parity-exempt: Go-only sediment seam. Upstream revises a soul in its own
// daily chat-reading reflection (framework/agents/queen/soul_reflection.py);
// the Go port now runs that daily pass too, and also rides the memory
// reflection so a soul still updates between daily runs.
func (r *ChatRuntime) sedimentSoul(result memory.LoopResult) {
	if r == nil || r.deps.HomeRoot == "" || r.queenName == "" {
		return
	}
	home := hivehome.Home{Root: r.deps.HomeRoot}
	cfg := soul.GetSoulConfig(HiveConfig(home), r.queenName)
	if cfg == nil {
		return
	}
	conclusions := memory.ConclusionsFromReflection(result, []memory.ScopeDir{{Scope: "queen", Dir: r.queenMemoryDir}})
	if len(conclusions) == 0 {
		return
	}
	items := make([]soul.SedimentConclusion, 0, len(conclusions))
	for _, conclusion := range conclusions {
		items = append(items, soul.SedimentConclusion{Text: conclusion.Text, Source: conclusion.Filename})
	}
	sent, err := soul.Sediment(soul.SedimentOptions{
		QueensDir:   home.QueensDir(),
		QueenID:     r.queenName,
		Config:      cfg,
		Conclusions: items,
	})
	if err != nil {
		log.Printf("chat runtime %s: soul sediment skipped: %v", r.sessionID, err)
		return
	}
	// Review mode (the soul default) only stages a proposal: say so, because the
	// pass's work does not reach the soul until the owner decides.
	if sent.Proposed {
		log.Printf("chat runtime %s: soul sediment proposed %s for %s", r.sessionID, sent.ProposalID, r.queenName)
	}
	if sent.Blocked {
		log.Printf("chat runtime %s: soul sediment waits for %s's pending soul proposal", r.sessionID, r.queenName)
	}
}

// configureMemoryScopes creates the session's global and queen memory scopes
// and, when the session has a directory to persist passes in, the reflection
// scheduler. Without a HIVE_HOME the runtime keeps its pre-memory behaviour
// (no recall, no reflection) instead of guessing.
//
// Parity: core/framework/server/queen_orchestrator.py::initialize_memory_scopes,
// core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
func (r *ChatRuntime) configureMemoryScopes() {
	homeRoot := r.deps.HomeRoot
	if homeRoot == "" {
		return
	}
	r.globalMemoryDir = memory.GlobalMemoryDir(homeRoot)
	r.queenMemoryDir = memory.QueenMemoryDir(homeRoot, r.queenName)
	_ = memory.InitMemoryDir(r.globalMemoryDir)
	_ = memory.InitMemoryDir(r.queenMemoryDir)
	if r.deps.Root == "" {
		return
	}
	r.reflection = &memory.ReflectionScheduler{
		SessionDir: r.deps.Root,
		LLM:        r.memoryLLM,
		GlobalDir:  r.globalMemoryDir,
		QueenDir:   r.queenMemoryDir,
		QueenID:    r.queenName,
		Config:     func() memory.ReflectionConfig { return chatMemoryReflectionConfig(homeRoot) },
		// A finished pass sediments its conclusions into her soul.
		OnReflect: r.sedimentSoul,
	}
	r.reflection.InitializeReflectionState()
}

// chatMemoryReflectionConfig reads the memory_reflection section of
// configuration.json, falling back to the same defaults as upstream.
//
// Parity: framework/config.py::get_memory_reflection_config
func chatMemoryReflectionConfig(homeRoot string) memory.ReflectionConfig {
	cfg := memory.DefaultReflectionConfig()
	section, _ := HiveConfig(hivehome.Home{Root: homeRoot})["memory_reflection"].(map[string]any)
	cfg.EveryTurns = reflectionPositiveInt(section["every_turns"], cfg.EveryTurns)
	cfg.LongEvery = reflectionPositiveInt(section["long_every"], cfg.LongEvery)
	if model, ok := section["model"].(string); ok && (model == "main" || model == "worker") {
		cfg.Model = model
	}
	return cfg
}

// reflectionPositiveInt ports get_memory_reflection_config's positive(): the
// configured value counts only when Python would see a real int of at least 1
// — `isinstance(value, int) and not isinstance(value, bool)`. true, "2" and
// 2.0 all fall back to the default, exactly as upstream.
//
// Parity: framework/config.py::get_memory_reflection_config
func reflectionPositiveInt(value any, fallback int) int {
	count, ok := strictJSONInteger(value)
	if !ok || count < 1 {
		return fallback
	}
	return count
}

// strictJSONInteger accepts only a genuine integer. hiveconf.Load decodes with
// UseNumber, so a configuration value arrives as json.Number carrying its
// literal text: Int64() succeeds for 7 but fails for 2.0 and 1e1, which keeps
// an integral float from being mistaken for an int (as a Float64 + truncation
// check would). Strings, bools and null are rejected outright; int and int64
// cover values tests construct directly.
func strictJSONInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || int64(int(parsed)) != parsed {
			return 0, false
		}
		return int(parsed), true
	case int:
		return typed, true
	case int64:
		if int64(int(typed)) != typed {
			return 0, false
		}
		return int(typed), true
	}
	return 0, false
}
