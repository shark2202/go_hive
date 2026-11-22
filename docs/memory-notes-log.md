# Memory notes — change log, history/restore API, owner guard, confirmed age

Lane `mem-notes-log` (2026-11-22, from `main` @ 6ce8decc). Ports the 1.0.15
「Notes in Time」 memory-log surface: every memory-file change is kept in
`log.jsonl` with who/why/before/after, the Memory page can list history, retire
and restore, a file the owner wrote cannot be changed by a queen or her
reflection, and a recalled note's age is counted from its last `confirmed` day.

## Golden capture (reference)

`docs/e2e-assets/memory-notes/` — probe against the Python 1.0.15 reference
(`probe_reference.py`, run with `uv run`), plus the upstream test modules
(`test_memory_log.py`, `test_routes_memories.py`, `test_routes_config_memory.py`,
`test_queen_memory.py`, `test_config.py`; 95 tests green):
`golden_log.jsonl`, `golden_history.json`, `golden_api.json`,
`golden_config.json`, `golden_guard.json`, `golden_confirm.json`, `summary.md`.

## `log.jsonl`

One JSON object per line, key order fixed (Python `json.dumps` separators,
verified byte-for-byte by `TestTheLogRecordsEachChangeWithItsEvidence`):

`{"at": <local ISO-8601, seconds, numeric offset>, "op": write|retire|restore,
"file": <name>, "author": owner|queen|reflection, "reason": <str>,
"sources": [ {…} ], "before": <str|null>, "after": <str|null>}`

`History()` adds `n` first — the 0-based line index (malformed lines still
count), so a rollback target stays stable. `retire` keeps the file's text in
the log; `restore` writes an earlier `after` (or a retirement's `before`) back
verbatim and logs itself as the owner's change.

## Memory file frontmatter

`author`, `learned`, `confirmed` are stamped by every write:
`learned` never moves later; `confirmed` moves to today when a conversation
supports the file and stays put when the change only tidies it up
(`confirms=false`, the long reflection's housekeeping). Recall age
(`FormatRelativeAge`) is `today − confirmed` — `last confirmed N days ago`
— falling back to the file's mtime for notes from before `confirmed` existed.

## Owner guard

`author: owner` files refuse queen/reflection writes and retirements with
`<file> was written by the owner; write what you learned to another file and
tell them where the two differ`. A seeded memory (POST create) claims nobody,
so the reflection can build on it.

## Routes (all reference fields verbatim)

| Method | Path | Response fields |
| --- | --- | --- |
| GET | `/api/memories` | `{memories:[{path, filename, name, type, description, author, learned, confirmed, mtime, scope, scope_name}]}` |
| GET | `/api/memories/file` | list fields + `content` |
| POST | `/api/memories/file` | list fields + `content` + `created` (idempotent `{created:false,path}`) |
| PUT | `/api/memories/file` | list fields + `content` (write is `author=owner`, reason `edited on the Memory page`) |
| DELETE | `/api/memories/file` | `{deleted: path}` (retires; text stays in the log) |
| GET | `/api/memories/history` | `{path, changes:[{n, at, op, file, author, reason, sources, before, after}]}` |
| POST | `/api/memories/restore` | memory object + `content`; body `{n:int}`; unknown `n` → 404 |
| GET | `/api/memories/retired` | `{retired:[{path, filename, n, at, author, reason, scope, scope_name}]}` |
| GET | `/api/config/memory` | `{memory:{recall_mode, recall_modes, recall_mode_env, reflection_model, reflection_models}}` |
| PUT | `/api/config/memory` | same; saves named keys, keeps each section's other keys; `HIVE_MEMORY_RECALL` overrides `recall_mode` |

Contract: `go_hive/contracts/api-routes.json` (missing-in-Go effective 5 → 0).

## Go implementation

- `internal/memory/memory_log.go` — `Write`/`Retire`/`History`/`Retired`/`Restore`,
  `AuthorOf`/`ConfirmedOn`, owner guard, atomic note writes (`atomicfs`),
  append-only `log.jsonl`; one process-wide mutex per read-modify-write.
- `internal/memory/memory.go` — `MemoryFile` carries `author`/`learned`/`confirmed`.
- `internal/memory/recall.go` — `FormatRelativeAge` counts from `confirmed`.
- `internal/memory/reflection.go` — reflection `write_memory_file` /
  `delete_memory_file` go through the log as `reflection`; long reflections pass
  `reorganised` (upstream `confirms=False`); default reflection model `worker`.
- `internal/server/routes_memories_extra.go` — the eight `/api/memories` routes.
- `internal/server/config_memory.go` — Settings → Memory GET/PUT.

Tests: `internal/memory/memory_log_test.go` (upstream `test_memory_log.py`
ported + line-format pin), `internal/server/routes_memories_log_test.go`
(upstream `test_routes_memories.py` + frozen field lists),
`internal/server/config_memory_test.go` (upstream `test_routes_config_memory.py`).

## Known divergences (registered)

- `sources` object keys marshal in sorted order (Python keeps caller
  insertion order); the value space and separators match.
- A log line missing `sources` re-serialises as `"sources": null` instead of
  omitting the key; both writers always emit `[]`.
- Cloud push on memory changes (`schedule_push`) belongs to the notes-push
  sibling lane; not wired here.

## 1.0.15 worker-slot wiring (lane `g1-resid`, 2026-11-22)

The `memory-notes-1.0.15-parity` backlog item `memory-reflection-worker-slot`
is closed: the reflection scheduler no longer runs on the session model.

Upstream `ef4779ef` ("her memory notes reach her before she answers") moved
reflection and the dated timeline onto the worker model:
`memory_reflection.model` — `"worker"` (default; the `worker_llm` slot, or her
own model when none is set) or `"main"` (her own model) — resolved at each
call, with the worker provider rebuilt only when the `worker_llm` section
changes (`framework/server/session_manager.py::_MemoryLLM`;
`framework/server/queen_orchestrator.py::_queen_loop` passes `_memory_llm` to
`subscribe_reflection_triggers` *and* `subscribe_timeline_triggers`).

The three upstream tests were run against the in-tree 1.0.15 reference to pin
the rules (`uv run pytest tests/test_queen_memory.py -k memory_llm`: 3 passed)
and are ported one-to-one.

Go implementation:

- `internal/server/chat_memory_llm.go` — `chatMemoryLLM`, the per-call
  resolver. `current()` mirrors `_MemoryLLM.current`: model != "worker" →
  session provider; else the `worker_llm` section (worker-provider cache keyed
  by the section's JSON, rebuilt only when it changes), falling back to the
  session provider when the slot is unset (`build_worker_llm` → None).
- `internal/server/chat_runtime.go` — the runtime builds the resolver over
  `deps.LLM` when it has a `HomeRoot`, hands it to the `ReflectionScheduler`,
  and uses it for both timeline writers (`startTimeline` backfill,
  `timelineAfterTurn`), matching upstream's shared `_memory_llm`.

Tests: `internal/server/chat_memory_llm_test.go` — the three upstream parity
tests (`test_memory_llm_follows_the_setting_while_the_session_runs`,
`..._builds_the_worker_model_again_only_when_its_slot_changes`,
`..._falls_back_to_her_model_without_a_worker_model`) plus runtime wiring
checks for reflection and the timeline.
