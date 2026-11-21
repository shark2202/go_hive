package server

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/soul"
)

// memoryTimelineStopFlushTimeout bounds the timeline flush Close runs after a
// session stops. Upstream spawns the flush fire-and-forget; the port waits on
// it (see ChatRuntime.Close) so a stopped session's directory is complete
// before teardown returns, hence the explicit bound.
const memoryTimelineStopFlushTimeout = 30 * time.Second

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

// chatLoopConfig is the queen loop's configuration: the defaults, the
// session-scoped spillover directory large tool results are written to, the
// global loop.max_tool_result_chars override, and — for a queen with a model of
// her own — her max_context_tokens window.
//
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func chatLoopConfig(root, homeRoot, queenID string) agentloop.LoopConfig {
	config := agentloop.DefaultLoopConfig()
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
	// configured default.
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

// injectPastConversationRecall appends the dated excerpts of earlier
// conversations that match the latest user message. It runs after the memory
// recall block and before ExecuteTurn, so related history is in front of the
// model even when it does not call search_messages.
//
// Parity: core/framework/agent_loop/past_conversations_reminder.py::PastConversationsReminderSource
func (r *ChatRuntime) injectPastConversationRecall(query, stamp string) {
	if r == nil || r.conv == nil || r.deps.HomeRoot == "" || r.deps.Root == "" {
		return
	}
	if len([]rune(strings.TrimSpace(query))) < memory.PastRecallMinMessageChars {
		return
	}
	ref := memory.ScopeRef{Scope: memory.SearchScopeQueens, Owner: r.queenName}
	block := memory.PastConversationRecallBlock(
		r.deps.HomeRoot,
		ref,
		query,
		filepath.Base(r.deps.Root),
	)
	// Upstream (past_conversations_reminder.py) renders on every
	// USER_PROMPT_SUBMIT, so an identical message gets the excerpts again;
	// there is no repeat guard to port.
	if block == "" {
		return
	}
	reminder := "<system-reminder>\nRecalled past conversation excerpts relevant to the latest " +
		"user message (supersedes earlier excerpt reminders):\n\n" + block + "\n</system-reminder>"
	// Excerpt recall is context, never a reason to fail a turn.
	_, _ = r.conv.AddUserMessage("["+stamp+"] [External event] "+reminder, agentloop.UserMessageOptions{})
}

// startTimeline enables the per-session timeline writer when the feature flag
// is on and arms the startup backfill for earlier sessions.
//
// Parity: core/framework/agents/queen/timeline.py::subscribe_timeline_triggers
func (r *ChatRuntime) startTimeline() {
	if r == nil || r.deps.Root == "" || r.deps.LLM == nil {
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
		_, _ = memory.BackfillTimelines(r.ctx, r.deps.LLM, sessionsDir, memory.BackfillOptions{
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
		_, _ = memory.UpdateSessionTimeline(r.ctx, r.deps.LLM, sessionDir)
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
	if _, err := soul.Sediment(soul.SedimentOptions{
		QueensDir:   home.QueensDir(),
		QueenID:     r.queenName,
		Config:      cfg,
		Conclusions: items,
	}); err != nil {
		log.Printf("chat runtime %s: soul sediment skipped: %v", r.sessionID, err)
	}
}

// chatMemoryReflectionConfig reads the memory_reflection section of
// configuration.json, falling back to the same defaults as upstream.
//
// Parity: framework/config.py::get_memory_reflection_config
func chatMemoryReflectionConfig(homeRoot string) memory.ReflectionConfig {
	cfg := memory.DefaultReflectionConfig()
	section, _ := HiveConfig(hivehome.Home{Root: homeRoot})["memory_reflection"].(map[string]any)
	positive := func(key string, fallback int) int {
		value, ok := toFloat(section[key])
		if !ok || value < 1 || value != float64(int(value)) {
			return fallback
		}
		return int(value)
	}
	cfg.EveryTurns = positive("every_turns", cfg.EveryTurns)
	cfg.LongEvery = positive("long_every", cfg.LongEvery)
	if model, ok := section["model"].(string); ok && (model == "main" || model == "worker") {
		cfg.Model = model
	}
	return cfg
}
