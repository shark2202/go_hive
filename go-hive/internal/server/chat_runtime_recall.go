package server

// Per-queen memory recall: the background pick, the cached block delivery and
// the session-start seed that ride a user turn, plus their timeout budget.
// Split out of chat_runtime.go to keep the runtime file inside the
// repository's frozen size budget; the code is unchanged.

import (
	"context"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

// injectRecall gives the turn the block picked for an earlier message and
// starts the pick for this message in the background; it is the upstream
// "background" mode (_recall_on_user_input). The EventBus drops a handler at
// 15s, so upstream's handler must return fast: it delivers only what is
// already cached and fires the pick as a task that joins the queen's next
// drain — in practice her next message. This method therefore never calls the
// model on the turn's critical path. "wait" and "index" are reminder sources,
// so it is silent while either is set.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
// (cached delivery, deduped against _last_injected_recall)
func (r *ChatRuntime) injectRecall(query, stamp string) {
	if r.globalMemoryDir == "" {
		return
	}
	if r.memoryRecallSettings().Mode != memory.MemoryRecallBackground {
		return
	}
	// Upstream: await _inject_recall_if_changed() — cached block only, no
	// model call; then asyncio.create_task(_bg_refresh()).
	r.deliverCachedRecall(stamp)
	r.pickRecallInBackground(query)
}

// deliverCachedRecall appends the cached pick as a <system-reminder> when it
// differs from the last one injected. Upstream's drain queues passive
// injections after anything already queued, so the reminder lands after the
// user's message. It runs on the turn goroutine; conversation writes stay
// single-writer.
//
// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
func (r *ChatRuntime) deliverCachedRecall(stamp string) {
	r.recallMu.Lock()
	block := joinRecallBlocks(r.recallGlobalCache, r.recallQueenCache)
	r.recallMu.Unlock()
	if block == "" || block == r.lastRecall {
		return
	}
	r.lastRecall = block
	reminder := "<system-reminder>\n" + agentloop.MemoryRecallHeader + "\n\n" + block + "\n</system-reminder>"
	// Recall is context, never a reason to fail a turn: a write error leaves
	// the conversation as it was and the turn runs without the reminder.
	_, _ = r.conv.AddUserMessage("["+stamp+"] [External event] "+reminder, agentloop.UserMessageOptions{})
}

// joinRecallBlocks renders the two cached scope blocks as one deliverable
// block: global first, then queen, blank line between, empty scopes skipped.
//
// Parity: core/framework/tools/queen_lifecycle_tools.py::QueenPhaseState.render_recall_block
func joinRecallBlocks(global, queen string) string {
	parts := make([]string, 0, 2)
	if global != "" {
		parts = append(parts, global)
	}
	if queen != "" {
		parts = append(parts, queen)
	}
	return strings.Join(parts, "\n\n")
}

// pickRecallInBackground runs the pick off the turn goroutine, mirroring
// upstream's _bg_refresh task: the result is cached, and the next drain (the
// next message) delivers it if it differs. The pick swallows its own failures
// the way select_memories does — an empty result overwrites the cache, as
// upstream's _refresh_recall_cache assigns whatever the pick returned.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input (_bg_refresh)
func (r *ChatRuntime) pickRecallInBackground(query string) {
	if r == nil {
		return
	}
	go r.refreshRecallCache(query)
}

// refreshRecallCache picks the scoped recall blocks for query and stores them
// as the deliverable cache. The empty-query guard mirrors upstream
// _refresh_recall_cache (`if not query`): an empty message makes no pick call,
// and any cached block is still delivered at the next drain.
//
// Parity: core/framework/server/queen_orchestrator.py::_refresh_recall_cache
func (r *ChatRuntime) refreshRecallCache(query string) {
	if r == nil || query == "" {
		return
	}
	globalBlock, queenBlock := r.recallBlocksForQuery(query)
	r.storeRecallBlocks(globalBlock, queenBlock)
}

// storeRecallBlocks hands a picked (global, queen) pair to the next turn.
func (r *ChatRuntime) storeRecallBlocks(global, queen string) {
	r.recallMu.Lock()
	r.recallGlobalCache = global
	r.recallQueenCache = queen
	r.recallMu.Unlock()
}

// recallSeedTimeout bounds the session-start seed pick. Upstream awaits
// select_memories inside asyncio.wait_for(..., 3.0) before the first model
// call, so a slow model never delays the first turn past three seconds.
const recallSeedTimeout = 3 * time.Second

// seedRecall warms the global recall cache on a session's first message,
// mirroring the seed the upstream identity hook runs at session_start
// (queen_orchestrator.py:1461-1479) with the session's initial message as its
// trigger: global scope only, up to 5 picks (the selector's default — the
// per-scope refresh uses 3), a 3s wait, and silent failure. It runs on the
// turn goroutine after the first message's own delivery and before the model
// call, the same place upstream awaits its session_start hook: the first
// message itself carries no recall, and the seed surfaces on a later message
// when the first message's refresh is slow or empty — its upstream effect too.
//
// Parity: core/framework/server/queen_orchestrator.py::_queen_identity_hook
func (r *ChatRuntime) seedRecall(query string) {
	r.seedRecallWithin(query, recallSeedTimeout)
}

// seedRecallWithin is seedRecall with the timeout injected for tests.
func (r *ChatRuntime) seedRecallWithin(query string, timeout time.Duration) {
	if r == nil || r.globalMemoryDir == "" || query == "" {
		return
	}
	if r.memoryRecallSettings().Mode != memory.MemoryRecallBackground {
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	selected := memory.SelectMemories(ctx, query, r.deps.LLM, r.globalMemoryDir, 5, llm.AuxMaxTokens())
	// Upstream's wait_for cancels the whole pick, so on timeout nothing is
	// written and the cache keeps what it had (TimeoutError branch). SelectMemories
	// would return its profile pins on a context error, so the cancellation is
	// checked here before the seed stores anything.
	if ctx.Err() != nil {
		return
	}
	// Upstream assigns whatever format_recall_injection returned, "" included.
	block := memory.FormatRecallInjection(selected, r.globalMemoryDir, "Global Memories")
	r.recallMu.Lock()
	r.recallGlobalCache = block
	r.recallMu.Unlock()
}
