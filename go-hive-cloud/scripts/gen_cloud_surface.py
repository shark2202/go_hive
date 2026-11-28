#!/usr/bin/env python3
"""Generate and verify the canonical go-hive-cloud API surface.

The surface (``contracts/cloud-surface.json``) is the single machine-readable
inventory of every HTTP endpoint, SSE stream and WebSocket the private cloud
replacement knows about. It is regenerated from three inputs:

  * registered routes      internal/httpapi/server.go :: register()
  * observed cloud contract contracts/observed-contract.json
  * curated metadata       the tables below (implemented / planned / inferred / unknown)

Status vocabulary
-----------------
  implemented  a route is registered by this module today
  planned      endpoint is observed/known upstream but not built yet (M2–M7)
  inferred     path is known from client code but no structured contract entry exists
  unknown      a client calls it, but neither method nor path is recoverable here

Usage
-----
  python3 scripts/gen_cloud_surface.py            # write JSON + HTML
  python3 scripts/gen_cloud_surface.py --check    # fail on drift or invariant break
"""
from __future__ import annotations

import argparse
import html
import json
import re
import sys
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
SERVER_GO = ROOT / "internal" / "httpapi" / "server.go"
OBSERVED = ROOT / "contracts" / "observed-contract.json"
OUT_JSON = ROOT / "contracts" / "cloud-surface.json"
OUT_HTML = ROOT / "docs" / "cloud-surface.html"

SCHEMA = "hive-cloud-surface/v1"
SERVICES = [
    {
        "id": "hive-backend",
        "role": "Inbox, team global-db, CRM grid, team senders pool",
        "families": ["gateway", "global-db", "inbox", "crm", "senders"],
        "base_env": ["HIVE_CLOUD_BASE"],
        "auth": "jwt <operator-jwt> for backend calls; Bearer <streamToken> for inbox/stream-token",
        "notes": "Closed-source Express/TS service; the contract here is recovered from open-source clients.",
    },
    {
        "id": "llm-proxy",
        "role": "Anthropic Messages proxy + image generation",
        "families": ["messages", "images"],
        "base_env": ["HIVE_LLM_BASE_URL", "HIVE_CLOUD_BASE"],
        "auth": "Bearer streamToken/JWT on /v1/messages; HIVE_API_KEY (also Bearer) on /v1/images/generations",
        "notes": (
            "Rust service at llm.open-hive.com; internal routing/billing is opaque. M5 proxies both observed "
            "client endpoints to explicitly configured upstreams (Messages: Anthropic-compatible base + "
            "x-api-key/bearer credential; images: OpenAI-compatible JSON endpoint + Bearer credential). Model "
            "routing, billing and rate limits are not implemented."
        ),
    },
    {
        "id": "aden-credentials",
        "role": "Aden credential broker (integrations and senders)",
        "families": ["credentials"],
        "base_env": ["ADEN_API_URL"],
        "auth": "api-key (ADEN_API_KEY)",
        "notes": "Observed base https://api.adenhq.com for /v1/credentials; sender registry defaults to https://app.open-hive.com.",
    },
    {
        "id": "go-hive-cloud-extension",
        "role": "Private-deploy operations and extensions",
        "families": ["health", "inbox-reply"],
        "base_env": ["HIVE_CLOUD_BASE"],
        "auth": "none for /health; operator JWT for /internal/inbox/reply",
        "notes": "Not part of the observed hive-backend contract; safe to change with this surface.",
    },
]
SERVICE_ORDER = {svc["id"]: i for i, svc in enumerate(SERVICES)}
STATUS_ORDER = {"implemented": 0, "planned": 1, "inferred": 2, "unknown": 3}

HTTP_METHODS = {
    "http.MethodGet": "GET",
    "http.MethodPost": "POST",
    "http.MethodPut": "PUT",
    "http.MethodPatch": "PATCH",
    "http.MethodDelete": "DELETE",
    "http.MethodHead": "HEAD",
    "http.MethodOptions": "OPTIONS",
}

ROUTE_RE = re.compile(
    r's\.handle\(\s*(?P<method>http\.Method[A-Za-z]+)\s*,\s*"(?P<path>[^"]+)"\s*,\s*'
    r"(?P<public>true|false)\s*,"
)


class SurfaceError(RuntimeError):
    pass


# ---------------------------------------------------------------------------
# Curated endpoint metadata
#
# Every key here that is registered in server.go must be status=implemented;
# every observed-contract endpoint must have at least one entry with the same
# method+path (unknown-path sender entries cannot match and are exempt).
# ---------------------------------------------------------------------------

def endpoint(
    id: str,
    service: str,
    method: str,
    path: str,
    auth: str,
    status: str,
    milestone: str,
    *,
    request: dict[str, Any] | None = None,
    response: dict[str, Any] | None = None,
    errors: list[dict[str, Any]] | None = None,
    sse_events: list[str] | None = None,
    anchors: list[str] | None = None,
    notes: str = "",
    client_method: str | None = None,
) -> dict[str, Any]:
    record: dict[str, Any] = {
        "id": id,
        "service": service,
        "method": method,
        "path": path,
        "auth": auth,
        "status": status,
        "milestone": milestone,
        "request": request or {"fields": {}},
        "response": response or {"status": None, "shape": None},
        "errors": errors or [],
        "sse_events": sse_events or [],
        "source_anchors": anchors or [],
        "notes": notes,
    }
    if client_method:
        record["client_method"] = client_method
    return record


SIGNED_OUT = {
    "status": 401,
    "body": {
        "error": "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."
    },
    "golden": "internal/httpapi/testdata/globaldb-unauthorized.json",
}
GATE_MESSAGE = {"status": 400, "body": {"error": "<gate or SQLite message>"}, "note": "store.GateError / store.SQLError"}
TIMEOUT = {"status": 408, "body": {"error": "statement exceeded 5s"}, "note": "configurable via --statement-timeout"}
INVALID_JSON = {"status": 400, "body": {"error": "invalid JSON body"}}

CURATED: list[dict[str, Any]] = [
    # ---- go-hive-cloud extensions (implemented) ---------------------------
    endpoint(
        "ext-health", "go-hive-cloud-extension", "GET", "/health", "none", "implemented", "M1",
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/health.json",
            "shape": {"status": "ok", "service": "hive-cloud", "version": "string", "time": "RFC3339"},
        },
        anchors=["internal/httpapi/server.go:149 handleHealth"],
        notes="Ops-only extension; not part of the observed cloud contract.",
    ),
    endpoint(
        "hb-gateway-availability", "hive-backend", "GET", "/v1/gateway/availability", "none",
        "implemented", "M1",
        request={"fields": {"from": "string? (query, passthrough)"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/gateway-availability.json",
            "shape": {"available": True, "gateway": "hive-cloud", "from": "string"},
            "note": "client never parses the body",
        },
        anchors=["internal/httpapi/server.go:150", "go_hive/go-hive/internal/cli/loader.go:1133", "core/framework/cli/loader/cli.py:965"],
        notes=(
            "Fire-and-forget startup ping. observed-contract.json files it under hive-backend while "
            "go_hive/docs/cloud-dependencies.md §1.5 traces the ping to api.adenhq.com — host attribution "
            "is unresolved; path + query passthrough is the contract."
        ),
    ),
    endpoint(
        "hb-refresh-stream-token-from-stream", "hive-backend", "POST",
        "/user/refresh-stream-token-from-stream", "bearer-stream-token", "implemented", "M1/M3",
        request={"fields": {"streamToken": "string"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/stream-token-refresh.json",
            "shape": {"streamToken": "string"},
            "tests": [
                "internal/httpapi/m3_contract_test.go:TestStreamTokenRotationWindow",
                "internal/httpapi/m3_contract_test.go:TestStreamTokenRejectedAfterRotationWindow",
                "harness/streamtoken-smoke",
            ],
        },
        errors=[
            INVALID_JSON,
            {
                "status": 401,
                "body": {"error": "hive_stream_token_invalid"},
                "golden": "internal/httpapi/testdata/stream-token-invalid.json",
            },
            {"status": 500, "body": {"error": "internal error"}},
        ],
        anchors=["internal/httpapi/server.go:152", "core/framework/server/streamtoken_refresh.py:154-190"],
        notes=(
            "Route-level auth is none: the old streamToken in the body is the credential. New token is "
            "signed with StreamSecrets[0], TTL 24h. Expired/wrong-signature/non-stream tokens → 401. M3 adds "
            "the dual-key rotation window: a token signed with the configured previous secret still refreshes "
            "while that secret is present; removing it rejects the old token. Rotation itself does not revoke "
            "an unexpired token — there is no observed revocation ledger (sid blacklist UNKNOWN)."
        ),
    ),
    endpoint(
        "ext-internal-inbox-reply", "go-hive-cloud-extension", "POST", "/internal/inbox/reply",
        "bearer-jwt", "implemented", "M1",
        request={"fields": {"correlation_token": "string", "text": "string", "team_id": "string?"}},
        response={
            "status": 200,
            "shape": {"ok": True, "message_id": "string", "reply_id": "string", "delivered": "int"},
            "tests": [
                "internal/httpapi/inbox_test.go:TestInternalInboxReplyErrors",
                "internal/httpapi/inbox_test.go:TestInboxSSEQueuedReply",
            ],
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "correlation_token is required"}},
            {"status": 400, "body": {"error": "text is required"}},
            {"status": 403, "body": {"error": "team mismatch"}},
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/inbox.go:262-327 handleInternalInboxReply"],
        notes=(
            "Private-deploy extension for a self-hosted inbox UI. The real cloud reply endpoint is "
            "UNKNOWN (closed-source Inbox UI), so this must never be treated as contract-compatible."
        ),
    ),
    # ---- global-db (implemented) -----------------------------------------
    endpoint(
        "hb-globaldb-sql", "hive-backend", "POST", "/v1/global-db/sql", "bearer-jwt", "implemented", "M1",
        request={"fields": {"sql": "string", "row_cap": "int?"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-sql-write.json",
            "shape": {"rows": "object[]", "rowCount": "int", "truncated": "bool"},
        },
        errors=[INVALID_JSON, {"status": 400, "body": {"error": "sql is required"}}, GATE_MESSAGE, TIMEOUT, SIGNED_OUT],
        anchors=["internal/httpapi/globaldb.go:12-64", "go_hive/go-hive/internal/globaldb/client.go:36-41"],
        notes="Free-form gated SQL (DDL+DML+SELECT). Writes advance the change cursor once per statement.",
    ),
    endpoint(
        "hb-globaldb-query", "hive-backend", "POST", "/v1/global-db/query", "bearer-jwt", "implemented", "M1",
        request={"fields": {"sql": "string", "row_cap": "int?"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-query.json",
            "shape": {"rows": "object[]", "rowCount": "int", "truncated": "bool"},
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "sql is required"}},
            {"status": 400, "body": {"error": "only read-only statements are allowed on /query"}},
            {"status": 400, "body": {"error": "<gate or SQLite message>"}},
            TIMEOUT,
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/globaldb.go:67-95", "go_hive/go-hive/internal/globaldb/client.go:44-49"],
        notes="Read-only pool + read-only statement gate.",
    ),
    endpoint(
        "hb-globaldb-upsert", "hive-backend", "POST", "/v1/global-db/upsert", "bearer-jwt", "implemented", "M1",
        request={"fields": {"table": "string", "row": "object", "source_colony": "string?", "mode": "string? (insert|update)"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-upsert.json",
            "shape": {"table": "string", "inserted": "int", "updated": "int", "pk": "object"},
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "table is required"}},
            {"status": 400, "body": {"error": "row must be a non-empty object"}},
            {"status": 409, "body": {"error": "conflict", "message": "row already exists", "pk": "object"}},
            {"status": 400, "body": {"error": "<gate or SQLite message>"}},
            TIMEOUT,
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/globaldb.go:98-141", "go_hive/go-hive/internal/globaldb/client.go:52-60"],
        notes="mode=insert with an existing PK → 409 conflict.",
    ),
    endpoint(
        "hb-globaldb-import", "hive-backend", "POST", "/v1/global-db/import", "bearer-jwt", "implemented", "M1",
        request={"fields": {"table": "string", "rows": "object[]", "source_colony": "string?"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-import.json",
            "shape": {"table": "string", "inserted": "int", "total": "int?", "pk": "object"},
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "table is required"}},
            {"status": 400, "body": {"error": "rows must be a non-empty array"}},
            {"status": 400, "body": {"error": "<gate or SQLite message>"}},
            TIMEOUT,
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/globaldb.go:144-182", "go_hive/go-hive/internal/globaldb/client.go:63-68"],
        notes="No client field dependency beyond 2xx; response shape is ours.",
    ),
    endpoint(
        "hb-globaldb-tables", "hive-backend", "GET", "/v1/global-db/tables", "bearer-jwt", "implemented", "M1",
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-tables.json",
            "shape": {"tables": [{"name": "string", "columns": "object[]", "row_count": "int", "primary_key": "string[]"}]},
        },
        errors=[SIGNED_OUT],
        anchors=["internal/httpapi/globaldb.go:185-197", "go_hive/go-hive/internal/globaldb/client.go:171"],
        notes="Internal and inbox tables are excluded from the listing.",
    ),
    endpoint(
        "hb-globaldb-changes", "hive-backend", "GET", "/v1/global-db/changes", "bearer-jwt", "implemented", "M1",
        request={"fields": {"since": "string? (cursor)"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-changes-init.json",
            "shape": {"cursor": "string|null", "covered": "string[]", "truncated": "bool", "tables": "map<string, {count, rows}>"},
        },
        errors=[{"status": 400, "body": {"error": "invalid since cursor: \"<cursor>\""}}, SIGNED_OUT],
        anchors=["internal/httpapi/globaldb.go:200-212", "go_hive/go-hive/internal/globaldb/client.go:179"],
        notes="Cursor is this service's decimal counter; the real cloud cursor format is UNKNOWN.",
    ),
    endpoint(
        "hb-globaldb-rows", "hive-backend", "GET", "/v1/global-db/tables/{table}/rows", "bearer-jwt", "implemented", "M1",
        request={"fields": {"table": "path", "query": "passthrough (?limit&offset&order&filters)"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-rows.json",
            "shape": {"table": "string", "columns": "object[]", "primary_key": "string[]", "rows": "object[]", "total": "int", "limit": "int", "offset": "int"},
        },
        errors=[{"status": 404, "body": {"error": "unknown table: <table>"}}, SIGNED_OUT],
        anchors=["internal/httpapi/globaldb.go:215-227", "go_hive/go-hive/internal/globaldb/client.go:183"],
        notes="Query string is forwarded to the row lister; filter semantics are ours.",
    ),
    endpoint(
        "hb-globaldb-patch-rows", "hive-backend", "PATCH", "/v1/global-db/tables/{table}/rows", "bearer-jwt",
        "implemented", "M1",
        request={"fields": {"table": "path", "pk": "object", "updates": "object"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/globaldb-patch.json",
            "shape": {"updated": "int"},
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "pk and updates must be objects"}},
            {"status": 404, "body": {"error": "row not found"}},
            {"status": 400, "body": {"error": "<gate or SQLite message>"}},
            TIMEOUT,
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/globaldb.go:230-268", "go_hive/go-hive/internal/globaldb/client.go:187-190"],
        notes="PATCH is the only row-write verb in the observed contract.",
    ),
    # ---- inbox (implemented) ---------------------------------------------
    endpoint(
        "hb-inbox-messages", "hive-backend", "POST", "/v1/inbox/messages", "bearer-jwt", "implemented", "M1/M2",
        request={
            "fields": {
                "runtime_id": "string", "session_id": "string", "colony_id": "string",
                "kind": "blocker|heartbeat|progress|done", "title": "string", "body": "string",
                "correlation_token": "string", "deep_link": "string|null",
            }
        },
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/inbox-message.json",
            "shape": {"id": "string"},
            "tests": [
                "internal/httpapi/inbox_contract_test.go:TestInboxMessageStoredContract",
                "internal/httpapi/inbox_contract_test.go:TestInboxObservedAuthSemantics",
                "harness/inbox-smoke",
            ],
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "invalid kind (want blocker|heartbeat|progress|done)"}},
            {"status": 400, "body": {"error": "session_id is required"}},
            {
                "status": 429,
                "body": {"error": "inbox rate limit exceeded"},
                "note": "private-deploy extension only; default disabled (--inbox-rate-limit)",
            },
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/inbox.go:78-145", "go_hive/go-hive/internal/sentinel/notifier.go:287-331"],
        notes=(
            "kind defaults to blocker, matching notifier.go. M1 delivered the route/wire shape; M2 adds the "
            "real-sentinel L2b closed loop, stored-field contract tests, audit proof and an opt-in per-team limiter."
        ),
    ),
    endpoint(
        "hb-inbox-resolve", "hive-backend", "POST", "/v1/inbox/resolve", "bearer-jwt", "implemented", "M1/M2",
        request={"fields": {"session_id": "string", "resolved_by": "string"}},
        response={
            "status": 200,
            "golden": "internal/httpapi/testdata/inbox-resolve.json",
            "shape": {"resolved": "int"},
            "tests": [
                "internal/httpapi/inbox_contract_test.go:TestInboxResolveContract",
                "harness/inbox-smoke",
            ],
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "session_id is required"}},
            {
                "status": 429,
                "body": {"error": "inbox rate limit exceeded"},
                "note": "private-deploy extension only; default disabled (--inbox-rate-limit)",
            },
            SIGNED_OUT,
        ],
        anchors=["internal/httpapi/inbox.go:148-165", "go_hive/go-hive/internal/sentinel/notifier.go:341-360"],
        notes=(
            "Client only checks <300; the resolved counter is our additive field. M2 verifies the close-once "
            "transition and the real notifier ResolveHiveSession call in harness/inbox-smoke."
        ),
    ),
    endpoint(
        "hb-inbox-runtime-stream", "hive-backend", "GET", "/v1/inbox/runtime/stream", "bearer-jwt",
        "implemented", "M1/M2",
        request={"fields": {"runtime_id": "string (query, required)", "kind": "string (query, connection bookkeeping only)"}},
        response={
            "status": 200,
            "media_type": "text/event-stream",
            "golden": "internal/httpapi/testdata/sse-reply-frame.txt",
            "shape": {"correlation_token": "string", "text": "string", "colony_id": "string?", "message_id": "string?"},
            "tests": [
                "internal/httpapi/inbox_contract_test.go:TestInboxStreamWireContract",
                "internal/httpapi/inbox_contract_test.go:TestInboxStreamFanoutAndIsolation",
                "internal/httpapi/inbox_contract_test.go:TestInboxStreamQueuedReplay",
                "harness/inbox-smoke",
            ],
        },
        errors=[
            {"status": 400, "body": {"error": "runtime_id is required"}},
            SIGNED_OUT,
        ],
        sse_events=["reply"],
        anchors=[
            "internal/httpapi/inbox.go:167-252 handleInboxRuntimeStream",
            "internal/httpapi/inbox.go:254-263 writeSSEEvent",
            "go_hive/go-hive/internal/sentinel/listeners.go:436-560",
        ],
        notes=(
            "SSE. First frame is ': connected', keepalive comment every 15s. Pending replies are replayed on "
            "connect; 'kind' does not filter replies (real cloud filter semantics UNKNOWN). M2 pins headers, "
            "comment frames, fan-out, isolation and replay-once behaviour plus the real listener closed loop."
        ),
    ),
    # ---- CRM grid (M4 subset) -------------------------------------------
    # The ten routes are recovered from the desktop proxies; the backend CRM
    # service is closed. Only the generic row/grid operations whose request and
    # response shapes are anchored in the open clients are implemented.
    # Schema, claim and release remain planned (docs/m4-notes.md).
    endpoint(
        "hb-crm-grid-schema", "hive-backend", "GET", "/v1/crm/grid/schema", "bearer-jwt", "planned", "M4",
        request={"fields": {}},
        response={"status": None, "shape": None, "note": "response shape UNKNOWN"},
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:752",
            "core/framework/server/routes_colony_workers.py:1720-1721",
            "contracts/observed-contract.json",
        ],
        notes=(
            "Projected/display-ready CRM entity schema. The desktop proxy is a pure pass-through and the "
            "backend is closed; no schema response was ever captured (the api-contract probe only reached "
            "the signed-out 401). Stays UNKNOWN and unregistered rather than inventing entity/column/stage keys."
        ),
    ),
    endpoint(
        "hb-crm-grid-query", "hive-backend", "POST", "/v1/crm/grid/{entity}/query", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {
            "entity": "path: people|companies|opportunities",
            "filter": "[{column, op, value}]? (eq|ne|gt|gte|lt|lte|contains|starts_with|ends_with|is_empty|is_not_empty)",
            "search": "string?",
            "order_by": "string?",
            "order_dir": "asc|desc?",
            "limit": "int? (1..500, default 100)",
            "offset": "int? (>=0)",
        }},
        response={
            "status": 200,
            "shape": {
                "table": "string", "columns": "[column]", "primary_key": "[string]",
                "rows": "[row]", "total": "int", "limit": "int", "offset": "int",
            },
            "tests": [
                "internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow",
                "internal/store/gridquery_test.go",
            ],
            "note": "generic grid row-page shape (contracts.md §http.global-data); CRM projection/custom-field flattening UNKNOWN",
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "filter must be a list"}},
            {"status": 400, "body": {"error": "each filter must be an object"}},
            {"status": 400, "body": {"error": "invalid identifier: '...'" }},
            {"status": 400, "body": {"error": "unknown column: ..."}},
            {"status": 400, "body": {"error": "unsupported filter op: '...'"}},
            {"status": 400, "body": {"error": "invalid limit/offset"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:755",
            "core/framework/server/routes_colony_workers.py:1724-1727",
            "core/framework/server/routes_colony_workers.py:1430-1490 (grid_query filter/search body)",
            "go_hive/docs/architecture/contracts.md:175-176",
            "internal/store/gridquery.go",
        ],
        notes=(
            "Filter/search/sort/pagination body mirrors grid_query.py and is validated against the entity's "
            "real columns. SQL is built with bound parameters; identifiers are allow-listed. Entity registry "
            "is the three CLI nouns (hive-crm people/companies/opportunities); the desktop view slugs "
            "(People/Orgs/Deals/Activity) are not recovered."
        ),
    ),
    endpoint(
        "hb-crm-grid-group-counts", "hive-backend", "POST", "/v1/crm/grid/{entity}/group-counts",
        "bearer-jwt", "implemented", "M4",
        request={"fields": {
            "entity": "path",
            "group_by": "string (required)",
            "filter": "[{column, op, value}]?",
            "search": "string?",
        }},
        response={
            "status": 200,
            "shape": {"table": "string", "group_by": "string", "groups": "[{value, count}]"},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
            "note": "matches the generic grid group-counts shape; capped at 200 groups",
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "group_by is required"}},
            {"status": 400, "body": {"error": "filter must be a list"}},
            {"status": 400, "body": {"error": "unknown column: ..."}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:759",
            "core/framework/server/routes_colony_workers.py:1730-1733",
            "core/framework/server/routes_colony_workers.py:1508-1560",
            "go_hive/go-hive/frontend/src/api/globalData.ts:128-140",
        ],
        notes="Same filter/search semantics as the query endpoint; one row per distinct group_by value, highest count first.",
    ),
    endpoint(
        "hb-crm-grid-get", "hive-backend", "GET", "/v1/crm/grid/{entity}/{id}", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {"entity": "path", "id": "path"}},
        response={
            "status": 200,
            "shape": {"table": "string", "columns": "[column]", "primary_key": "[string]", "rows": "[row; exactly 1]", "total": 1, "limit": 1, "offset": 0},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
            "note": "one-row generic page; the closed backend's single-record envelope is UNKNOWN",
        },
        errors=[
            {"status": 404, "body": {"error": "row not found"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:763",
            "core/framework/server/routes_colony_workers.py:1736-1740",
            "go_hive/docs/architecture/contracts.md:175",
        ],
        notes="Archived (soft-deleted) records read as 404.",
    ),
    endpoint(
        "hb-crm-grid-create", "hive-backend", "POST", "/v1/crm/grid/{entity}", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {
            "entity": "path",
            "fields": "object (required; non-empty)",
            "identities": "object|array? — only empty values accepted; shape/dedupe semantics UNKNOWN",
            "because": "string? (audit reason)",
        }},
        response={
            "status": 200,
            "shape": {"table": "string", "columns": "[column]", "primary_key": "[string]", "rows": "[row; the created record]", "total": 1, "limit": 1, "offset": 0},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
            "note": "id/timestamps are server-assigned; response envelope from the generic row contract",
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "fields must be a non-empty object"}},
            {"status": 400, "body": {"error": "identities is not supported by this private deployment (shape UNKNOWN)"}},
            {"status": 400, "body": {"error": "because must be a string"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:768",
            "core/framework/server/routes_colony_workers.py:1744-1746",
            "go_hive/docs/architecture/contracts.md:175-176",
        ],
        notes=(
            "Write body keys are recovered ({fields, identities?, because?}); the identity/dedupe shape and "
            "the backend's projected response are UNKNOWN, so non-empty identities is rejected rather than guessed."
        ),
    ),
    endpoint(
        "hb-crm-grid-patch", "hive-backend", "PATCH", "/v1/crm/grid/{entity}/{id}", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {
            "entity": "path", "id": "path",
            "fields": "object? (must name existing columns)",
            "identities": "object|array? — only empty values accepted; shape UNKNOWN",
            "because": "string? (audit reason)",
        }},
        response={
            "status": 200,
            "shape": {"table": "string", "columns": "[column]", "primary_key": "[string]", "rows": "[row; the updated record]", "total": 1, "limit": 1, "offset": 0},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "unknown column: ..."}},
            {"status": 400, "body": {"error": "identities is not supported by this private deployment (shape UNKNOWN)"}},
            {"status": 404, "body": {"error": "row not found"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:772",
            "core/framework/server/routes_colony_workers.py:1750-1753",
            "go_hive/docs/architecture/contracts.md:175-176",
        ],
        notes="fields is optional in the recovered body; an empty patch returns the current record and records the audit reason.",
    ),
    endpoint(
        "hb-crm-grid-delete", "hive-backend", "DELETE", "/v1/crm/grid/{entity}/{id}", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {"entity": "path", "id": "path", "because": "string? (optional body)"}},
        response={
            "status": 200,
            "shape": {"deleted": "int"},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
            "note": "soft delete (archive); archived rows disappear from query/get",
        },
        errors=[
            {"status": 400, "body": {"error": "because must be a string"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:777",
            "core/framework/server/routes_colony_workers.py:1757-1760",
            "go_hive/docs/architecture/contracts.md:175-176",
        ],
        notes=(
            "The proxy tolerates an absent/unparseable DELETE body and forwards null; this handler mirrors that. "
            "The archive representation is internal; the real cloud's archived-row visibility rules are UNKNOWN."
        ),
    ),
    endpoint(
        "hb-crm-grid-stage", "hive-backend", "POST", "/v1/crm/grid/{entity}/{id}/stage", "bearer-jwt",
        "implemented", "M4",
        request={"fields": {
            "entity": "path", "id": "path",
            "value": "string (required, non-empty)",
            "because": "string?",
        }},
        response={
            "status": 200,
            "shape": {"table": "string", "columns": "[column]", "primary_key": "[string]", "rows": "[row; the updated record]", "total": 1, "limit": 1, "offset": 0},
            "tests": ["internal/httpapi/m4_contract_test.go:TestCRMGridContractWorkflow"],
            "note": "stage values are team-defined; no transition graph is enforced",
        },
        errors=[
            INVALID_JSON,
            {"status": 400, "body": {"error": "value is required"}},
            {"status": 404, "body": {"error": "row not found"}},
            {"status": 404, "body": {"error": "unknown entity: ..."}},
            SIGNED_OUT,
        ],
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:782",
            "core/framework/server/routes_colony_workers.py:1764-1771",
            "go_hive/go-hive/frontend/src/components/data-grid/gridUtils.stageLabel.test.ts:1-38",
        ],
        notes=(
            "The write body is recovered. The backend's entity/stage validation and 'stage_changed' interaction "
            "are UNKNOWN; this deployment accepts any non-empty team-defined value and does not enforce transitions."
        ),
    ),
    endpoint(
        "hb-crm-claim", "hive-backend", "POST", "/v1/crm/claim", "bearer-jwt", "planned", "M4",
        request={"fields": {"ids": "[string]", "because": "string?"}},
        response={"status": None, "shape": None, "note": "response keys/claim lease semantics UNKNOWN"},
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:787",
            "core/framework/server/routes_colony_workers.py:1775-1776",
            "go_hive/go-hive/internal/skills/_default_skills/worker-delegation/SKILL.md:76",
        ],
        notes=(
            "Request body recovered ({ids, because?}) and the CLI contract says claim atomically wins only "
            "unclaimed people and returns the rest under `skipped`. The response's winner key, the lease owner "
            "identity and the lease lifetime are UNKNOWN, so the endpoint stays unregistered."
        ),
    ),
    endpoint(
        "hb-crm-release", "hive-backend", "POST", "/v1/crm/release", "bearer-jwt", "planned", "M4",
        request={"fields": {"ids": "[string]", "because": "string?"}},
        response={"status": None, "shape": None, "note": "response keys/release semantics UNKNOWN"},
        anchors=[
            "go_hive/go-hive/internal/server/routes_colony_workers_global.go:790",
            "core/framework/server/routes_colony_workers.py:1780-1781",
            "go_hive/go-hive/internal/skills/_default_skills/worker-delegation/SKILL.md:77",
        ],
        notes=(
            "Request body recovered ({ids, because?}); release frees a claim so another colony can win it. "
            "The response shape and the ownership model (same-user colonies, lease expiry) are UNKNOWN."
        ),
    ),
    # ---- unknown backend surfaces ----------------------------------------
    endpoint(
        "hb-inbox-stream", "hive-backend", "GET", "/v1/inbox/stream", "bearer-jwt", "unknown", "M2",
        request={"fields": {}},
        response={"status": None, "shape": None},
        anchors=["go_hive/docs/hive-inbox.html §02"],
        notes=(
            "Desktop SSE endpoint named in docs/hive-inbox.html; event names and payloads are not recoverable "
            "from the OSS repo. Registered as a placeholder only — do not implement from guesswork."
        ),
    ),
    # Inbox desktop UI endpoints. docs/internal/hive-inbox-design.md §8 names the
    # paths, but the UI is closed-source, observed-contract.json has no entry and
    # no request/response shape is recoverable. They stay unknown and
    # unregistered; the contract test asserts 404 (or 405 on the one path that
    # collides with an implemented method).
    endpoint(
        "hb-inbox-list-messages", "hive-backend", "GET", "/v1/inbox/messages", "bearer-jwt", "unknown", "M2",
        request={"fields": {"unread": "bool? (documented filter)"}},
        response={"status": None, "shape": None},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02"],
        notes=(
            "Desktop/web list (own + team-visible, unread filter). Response shape, pagination and unread "
            "accounting UNKNOWN; not implemented. GET on this path answers 405 because POST is registered."
        ),
    ),
    endpoint(
        "hb-inbox-message-reply", "hive-backend", "POST", "/v1/inbox/messages/{id}/reply", "bearer-jwt",
        "unknown", "M2",
        request={"fields": {"id": "path", "text": "string? (shape unknown)"}},
        response={"status": None, "shape": None},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02"],
        notes=(
            "User answer from the Inbox UI (closed source). Request/response shape and reply delivery "
            "semantics UNKNOWN; this module uses the self-hosted POST /internal/inbox/reply extension instead."
        ),
    ),
    endpoint(
        "hb-inbox-message-read", "hive-backend", "POST", "/v1/inbox/messages/{id}/read", "bearer-jwt",
        "unknown", "M2",
        request={"fields": {"id": "path"}},
        response={"status": None, "shape": None},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02"],
        notes="Per-user read marker (inbox_message_reads). Response shape UNKNOWN; not implemented.",
    ),
    endpoint(
        "hb-inbox-mark-all-read", "hive-backend", "POST", "/v1/inbox/messages/mark-all-read", "bearer-jwt",
        "unknown", "M2",
        request={"fields": {}},
        response={"status": None, "shape": None},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02"],
        notes="Mark-all-read. Response shape UNKNOWN; not implemented.",
    ),
    endpoint(
        "hb-inbox-unread-count", "hive-backend", "GET", "/v1/inbox/messages/unread-count", "bearer-jwt",
        "unknown", "M2",
        request={"fields": {}},
        response={"status": None, "shape": {"unread": "int? (guess, not observed)"}},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02"],
        notes="Badge count endpoint. Response key UNKNOWN (the int is not observed); not implemented.",
    ),
    endpoint(
        "hb-inbox-runtime-heartbeat", "hive-backend", "POST", "/v1/inbox/runtime/heartbeat", "bearer-jwt",
        "unknown", "M2",
        request={"fields": {"runtime_id": "string? (shape unknown)"}},
        response={"status": None, "shape": None},
        anchors=["docs/internal/hive-inbox-design.md §8", "go_hive/docs/hive-inbox.html §02, §07"],
        notes=(
            "Presence keepalive: the page says the endpoint exists upstream but the runtime never calls it. "
            "Request/response shape UNKNOWN; not implemented."
        ),
    ),
    endpoint(
        "hb-refresh-stream-token-legacy", "hive-backend", "POST", "/user/refresh-stream-token", "bearer-jwt",
        "unknown", "M3",
        request={"fields": {}},
        response={"status": None, "shape": None},
        anchors=["core/framework/server/streamtoken_refresh.py header"],
        notes="Desktop-only legacy path named in the Python refresh module; request/response UNKNOWN.",
    ),
    # ---- senders (inferred / unknown, M4.5) ------------------------------
    endpoint(
        "hb-senders-runtime", "hive-backend", "GET", "/v1/senders/runtime", "api-key", "inferred", "M4.5",
        request={"fields": {}},
        response={
            "status": None,
            "shape": {
                "senders": [
                    {
                        "id": "string", "name": "string", "provider": "google|mailjet|sendgrid|hubspot",
                        "auth_type": "oauth|api_key", "from_email": "string", "from_name": "string?",
                        "integration_id": "string?", "secret": "object?", "weight": "int",
                        "daily_limit": "int?", "enabled": "bool", "tags": "string[]", "can_receive": "bool",
                    }
                ]
            },
            "note": "array shape inferred from SenderConfig.from_dict, not observed",
        },
        anchors=["tools/src/aden_tools/senders/registry.py:3", "tools/src/aden_tools/senders/registry.py:135"],
        notes=(
            "Path documented in the registry docstring; client method list_team_senders(). Field names inferred "
            "from SenderConfig.from_dict (registry.py:53-68). Team is derived from ADEN_API_KEY."
        ),
        client_method="list_team_senders",
    ),
    endpoint(
        "hb-senders-reserve-send", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"sender_id": "string", "to_email": "string", "subject": "string?", "colony_id": "string?",
                            "conversation_token": "string?", "message_id": "string?", "campaign_id": "string?",
                            "step": "int?", "idempotency_key": "string"}},
        response={"status": None, "shape": None},
        anchors=["tools/src/aden_tools/senders/sendlog.py:96-114"],
        notes="Client method reserve_send(payload) — HTTP method/path UNKNOWN; payload keys from sendlog.py.",
        client_method="reserve_send",
    ),
    endpoint(
        "hb-senders-complete-send", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"send_id": "path/body? (shape unknown)", "status": "string",
                            "provider_message_id": "string?", "error": "string?"}},
        response={"status": None, "shape": None},
        anchors=["tools/src/aden_tools/senders/sendlog.py:130-148"],
        notes="Client method complete_send(send_id, outcome) — HTTP method/path UNKNOWN.",
        client_method="complete_send",
    ),
    endpoint(
        "hb-senders-usage-today", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {}},
        response={"status": None, "shape": {"<sender_id>": {"sent_today": "int"}}, "note": "inferred from caller"},
        anchors=["tools/src/aden_tools/senders/sendlog.py:153-168"],
        notes="Client method sender_usage_today() — HTTP method/path UNKNOWN.",
        client_method="sender_usage_today",
    ),
    endpoint(
        "hb-senders-suppress-recipient", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"email": "string", "reason": "string", "note": "string"}},
        response={"status": None, "shape": None},
        anchors=["tools/src/aden_tools/senders/sendlog.py:171-179"],
        notes="Client method suppress_recipient(email, reason, note) — HTTP method/path UNKNOWN.",
        client_method="suppress_recipient",
    ),
    endpoint(
        "hb-senders-list-suppressions", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {}},
        response={"status": None, "shape": None},
        anchors=["tools/src/aden_tools/senders/sendlog.py:182-191"],
        notes="Client method list_suppressions() — HTTP method/path UNKNOWN.",
        client_method="list_suppressions",
    ),
    endpoint(
        "hb-senders-list-sends", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"sender_id": "string?", "to_email": "string?", "colony_id": "string?", "limit": "int?"}},
        response={"status": None, "shape": None},
        anchors=["tools/src/aden_tools/senders/sendlog.py:194-203"],
        notes="Client method list_sends(**filters) — HTTP method/path UNKNOWN.",
        client_method="list_sends",
    ),
    endpoint(
        "hb-senders-create-team-sender", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"provider": "string", "from_email": "string", "name": "string?", "from_name": "string?",
                            "api_key": "string?", "secret_key": "string?", "integration_id": "string?",
                            "smtp_host": "string?", "smtp_port": "int?", "username": "string?", "password": "string?",
                            "weight": "int?", "daily_limit": "int?", "tags": "string[]?"}},
        response={"status": None, "shape": {"id": "string", "name": "string", "validation": {"valid": "bool", "error": "string?"}}},
        anchors=["tools/src/aden_tools/senders/setup.py:347"],
        notes="Client method create_team_sender(payload) — HTTP method/path UNKNOWN.",
        client_method="create_team_sender",
    ),
    endpoint(
        "hb-senders-provider-verified", "hive-backend", "", "", "api-key", "unknown", "M4.5",
        request={"fields": {"provider": "string", "secret": "object"}},
        response={"status": None, "shape": [{"email": "string"}]},
        anchors=["tools/src/aden_tools/senders/setup.py:166"],
        notes="Client method list_provider_verified_senders(provider, secret) — HTTP method/path UNKNOWN.",
        client_method="list_provider_verified_senders",
    ),
    # ---- llm-proxy (M5 pass-through) --------------------------------------
    endpoint(
        "llm-messages", "llm-proxy", "POST", "/v1/messages", "bearer-stream-token", "implemented", "M5",
        request={"fields": {"anthropic_messages_body": "object", "stream": "bool?"}},
        response={
            "status": 200,
            "shape": {"anthropic_messages_response": "object", "sse": "upstream-defined when Content-Type is text/event-stream"},
            "note": (
                "Upstream status/body is forwarded verbatim (JSON or SSE); the proxy does not rewrite "
                "success responses or enumerate event names."
            ),
            "tests": [
                "internal/httpapi/m5_contract_test.go:TestLLMMessagesNonStreamPassThrough",
                "internal/httpapi/m5_contract_test.go:TestLLMMessagesSSEPassThrough",
                "internal/httpapi/m5_contract_test.go:TestLLMMessagesUpstreamErrorsForwarded",
                "internal/httpapi/m5_contract_test.go:TestLLMMessagesClientCancellation",
                "harness/llm-proxy-smoke",
            ],
        },
        errors=[
            {
                "status": 401,
                "body": {"error": {"type": "hive_stream_token_invalid"}},
                "golden": "internal/httpapi/testdata/llm-token-invalid.json",
            },
            {
                "status": 400,
                "body": {"type": "error", "error": {"type": "llm_upstream_not_configured", "message": "string"}},
                "note": "Valid credential, no --llm-upstream-url. 400 keeps the go_hive keycheck (200/400/429) valid without a fake completion.",
            },
            {
                "status": 502,
                "body": {"type": "error", "error": {"type": "llm_upstream_unavailable", "message": "string"}},
                "note": "Transport/dial failure after credential validation.",
            },
            {
                "status": 504,
                "body": {"type": "error", "error": {"type": "llm_upstream_timeout", "message": "string"}},
                "note": "--llm-request-timeout elapsed.",
            },
        ],
        anchors=[
            "go_hive/go-hive/internal/llm/keycheck.go:564-569",
            "core/framework/llm/litellm.py:1345-1380 (_is_hive_stream_token_invalid)",
            "internal/httpapi/llm.go:handleLLMMessages",
            "internal/httpapi/llm_relay.go:forwardMessagesHeaders",
            "contracts/observed-contract.json",
        ],
        notes=(
            "M5 proxies Anthropic Messages to an explicitly configured upstream (--llm-upstream-url) with "
            "an explicitly configured credential (--llm-upstream-key) and auth header style (x-api-key or bearer). "
            "The M3 Bearer boundary is preserved: invalid/expired/wrong-signature → 401 "
            "{error:{type:hive_stream_token_invalid}}. With a valid credential the body is streamed verbatim "
            "(unknown fields preserved); anthropic-version/anthropic-beta are forwarded; the client's Authorization "
            "and x-api-key are stripped and replaced with the upstream credential. Upstream 4xx/5xx (including 429) "
            "are relayed verbatim. text/event-stream responses are relayed line-by-line with a flush per line, so "
            "every upstream event reaches the client immediately. No upstream configured → 400 "
            "llm_upstream_not_configured (never 200). Model routing, billing and rate limits remain UNKNOWN and are "
            "not implemented; the model field is passed through unchanged."
        ),
    ),
    endpoint(
        "llm-images-generations", "llm-proxy", "POST", "/v1/images/generations", "bearer-stream-token", "implemented", "M5",
        request={
            "fields": {
                "prompt": "string",
                "model": "string?",
                "size": "string?",
                "quality": "string?",
                "n": "int?",
                "output_format": "string?",
                "image": "string[]? (base64 reference images; sent as JSON, never multipart)",
            }
        },
        response={
            "status": 200,
            "shape": {"data": [{"b64_json": "string"}], "usage": "object?"},
            "note": "Upstream status/body is forwarded verbatim; the backend model/format is UNKNOWN.",
            "tests": [
                "internal/httpapi/m5_contract_test.go:TestLLMImagesPassThrough",
                "internal/httpapi/m5_contract_test.go:TestLLMImagesUpstreamErrorsForwarded",
                "harness/llm-proxy-smoke",
            ],
        },
        errors=[
            {
                "status": 401,
                "body": {"error": {"type": "hive_stream_token_invalid"}},
                "golden": "internal/httpapi/testdata/llm-token-invalid.json",
            },
            {
                "status": 503,
                "body": {"type": "error", "error": {"type": "llm_upstream_not_configured", "message": "string"}},
                "note": "Valid credential, no --images-upstream-url.",
            },
            {
                "status": 502,
                "body": {"type": "error", "error": {"type": "llm_upstream_unavailable", "message": "string"}},
            },
            {
                "status": 504,
                "body": {"type": "error", "error": {"type": "llm_upstream_timeout", "message": "string"}},
            },
        ],
        anchors=[
            "go_hive/go-hive/internal/tools/integrations/image.go:36-37,280-300,330-380,420-470",
            "tools/src/aden_tools/tools/image_gen_tool/image_gen_tool.py:1-30,300-420",
            "internal/httpapi/llm.go:handleLLMImages",
            "contracts/observed-contract.json",
        ],
        notes=(
            "Client contract: Authorization: Bearer <HIVE_API_KEY> (the same per-user proxy credential as "
            "/v1/messages), JSON body with prompt/model/size/quality/n/output_format and optional base64 `image` "
            "reference array, response {data:[{b64_json}], usage}. M5 forwards the JSON body verbatim to an "
            "explicitly configured OpenAI-compatible images upstream (--images-upstream-url) with "
            "Authorization: Bearer <--images-upstream-key>, because the proxy is documented as JSON-only. "
            "No images upstream → 503 llm_upstream_not_configured. Image model routing/backend remains UNKNOWN."
        ),
    ),
    # ---- aden credentials (planned, M6) ----------------------------------
    endpoint(
        "aden-list-credentials", "aden-credentials", "GET", "/v1/credentials", "api-key", "planned", "M6",
        request={"fields": {}},
        response={"status": None, "shape": None},
        anchors=["core/framework/credentials/aden/client.py:345-360", "contracts/observed-contract.json"],
        notes="Integration listing.",
    ),
    endpoint(
        "aden-get-credential", "aden-credentials", "GET", "/v1/credentials/{integration_id}", "api-key",
        "planned", "M6",
        request={"fields": {"integration_id": "path"}},
        response={"status": None, "shape": None},
        anchors=["core/framework/credentials/aden/client.py:362-380", "contracts/observed-contract.json"],
        notes="Resolve one integration credential.",
    ),
    endpoint(
        "aden-refresh-credential", "aden-credentials", "POST", "/v1/credentials/{integration_id}/refresh",
        "api-key", "planned", "M6",
        request={"fields": {"integration_id": "path"}},
        response={"status": None, "shape": None},
        anchors=["core/framework/credentials/aden/client.py:381-396", "contracts/observed-contract.json"],
        notes="Force a provider token refresh.",
    ),
    endpoint(
        "aden-validate-credential", "aden-credentials", "GET", "/v1/credentials/{integration_id}/validate",
        "api-key", "planned", "M6",
        request={"fields": {"integration_id": "path"}},
        response={"status": None, "shape": None},
        anchors=["core/framework/credentials/aden/client.py:397-408", "contracts/observed-contract.json"],
        notes="Live-validate an integration credential.",
    ),
]

STREAMS: list[dict[str, Any]] = [
    {
        "id": "inbox-runtime-reply",
        "service": "hive-backend",
        "protocol": "sse",
        "method": "GET",
        "path": "/v1/inbox/runtime/stream",
        "auth": "bearer-jwt",
        "status": "implemented",
        "milestone": "M1",
        "event": "reply",
        "payload_fields": {
            "correlation_token": "string",
            "text": "string",
            "colony_id": "string?",
            "message_id": "string?",
        },
        "golden": "internal/httpapi/testdata/sse-reply-frame.txt",
        "source_anchors": [
            'internal/httpapi/inbox.go:213,232 writeSSEEvent(..., "reply", ...)',
            "go_hive/go-hive/internal/sentinel/listeners.go:436-560",
        ],
        "notes": "Comment frames: ': connected' on connect, ': keepalive' every 15s. No total timeout.",
    }
]

WEBSOCKETS: list[dict[str, Any]] = []
WEBSOCKET_POLICY = {
    "supported": False,
    "note": (
        "No WebSocket transport exists in go-hive-cloud today. Any Upgrade/websocket code must append an "
        "entry here (path, subprotocol, message schema) or scripts/gen_cloud_surface.py --check fails."
    ),
}

INVARIANTS = [
    {
        "id": "INV-01",
        "rule": "Registered routes == endpoints with status=implemented (exact method+path set).",
        "enforced_by": ["scripts/gen_cloud_surface.py --check", "internal/httpapi/contract_test.go"],
    },
    {
        "id": "INV-02",
        "rule": "Every endpoint in contracts/observed-contract.json is classified in this surface.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
    {
        "id": "INV-03",
        "rule": "Every SSE event name written by the code appears in streams[].event, and vice versa.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
    {
        "id": "INV-04",
        "rule": "Any WebSocket/Upgrade code requires a websockets[] entry; the list must not stay empty.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
    {
        "id": "INV-05",
        "rule": "Routes marked planned/inferred/unknown must not be registered (404 in contract_test).",
        "enforced_by": ["internal/httpapi/contract_test.go"],
    },
    {
        "id": "INV-06",
        "rule": "status=unknown entries either carry an anchor-backed method+path or leave both empty; a path is never guessed.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
    {
        "id": "INV-07",
        "rule": "Every non-implemented endpoint carries at least one source anchor.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
    {
        "id": "INV-08",
        "rule": "/health and /internal/inbox/reply are go-hive-cloud extensions, never official contract.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check", "internal/httpapi/contract_test.go"],
    },
    {
        "id": "INV-09",
        "rule": "Every implemented endpoint's response carries a golden or test reference.",
        "enforced_by": ["scripts/gen_cloud_surface.py --check"],
    },
]

UNKNOWNS = [
    "Real cloud 401 body: observed-contract says {error:signed_out}; the client copy is the verbatim sentence (implemented).",
    "JWT issuer, signing algorithm, claims and login/device flow.",
    "GET /v1/inbox/stream (desktop SSE): event names and payloads.",
    (
        "Inbox desktop/web UI endpoints (GET /v1/inbox/messages, POST /v1/inbox/messages/{id}/reply, "
        "POST .../{id}/read, POST .../mark-all-read, GET .../unread-count): paths named in "
        "docs/internal/hive-inbox-design.md §8 and go_hive/docs/hive-inbox.html §02, but request/response "
        "shapes are not in observed-contract.json — registered unknown, deliberately not implemented."
    ),
    "POST /v1/inbox/runtime/heartbeat: documented upstream but the runtime never calls it; request/response UNKNOWN.",
    "Inbox runtime stream `kind` filter semantics: upstream filtering is UNKNOWN; this module records kind as connection bookkeeping only.",
    "Inbox read-state/unread-count model (inbox_message_reads) and list pagination: UNKNOWN; no endpoint implemented.",
    "POST /user/refresh-stream-token (desktop legacy): request/response.",
    (
        "streamToken revocation: no sid blacklist or revocation ledger is observable. This module has none; "
        "an unexpired token stays valid after rotation, and the only revocation lever is removing a signing "
        "secret (master-secret rotation, dual-key window)."
    ),
    (
        "streamToken/direct JWT claims beyond user_id, team_id, typ, iss, iat, exp: the real cloud signer, "
        "issuer and login/device flow are UNKNOWN."
    ),
    (
        "LLM proxy internal semantics: model routing, billing, rate limits, provider-key selection and the "
        "exact upstream 401 message are UNKNOWN. M5 forwards the request body/model and the upstream status/body "
        "verbatim to an explicitly configured upstream; it has no routing table and does not mint provider keys."
    ),
    (
        "Anthropic SSE event subset of the real cloud: UNKNOWN. M5 relays whatever `text/event-stream` bytes the "
        "configured upstream produces (line-by-line, flushed per line) instead of enumerating event names."
    ),
    (
        "Image generation backend/model/format: UNKNOWN. M5 forwards the observed JSON client contract "
        "(prompt/model/size/quality/n/output_format/image[]) to an explicitly configured OpenAI-compatible "
        "images endpoint; it does not implement OpenAI multipart edits itself."
    ),
    "LLM proxy refresh TTL/rotation policy on the real cloud: 24h is the client-observed TTL; server internals UNKNOWN.",
    "CRM entity schema, stage rules and claim/release semantics.",
    "Senders send-log/setup HTTP paths: 8 client methods, no server code in the OSS repo.",
    "Aden credential refresh/validate payload details.",
    "Gateway availability host attribution: hive-backend vs api.adenhq.com.",
    "cloud_sync reconcile protocol (not in go-hive-cloud scope).",
]


# ---------------------------------------------------------------------------
# Source readers
# ---------------------------------------------------------------------------

def parse_routes() -> list[dict[str, Any]]:
    text = SERVER_GO.read_text(encoding="utf-8")
    start = text.find("func (s *Server) register() {")
    if start < 0:
        raise SurfaceError(f"cannot find register() in {SERVER_GO}")
    end = text.find("\n}", start)
    body = text[start:end if end >= 0 else len(text)]
    routes = []
    for match in ROUTE_RE.finditer(body):
        method = HTTP_METHODS.get(match.group("method"))
        if method is None:
            raise SurfaceError(f"unmapped method token {match.group('method')}")
        routes.append(
            {
                "method": method,
                "path": match.group("path"),
                "public": match.group("public") == "true",
            }
        )
    if not routes:
        raise SurfaceError("no s.handle(...) routes parsed from register()")
    if "mux.Handle" in body:
        raise SurfaceError(
            "register() bypasses s.handle(...) with a direct mux.Handle call; route surface cannot be parsed"
        )
    return routes


def parse_observed() -> list[dict[str, Any]]:
    doc = json.loads(OBSERVED.read_text(encoding="utf-8"))
    out = []
    for service, block in doc.get("services", {}).items():
        for ep in block.get("endpoints", []):
            out.append(
                {
                    "service": service,
                    "method": ep.get("method", ""),
                    "path": ep.get("path", ""),
                    "status": ep.get("status", ""),
                }
            )
    return out


def scan_sse_events() -> set[str]:
    """Event names passed literally to writeSSEEvent(), plus 'event: <literal>' frames."""
    events: set[str] = set()
    call_re = re.compile(r'writeSSEEvent\(\s*[^,]+,\s*"([^"]+)"')
    frame_re = re.compile(r'"event:\s*([A-Za-z0-9_.-]+)\\n')
    for path in sorted((ROOT / "internal").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        events.update(call_re.findall(text))
        events.update(frame_re.findall(text))
    return events


WS_MARKERS = (
    "Sec-WebSocket",
    "websocket.Upgrade",
    'Header.Get("Upgrade")',
    'Header.Get("upgrade")',
    "golang.org/x/net/websocket",
    "nhooyr.io/websocket",
    "gorilla/websocket",
)


def scan_websocket_code() -> list[str]:
    hits: list[str] = []
    for path in sorted(ROOT.rglob("*.go")):
        if path.name.endswith("_test.go") or "testdata" in path.parts:
            continue
        text = path.read_text(encoding="utf-8")
        for marker in WS_MARKERS:
            if marker in text:
                hits.append(f"{path.relative_to(ROOT)}: {marker}")
    return hits


# ---------------------------------------------------------------------------
# Surface assembly
# ---------------------------------------------------------------------------

def build_surface() -> tuple[dict[str, Any], list[str]]:
    problems: list[str] = []
    routes = parse_routes()
    observed = parse_observed()
    registered = {(r["method"], r["path"]): r for r in routes}

    curated_by_key: dict[tuple[str, str], dict[str, Any]] = {}
    for record in CURATED:
        key = (record["method"], record["path"])
        if record["status"] == "implemented":
            if key in curated_by_key:
                problems.append(f"duplicate curated implemented entry for {record['method']} {record['path']}")
            curated_by_key[key] = record

    # Routes without curated metadata are an invariant break: no silent surface.
    for key in registered:
        if key not in curated_by_key:
            problems.append(f"registered route {key[0]} {key[1]} has no curated surface entry")

    # Curated implemented entries must actually be registered.
    for key, record in curated_by_key.items():
        if key not in registered:
            problems.append(f"surface marks {key[0]} {key[1]} implemented but server.go does not register it")
        elif registered[key]["public"] != (record["auth"] != "bearer-jwt"):
            problems.append(
                f"public flag mismatch for {key[0]} {key[1]}: route public={registered[key]['public']} "
                f"auth={record['auth']}"
            )

    # A planned/inferred/unknown record may not shadow an implemented route.
    implemented_keys = set(curated_by_key)
    for record in CURATED:
        key = (record["method"], record["path"])
        if record["status"] != "implemented" and key in implemented_keys:
            problems.append(f"{record['id']}: non-implemented entry duplicates implemented route {key[0]} {key[1]}")

    # Observed-contract coverage: every observed endpoint must be classified.
    surface_keys = {(ep["method"], ep["path"]) for ep in CURATED}
    for ep in observed:
        if (ep["method"], ep["path"]) not in surface_keys:
            problems.append(
                f"observed-contract endpoint not classified: {ep['method']} {ep['path']} ({ep['service']})"
            )

    # Unknown entries may name a path only when an anchor backs both halves.
    for record in CURATED:
        if record["status"] == "unknown":
            has_method = bool(record["method"])
            has_path = bool(record["path"])
            if has_method != has_path:
                problems.append(f"{record['id']}: status=unknown must carry both method+path or neither")
            if has_method and not record["source_anchors"]:
                problems.append(f"{record['id']}: unknown endpoint with a path needs a source anchor")
        if record["status"] != "implemented" and not record["source_anchors"]:
            problems.append(f"{record['id']}: non-implemented endpoint has no source anchor")
        if record["status"] == "inferred" and not record["path"]:
            problems.append(f"{record['id']}: status=inferred requires a known path")
        if record["status"] == "implemented":
            response = record["response"]
            if not response.get("golden") and not response.get("tests"):
                problems.append(f"{record['id']}: implemented endpoint lacks a response golden/test reference")

    # SSE: code events must match the registered streams exactly.
    code_events = scan_sse_events()
    surface_events = {stream["event"] for stream in STREAMS}
    if code_events != surface_events:
        problems.append(
            f"SSE event drift: code={sorted(code_events)} surface={sorted(surface_events)}"
        )

    # WebSockets: code markers require an explicit entry.
    ws_hits = scan_websocket_code()
    if ws_hits and not WEBSOCKETS:
        problems.append("websocket/Upgrade code present but websockets[] is empty: " + "; ".join(ws_hits))

    endpoints = []
    for record in CURATED:
        endpoints.append(json.loads(json.dumps(record)))  # deep copy, stable field order
    endpoints.sort(
        key=lambda ep: (
            SERVICE_ORDER.get(ep["service"], 99),
            STATUS_ORDER.get(ep["status"], 99),
            ep["path"],
            ep["method"],
            ep["id"],
        )
    )

    counts = {status: 0 for status in STATUS_ORDER}
    for ep in endpoints:
        counts[ep["status"]] = counts.get(ep["status"], 0) + 1

    doc: dict[str, Any] = {
        "schema": SCHEMA,
        "generated_by": "scripts/gen_cloud_surface.py",
        "canonical": True,
        "sources": [
            "internal/httpapi/server.go::register()",
            "contracts/observed-contract.json",
            "tools/src/aden_tools/senders/{registry,sendlog,setup}.py",
            "go_hive/go-hive/internal/globaldb/client.go",
            "go_hive/go-hive/internal/sentinel/{notifier,listeners}.go",
            "go_hive/go-hive/internal/llm/keycheck.go",
            "core/framework/llm/litellm.py",
            "core/framework/credentials/aden/client.py",
            "core/framework/server/streamtoken_refresh.py",
        ],
        "services": SERVICES,
        "endpoints": endpoints,
        "streams": STREAMS,
        "websockets": WEBSOCKETS,
        "websocket_policy": WEBSOCKET_POLICY,
        "invariants": INVARIANTS,
        "unknowns": UNKNOWNS,
        "summary": {
            "services": len(SERVICES),
            "endpoints_total": len(endpoints),
            "implemented": counts.get("implemented", 0),
            "planned": counts.get("planned", 0),
            "inferred": counts.get("inferred", 0),
            "unknown": counts.get("unknown", 0),
            "sse_streams": len(STREAMS),
            "websockets": len(WEBSOCKETS),
            "observed_contract_endpoints": len(observed),
        },
    }
    return doc, problems


# ---------------------------------------------------------------------------
# HTML rendering (from the JSON document, never hand-written)
# ---------------------------------------------------------------------------

def esc(value: Any) -> str:
    return html.escape("" if value is None else str(value), quote=True)


def pill(status: str) -> str:
    cls = {"implemented": "ok", "planned": "info", "inferred": "warn", "unknown": "bad"}.get(status, "")
    return f'<span class="pill {cls}">{esc(status)}</span>'


def method_cell(method: str, path: str) -> str:
    if not method and not path:
        return '<span class="muted">（未知路径）</span>'
    return f"<code>{esc(method)}</code> {esc(path)}"


def render_service_sections(doc: dict[str, Any]) -> str:
    blocks = []
    for i, svc in enumerate(doc["services"]):
        eps = [ep for ep in doc["endpoints"] if ep["service"] == svc["id"]]
        counts = {status: sum(1 for ep in eps if ep["status"] == status) for status in STATUS_ORDER}
        rows = []
        for ep in eps:
            golden = ep.get("response", {}).get("golden") or ""
            resp_status = ep.get("response", {}).get("status")
            resp = f"{resp_status}" if resp_status is not None else "—"
            if golden:
                resp += f"<br><code>{esc(golden)}</code>"
            client = ep.get("client_method")
            method_text = method_cell(ep["method"], ep["path"])
            if client:
                method_text += f'<br><span class="sub">client: <code>{esc(client)}</code></span>'
            anchors = "<br>".join(f"<code>{esc(a)}</code>" for a in ep["source_anchors"]) or "—"
            rows.append(
                "<tr>"
                f"<td>{method_text}</td>"
                f"<td>{esc(ep['auth'])}</td>"
                f"<td>{pill(ep['status'])}</td>"
                f"<td>{esc(ep['milestone'])}</td>"
                f"<td>{resp}</td>"
                f"<td>{anchors}</td>"
                f"<td>{esc(ep['notes'])}</td>"
                "</tr>"
            )
        blocks.append(
            f'<section id="svc-{esc(svc["id"])}"><h2><span class="num">{i + 1:02d}</span>{esc(svc["id"])}</h2>'
            f'<p class="sub">{esc(svc["role"])} · families: '
            + ", ".join(f"<code>{esc(x)}</code>" for x in svc.get("families", []))
            + " · base env: "
            + ", ".join(f"<code>{esc(x)}</code>" for x in svc["base_env"])
            + f' · auth: {esc(svc["auth"])}</p>'
            + f'<p class="sub">implemented {counts["implemented"]} / planned {counts["planned"]} / '
            + f'inferred {counts["inferred"]} / unknown {counts["unknown"]}</p>'
            '<div class="tablewrap"><table><colgroup>'
            '<col style="width:20%"><col style="width:9%"><col style="width:9%"><col style="width:7%">'
            '<col style="width:14%"><col style="width:19%"><col style="width:22%">'
            "</colgroup><thead><tr><th>方法 / 路径</th><th>auth</th><th>状态</th><th>里程碑</th>"
            "<th>响应 / golden</th><th>来源锚点</th><th>备注</th></tr></thead><tbody>"
            + "\n".join(rows)
            + "</tbody></table></div></section>"
        )
    return "\n".join(blocks)


def render_streams(doc: dict[str, Any]) -> str:
    if not doc["streams"]:
        return '<p class="sub">（无已登记 SSE 流）</p>'
    rows = []
    for stream in doc["streams"]:
        fields = ", ".join(f"<code>{esc(k)}</code>: {esc(v)}" for k, v in stream["payload_fields"].items())
        rows.append(
            "<tr>"
            f"<td><code>{esc(stream['method'])}</code> {esc(stream['path'])}</td>"
            f"<td><code>{esc(stream['event'])}</code></td>"
            f"<td>{pill(stream['status'])}</td>"
            f"<td>{esc(stream['milestone'])}</td>"
            f"<td>{fields}</td>"
            f"<td><code>{esc(stream['golden'])}</code></td>"
            f"<td>{esc(stream['notes'])}</td>"
            "</tr>"
        )
    return (
        '<div class="tablewrap"><table><colgroup>'
        '<col style="width:20%"><col style="width:9%"><col style="width:9%"><col style="width:7%">'
        '<col style="width:28%"><col style="width:12%"><col style="width:15%">'
        "</colgroup><thead><tr><th>端点</th><th>event</th><th>状态</th><th>里程碑</th><th>payload 字段</th>"
        "<th>golden</th><th>备注</th></tr></thead><tbody>" + "\n".join(rows) + "</tbody></table></div>"
    )


def render_invariants(doc: dict[str, Any]) -> str:
    items = []
    for inv in doc["invariants"]:
        checks = "；".join(f"<code>{esc(x)}</code>" for x in inv["enforced_by"])
        items.append(f"<li><b>{esc(inv['id'])}</b> — {esc(inv['rule'])}<br><span class=\"sub\">门禁：{checks}</span></li>")
    return "<ol>" + "\n".join(items) + "</ol>"


def render_html(doc: dict[str, Any]) -> str:
    s = doc["summary"]
    cards = [
        ("端点总数", s["endpoints_total"], f'{s["services"]} 个服务；本文件是唯一登记处'),
        ("implemented", s["implemented"], "server.go 已注册；与路由集合严格相等"),
        ("planned", s["planned"], "M2–M7 已观察/已知，尚未实现"),
        ("inferred / unknown", f'{s["inferred"]} / {s["unknown"]}', "inferred 有路径；unknown 无锚点不填"),
        ("SSE 流", s["sse_streams"], "event 名与代码写出值强校验"),
        ("WebSocket", s["websockets"], "当前无 WS；出现 Upgrade 代码即失败"),
    ]
    card_html = "\n".join(
        f'<div class="card"><div class="k">{esc(k)}</div><div class="v">{esc(v)}</div><div class="d">{esc(d)}</div></div>'
        for k, v, d in cards
    )
    ws_note = doc["websocket_policy"]["note"]
    unknowns = "\n".join(f"<li>{esc(item)}</li>" for item in doc["unknowns"])
    toc = "".join(f'<a href="#svc-{esc(svc["id"])}">{esc(svc["id"])}</a>' for svc in doc["services"])
    n_streams = len(doc["services"]) + 1
    n_inv = n_streams + 1
    n_unk = n_inv + 1
    n_reuse = n_unk + 1
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>go-hive-cloud · API / SSE / WS surface 不变量</title>
<style>
:root{{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}}
*{{box-sizing:border-box}} body{{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}}
a{{color:var(--accent2);text-decoration:none}} a:hover{{text-decoration:underline}}
.wrap{{max-width:1280px;margin:0 auto;padding:44px 26px 80px}}
header{{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}}
.eyebrow{{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}}
h1{{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em}} h1 span{{color:var(--accent)}}
.lede{{color:var(--muted);max-width:1080px;font-size:15px}} .meta{{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px}} .meta b{{color:var(--text)}}
section{{margin:38px 0}} h2{{font-size:22px;margin:0 0 8px}} h2 .num{{color:var(--accent);font-size:14px;margin-right:8px}} h3{{font-size:15.5px;margin:22px 0 8px;color:#cfdae6}}
p{{color:#c6d2df;margin:8px 0}} .sub{{color:var(--muted);font-size:12.5px}}
.grid{{display:grid;gap:12px}} .g4{{grid-template-columns:repeat(3,minmax(0,1fr))}} .grid>*{{min-width:0}}
@media(max-width:900px){{.g4{{grid-template-columns:1fr 1fr}}}} @media(max-width:560px){{.g4{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .v{{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;max-width:100%}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.info{{color:var(--accent2);border-color:#17324d;background:#0d1622}} .pill.bad{{color:var(--bad);border-color:#4d1d1d;background:#1a0f0f}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}}
.toc{{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px}} .toc a{{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)}} .toc a:hover{{color:var(--text);text-decoration:none}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
.muted{{color:var(--muted)}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}.toc a{{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">Hive Cloud · API Surface Invariant</div>
<h1>go-hive-cloud <span>全量 API / SSE / WS surface</span></h1>
<p class="lede">这张表是私有化云替身的唯一登记处：{s["implemented"]} 条已实现路由、M2–M7 的计划端点、senders 的推断/未知路径、SSE 事件与未来 WebSocket 全部在此。增删路由而不更新本文件（或反向）会让门禁失败。</p>
<div class="meta"><span>schema：<b>{esc(doc["schema"])}</b></span><span>来源：<b>server.go::register() + observed-contract.json + 客户端源码</b></span><span>产物：<b>cloud-surface.json + 本页</b></span><span>门禁：<b>gen_cloud_surface.py --check</b></span></div>
<div class="toc"><a href="#cards">统计卡片</a>{toc}<a href="#streams">SSE / WS</a><a href="#invariants">不变量</a><a href="#unknowns">缺口 / 未知</a><a href="#reuse">复用与维护</a></div></header>

<section id="cards"><h2><span class="num">00</span>统计卡片</h2>
<div class="grid g4">
{card_html}
</div>
<div class="callout info">状态口径：<b>implemented</b> = 本模块今天注册的路由；<b>planned</b> = 已观察/已知但属于后续里程碑；<b>inferred</b> = 客户端文档给出路径、但结构化契约未收录；<b>unknown</b> = 只有客户端方法名（或只有文档提及），方法/路径无锚点时可留空——<b>不猜</b>。</div>
</section>
{render_service_sections(doc)}
<section id="streams"><h2><span class="num">{n_streams:02d}</span>SSE 流与 WebSocket</h2>
<h3>SSE</h3>
{render_streams(doc)}
<h3>WebSocket</h3>
<div class="tablewrap"><table><thead><tr><th style="width:20%">状态</th><th>说明</th></tr></thead><tbody>
<tr><td><span class="pill warn">none</span></td><td>{esc(ws_note)}</td></tr>
</tbody></table></div>
</section>
<section id="invariants"><h2><span class="num">{n_inv:02d}</span>不变量（门禁强制）</h2>
{render_invariants(doc)}
</section>
<section id="unknowns"><h2><span class="num">{n_unk:02d}</span>缺口 / 未知（不得虚构）</h2>
<ul>
{unknowns}
</ul>
<p class="sub">unknown 项只有在拿到源码锚点后才能升级为 inferred/planned/implemented；升级时必须同时改本生成器的 curated 表与 source_anchors。</p>
</section>
<section id="reuse"><h2><span class="num">{n_reuse:02d}</span>复用与维护</h2>
<pre># 重新生成 JSON + HTML（在 go_hive/go-hive-cloud 下）
python3 scripts/gen_cloud_surface.py

# 门禁：字节一致 + 路由集合/observed 分类/SSE/WS 不变量
python3 scripts/gen_cloud_surface.py --check
# 或
./scripts/check_cloud_surface.sh</pre>
<p>复用场景：① 新增/删除路由前确认是否触碰契约；② 规划 M2–M7 时看哪些端点已观察、哪些仍是 unknown；③ 给私有化部署方交代"哪些能兼容、哪些不能承诺"；④ 代码评审时核对 SSE/WS 是否偷偷变化。</p>
<p class="sub">本页由 <code>scripts/gen_cloud_surface.py</code> 从 <code>contracts/cloud-surface.json</code> 渲染；不要手改 HTML 或 JSON。生成器解析 <code>server.go::register()</code> 与 <code>contracts/observed-contract.json</code>，curated 元数据在脚本内。</p>
</section>
<footer>维护口径：路由集合 = implemented 集合；observed 端点必须被分类；SSE 事件名必须与代码写出值一致；WebSocket 变更必须显式登记。任何一项漂移，<code>--check</code> 直接失败。</footer>
</div></body></html>
"""


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify generated files and surface invariants")
    args = parser.parse_args()

    try:
        doc, problems = build_surface()
    except SurfaceError as exc:
        print(f"cloud-surface: {exc}", file=sys.stderr)
        return 1

    json_text = json.dumps(doc, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(doc)

    if args.check:
        if not OUT_JSON.exists():
            problems.append(f"missing output: {OUT_JSON.relative_to(ROOT)}")
        elif OUT_JSON.read_text(encoding="utf-8") != json_text:
            problems.append(f"drift: {OUT_JSON.relative_to(ROOT)} (run python3 scripts/gen_cloud_surface.py)")
        if not OUT_HTML.exists():
            problems.append(f"missing output: {OUT_HTML.relative_to(ROOT)}")
        elif OUT_HTML.read_text(encoding="utf-8") != html_text:
            problems.append(f"drift: {OUT_HTML.relative_to(ROOT)} (run python3 scripts/gen_cloud_surface.py)")

        if problems:
            for problem in problems:
                print(f"cloud-surface: {problem}")
            return 1

        s = doc["summary"]
        print(
            "cloud-surface: OK — "
            f'{s["endpoints_total"]} endpoints '
            f'(implemented {s["implemented"]} / planned {s["planned"]} / '
            f'inferred {s["inferred"]} / unknown {s["unknown"]}), '
            f'SSE {s["sse_streams"]}, WS {s["websockets"]}; '
            f'observed-contract {s["observed_contract_endpoints"]} classified; '
            f'routes {s["implemented"]}/{s["implemented"]}'
        )
        return 0

    OUT_JSON.parent.mkdir(parents=True, exist_ok=True)
    OUT_HTML.parent.mkdir(parents=True, exist_ok=True)
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    s = doc["summary"]
    print(
        f'cloud-surface: wrote {OUT_JSON.relative_to(ROOT)} and {OUT_HTML.relative_to(ROOT)} — '
        f'{s["endpoints_total"]} endpoints (implemented {s["implemented"]} / planned {s["planned"]} / '
        f'inferred {s["inferred"]} / unknown {s["unknown"]}), SSE {s["sse_streams"]}, WS {s["websockets"]}'
    )
    if problems:
        # Generation still writes what it can; surface the drift so it is not silent.
        for problem in problems:
            print(f"cloud-surface: {problem}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
