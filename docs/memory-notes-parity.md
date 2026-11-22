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
| Order at turn start | cached block drained **after** the user message enters the conversation (E1) | user message committed in `runTurn` (`chat_runtime.go:908-920`), then `injectRecall` (`:945`) → `deliverCachedRecall` (`:1182`) | aligned |
| Pick timing | model call runs as a background task, never awaited by the turn (`queen_orchestrator.py:1338`; probe H) | `go r.refreshRecallCache` (`chat_runtime.go:1203-1214`); `TestChatRuntimeBackgroundRecallNeverBlocksTheTurn` | aligned |
| What the current turn sees | only the cached block (from the prior message, or the seed) — never the pick for its own message | `deliverCachedRecall` reads `recallCache` only (`:1182-1197`); the fresh pick writes `recallCache` (`:1216-1225`) | aligned, same-turn continuation caveat in residuals |
| Where the fresh block goes | `phase_state._cached_*` → `_inject_recall_if_changed` → `_passive_injections` → next drain (E3: absent from every request of the producing turn, delivered with the next message) | `recallCache` → next turn's `deliverCachedRecall` (same ordering; `TestChatRuntimeBackgroundRecallLandsWithTheNextMessage`) | aligned |
| Dedupe | `_last_injected_recall`, unchanged block not repeated (`queen_orchestrator.py:1296-1298`) | `lastRecall` (`chat_runtime.go:1186-1190`); `TestChatRuntimeBackgroundRecallReinjectsOnlyOnChange` | aligned |
| Empty message | no pick (`_refresh_recall_cache` guard `:1249`); the cached block is still delivered because the immediate delivery runs before the refresh | `refreshRecallCache` returns on `query == ""` (`:1217`); `deliverCachedRecall` already ran; `TestChatRuntimeBackgroundRecallEmptyMessageKeepsTheCache` | aligned |
| Pick timeout | none in the selector: a 0.6s pick takes 0.6s inside `build_scoped_recall_blocks` (probe F2). The only cap is the 3s session-start seed (F3) | none; `BuildScopedRecallBlocks` uses the session context, no extra deadline | aligned (no cap on either side) |
| Pick scope concurrency | both scopes in flight via `asyncio.gather` (`recall_selector.py:247-250`; probe F1: two 0.15s picks finish in < 0.28s) | sequential, global then queen (`internal/memory/recall.go:205-216`) | residual (latency only) |
| Session-start seed | global scope only, up to 5 profile pins, `asyncio.wait_for(..., 3.0)` (`queen_orchestrator.py:1461-1479`; probe F3: a 5s pick is cancelled at ~3s and the cache stays empty; F4: seed keeps 5 pins, refresh keeps 3) | not ported | residual |
| Failure behaviour | `select_memories` returns the profile pins on any error (`recall_selector.py:167-173`); `_refresh_recall_cache` still assigns the (possibly empty) result | `SelectMemories` returns pins on error; `refreshRecallCache` assigns the joined result | aligned |

The observable contract now matches upstream's documented one: *"the same pick,
made while she answers, so it often arrives a message late"*
(`config.py:1066`, `docs/configuration.md`). The first message of a cold session
carries no recall (nothing is cached yet); its pick rides the second message.

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
- Go tests (upstream test names as parity anchors):
  - `internal/agentloop/memory_reminders_test.go` — all four `memory_reminders.py`
    sources' cases ported (`test_index_*`, `test_wait_*`) plus the no-model-call invariant.
  - `internal/memory/notes_test.go` — `test_index_lists_each_scopes_notes` + scan order.
  - `internal/memory/recall_config_test.go` — the config/env matrix.
  - `internal/memory/recall_index_test.go` — per-owner index, deletion rebuild,
    incremental refresh, cold-build deadline, whole-store coverage (206 sessions).
  - `internal/server/chat_memory_notes_test.go` — session wiring: registered/active
    sources, index listing without a model call, wait pick before the answer, and
    the five background cases (mode gate, next-message delivery, non-blocking
    pick, empty message, change-only re-injection).
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
- `background` session-start seeding is not ported: upstream warms the cache once
  at queen start when the session was created with an initial prompt — global
  scope only, up to 5 profile pins, `asyncio.wait_for(..., 3.0)`
  (`queen_orchestrator.py:1461-1479`; probe F3/F4). Its delivery is
  race-dependent even upstream (the seed only surfaces when the concurrent
  refresh has not replaced it). Go starts cold: the first completed refresh is
  the first delivery. The effect is limited to a slow or failed first refresh.
- The two scope picks are sequential in Go (`internal/memory/recall.go:205-216`)
  while upstream runs them concurrently (`recall_selector.py:247-250`; probe F1).
  Result sets are identical; on the background path the extra latency is off the
  turn, on the wait path it counts against `wait_seconds`.
- Delivery happens at the next client turn's start. Upstream drains at every
  outer-loop iteration (`agent_loop.py:1860`), so a rare same-turn continuation
  path (judge RETRY, ask_user re-entry) can deliver the pick one model call
  earlier than Go. A chat queen normally parks after a tool-free turn (probe E4),
  so the common path is the same: the pick rides the next message.
