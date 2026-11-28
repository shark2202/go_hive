# Soul spec — a queen's own notes about herself

`internal/soul/` is the queen self-model: a small, sectioned set of dated lines she writes **about herself**, revised once a day from what was actually said in her chats, and injected into her static system prompt under `<soul>`.

- Python reference: `framework/agents/queen/soul.py`, `soul_reflection.py`, `soul_probes.py`, `framework/server/routes_soul.py`, `framework/config.py::get_soul_config`.
- Go port: `go_hive/go-hive/internal/soul/` (4,317 lines incl. tests) + `internal/server/routes_credentials_soul.go`, `routes_soul_probe.go`.
- Position in the prompt: **identity → soul → phase base prompt** (`QueenPhaseState.get_static_prompt`). The header says it: *"Where they disagree with your identity above, your identity comes first."*

## 1. Storage layout

`$HIVE_HOME/queens/<queen_id>/soul/`

| File | Content |
|---|---|
| `soul.json` | Current soul: `{version:int, updated_at:ISO-8601, sections:{key:[{text,date}]}}` |
| `history/v<N>.json` | Every saved version, same shape as `soul.json` |
| `log.jsonl` | Append-only: one line per save, `{version, at, author, note, edits[]}`. `author` is `reflection`, `sediment`, or the editing surface |
| `proposals/<id>.json` | A reflection's proposed edits: `{id, created_at, base_version, edits[], summary, status, applied[], sources[], bootstrap, probes[]}`; an edit is `{op, section, text, old, date, evidence[], reason, expired}` |
| `probes.json` | Fixed questions for the "try her out" probe: `[{q, anchor}]` |
| `state.json` | Reflection bookkeeping: `{cursors:{session_id: last_event_at}, last_run, attempts:{day,count,at}}`；Go 额外写 `last_error` / `last_error_at`（增量字段，失败可见，不写 `last_run`） |
| `sediment.json` | **Go-only** bookkeeping for the sediment seam (what it wrote, when); never soul content |

Atomic writes are temp-file + rename, 2-space JSON with `": "`/`", "` spacing and no trailing newline (Python `json.dumps(indent=2)` shape). Sections are a map; item text is trimmed and blank items are dropped on read.

## 2. Configuration — `configuration.json`

```json
{"soul": {"queens": {"queen_growth": {
  "enabled": true, "mode": "review", "hour": 4, "model": "queen",
  "max_edits": 5, "bootstrap_edits": 30,
  "include_titles": [], "exclude_titles": [],
  "sources": {"file": "…", "keys": []}, "bootstrap_memories": [],
  "sections": [{"key": "now", "title": "…", "limit": 300, "max_age_days": 14, "evidence": 1}],
  "session_chars": 12000, "message_chars": 400
}}}}
```

**No entry ⇒ soul off** for that queen (`GET /api/queen/<id>/soul` answers `{"queen_id": …, "enabled": false}`; `PromptFor` renders nothing). `"enabled": false` is the same as absent. Nothing is enabled out of the box.

Batch enabling (no product-wide switch exists — the per-queen entry is the switch):

```bash
python3 go_hive/scripts/enable_souls.py --home ~/.hive-go --dry-run   # show the plan
python3 go_hive/scripts/enable_souls.py --home ~/.hive-go             # every queen, review mode
```

The helper writes the same `{enabled: true, mode: review}` entry for every queen found in `$HIVE_HOME/queens/` plus the 13 shipped ids, keeps existing entries untouched (unless `--force`), excludes the shared `default` persona unless `--include-default`, and backs the file up before writing.

| Key | Default | Rules |
|---|---|---|
| `mode` | `review` | `review` = proposals wait for approval; `auto` = they apply at once |
| `hour` | `4` | Reflection runs at/after this local hour, once a day |
| `model` | `queen` | `queen` = her own model slot; `worker` = the worker/memory slot |
| `max_edits` | `5` | Edits one ordinary reflection may propose (≥1) |
| `bootstrap_edits` | `30` | Edits the first draft may make (≥1) |
| `sections` | five below | Invalid/duplicate lists fall back to defaults |
| `header` | fixed sentence | Line above the soul inside `<soul>`; overridable |
| `session_chars` / `message_chars` | `12000` / `400` | How much of each chat / each message the reflection reads |
| `include_titles` / `exclude_titles` | `[]` | Regexes matched against **chat session titles**: when `include_titles` is set only matching chats are read; any match on `exclude_titles` skips that chat |
| `sources.file` / `sources.keys` | – | When set, chats are read from that JSON file (per key) instead of `events.jsonl` |
| `bootstrap_memories` | `[]` | Memory files the first draft absorbs |

Default sections (order = render order):

| key | title | limit (chars) | evidence | expiry |
|---|---|---:|---:|---|
| `now` | Now: what's on my mind, what I'm working on | 300 | 1 | 14 days (`max_age_days`) |
| `people` | Me and them | 800 | 2 | – |
| `threads` | Open threads and promises | 600 | 1 | – |
| `views` | What I think, and why | 600 | 2 | – |
| `changes` | How I've changed | 400 | 2 | – |

Saving enforces the limits: any section over its `limit` fails the save with `<title>: N/M characters` (no truncation).

## 3. Rendering into the prompt

`PromptFor(config, queensDir, queenID)` → `Render`:

```
<soul>
These are your own notes about yourself, written by you from what has happened to you.
Where they disagree with your identity above, your identity comes first.

## Now: what's on my mind, what I'm working on
- … (11/23)

## Me and them
- …
</soul>
```

Configured section order; empty sections omitted; `date` shortened to `M/D`; nothing rendered when the queen is disabled or has no items (empty `<soul>` is not emitted). It rides the **cached static prefix**, right below identity, above the phase prompt.

## 4. The daily reflection

`DailyRunner.Run` (server lifetime): one pass after a two-minute settle delay (upstream `scheduler(first_after_s=120.0)`, catching up a day the process was down), then every 10 minutes; passes are sequential, never overlapping. The model one reflection runs on follows upstream `llm_for`: the `worker_llm` slot when her `model` is `worker` and that slot is configured, else her `queen_llm.<id>` section, else the main `llm`. `ReflectionDue`:

- local hour ≥ `cfg.hour`;
- `last_run` ≠ today;
- in `review` mode: **no pending proposal** (the previous one must be decided first);
- failed days retry **1 hour apart, at most 3 attempts per day** (`state.attempts`); a failure is never recorded as done.

One reflection (`Reflect`): reads each session's `events.jsonl` since that session's cursor (`state.cursors`), filters per config (`session_chars`/`message_chars`), shows her the chats plus her current soul, and lets her propose edits through tools — at most `max_edits` (or `bootstrap_edits` for the first draft, which also reads `bootstrap_memories`), 8 model round-trips (16 for bootstrap). The code enforces:

- every `evidence` quote must appear **word for word** in what she read (min 4 chars);
- a section that requires N evidence quotes needs them from **different people or chats**;
- proposed dates may point at most 90 days back; items older than `max_age_days` are dropped;
- section limits and total edits are enforced before anything is written.

In `review` mode the proposal waits; `auto` applies immediately. Cursors advance so each chat is read once.

## 5. HTTP surface (`routes_soul.py` parity)

| Method + path | Behavior |
|---|---|
| `GET /api/queen/{id}/soul` | `enabled:false`, or the full view (soul, sections with limits, problems, pending proposal, `last_run`/`last_result`) |
| `PUT /api/queen/{id}/soul` | Body `{"sections": {key: [{"text","date"}]}}`; unknown keys → 400 `Unknown sections: …`; over-limit → 409; unknown queen → 404 |
| `POST /api/queen/{id}/soul/proposals/{pid}/decide` | `{"accept": [indexes] | null}`; `null`/absent = all; non-pending → 409 `This proposal was already <status>`; bad type → 400 `'accept' must be a list of change indexes, or null for all` |
| `POST /api/queen/{id}/soul/rollback` | `{"version": N}` restores that history version; bad type → 400 `'version' must be a version number` |
| `POST /api/queen/{id}/soul/reflect` | Manual run; `{"bootstrap": bool}`; already reflecting → 409 `She is reflecting already`; pending proposal → 409 `Decide on the waiting proposal first`; accepted → 202 `{"started": true}` |
| `POST /api/queen/{id}/soul/proposals/{pid}/probe` | Asks her the `probes.json` questions against the proposal, before and after the edits; a question whose header names a speaker id (`[Ann(12345)]:`) is answered with that speaker's memory files appended as `<recalled_memories>` exactly as live recall brings them (her scope before global, at most 3); no pending proposal → 404 `No pending proposal by that id`; no questions → 409; already probing → 409 `Already trying her out` |

## 6. Known divergence (registered)

`sediment.go` is **Go-only** (`// Parity-exempt: Go-only sediment seam`): after a *memory* reflection, its durable conclusions are also offered to the soul (section `now` or the first configured one, ≤3 conclusions, ≤240 runes each). Upstream only has the daily soul reflection. The seam obeys the soul's approval rules like that daily reflection does:

- `mode: auto` — the conclusions are saved at once, `sediment.json` tracks what the seam wrote so it may drop those items again when the section fills (never a human's item).
- `mode: review` (the default) — nothing is written to the soul; the conclusions become one **pending proposal** (`proposals/<date>-memory-<HHMMSS>.json`, only `add` edits, `sources` = the memory files) that the owner decides through the ordinary decide route. An existing pending proposal blocks the pass entirely, exactly as it blocks the daily reflection, and review mode never evicts: a full section drops the conclusion instead.

Storage stays upstream-shaped (`soul.json` + `history/` + `log.jsonl` via `Save`), so the soul routes read it unchanged.

## 7. Invariants and checks

- A queen id must be exactly one path element (`ErrInvalidQueenID`), and sediment refuses a queen with no directory (`ErrUnknownQueen`) — a soul write can never invent a queen or escape `queens/<id>/soul`.
- Soul files are read defensively: missing/malformed JSON reads as empty, never as an error; writes are atomic.
- `go test ./internal/soul -count=1` → **29 tests pass** (this tree, 2026-11-23).

## 8. File map

| Concern | File |
|---|---|
| Data model (`Soul`, `Item`, `Proposal`, `Edit`) | `internal/soul/soul.go` |
| Config resolution + defaults | `internal/soul/config.go` (`get_soul_config` parity) |
| Load/save/history/proposals + rendering | `internal/soul/store.go` (`soul.py` parity) |
| Daily reflection (prompt, tools, evidence checks, bootstrap) | `internal/soul/reflection.go` + `reflection_run.go` (`soul_reflection.py`) |
| Probes | `internal/soul/probes.go` (`soul_probes.py`) |
| Go-only sediment | `internal/soul/sediment.go` |
| HTTP | `internal/server/routes_credentials_soul.go`, `routes_soul_probe.go` |
| Prompt injection | `internal/server/chat_queen_static.go` + `chat_runtime_colony.go` (`soul.PromptFor`) |