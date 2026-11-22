# Memory notes push + recall index (upstream 1.0.15) — Go parity

Scope: how a queen's memory notes reach her before she answers (`memory_recall.mode`
"index"/"wait"/"background"), and recall reading a per-owner index instead of rescanning every
session. Sibling lanes own the note history/`log.jsonl` and the `/api/memories*` routes; both are
out of scope here.

Upstream reference (in-tree): `core/framework/agent_loop/memory_reminders.py`,
`core/framework/config.py` (`MemoryRecallConfig`, `MEMORY_RECALL_MODES`),
`core/framework/server/queen_orchestrator.py` (`_memory_index`, `_recall_for_message`,
`_recall_on_user_input`, `_inject_recall_if_changed`, `_refresh_recall_cache`),
`core/framework/server/routes_execution.py` (`deliver_to_queen`),
`core/framework/agent_loop/agent_loop.py` (`inject_event(wake=False)`,
`_drain_injection_queue`), `tools/src/memory_tools/turn_index.py`,
`tools/src/memory_tools/recall.py`.

## Modes

| Upstream | Go | Notes |
|---|---|---|
| `index` (default): every note's one-line manifest, in full at session start and after compaction, then only changes; she opens notes with `terminal_exec`; no extra model call | `internal/agentloop/memory_reminders.go::MemoryIndexReminderSource` + `internal/memory/notes.go::MemoryIndex` | Headers byte-match `INDEX_HEADER` / `INDEX_CHANGED_HEADER`; scope labels `Shared with every queen` / `Yours alone` (queen_orchestrator `_memory_index`). |
| `wait`: a model picks the notes for the message, before she answers, capped by `wait_seconds` | `MemoryRecallReminderSource`; pick = `ChatRuntime.memoryRecallForMessage` (`BuildScopedRecallBlocks`) | Waits in a goroutine up to `wait_seconds`; an unchanged pick is not repeated; POST_COMPACT resets the last pick. The pick also refreshes the shared cache, as upstream `_recall_for_message` does. |
| `background`: the same pick, made while she answers; the cached block (the pick for an earlier message) rides the next drain | `ChatRuntime.injectRecall` (`chat_runtime.go:1162`): `deliverCachedRecall` (`:1182`) + `pickRecallInBackground` (`:1203`) | The pick never runs on the turn's critical path. Timing table below. |

`HIVE_MEMORY_RECALL` > `configuration.json: memory_recall.mode` > `index`, read at
each message: a Settings/env change reaches running sessions from their next
message. `wait_seconds` defaults to 10 and rejects bool/string/`<=0`.

## Background mode timing (upstream `_recall_on_user_input`)

Evidence: `go_hive/scripts/background_recall_probe.py` runs the real reference
modules in-process (fake provider, no network) and writes
`go_hive/docs/e2e-assets/memory-notes-1015/bg-recall-probe.json` — **36/36
cases pass** at upstream tree `151dbe66`. It exercises the real `AgentLoop`
injection/drain path, the real `recall_selector` timing, and pins every line
number cited here.

Measured upstream sequence, per user message:

1. `deliver_to_queen` **publishes `CLIENT_INPUT_RECEIVED` before it queues the
   message** (`routes_execution.py:795` comment; publish `:806`; `inject_event`
   `:826`). `EventBus.publish` awaits handlers with a 15s cap — probe H: a 0.25s
   handler makes publish take 0.25s; a handler that spawns a task returns
   immediately while the task finishes later.
2. `_recall_on_user_input` (`queen_orchestrator.py:1307`) runs only in
   `background` mode (`:1324`): it delivers the **cached** block through
   `_inject_recall_if_changed` (`:1327`, defined at `:1290`, dedupe at `:1296`)
   and then spawns `_bg_refresh` as a task (`:1338`).
3. `_bg_refresh` calls `_refresh_recall_cache` (`:1247`; empty-query guard
   `:1249`; two-scope pick at `:1256-1262`; cache assignment `:1263-1264`) and
   then `_inject_recall_if_changed` again (`:1332`).
4. `inject_event(wake=False)` appends to `_passive_injections`
   (`agent_loop.py:3582`) — it neither wakes the loop nor runs a turn.
5. The drain rewrites passive entries into the injection queue **after anything
   already queued** (`agent_loop.py:7079-7081`), at the outer loop's iteration
   start (`agent_loop.py:1860`). Each injected entry is stamped with the
   drain-time clock (`cursor_persistence.py:271`).
6. A queen normally **parks at the end of the turn**: `real_tool_results`
   resets each inner turn, so the judge sees zero real tool results and accepts
   (probe E4: 2 LLM calls, judge called once with `real_tool_results=0`). The
   next drain is therefore her next message; a same-turn drain needs an extra
   outer-loop iteration (judge RETRY / ask_user re-entry), which a chat queen
   normally does not run.

Item-by-item comparison with the Go port:

| Step | Upstream (measured, file:line) | Go (`go_hive/go-hive`) | Verdict |
|---|---|---|---|
| Order at turn start | cached block drained **after** the user message enters the conversation (E1) | user message committed in `runTurn` (`chat_runtime.go:928-955`), then `injectRecall` (`:956`) → `deliverCachedRecall` (`:1200`) | aligned |
| Pick timing | model call runs as a background task, never awaited by the turn (`queen_orchestrator.py:1338`; probe H) | `go r.refreshRecallCache` (`chat_runtime.go:1236-1240`); `TestChatRuntimeBackgroundRecallNeverBlocksTheTurn` | aligned |
| What the current turn sees | only the cached block (from the prior message, or the seed) — never the pick for its own message | `deliverCachedRecall` reads the two cached fields only (`:1200-1212`); the fresh pick writes them (`:1249-1255`) | aligned, same-turn continuation caveat in residuals |
| Where the fresh block goes | `phase_state._cached_*` → `_inject_recall_if_changed` → `_passive_injections` → next drain (E3: absent from every request of the producing turn, delivered with the next message) | `recallGlobalCache`/`recallQueenCache` → next turn's `deliverCachedRecall` (same ordering; `TestChatRuntimeBackgroundRecallLandsWithTheNextMessage`) | aligned |
| Dedupe | `_last_injected_recall`, unchanged block not repeated (`queen_orchestrator.py:1296-1298`) | `lastRecall` (`chat_runtime.go:1204-1207`); `TestChatRuntimeBackgroundRecallReinjectsOnlyOnChange` | aligned |
| Empty message | no pick (`_refresh_recall_cache` guard `:1249`); the cached block is still delivered because the immediate delivery runs before the refresh | `refreshRecallCache` returns on `query == ""` (`:1250`); `deliverCachedRecall` already ran; `TestChatRuntimeBackgroundRecallEmptyMessageKeepsTheCache` | aligned |
| Pick timeout | none in the selector: a 0.6s pick takes 0.6s inside `build_scoped_recall_blocks` (probe F2). The only cap is the 3s session-start seed (F3) | none; `BuildScopedRecallBlocks` uses the session context, no extra deadline | aligned (no cap on either side) |
| Pick scope concurrency | both scopes in flight via `asyncio.gather` (`recall_selector.py:247-250`; probe F1: two 0.15s picks finish in < 0.28s) | both scopes in flight: one goroutine per scope, `sync.WaitGroup`, blocks kept in scope order (`internal/memory/recall.go::BuildScopedRecallBlocks`); `TestBuildScopedRecallBlocksRunsScopesConcurrently` | aligned (measured below) |
| Session-start seed | global scope only, up to 5 profile pins, `asyncio.wait_for(..., 3.0)` at session_start (`queen_orchestrator.py:1461-1479`; probe S1-S9/F3/F4) | `ChatRuntime.seedRecall` (`chat_runtime.go`), called from `runTurn` for a fresh session's first message; global scope, `SelectMemories(..., maxResults 5)`, 3s context, silent failure | aligned (see "Session-start seed" below for the bounded differences) |
| Failure behaviour | `select_memories` returns the profile pins on any error (`recall_selector.py:167-173`); `_refresh_recall_cache` still assigns the (possibly empty) result | `SelectMemories` returns pins on error; `refreshRecallCache` assigns the joined result | aligned |

The observable contract now matches upstream's documented one: *"the same pick,
made while she answers, so it often arrives a message late"*
(`config.py:1066`, `docs/configuration.md`). The first message of a cold session
carries no recall (nothing is cached yet); its pick rides the second message.
The session-start seed does not change that: it warms the cache *after* the
first message's own delivery, so it only surfaces when that message's refresh is
slow or empty.

## Session-start seed

Upstream's queen identity hook runs one warm pick at `session_start` when the
session was created with an initial message (`trigger = ctx.trigger or ""` at
`queen_orchestrator.py:1378`; the loop passes `initial_message` — agent_loop
`:1554`): global scope only, `select_memories(trigger, llm, global_dir)` with the
selector's default `max_results=5`, awaited at most 3s. A timeout leaves the
cache unchanged ("will retry on first turn"); a fast failure still seeds the
profile pins the selector returns.

Go port: `ChatRuntime.runTurn` computes `firstSessionMessage` before any
conversation write (`request.clientInput && r.conv.MessageCount() == 0` — the
create-session initial-prompt turn) and, after `injectRecall` has delivered the
(empty) cache and started the normal pick, calls `seedRecall(message)` in
background mode only. `seedRecall` is the selector call with `maxResults 5`
against `globalMemoryDir`, bounded by a 3s context, and it leaves both cache
fields untouched when the context ends (upstream's `wait_for` cancels the
coroutine before the assignment).

The cache is now two fields (`recallGlobalCache` / `recallQueenCache`) rendered
by `joinRecallBlocks`, mirroring `phase_state._cached_global_recall_block` /
`_cached_queen_recall_block` + `render_recall_block`: the seed writes the global
field only, so a seed that lands after the first refresh replaces the global
block and keeps the queen block, exactly as upstream's race does.

Differences from upstream (all bounded):

- The seed query is the first message as Go records it; upstream queries the
  assembled initial message, i.e. `"user_request: <prompt>"` (probe S9).
- The trigger gate is "fresh session's first message" (empty conversation at
  turn start). Upstream also seeds a *resumed* session created with an
  initial prompt; Go does not (its conversation already has history, so
  `MessageCount() == 0` is false), and the cache stays cold until the first
  refresh. A resume without a prompt seeds on neither side.
- A session that starts with the message-less opening turn (no initial prompt)
  seeds nothing, matching upstream's empty trigger.
- The seed blocks the turn goroutine for up to 3s before the first model call
  (upstream awaits the same hook), but it runs after the first message's
  delivery, so message 1 itself stays unseeded on both sides.

## Two-scope pick concurrency (measured)

Both scope picks are model calls, so a serial selector doubles the wall time of
the "wait"-mode pick (the one the user waits on before she answers). Upstream
runs them at once with `asyncio.gather` (`recall_selector.py:247-250`; probe F1:
two 0.15s picks finish in 0.153s). Go now does the same: one goroutine per
scope, a `sync.WaitGroup` join, and each result written to its own variable so
the returned `(global, queen)` order is fixed by scope, not by completion order
(at most two goroutines; the bound is structural, not configurable).

Measurement (this tree, 2026-11-22): real `BuildScopedRecallBlocks` through the
real `llm.NewHTTPProvider` HTTP transport against a loopback OpenAI-compatible
endpoint that delays each pick by the stated amount; memory dirs hold 4 notes
per scope; 5 runs per row, medians; an in-flight tracker proves the shapes
(serial max-inflight 1, concurrent max-inflight 2, 20 requests per row).

| Per-pick latency | Serial (2 calls) | Concurrent (2 calls) | Gap |
|---|---|---|---|
| 0 ms (loopback floor) | 0.43 ms | 0.28 ms | 0.15 ms |
| 30 ms | 65.1 ms | 32.7 ms | 32.4 ms |
| 150 ms | 305.5 ms | 153.2 ms | 152.3 ms |
| 300 ms | 605.8 ms | 302.9 ms | 303.0 ms |

The gap is one pick's latency: serial is `t_global + t_queen`, concurrent is
`max(t_global, t_queen)`, so the fixed orchestration overhead cancels (0.15 ms
at the loopback floor). Any real pick slower than 50 ms — every hosted model
call, and any local model doing more than a trivial decode — puts the saving
above the 50 ms bar; the registration of this issue was therefore closed by
making the call shape match upstream rather than by a latency exemption.

No new raciness: the `llm.Completer` contract is already used concurrently in
production (colony workers run in goroutines and share one completer,
`colony/runtime.go::buildContext`), `ProviderSlot` snapshots the live transport
under an RWMutex (`llm/hotswap.go`), `Provider` is immutable after construction
with mutex-guarded `KeyPool` and `CallLimiter`, and `http.Client` is safe for
concurrent use. The two `SelectMemories` calls touch only local state and file
reads, and the pick has no prescribed request order (results are identical
either way). `go test -race -count=5 ./internal/memory/ ./internal/server/`
covers both sides.

## Recall index

- Per (scope, owner): `$HIVE_HOME/.message_index/recall/<scope>/<owner>.json`
  mirroring `paths.py::turn_index_path`'s layout. The engine is a JSON body, not
  SQLite: the Python engine is not portable without cgo, so this port keeps the
  contract it carries — one derived file per owner, same results, same lifecycle
  (see residuals).
- First query after upgrade or deletion starts the build in the background and
  waits only up to the recall cap (default 3s, `HIVE_PAST_RECALL_TIMEOUT`); the
  turn proceeds without excerpts while the build keeps going. Warm queries read
  the file and re-read only sessions whose `events.jsonl` size changed.
- Deleting the file costs a rebuild, nothing more (`BuildRecallIndex` /
  `RecallIndexPath` are the only exported seams).
- The index covers every session of the owner — the old 200-session scan window
  is gone for recall, matching the index's whole-store scope.

## Env/config contract

`config-keys.json`: `memory_recall.mode` and `memory_recall.wait_seconds` are
`aligned` (defaults, types, invalid-value rules and env override verified against
`core/framework/config.py`). `env-vars.json`: `HIVE_MEMORY_RECALL` is `aligned`
(both sides read it; Go read site `internal/memory/recall_config.go`). Audit
counts were corrected to the tree: python 90 / go 119 / common 84 / python_only 6
(python gained `HIVE_PAST_RECALL_TIMEOUT` in the 1.0.15 sync; this lane adds the
Go reader for `HIVE_MEMORY_RECALL`). `freeze_env_config_contract.py --check` is green.

## Evidence

- Python probe (real reference modules, 51/51 cases): `docs/e2e-assets/memory-notes-1015/py-probe.json`,
  generated by `go_hive/scripts/memory_notes_1015_probe.py`
  (`PYTHONPATH=core:tools/src .venv/bin/python …`). Covers the config matrix, the
  index listing/diff/compact/no-model-call cases, the wait pick/cap, and the
  recall index (per-owner paths, delete+rebuild, per-owner isolation, the cold
  build past the cap while the thread keeps going, the 3s default and env override).
- Background-timing probe (36/36 cases): `docs/e2e-assets/memory-notes-1015/bg-recall-probe.json`,
  generated by `go_hive/scripts/background_recall_probe.py`. Reality-checks the
  `_recall_on_user_input` sequence piece by piece (E1-E4 loop drain, F1-F5 pick
  timing/seed, H publish-await) and records the measured line numbers (G).
- Session-start seed probe (25/25 cases): `docs/e2e-assets/memory-notes-1015/recall-seed-probe.json`,
  generated by `go_hive/scripts/recall_seed_probe.py`. Runs the real
  `select_memories` / `format_recall_injection` / `QueenPhaseState.render_recall_block`
  / `_build_initial_message` in-process and measures: global-only scope (S1),
  the 5-pick cap with relevance first (S2), the 3s timeout leaving the cache
  unchanged (S3, 3.0s), a fast error still seeding the pins (S4, 0.0s), the
  mode gate matrix (S5/S6), an empty directory (S7), the global-field-only
  write (S8), and the seed's trigger string (S9), plus the seed anchors (A).
- Go tests (upstream test names as parity anchors):
  - `internal/agentloop/memory_reminders_test.go` — all four `memory_reminders.py`
    sources' cases ported (`test_index_*`, `test_wait_*`) plus the no-model-call invariant.
  - `internal/memory/notes_test.go` — `test_index_lists_each_scopes_notes` + scan order.
  - `internal/memory/memory_test.go` — `TestBuildScopedRecallBlocksRunsScopesConcurrently`
    pins the `asyncio.gather` shape (a barrier completer that only releases once
    both scopes are in flight; the old serial code times out) and the
    scope-ordered blocks when the queen pick finishes first.
  - `internal/memory/recall_config_test.go` — the config/env matrix.
  - `internal/memory/recall_index_test.go` — per-owner index, deletion rebuild,
    incremental refresh, cold-build deadline, whole-store coverage (206 sessions).
  - `internal/server/chat_memory_notes_test.go` — session wiring: registered/active
    sources, index listing without a model call, wait pick before the answer, and
    the five background cases (mode gate, next-message delivery, non-blocking
    pick, empty message, change-only re-injection).
  - `internal/server/chat_recall_seed_test.go` — the session-start seed:
    global-only 5-cap pick, the 3s timeout keeping the cache, index/wait
    silence, the seed surfacing while the first refresh stalls (and the
    refresh replacing it when it lands), and no seed on a message-less
    opening turn.
  - `internal/server/chat_runtime_test.go` — scoped recall content on the turn
    that receives the cached block (`TestChatRuntimeInjectsQueenScopedRecallReminder`,
    `TestChatRuntimeRecallIsScopedToItsQueen`), both delivered on message 2.

## Residuals (registered, not blockers)

- Format: the index body is JSON, not SQLite (`turn_index.py` engine). Behavioural
  parity is asserted by `recall_index_test.go`; the layout and lifecycle match.
- Pre-existing scoring divergence kept as-is: Python's BM25 `length` is
  `len(user)*2 + len(reply)` characters while the Go scorer sums term counts
  (`past_conversations.go`). This lane feeds both the indexed and scan paths the
  same Go lengths, so no result moved; aligning the metric is a separate change.
- `background` session-start seeding is ported (see "Session-start seed"); its
  two bounded differences are the seed query string and the absence of a seed on
  a resumed session that was created with an initial prompt. Delivery remains
  race-dependent on both sides (the refresh can replace the seed before it is
  ever delivered), which is the upstream behaviour.
- The two scope picks run concurrently on both sides: Go uses one goroutine per
  scope with the blocks kept in scope order
  (`internal/memory/recall.go::BuildScopedRecallBlocks`), matching
  `recall_selector.py:247-250` (probe F1: two 0.15s picks finish under 0.28s).
- Delivery happens at the next client turn's start. Upstream drains at every
  outer-loop iteration (`agent_loop.py:1860`), so a rare same-turn continuation
  path (judge RETRY, ask_user re-entry) can deliver the pick one model call
  earlier than Go. A chat queen normally parks after a tool-free turn (probe E4),
  so the common path is the same: the pick rides the next message.
