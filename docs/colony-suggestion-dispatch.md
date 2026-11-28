# Colony creation dispatch: suggest_colony + task_create(new_colony=true)

> Lane: `colony-suggestion-dispatch`. Two queen tool calls open the "Create
> Colony" popup. Both are **framework-level** intercepts in the agent loop:
> the registered executor never runs, the loop returns a synthetic result to
> the model, and the host parks the turn (`colony_suggestion`) after emitting
> `COLONY_SUGGESTION_REQUESTED`.
>
> Python reference: `core/framework/agent_loop/agent_loop.py:5076-5175`
> (`suggest_colony`) and `:5178-5260` (`task_create(new_colony=true)`).

## 1. Dispatch chains

### 1.1 DM `suggest_colony` (independent phase)

1. The queen calls `suggest_colony(colony_id, reason?)`.
2. `internal/agentloop/colony_dispatch.go` intercepts it before
   `executeToolBatch`:
   - non-queen stream → queen-only error (ordinary tool error; turn goes on);
   - phase provider says anything but `independent` → phase error;
   - slug must match `^[a-z0-9_]+$` (after trimming);
   - success → synthetic "popup opened" tool result, `TurnResult.PendingColonySuggestion`
     carries `{colony_id, reason}`, and the turn ends after the batch.
3. `ChatRuntime.runTurn` stores the payload, parks on
   `agentloop.ParkReasonColonySuggestion`, and publishes
   `colony_suggestion_requested` (DM variant) before `execution_completed`.
4. The cursor's `pending_input` carries `colony_suggestion`, so a restarted
   runtime re-announces the popup.
5. Accept: the frontend POSTs `/api/sessions` with `colony_id` +
   `source_session_id`; the backend forks (DM path). Dismiss: the frontend
   injects a normal chat message ("Dismissed colony suggestion…").

### 1.2 Colony `task_create(new_colony=true)` (colony phase)

1. The queen calls `task_create(new_colony=true, goal, handoff, tasks)`.
2. The loop intercepts:
   - non-queen → queen-only error;
   - phase must be `colony` (when a phase provider exists);
   - `goal`, `handoff`, and a non-empty `tasks` array are required, each with
     the upstream error text;
   - the optional `PivotPayloadSink` runs first (it can veto with an error
     string); on success it stashes the payload on
     `session.pending_colony_pivot` while the call is still in flight;
   - success → synthetic "popup opened" tool result,
     `TurnResult.PendingColonyPivot` carries
     `{goal, handoff, tasks, source_phase:"colony"}`, turn ends after the batch.
3. `ChatRuntime.runTurn` parks on `colony_suggestion` and emits the colony
   variant (`colony_id=""`, `source_phase="colony"`, `goal`, `handoff`,
   `task_count`).
4. Dismiss: `POST /api/sessions/{id}/dismiss-colony-pivot` injects
   `colonyPivotDismissMessage` (`routes_session_colony.go`) and clears the
   pivot, waking the parked turn.
5. Accept: the frontend POSTs `/api/sessions` with `colony_id` +
   `source_session_id`; see §5 (Colony pivot accept) for the lean-handoff path.

## 2. Error texts (byte-exact)

| Case | Python | Go constant | Text |
|---|---|---|---|
| suggest, non-queen | `agent_loop.py:5083-5096` | `ColonySuggestionQueenOnlyError` | `ERROR: suggest_colony is queen-only. Workers fan out via run_worker inside an existing colony.` |
| suggest, wrong phase | `agent_loop.py:5100-5133` | `ColonySuggestionPhaseError` | `ERROR: suggest_colony is only available in the independent (DM) phase. You are already inside a colony — fan out with run_worker, schedule recurring runs with set_trigger, or share protocol with workers via write_skill.` |
| suggest, bad slug | `agent_loop.py:5135-5147` | `ColonySuggestionSlugError` | `ERROR: colony_id must be lowercase alphanumeric with underscores (e.g. 'morning_hn_digest'). The popup is pre-filled with this slug, so make it user-friendly.` |
| pivot, non-queen | `agent_loop.py:5198-5208` | `ColonyPivotQueenOnlyError` | `ERROR: task_create(new_colony=true) is queen-only. Workers don't spawn colonies.` |
| pivot, wrong phase | `agent_loop.py:5210-5226` | `ColonyPivotPhaseErrorFormat` | `ERROR: task_create(new_colony=true) is colony-only (currently '<phase>'). In DM, use new_session for unrelated work, or suggest_colony to spawn the first colony.` |
| pivot, no goal | `agent_loop.py:5228-5238` | `ColonyPivotGoalError` | <code>ERROR: task_create(new_colony=true) requires a `goal` — state the new colony's purpose in one sentence, in the user's terms. The new colony anchors on this.</code> |
| pivot, no handoff | `agent_loop.py:5240-5250` | `ColonyPivotHandoffError` | <code>ERROR: task_create(new_colony=true) requires a `handoff`. The new colony inherits NOTHING from this conversation; without a handoff its queen starts blind. Write a complete, objective brief: user goal in their terms, concrete data (names, URLs, IDs, file paths, account to use, exact requirements), decisions made and options ruled out, constraints, what 'done' looks like.</code> |
| pivot, no tasks | `agent_loop.py:5252-5260` | `ColonyPivotTasksError` | <code>ERROR: task_create(new_colony=true) requires a non-empty `tasks` array — the plan that gets seeded into the new colony.</code> |

Success results (model-visible, synthetic):

- suggest: `Colony creation popup opened with colony_id='<slug>'. Waiting for the user to confirm or dismiss. …`
- pivot: `Colony pivot popup opened (goal: <goal[:80]>). Waiting for the user to confirm a slug and create the new colony, or dismiss. …`

## 3. Payload fields

`COLONY_SUGGESTION_REQUESTED` (`eventbus.EmitColonySuggestionRequested`):

| Field | DM variant | Colony pivot variant |
|---|---|---|
| `colony_id` | queen-proposed slug | `""` (user fills it) |
| `reason` | optional queen rationale | absent |
| `source_session_id` | source session id | source session id |
| `source_phase` | absent | `"colony"` |
| `goal` | absent | queen-authored goal |
| `handoff` | absent | queen-authored brief |
| `task_count` | absent | `len(tasks)` |

Stashed payloads:

| Where | DM | Colony |
|---|---|---|
| `TurnResult` | `PendingColonySuggestion` `{colony_id, reason}` | `PendingColonyPivot` `{goal, handoff, tasks, source_phase}` |
| `ChatRuntime` | `pendingColonySuggestion` | `pendingColonyPivot` |
| `Session` | — | `session.pending_colony_pivot` (same map) |
| cursor `pending_input` | `colony_suggestion` | `colony_pivot` |
| park reason | `colony_suggestion` | `colony_suggestion` |

## 4. Phase tool surface

- `suggest_colony` is advertised in the independent phase only:
  `ChatRuntime.toolsForTurn` → `filterToolsForPhase` (adds the synthetic
  builder when the catalogue lacks it, drops it in colony), and
  `PhaseToolSurface.GetCurrentTools` mirrors the same gate for
  `/api/sessions/{id}/live_tools`.
- Colony-phase `task_create` exposes `new_colony` + `handoff`
  (`enableColonyTaskPivot`, wired for created and preloaded colony sessions).
  The handler is a defensive stub: if it ever runs, the intercept failed and
  the model gets a wiring-bug error. Limitation: a session created as DM and
  later attached to a colony via `POST /api/sessions/{id}/colony` keeps the
  `new_session` schema for the rest of its life (the catalogue is built once
  at create time); only sessions created/preloaded with a colony get
  `new_colony`.
- DM-phase `task_create` keeps `new_session` + `handoff` (existing wiring).

## 5. Accept/dismiss paths

| Path | Status |
|---|---|
| DM dismiss (normal chat message) | works |
| Colony pivot dismiss route | works (`handleDismissColonyPivot`, pending pivot cleared) |
| DM accept fork (`POST /api/sessions` + `source_session_id`) | fork exists; wake-up injection and `COLONY_CREATED` added here |
| DM accept: compact source transcript into the queen seed | **implemented** — `colony_fork_seed.go` copytree-merges the source queen session, rewrites `meta.json`, schedules a bounded background compaction into the colony's `seed_conversation`, and awaits the marker before opening the session; fail-soft to the raw copied transcript (`gap:dm_fork_seed_compaction` fixed, commit `2f73bbfd`) |
| Colony pivot accept (lean handoff: sibling colony seeded from `goal`/`handoff`/`tasks`, no compaction, no spawn lock) | **implemented** — `colony_pivot_lean.go` creates the sibling colony, seeds the handoff-only queen transcript and the task plan, leaves the source colony live and unlocked, and wakes the source queen (`gap:colony_pivot_lean_handoff` fixed, commit `96e64c62`) |

> 2026-11-24 更新：上述残余已在 `de827224` 修复。DM fork 的 `worker.json` 通过 `colony.BuildMeta` 继承 live queen 的 tool snapshot、phase-filtered skills catalog、protocols、skill dirs、loop_config、queen phase/id、tracker binding、searchable_tools 与 spawned_from/at；源会话 meta 写入 `agent_path`/`agent_name` 和 source binding/ColonyID；没有 live queen 时在创建 colony 目录前返回 503。`gap:dm_fork_worker_surface` 已标 fixed。

## 6. Tests

| Test | Parity |
|---|---|
| `agentloop/TestSuggestColonyIsQueenOnly` | `agent_loop.py:5083-5096` |
| `agentloop/TestSuggestColonyRejectsColonyPhase` | `agent_loop.py:5100-5133` |
| `agentloop/TestSuggestColonyRejectsInvalidSlug` | `agent_loop.py:5135-5147` |
| `agentloop/TestSuggestColonySuccessParksWithPayload` | `agent_loop.py:5150-5175` |
| `agentloop/TestSuggestColonyWithoutPhaseProviderSkipsPhaseGate` | `agent_loop.py:5100-5106` |
| `agentloop/TestTaskCreateNewColonyGates` | `agent_loop.py:5178-5226` |
| `agentloop/TestTaskCreateNewColonyRequiresGoalHandoffAndTasks` | `agent_loop.py:5228-5260` |
| `agentloop/TestTaskCreateNewColonySuccessParksWithPayload` | `agent_loop.py:5270-5330` |
| `agentloop/TestTaskCreateNewColonySinkVetoSkipsThePopup` | `agent_loop.py:5272-5295` |
| `agentloop/TestTaskCreateWithoutNewColonyStillExecutes` | intercept boundary |
| `server/TestSuggestColonyParksAndEmitsDMVariant` | `_await_user_input` DM event |
| `server/TestTaskCreateNewColonyParksStashesPivotAndEmitsColonyVariant` | `_await_user_input` colony event |
| `server/TestColonyPivotParkPersistsCursorPayloadAndDismissWakes` | cursor persistence + dismiss route |
| `server/TestRestoredColonySuggestionReannouncesAfterRestart` | restored-wait re-announce |
| `server/TestToolsForTurnAdvertisesSuggestColonyOnlyInIndependentPhase` | `_phase_tools` |
| `cli/TestEnableColonyTaskPivotAdvertisesNewColonyAndStubRefusesExecution` | `_request_new_colony_with_plan`, `PivotHandler` |
| `cli/TestCreateColonyFromSourceWakesSourceQueenAndPublishesColonyCreated` | `_inject_colony_confirmation`, `COLONY_CREATED` |
| `cli/TestCreateColonyFromSourceCopiesQueenSessionAndRewritesMeta` | `fork_session_into_colony` (copytree + meta) |
| `cli/TestCreateColonyFromSourceAwaitsCompactionBeforeInitialPrompt` | `fork_session_into_colony` (compaction await) |
| `cli/TestCreateColonyFromSourceSpawnsLeanHandoffSibling` | `_create_sibling_colony_from_colony` (lean handoff) |

## 7. Gates

```
gofmt -l internal/ cmd/            # empty
go vet ./...
go test ./internal/agentloop ./internal/server ./internal/tasks ./internal/cli -count=1
make parity
bash scripts/merge_postcheck.sh    # exit 0
```
