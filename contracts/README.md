# Contracts: OpenAPI 3.1 + AsyncAPI 3

This directory now carries two **canonical, generated** contract documents next to the
existing frozen evidence:

| File | Kind | Source of truth |
| --- | --- | --- |
| `openapi.json` | OpenAPI 3.1.0 | generated from `api-routes.json` + `api-responses/reference/*` |
| `asyncapi.json` | AsyncAPI 3.0.0 | generated from `sse-frames/`, `channel-plugin-protocol.json`, `docs/channel-websocket-design.json` |
| `gen_openapi_contract.py` (in `go_hive/scripts/`) | generator | single source of truth for both documents |
| `check_openapi_contract.py` (in `go_hive/scripts/`) | drift checker | validates shape, refs, coverage and digests |
| `test_gen_openapi_contract.py` (in `go_hive/scripts/`) | focused tests | determinism and coverage guards |

The change is **additive**. No existing frozen artifact was renamed, rewritten or deleted,
and `api.proto`, `api-routes.json`, `api-responses/`, `sse-frames/` and the channel protocol
remain the evidence of record until the migration is explicitly completed.

## Commands

```bash
# Regenerate both canonical documents (deterministic; no timestamps)
uv run python go_hive/scripts/gen_openapi_contract.py --write

# Byte-for-byte drift check against the generator (exit 1 on drift)
uv run python go_hive/scripts/gen_openapi_contract.py --check

# Full validation: JSON shape, local $refs, route/method/snapshot coverage,
# AsyncAPI channel/message refs, source digests, generator drift
uv run python go_hive/scripts/check_openapi_contract.py

# Focused guard tests
uv run python go_hive/scripts/test_gen_openapi_contract.py
```

Never edit `openapi.json` or `asyncapi.json` by hand: edit the generator, then run
`--write` and the checker.

## CI wiring

The checker is a standing gate, not a release-only step: `make ci` runs
`compat-check`, which calls `go_hive/scripts/check_openapi_contract.py` as **C13**
(stdlib only, offline, ~4 s). Any non-zero checker exit — validation error, generator
drift, or unreadable document — is a gate FAIL, and a missing checker is a FAIL too,
never a SKIP.

```bash
make -C go_hive/go-hive compat-check   # C1–C13, verdict=PASS required
```

The legacy `api.proto` is intentionally kept: it remains the field-shape evidence
checked by C7 (`check_api_proto.py`), while C13 guards the canonical documents.

## Coverage

### OpenAPI (`openapi.json`)

| Metric | Value |
| --- | --- |
| Target routes / operations | **226 / 226** (GET 92, POST 85, DELETE 20, PUT 18, PATCH 11) |
| Paths | 181 |
| Tags | 27 |
| Frozen snapshot endpoints | **107 / 107 mapped** |
| Operations with a snapshot | 87 |
| Operations `complete` (2xx JSON schema) | 62 |
| Operations `partial` | 154 |
| Operations `skipped` (documented reasons) | 10 |
| Operations with no snapshot | 139 |
| Response objects | 244 |
| Response schemas | 102 |
| Schemas | 73 (72 snapshot-derived + `ErrorResponse`) |

Two duplicate `(route, status)` snapshot groups (`session_create` / `session2_create` and
`session_detail` / `session_detail_cold`) are merged into one response each and recorded in
`x-snapshot-endpoints`.

### AsyncAPI (`asyncapi.json`)

| Metric | Value |
| --- | --- |
| Channels | 5 (1 WebSocket design-only + 4 SSE streams) |
| Operations | 24 (20 channel/WS + 4 SSE receive operations) |
| Messages | 36 (20 channel/WS + 16 SSE event messages) |
| Schemas | 26 (9 `hive-channel/1` envelopes + `SseFrame` + 16 event data schemas) |
| SSE event types | 16 |
| SSE frames | live 35, history 43, replay 17, stop 18 |
| WebSocket status | **design-only / not production** |

## Status semantics

* `x-contract-status: complete` — at least one 2xx JSON response schema derived from a frozen
  reference snapshot.
* `x-contract-status: partial` — no 2xx JSON schema: no snapshot exists, or the body is
  `text/event-stream` / `text/plain`, or only failure snapshots exist.
* `x-contract-status: skipped` — no snapshot by design; `x-skip-class` and `x-skip-reason`
  reproduce `api-responses/index.json.skipped`.
* `x-contract-status: design-only` — AsyncAPI WebSocket channel only; `x-production` is
  `false` and the address is **not** a production route.
* The top-level `x-contract-status` is `partial` for both documents because route coverage is
  complete but schema coverage is not.

Routes with no snapshot get a single `default` response with no schema — never an invented one.

## How schemas are inferred

* Examples are the frozen `canonical` payloads in `api-responses/reference/*.json`.
* Property types prefer the snapshot `shape` map (e.g. `number(int)` → `integer`,
  `number(float)` → `number`) so canonicalised placeholders do not become strings.
* `required` means “present in every observed sample”, not a handler guarantee. This is
  documented in `x-contract.conventions`.
* Objects set `additionalProperties: true`; empty arrays leave `items: {}` with a note.
* Plain `{"error": "..."}` failure bodies reference the generic `ErrorResponse`; richer
  failure bodies get a route-specific schema.
* SSE data schemas use the raw reference capture only for placeholder types (`seq`,
  `snapshot_seq`, …); the canonical frame goldens supply presence and event names.
* Non-JSON snapshots are typed as strings: `text/event-stream` points at the AsyncAPI frame
  contract, `text/plain` keeps the observed body as the example.
* Request bodies are **not** frozen, so no request schemas are emitted.

## Legacy / frozen relationships

| Artifact | Role | Consumed by |
| --- | --- | --- |
| `api-routes.json` | authoritative Go target route table (226 routes) | generator, checker |
| `api-responses/index.json` | 107-endpoint snapshot index, skip list | generator, checker |
| `api-responses/reference/*.json` | frozen reference payloads + shape maps | generator |
| `api-responses/target/*.json` | Go target evidence | not consumed (parity evidence only) |
| `api.proto` | partial proto3 response shape (42 messages, 14 endpoints) | unchanged legacy |
| `sse-frames/index.json` + `reference/*.json` | SSE frame goldens | generator, checker |
| `channel-plugin-protocol.json` | `hive-channel/1` JSON-RPC schemas | generator (schema reuse) |
| `docs/channel-websocket-design.json` | WebSocket design (status `design`) | generator (design-only channel) |
| `api-proto-check.json`, `cli-surface.json`, … | other frozen contracts | unchanged |

Migration rules:

1. Do not delete, rewrite or reformat any legacy artifact in this change train.
2. `openapi.json` / `asyncapi.json` are regenerated, never hand-edited.
3. Existing CI must keep passing; the new checker is additive and exits non-zero on drift.
4. Retire `api.proto` / snapshot artifacts only in a later, explicitly approved migration.

## Residual gaps

* **139 target routes have no response snapshot** (129 `partial` without a skip reason,
  10 `skipped` with documented reasons). Their response schemas are intentionally absent.
* **No request bodies.** Request schemas need a separate capture lane.
* **Three non-JSON snapshots** (`session_events`, attachment download/missing) are typed as
  strings; their probe wrappers are not HTTP bodies.
* **Two duplicate snapshot groups** collapse to one response/example each.
* **Additional SSE routes are not frozen**: `GET /api/events/global`,
  `GET /api/sessions/live`, `GET /api/sessions/{session_id}/workers/stream`,
  `GET /api/browser/status/stream`. They are listed in `x-contract.sse.not_frozen`.
* **WebSocket is design-only.** The stdio channel transport is unchanged; no production
  WebSocket route is claimed.
* The Python-only SPA skip entry `GET /{path:.*}` has no Go target route and is not
  represented; the Go `GET /` SPA fallback is.
* Query parameters appear only when observed in a snapshot (`since`, `path`); requiredness is
  not frozen.

## Suggested CI hook (not added here)

```bash
uv run python go_hive/scripts/gen_openapi_contract.py --check
uv run python go_hive/scripts/check_openapi_contract.py
uv run python go_hive/scripts/test_gen_openapi_contract.py
```
