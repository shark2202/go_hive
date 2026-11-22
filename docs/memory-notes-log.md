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
- `memory_reflection.model` is read/served, but the reflection scheduler still
  runs on the session model — the `worker_llm`-slot wiring stays on the
  `memory-notes-1.0.15-parity` backlog item (`worker 模型`).
- Cloud push on memory changes (`schedule_push`) belongs to the notes-push
  sibling lane; not wired here.
