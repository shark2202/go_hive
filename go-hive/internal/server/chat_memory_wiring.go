package server

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
)

// chatLoopConfig is the queen loop's configuration: the defaults plus the
// session-scoped spillover directory large tool results are written to.
//
// Parity: core/framework/server/queen_orchestrator.py::_queen_loop
func chatLoopConfig(root string) agentloop.LoopConfig {
	config := agentloop.DefaultLoopConfig()
	if root != "" {
		config.SpilloverDir = filepath.Join(root, "data")
	}
	return config
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
	if block == "" || block == r.lastPastRecall {
		return
	}
	r.lastPastRecall = block
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
