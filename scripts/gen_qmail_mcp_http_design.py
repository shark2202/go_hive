#!/usr/bin/env python3
"""Generate the go_hive queen-mail MCP HTTP requirements -> design -> acceptance chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/qmail-mcp-http-design.html
  - go_hive/docs/qmail-mcp-http-design.json

v1 is deliberately the same model as the stdio adapter: two tools, a fixed
queen/conversation seat, a bearer token in the header, and no extra inbox.
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "qmail-mcp-http-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "qmail-mcp-http-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {
    "schema_version": 1,
    "feature": "qmail-mcp-http",
    "status": "design",
    "decision": (
        "Expose the existing stdio queen-mail MCP surface over an opt-in "
        "Streamable HTTP endpoint configured by queen_mail.mcp_http.<id>, with "
        "the URL /mcp/queen-mail/<id>/<conversation_ref> and an Authorization "
        "bearer token. Hive queen letters and replies to that seat are pushed to "
        "a signed webhook only when webhook.enabled is true; webhook.enabled "
        "defaults to false and a disabled webhook leaves the seat inbound-only. "
        "v1 has no pull tool, no participants.json and no extra MCP tools."
    ),
    "goal_beyond_request": (
        "Let another agent or harness use the same two queen-mail MCP tools as "
        "a local Claude Code queen, with a fixed sender identity and no process-"
        "local environment, while keeping the existing stdio adapter untouched. "
        "The optional Hive-queen-to-remote-harness direction is delivered by a "
        "signed webhook push when enabled, not by a new MCP read tool."
    ),
    "problem": {
        "statement": (
            "The current queen-mail MCP command is stdio and its identity is the "
            "HIVE_QUEEN_MAIL_URL/TOKEN/FROM environment. That gives one client one "
            "process and one fixed conversation. A remote harness cannot share it "
            "safely, cannot be identified by URL, and cannot be configured next to "
            "the existing queen_mail.claude_code entries. The same gap leaves no "
            "path for a Hive queen to deliver a reply or a new letter to a remote "
            "harness."
        ),
        "user_value": [
            "One HTTP endpoint can serve several external harnesses.",
            "Each URL/token pair has a fixed queen and conversation_ref, so the sender is never a tool argument.",
            "The configuration mirrors queen_mail.claude_code.<id> instead of adding a second participant database.",
            "The model-facing tool names and semantics remain exactly list_queens and message_queen.",
            "A Hive queen can push a reply or a new letter to the configured webhook of that remote seat when webhook.enabled is true.",
            "The local stdio path stays unchanged and the endpoint is off unless configured; the webhook is optional and default false.",
            "A webhook-disabled seat remains usable as an inbound-only MCP participant; no outbound push is implied.",
        ],
        "why_not_now": (
            "This is a Go-only extension; upstream Hive has no HTTP MCP server for "
            "queen mail. It is not part of the 1.0 parity claim and remains at design "
            "status until the configuration and reply boundary are accepted."
        ),
    },
    "protocol": {
        "name": "Model Context Protocol",
        "transport": "Streamable HTTP",
        "versions": ["2025-03-26", "2025-06-18", "2025-11-25"],
        "stdio_versions": ["2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25"],
        "endpoint": "/mcp/queen-mail/<id>/<conversation_ref>",
        "legacy": "HTTP+SSE from 2024-11-05 is out of scope; stdio is unchanged.",
        "auth_header": "Authorization: Bearer ***",
        "session_header": "Mcp-Session-Id",
        "protocol_header": "MCP-Protocol-Version",
        "source": "https://modelcontextprotocol.io/specification/2025-06-18/basic/transports",
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Expose an opt-in Streamable HTTP MCP endpoint serving the same two queen-mail tools while the existing stdio adapter remains unchanged.",
            "acceptance": "HTTP clients and the stdio golden both pass; no remote tool is added to the local model catalogue.",
            "traces": ["D1", "D2", "D5", "A1", "A2", "A8"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "Configure each remote client with queen_mail.mcp_http.<id>, mirroring the queen_mail.claude_code.<id> shape; the entry binds a queen, a conversation_ref, a token and enabled.",
            "acceptance": "Config parsing, missing fields, duplicate conversation_ref, disabled entries and unknown queen cases pass; no participants.json is created.",
            "traces": ["D3", "D4", "A3", "A4"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Use URL /mcp/queen-mail/<id>/<conversation_ref> with the bearer token in the Authorization header; the path is routing metadata and never the authorization source.",
            "acceptance": "Wrong token, wrong path, unknown id, mismatched ref and path traversal fail closed with no letter or audit mutation.",
            "traces": ["D3", "D4", "D7", "A3", "A6"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Keep the model-facing surface exactly list_queens and message_queen; do not add read_letters, reply_letter, an inbox or an admin tool in v1.",
            "acceptance": "Tool schema, descriptions, input/output shapes and errors match the stdio pair; tools/list has no extra remote tool.",
            "traces": ["D4", "D5", "A5"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Implement Streamable HTTP with POST, optional GET SSE and DELETE, Mcp-Session-Id, protocol-version negotiation and standard JSON-RPC errors; keep stdio versions unchanged.",
            "acceptance": "Fake clients cover initialize, session reuse, JSON/SSE, DELETE, version rejection, split frames and malformed requests.",
            "traces": ["D2", "A2"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Make the endpoint least-privilege: no token in the URL, no query token, no desktop token, no public bind by default, Origin/Host checks, and a TLS boundary for remote use.",
            "acceptance": "Missing/wrong token, query token, desktop token, bad Origin and non-loopback without an HTTPS public_url fail closed.",
            "traces": ["D1", "D7", "A6"],
        },
        {
            "id": "R7",
            "priority": "P1",
            "statement": "Reuse the existing queen-mail limits and audit: configured queen participation, daily limit, max_depth, self-send, unknown queen and mail.jsonl metadata behavior.",
            "acceptance": "Daily limit, max depth, self-send, unknown queen and audit rows are identical to the local path; rejected sends have no partial state.",
            "traces": ["D6", "A7"],
        },
        {
            "id": "R8",
            "priority": "P1",
            "statement": "Read mcp_http configuration at serve startup like the existing queen_mail section; changing an entry requires a serve restart.",
            "acceptance": "Startup accepts enabled entries and rejects invalid ones; editing an entry without restart has no effect; restart applies it.",
            "traces": ["D8", "A3", "A7"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "Do not turn this endpoint into Hive-to-Hive federation. When webhook.enabled is true, a reply or a queen-initiated letter for a remote seat is delivered by signed webhook push; when it is false, the seat is inbound-only and no outbound delivery is implied. There is no inbound/pull MCP read path.",
            "acceptance": "Negative test proves no federation route exists; with webhook enabled a remote seat receives the expected webhook; with it disabled no push or outbox delivery is created; no read_letters/cursor is exposed.",
            "traces": ["D10", "D11", "A10", "A17"],
        },
        {
            "id": "R10",
            "priority": "P1",
            "statement": "Add no Go dependency, no cgo requirement, no bundled runtime and no new local model tool; keep the pure-Go release boundary.",
            "acceptance": "CGO_ENABLED=0 tests and six-platform builds pass; go.mod has no new module; the local tool catalogue is unchanged.",
            "traces": ["D9", "A9"],
        },
        {
            "id": "R11",
            "priority": "P0",
            "statement": "Make webhook delivery optional and disabled by default. The webhook object or webhook.enabled=false must leave the MCP endpoint usable inbound-only; when webhook.enabled=true, every Hive queen letter or reply addressed to that remote seat is delivered through the configured signed webhook. Do not add a pull/read tool or cursor in v1.",
            "acceptance": "An entry with no webhook or webhook.enabled=false mounts and passes list/send without URL/secret requirements and creates no outbound delivery. When enabled, a reply and a queen-initiated letter each produce one signed webhook; a down endpoint retries and then records a dead letter; no read_letters tool exists.",
            "traces": ["D11", "D12", "A13", "A14", "A15", "A16", "A17"],
        },
    ],
    "non_goals": [
        "Do not add participants.json or an inbox file; the config entry itself is the participant record.",
        "Do not make webhook mandatory or default it on; a missing webhook object and webhook.enabled=false both mean inbound-only.",
        "Do not add read_letters, reply_letter, a cursor or any pull endpoint in v1; when enabled, webhook push is the only Hive-to-harness channel.",
        "Do not add a Hive-to-Hive federation transport or remote Hive registration.",
        "Do not put the token in the URL path or query string; use the Authorization header.",
        "Do not expose the endpoint on 0.0.0.0 by default or reuse HIVE_DESKTOP_TOKEN.",
        "Do not add a new Go module, cgo dependency or bundled runtime.",
        "Do not change the local model-facing tool catalogue or the stdio MCP handshake.",
    ],
    "assumptions": [
        "<id> is a stable configuration key and URL segment; it is not accepted as an unverified queen identity.",
        "queen_id in the entry must name a queen or Claude Code queen already known to queen mail; an unknown queen is a startup/config error, not a new queen.",
        "conversation_ref is a stable, URL-safe identifier owned by that entry; it is unique across entries and is not chosen by the tool caller.",
        "The external harness that enables webhook has an HTTPS endpoint that can verify a webhook HMAC and deduplicate by delivery_id; if it is down, Hive retries and may dead-letter rather than dropping silently. A disabled webhook has no such endpoint requirement.",
        "The endpoint exposes only list_queens and message_queen to the remote harness; when enabled, Hive-to-harness delivery is out-of-band webhook push, not an MCP read tool. When disabled, the seat is inbound-only.",
        "A webhook-disabled seat is not advertised as a Hive-deliverable correspondent; a Hive queen cannot target it and no outbound queue is created.",
        "Queen-mail configuration is read at serve startup; mcp_http changes require a restart, matching the current queen_mail lifecycle.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "Use stdio over SSH or a generic MCP proxy",
            "verdict": "deferred",
            "reason": "Good for one trusted machine, but no shared HTTP endpoint, no per-URL identity and no configuration next to queen_mail entries.",
        },
        {
            "id": "O2",
            "name": "Add participants.json plus inbox and four duplex tools",
            "verdict": "rejected",
            "reason": "More capable, but no longer the stdio surface the user asked for; it adds a second identity store, persistence and reply consumption before the simple path is proven.",
        },
        {
            "id": "O3",
            "name": "Put the bearer token in the URL path",
            "verdict": "rejected",
            "reason": "URLs leak into access logs, traces, errors and screenshots. A header token is the standard, revocable boundary and keeps the path non-secret.",
        },
        {
            "id": "O4",
            "name": "queen_mail.mcp_http.<id> plus /mcp/queen-mail/<id>/<conversation_ref>, header token and an optional signed webhook",
            "verdict": "chosen",
            "reason": "Mirrors the existing claude_code.<id> configuration shape, keeps the stdio tool pair, removes the extra participant file, gives each client a fixed seat, and adds the optional Hive-to-harness direction without a pull API. webhook.enabled defaults false, so the inbound-only endpoint works without a callback service.",
        },
        {
            "id": "O5",
            "name": "Hive-to-Hive federation in the queenmail backend",
            "verdict": "separate",
            "reason": "Correct layer for reliable cross-instance delivery, but a different problem: signed envelopes, retries, leases and remote discovery. Do not mix it into this adapter.",
        },
        {
            "id": "O6",
            "name": "Add read_letters/cursor pull as the first Hive-to-harness channel",
            "verdict": "rejected",
            "reason": "The user explicitly wants push first. Pull adds cursor bootstrap, ack state, polling cost and a second delivery semantic; when enabled, webhook push is the only v1 outbound channel.",
        },
    ],
    "design": {
        "D1": {
            "title": "Deployment shape",
            "text": (
                "Mount the MCP handler under the existing serve process at "
                "/mcp/queen-mail/. It is off unless at least one enabled mcp_http "
                "entry exists. Default serve binding remains loopback; remote use is "
                "a reverse-proxy/TLS deployment. The handler does not widen the rest "
                "of the serve route surface and does not reuse HIVE_DESKTOP_TOKEN."
            ),
        },
        "D2": {
            "title": "Streamable HTTP transport",
            "text": (
                "POST accepts JSON-RPC and returns application/json or "
                "text/event-stream; GET provides the optional SSE stream; DELETE ends "
                "the session. After initialize the server returns Mcp-Session-Id; "
                "subsequent requests carry MCP-Protocol-Version. HTTP accepts "
                "2025-03-26, 2025-06-18 and 2025-11-25; stdio keeps its four versions."
            ),
        },
        "D3": {
            "title": "Configuration entry",
            "text": (
                "queen_mail.mcp_http.<id> is a map entry like "
                "queen_mail.claude_code.<id>. Required fields are queen_id, "
                "conversation_ref and exactly one of token or token_sha256. Optional "
                "enabled defaults to true. The entry is the participant record; no "
                "participants.json is read or written. Duplicate conversation_ref and "
                "unknown queen_id fail startup."
            ),
        },
        "D4": {
            "title": "URL and identity binding",
            "text": (
                "The canonical URL is /mcp/queen-mail/<id>/<conversation_ref>. The "
                "header token is authoritative; the path is only a routing hint and "
                "must exactly match the entry. <id> and <conversation_ref> are "
                "URL-safe segments with no slash, dot-dot or control characters. A "
                "token bound to another seat, a path mismatch or an unknown id fails "
                "closed before Mail is called."
            ),
        },
        "D5": {
            "title": "Tool parity",
            "text": (
                "Expose exactly list_queens and message_queen with the same names, "
                "descriptions, inputSchema, outputSchema and tool-result envelopes as "
                "the stdio adapter. There is no read_letters, reply_letter, admin or "
                "raw protocol tool. The local model's default catalogue is not "
                "changed; the remote endpoint is a separate HTTP surface."
            ),
        },
        "D6": {
            "title": "Mail bridge",
            "text": (
                "After auth, list_queens calls Correspondents(queen_id); message_queen "
                "calls the existing Send(writer, fromRef, to, text, nil). Daily limit, "
                "max_depth, self-send, unknown queen and audit metadata remain the "
                "existing queen_mail behavior. Local Hive and Claude Code delivery are "
                "unchanged; only a target that is a webhook-enabled remote mcp_http "
                "seat goes to the webhook dispatcher. A webhook-disabled seat is not "
                "a deliverable correspondent. The adapter creates no inbox and no read path."
            ),
        },
        "D7": {
            "title": "Network and secret security",
            "text": (
                "Token only in Authorization; reject query/cookie/path tokens. "
                "Constant-time comparison; token_sha256 is preferred over plaintext "
                "token. No access log records Authorization or the token; the path logs "
                "only the non-secret id/ref. Validate Origin and Host; no CORS wildcard; "
                "cap request bodies; require an HTTPS public_url for non-loopback."
            ),
        },
        "D8": {
            "title": "Config lifecycle",
            "text": (
                "Parse mcp_http in the same startup read as queen_mail. Invalid fields, "
                "duplicate refs or unknown queens fail the MCP listener with a fixable "
                "error; the rest of serve may continue. Changing an entry requires a "
                "restart; there is no hot reload in v1. Disabled entries return 404, "
                "not a fallback to another seat."
            ),
        },
        "D9": {
            "title": "Package and build boundary",
            "text": (
                "internal/qmailmcp owns MCP transport and config/principal validation; "
                "internal/qmailwebhook owns the durable outbox, HMAC, retry and HTTP "
                "client. Neither may import internal/server, internal/cli or "
                "internal/tools/builtin. internal/server mounts a thin adapter and "
                "supplies the Mail methods. No dependency, cgo flag, model tool "
                "registration or local catalogue change is introduced."
            ),
        },
        "D10": {
            "title": "Federation and pull boundary",
            "text": (
                "This is not a remote Hive peer. No federation endpoint, remote queen "
                "registration, delivery lease or remote retry protocol is added. The "
                "webhook HMAC and retries are local delivery to a remote seat, not a "
                "federation handshake. There is no read_letters, cursor or pull endpoint "
                "in v1: when webhook.enabled is true, Hive-to-remote delivery is push-only "
                "through the configured webhook; when false, there is no outbound path."
            ),
        },
        "D11": {
            "title": "Remote seat and optional delivery",
            "text": (
                "Every mcp_http.<id> entry is a remote seat. With webhook.enabled=true "
                "it is added to the queen-mail roster so a Hive queen can list it and "
                "write to it, and Mail.deliver routes a letter or reply addressed to "
                "that seat into the webhook dispatcher instead of a local Hive session. "
                "A queen reply to the seat's conversation_ref enters the same path. "
                "With webhook absent or enabled=false the seat remains inbound-only: it "
                "is not advertised as a Hive-deliverable correspondent, no outbox or "
                "dispatcher is started, and a forced send returns a typed webhook_disabled "
                "error rather than silently dropping the letter. The MCP tool surface "
                "stays list_queens and message_queen."
            ),
        },
        "D12": {
            "title": "Webhook delivery contract",
            "text": (
                "When webhook.enabled is true, persist a durable per-seat outbox, then "
                "POST one JSON event per letter or reply with delivery_id, event kind, "
                "participant id, letter id, from/to, depth, caused_by and optional text. "
                "Sign the raw body with HMAC-SHA256 over timestamp.body and send "
                "X-Hive-Webhook-Id, X-Hive-Webhook-Timestamp and X-Hive-Webhook-Signature. "
                "Retry 408/429/5xx and network errors with exponential backoff and jitter; "
                "never retry a permanent 4xx. Preserve per-seat order, expose attempts/state, "
                "and move exhausted deliveries to a dead-letter record. There is no pull fallback."
            ),
        },
    },
    "package_layout": {
        "core": "internal/qmailmcp",
        "webhook": "internal/qmailwebhook",
        "adapter": "internal/server/qmail_mcp_adapter.go",
        "existing": "internal/queenmail/mcp_stdio.go (unchanged)",
        "rule": (
            "internal/qmailmcp owns MCP JSON-RPC, Streamable HTTP and mcp_http entry "
            "validation; internal/qmailwebhook owns the durable outbox, HMAC signing, "
            "retry/backoff and webhook HTTP client. Neither may import internal/server, "
            "internal/cli or internal/tools/builtin. The server adapter mounts the "
            "handler, supplies the existing Mail methods and wires the optional webhook "
            "dispatcher as the remote seat deliverer only when webhook.enabled is true."
        ),
        "maintenance": (
            "Fake MCP clients, webhook receiver fixtures and config tests live with "
            "the two packages; the adapter gets one httptest wiring test and one real "
            "serve E2E. The stdio handshake golden remains byte-identical and is part "
            "of the gate."
        ),
    },
    "tool_surface": [
        {"name": "list_queens", "kind": "read-only", "surface": "HTTP and stdio", "auth": "token-bound seat", "approval": "none"},
        {"name": "message_queen", "kind": "write / send letter", "surface": "HTTP and stdio", "auth": "token-bound seat", "approval": "token scope; existing queen_mail limits"},
    ],
    "identity_contract": {
        "config": "queen_mail.mcp_http.<id>",
        "id": "Stable URL/config key; not an unverified queen identity.",
        "queen_id": "Queen or Claude Code queen already known to queen mail.",
        "conversation_ref": "Stable reply/thread address; unique across entries; URL-safe.",
        "token": "Bearer secret in the Authorization header; token_sha256 preferred.",
        "webhook": "Optional push target for queen-initiated letters and replies; absent or enabled=false means inbound-only, no pull store.",
        "path": "/mcp/queen-mail/<id>/<conversation_ref>",
        "rule": "The header token binds the entry; the path must match it; tool arguments cannot change the sender.",
    },
    "transport_contract": {
        "request": {
            "method": "POST",
            "headers": ["Authorization", "Content-Type: application/json", "Accept: application/json, text/event-stream", "Mcp-Session-Id (after initialize)", "MCP-Protocol-Version"],
            "body": "JSON-RPC 2.0 request or notification",
        },
        "session": ["initialize", "server returns Mcp-Session-Id and negotiated protocol", "subsequent tools/list and tools/call carry the session", "DELETE terminates", "expired session returns 404"],
        "responses": ["application/json JSON-RPC response", "text/event-stream for server messages", "HTTP errors carry a stable JSON error code"],
        "initialize_example": {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "external-harness", "version": "1.0"}},
        },
        "tool_call_example": {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/call",
            "params": {"name": "message_queen", "arguments": {"queen_id": "queen_brand_design", "message": "Please review the launch brief."}},
        },
    },
    "webhook_contract": {
        "enabled_default": False,
        "enabled_by": "webhook.enabled",
        "optional": "The webhook object is optional. Missing webhook or webhook.enabled=false mounts the MCP endpoint inbound-only; URL, secret and retry fields are not required.",
        "disabled_behavior": "A disabled webhook creates no outbox, dispatcher or dead-letter directory. The seat is not advertised as a Hive-deliverable correspondent; a forced Hive-to-seat send returns webhook_disabled and no letter is silently queued.",
        "direction": "Hive queen -> remote harness only when webhook.enabled=true; no pull/cursor in v1.",
        "events": ["message", "reply"],
        "headers": ["X-Hive-Webhook-Id", "X-Hive-Webhook-Timestamp", "X-Hive-Webhook-Signature", "X-Hive-Webhook-Event"],
        "signature": "HMAC-SHA256(secret, timestamp + '.' + raw_body), hex, header v1=<hex>",
        "retry": {
            "retry_on": "network error, timeout, 408, 429, 5xx",
            "no_retry": "other 4xx",
            "max_attempts": 6,
            "initial_backoff_ms": 500,
            "max_backoff_ms": 30000,
            "jitter": True,
            "order": "FIFO per participant",
            "dead_letter": "$HIVE_HOME/queen_mail/webhook_outbox/<id>/dead/",
        },
        "payload_example": {
            "version": 1,
            "delivery_id": "wh_01J...",
            "event": "reply",
            "participant_id": "harness_a",
            "letter": {
                "id": "hop_63af...",
                "kind": "reply",
                "from": "queen_technology",
                "to": "harness_a",
                "from_session": "session_...",
                "to_session": "remote_harness_a",
                "caused_by": "hop_...",
                "depth": 2,
                "chars": 42,
                "text": "ACK from the queen.",
            },
        },
        "security": [
            "HTTPS only for non-loopback URLs.",
            "Block private/loopback/link-local addresses unless allow_private_network is explicitly true.",
            "Do not follow redirects to a new host.",
            "Never log the secret, signature, Authorization, or body.",
            "Receiver must verify signature and deduplicate delivery_id.",
        ],
    },
    "config_contract": {
        "status": "design",
        "config": "queen_mail.mcp_http.<id>",
        "authority": "configuration.json owns queen_mail.mcp_http.<id>; there is no participants.json and no inbox pull path in v1.",
        "files": [
            {"level": "hive", "path": "$HIVE_HOME/configuration.json", "own": "queen_mail.mcp_http.<id> entries: id, queen_id, conversation_ref, token/token_sha256, enabled, optional webhook.enabled (default false) and its enabled-only fields"},
        ],
        "sample": {
            "queen_mail": {
                "enabled": True,
                "mcp_http": {
                    "harness_a": {
                        "queen_id": "queen_technology",
                        "conversation_ref": "remote_harness_a",
                        "token_sha256": "<sha256>",
                        "enabled": True,
                        "webhook": {"enabled": False},
                    }
                },
            }
        },
        "enabled_sample": {
            "queen_mail": {
                "mcp_http": {
                    "harness_a": {
                        "queen_id": "queen_technology",
                        "conversation_ref": "remote_harness_a",
                        "token_sha256": "<sha256>",
                        "enabled": True,
                        "webhook": {
                            "enabled": True,
                            "url": "https://harness.example/hive/qmail",
                            "secret_env": "HARNESS_A_QMAIL_SECRET",
                            "events": ["message", "reply"],
                            "headers": {"X-Tenant": "harness_a"},
                            "include_text": True,
                            "timeout_ms": 5000,
                            "max_attempts": 6,
                            "initial_backoff_ms": 500,
                            "max_backoff_ms": 30000,
                            "max_payload_bytes": 65536,
                            "allow_private_network": False,
                            "dead_letter_retention_hours": 168,
                        },
                    }
                },
            }
        },
        "fields": [
            {"name": "queen_id", "required": "yes", "meaning": "sender identity; must already be a known queen"},
            {"name": "conversation_ref", "required": "yes", "meaning": "stable reply/thread address; unique across entries"},
            {"name": "token", "required": "one of token/token_sha256", "meaning": "raw bearer token; local development only"},
            {"name": "token_sha256", "required": "one of token/token_sha256", "meaning": "hash of the bearer token; preferred"},
            {"name": "enabled", "required": "no", "meaning": "defaults true; false returns 404"},
            {"name": "webhook", "required": "no", "meaning": "optional object; absent is equivalent to webhook.enabled=false"},
            {"name": "webhook.enabled", "required": "no", "meaning": "default false; false keeps the MCP seat inbound-only"},
            {"name": "webhook.url", "required": "only when enabled", "meaning": "HTTPS receiver URL; no redirects"},
            {"name": "webhook.secret_env", "required": "one of secret/secret_env when enabled", "meaning": "environment variable holding the HMAC secret"},
            {"name": "webhook.secret", "required": "one of secret/secret_env when enabled", "meaning": "inline secret for local development only"},
            {"name": "webhook.events", "required": "no", "meaning": "message/reply filter; default both"},
            {"name": "webhook.headers", "required": "no", "meaning": "extra static headers for receiver authentication"},
            {"name": "webhook.include_text", "required": "no", "meaning": "include letter text in payload; default true when enabled"},
            {"name": "webhook.timeout_ms", "required": "no", "meaning": "per-attempt timeout, 1000..30000"},
            {"name": "webhook.max_attempts", "required": "no", "meaning": "1..10, default 6"},
            {"name": "webhook.initial_backoff_ms", "required": "no", "meaning": "first retry delay, default 500"},
            {"name": "webhook.max_backoff_ms", "required": "no", "meaning": "retry cap, default 30000"},
            {"name": "webhook.max_payload_bytes", "required": "no", "meaning": "payload cap, default 65536"},
            {"name": "webhook.allow_private_network", "required": "no", "meaning": "default false; SSRF guard exception"},
            {"name": "webhook.dead_letter_retention_hours", "required": "no", "meaning": "retain exhausted deliveries; default 168"},
        ],
        "reload": "Changes require a serve restart, matching the current queen_mail startup read.",
    },
    "failure_modes": [
        {"case": "missing, wrong or revoked token", "result": "401; no letter or audit mutation"},
        {"case": "path id/ref does not match the token entry", "result": "403; no Mail call"},
        {"case": "unknown/disabled id, unknown queen, duplicate ref", "result": "404 or startup config error; no fallback"},
        {"case": "daily limit or max_depth exhausted", "result": "existing queen_mail error; no secondary effect"},
        {"case": "malformed or oversized body", "result": "400/413; no letter or audit mutation"},
        {"case": "backend unavailable", "result": "503 with retryable code; no fabricated letter"},
        {"case": "HTTP client disconnects during send", "result": "server completes under its context; retry may duplicate because v1 has no idempotency key"},
        {"case": "webhook absent or webhook.enabled=false", "result": "MCP endpoint stays mounted inbound-only; no URL/secret required; no outbox, dispatcher or dead-letter directory is created"},
        {"case": "Hive queen targets a webhook-disabled remote seat", "result": "seat is not a deliverable correspondent; a forced send returns webhook_disabled and no letter is silently queued"},
        {"case": "webhook receiver down or 5xx", "result": "only when enabled: persist in outbox, retry with backoff, then dead-letter; no pull fallback"},
        {"case": "webhook 2xx but receiver failed internally", "result": "at-least-once delivery is the contract; receiver deduplicates by delivery_id"},
        {"case": "webhook signature mismatch or SSRF target", "result": "receiver rejects; Hive blocks private targets unless explicitly allowed"},
    ],
    "limits": {
        "body_bytes": 1048576,
        "message_bytes": 32768,
        "request_timeout_seconds": 180,
        "max_sessions": 64,
        "session_idle_minutes": 30,
        "webhook_timeout_ms": 5000,
        "webhook_max_attempts": 6,
        "webhook_max_payload_bytes": 65536,
        "daily_limit": "existing queen_mail.daily_limit",
        "max_depth": "existing queen_mail.max_depth",
    },
    "acceptance": [
        {"id": "A1", "test": "Config and mount", "pass": "enabled mcp_http entry mounts the endpoint; missing fields, unknown queen and duplicate ref fail startup; no participants file is created."},
        {"id": "A2", "test": "MCP conformance", "pass": "initialize, tools/list, tools/call, JSON/SSE, session reuse, DELETE and version rejection pass with fake clients."},
        {"id": "A3", "test": "URL and token binding", "pass": "correct token/path succeeds; wrong token, wrong path, unknown id, mismatched ref and traversal chars fail closed."},
        {"id": "A4", "test": "Multiple clients", "pass": "two mcp_http entries with different ids/refs keep distinct sender identities, quotas and audit rows; sharing a token or ref is rejected."},
        {"id": "A5", "test": "Stdio tool parity", "pass": "list_queens and message_queen names, descriptions, schemas, results and errors match the stdio pair; tools/list contains no extra tool."},
        {"id": "A6", "test": "Security", "pass": "query/path token rejected; desktop token rejected; Origin/Host checks, body cap and secret redaction pass."},
        {"id": "A7", "test": "Limits and audit", "pass": "daily limit, max_depth, self-send, unknown queen, body cap and no-partial-side-effect cases match existing queen mail."},
        {"id": "A8", "test": "Stdio regression", "pass": "existing queen-mail MCP handshake, local catalogue and two-tool stdio surface are byte-identical."},
        {"id": "A9", "test": "Build and dependency audit", "pass": "CGO_ENABLED=0 package tests and six-platform builds pass; go.mod has no new module; no bundled runtime."},
        {"id": "A10", "test": "Negative federation/pull boundary", "pass": "no federation route, inbox file, read_letters, cursor or reply_letter is registered; docs state the optional-push and no-pull boundary."},
        {"id": "A11", "test": "Design artifact drift", "pass": "generator --check returns 0; --check failure path returns 1; HTML renders at 1440 and 390 without page-level overflow."},
        {"id": "A12", "test": "Real heterogeneous E2E", "pass": "two different MCP clients complete list_queens/message_queen against a real serve; when webhook.enabled=true a webhook receiver also gets the queen reply; missing client or receiver is an external gap, not PASS."},
        {"id": "A13", "test": "Webhook payload and signature", "pass": "reply and queen-initiated message produce one event each; delivery_id, letter fields and HMAC over timestamp.body are byte-stable; receiver rejects wrong signature."},
        {"id": "A14", "test": "Retry, order and dead-letter", "pass": "network/408/429/5xx retries use backoff+jitter; other 4xx do not retry; per-seat order is preserved; exhausted deliveries land in dead-letter with attempts."},
        {"id": "A15", "test": "Webhook security", "pass": "non-HTTPS or private/loopback URL is rejected unless allow_private_network; redirects are not followed; secret/token/body absent from logs; replay by delivery_id is detectable."},
        {"id": "A16", "test": "No pull fallback", "pass": "tools/list contains only list_queens and message_queen; no read_letters, cursor, ack or inbox read endpoint exists."},
        {"id": "A17", "test": "Webhook optional/default off", "pass": "missing webhook and webhook.enabled=false both mount the MCP endpoint; inbound list/send pass without URL/secret; no outbox/dispatcher is created; a forced Hive-to-seat send returns webhook_disabled; enabled=true still passes A13-A15."},
    ],
    "review_focus": [
        {"input": "conversation_ref contains slash, percent-encoding or dot-dot", "expected": "reject before routing; no path normalization ambiguity", "owner": "T1"},
        {"input": "token stored as plaintext in configuration.json", "expected": "document 0600 and prefer token_sha256; never log the value", "owner": "T1"},
        {"input": "two entries share conversation_ref or id", "expected": "startup error; no ambiguous sender/reply route", "owner": "T1"},
        {"input": "webhook endpoint is down during a queen reply", "expected": "persist, retry with backoff, preserve order, dead-letter after max attempts; no pull fallback", "owner": "T7"},
        {"input": "webhook URL points at loopback or a private address", "expected": "reject unless allow_private_network; no redirects; no SSRF", "owner": "T8"},
    ],
    "tasks": [
        {"id": "T1", "depends_on": [], "writes": ["internal/qmailmcp/config.go", "internal/qmailmcp/identity.go", "internal/qmailmcp/config_test.go"], "done": "mcp_http entries parse like claude_code, validation/duplicate/unknown-queen tests pass."},
        {"id": "T2", "depends_on": ["T1"], "writes": ["internal/qmailmcp/transport.go", "internal/qmailmcp/protocol.go", "internal/qmailmcp/transport_test.go"], "done": "Streamable HTTP session, JSON/SSE, protocol and URL binding tests pass."},
        {"id": "T3", "depends_on": ["T2"], "writes": ["internal/qmailmcp/mail_bridge.go", "internal/server/qmail_mcp_adapter.go", "internal/server/qmail_mcp_adapter_test.go"], "done": "adapter mounts the endpoint, calls Correspondents/Send and does not widen serve routes."},
        {"id": "T4", "depends_on": ["T2", "T3"], "writes": ["internal/qmailmcp/security.go", "internal/qmailmcp/limits.go", "internal/qmailmcp/failure_test.go"], "done": "Origin/Host, token/path, body cap, no-partial-effect and secret-redaction tests pass."},
        {"id": "T5", "depends_on": ["T3", "T4"], "writes": ["internal/qmailmcp/conformance_test.go", "docs/qmail-mcp-http-e2e.md"], "done": "two fake clients plus one real serve with two ids pass; stdio golden remains byte-identical."},
        {"id": "T6", "depends_on": ["T5", "T8"], "writes": [], "done": "CGO=0 six-platform build, full CI, dependency and docs/render gates pass."},
        {"id": "T7", "depends_on": ["T1", "T3"], "writes": ["internal/qmailwebhook/outbox.go", "internal/qmailwebhook/dispatcher.go", "internal/qmailwebhook/sign.go", "internal/qmailwebhook/webhook_test.go"], "done": "durable outbox, HMAC, retry/backoff/jitter, order, dead-letter and receiver fixtures pass."},
        {"id": "T8", "depends_on": ["T4", "T7"], "writes": ["internal/qmailmcp/mail_bridge.go", "internal/server/qmail_mcp_adapter.go", "internal/server/qmail_mcp_webhook_test.go"], "done": "remote seat routing, reply delivery, webhook wiring and SSRF/HTTPS policy tests pass."},
    ],
    "risks": [
        {"risk": "webhook-enabled delivery has no pull fallback", "impact": "receiver outage beyond retries loses delivery unless dead-letter is replayed", "mitigation": "persistent outbox, bounded retries, dead-letter retention, admin replay, alerting; disabled webhook has no outbound delivery to lose."},
        {"risk": "at-least-once retries can duplicate a webhook", "impact": "receiver processes the same letter twice", "mitigation": "stable delivery_id, receiver deduplication, persisted attempt state."},
        {"risk": "webhook URL is an SSRF or data-exfiltration path", "impact": "internal service access or letter text exfiltration", "mitigation": "HTTPS, private-IP block, no redirects, allowlist/opt-in, HMAC, no body logs."},
        {"risk": "per-seat ordering breaks under concurrent retries", "impact": "replies arrive out of order", "mitigation": "single FIFO worker per seat, retry head-of-line, bounded concurrency across seats."},
        {"risk": "plaintext token or webhook secret in configuration.json", "impact": "config leak grants send or spoofing capability", "mitigation": "prefer token_sha256 and secret_env, 0600 config, rotation, no logging."},
        {"risk": "v1 has no pull, so an offline harness cannot catch up", "impact": "operator may believe delivery is durable when receiver is permanently gone", "mitigation": "document push-only, dead-letter retention, admin replay; add pull only as a later approved feature."},
    ],
    "open_questions": [
        "Should webhook be one endpoint per mcp_http seat, or a list of endpoints with per-event filters? v1 one URL; flexible config can add a list later.",
        "What is the dead-letter replay interface: local CLI only, or a protected admin HTTP route?",
        "Should webhook payload include letter text by default, or only metadata plus a separate encrypted body?",
        "Should a 2xx with an empty/invalid ack body count as delivered, or require a signed receiver ack?",
        "Should retries preserve strict per-seat order at the cost of head-of-line blocking, or allow parallel delivery with sequence numbers?",
        "Which protocol versions are mandatory in the first release: 2025-06-18 only, or the 2025-03-26/2025-06-18/2025-11-25 set?",
        "Should v1 add idempotency for message_queen, or explicitly document duplicate risk on retry?",
    ],
    "source_refs": [
        {"label": "MCP Streamable HTTP transport", "url": "https://modelcontextprotocol.io/specification/2025-06-18/basic/transports", "use": "POST/GET/DELETE, session id, SSE, protocol version"},
        {"label": "MCP authorization", "url": "https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization", "use": "bearer token and protected-resource guidance"},
        {"label": "Current stdio adapter", "url": "https://github.com/shark2202/go_hive/blob/main/go_hive/go-hive/internal/queenmail/mcp_stdio.go", "use": "existing JSON-RPC surface, environment identity and timeout"},
        {"label": "Current qmail routes", "url": "https://github.com/shark2202/go_hive/blob/main/go_hive/go-hive/internal/server/routes_queens_mail.go", "use": "queens/letters REST contract and token header"},
        {"label": "Queen mail design notes", "url": "https://github.com/shark2202/go_hive/blob/main/go_hive/docs/queen-mail.md", "use": "local/Claude Code delivery, audit and known differences"},
    ],
}


def render_html(data: dict) -> str:
    def lis(items):
        return "".join(f"<li>{ESC(x)}</li>" for x in items)

    req_rows = "".join(
        f"<tr><td><code>{ESC(r['id'])}</code></td><td>{ESC(r['priority'])}</td>"
        f"<td>{ESC(r['statement'])}</td><td>{ESC(r['acceptance'])}</td><td>{ESC(', '.join(r['traces']))}</td></tr>"
        for r in data["requirements"]
    )
    opt_rows = "".join(
        f"<tr><td><code>{ESC(o['id'])}</code></td><td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] in {'deferred', 'separate'} else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td></tr>"
        for o in data["options"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(k)}</div><h3>{ESC(v['title'])}</h3><p>{ESC(v['text'])}</p></div>"
        for k, v in data["design"].items()
    )
    tool_rows = "".join(
        f"<tr><td><code>{ESC(t['name'])}</code></td><td>{ESC(t['kind'])}</td><td>{ESC(t['surface'])}</td><td>{ESC(t['auth'])}</td><td>{ESC(t['approval'])}</td></tr>"
        for t in data["tool_surface"]
    )
    identity_rows = "".join(
        f"<tr><td><code>{ESC(k)}</code></td><td>{ESC(v)}</td></tr>" for k, v in data["identity_contract"].items()
    )
    config_rows = "".join(
        f"<tr><td><code>{ESC(row['level'])}</code></td><td><code>{ESC(row['path'])}</code></td><td>{ESC(row['own'])}</td></tr>"
        for row in data["config_contract"]["files"]
    )
    field_rows = "".join(
        f"<tr><td><code>{ESC(f['name'])}</code></td><td>{ESC(f['required'])}</td><td>{ESC(f['meaning'])}</td></tr>"
        for f in data["config_contract"]["fields"]
    )
    config_text = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    enabled_config_text = json.dumps(data["config_contract"]["enabled_sample"], ensure_ascii=False, indent=2)
    init_text = json.dumps(data["transport_contract"]["initialize_example"], ensure_ascii=False, indent=2)
    call_text = json.dumps(data["transport_contract"]["tool_call_example"], ensure_ascii=False, indent=2)
    webhook_payload_text = json.dumps(data["webhook_contract"]["payload_example"], ensure_ascii=False, indent=2)
    webhook_retry_text = json.dumps(data["webhook_contract"]["retry"], ensure_ascii=False, indent=2)
    webhook_header_items = lis(data["webhook_contract"]["headers"])
    webhook_security_items = lis(data["webhook_contract"]["security"])
    failure_rows = "".join(
        f"<tr><td>{ESC(f['case'])}</td><td>{ESC(f['result'])}</td></tr>" for f in data["failure_modes"]
    )
    limit_items = "".join(f"<li><code>{ESC(k)}</code>: {ESC(v)}</li>" for k, v in data["limits"].items())
    acc_rows = "".join(
        f"<tr><td><code>{ESC(a['id'])}</code></td><td>{ESC(a['test'])}</td><td>{ESC(a['pass'])}</td></tr>" for a in data["acceptance"]
    )
    review_rows = "".join(
        f"<tr><td>{ESC(r['input'])}</td><td>{ESC(r['expected'])}</td><td><code>{ESC(r['owner'])}</code></td></tr>"
        for r in data["review_focus"]
    )
    task_rows = "".join(
        f"<tr><td><code>{ESC(t['id'])}</code></td><td>{ESC(', '.join(t['depends_on']) or '—')}</td>"
        f"<td><code>{ESC(', '.join(t['writes']) or '—')}</code></td><td>{ESC(t['done'])}</td></tr>"
        for t in data["tasks"]
    )
    risk_rows = "".join(
        f"<tr><td>{ESC(r['risk'])}</td><td>{ESC(r['impact'])}</td><td>{ESC(r['mitigation'])}</td></tr>"
        for r in data["risks"]
    )
    source_items = "".join(
        f"<li><a href=\"{ESC(s['url'])}\">{ESC(s['label'])}</a> — {ESC(s['use'])}</li>" for s in data["source_refs"]
    )
    meta = (
        f"<span>状态：<b>{ESC(data['status'])}</b></span>"
        f"<span>需求：<b>{len(data['requirements'])}</b></span>"
        f"<span>设计项：<b>{len(data['design'])}</b></span>"
        f"<span>验收：<b>{len(data['acceptance'])}</b></span>"
        f"<span>工具：<b>{len(data['tool_surface'])}</b></span>"
    )
    versions = " / ".join(data["protocol"]["versions"])
    stdio_versions = " / ".join(data["protocol"]["stdio_versions"])
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>qmail-mcp-http · 需求—设计链</title>
<style>
:root{{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}}
*{{box-sizing:border-box}} body{{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}}
a{{color:var(--accent2);text-decoration:none}} a:hover{{text-decoration:underline}}
.wrap{{max-width:1380px;margin:0 auto;padding:44px 26px 80px}}
header{{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}}
.eyebrow{{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}}
h1{{font-size:30px;line-height:1.25;margin:10px 0;letter-spacing:-.02em}} h1 span{{color:var(--accent)}}
.lede{{color:var(--muted);max-width:1120px;font-size:15px}} .meta{{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px}} .meta b{{color:var(--text)}}
section{{margin:38px 0}} h2{{font-size:22px;margin:0 0 8px}} h2 .num{{color:var(--accent);font-size:14px;margin-right:8px}} h3{{font-size:15.5px;margin:16px 0 8px;color:#cfdae6}}
p{{color:#c6d2df;margin:8px 0}} .sub{{color:var(--muted);font-size:12.5px}}
.grid{{display:grid;gap:12px}} .g2{{grid-template-columns:repeat(2,minmax(0,1fr))}} .g3{{grid-template-columns:repeat(3,minmax(0,1fr))}} .grid>*{{min-width:0}}
@media(max-width:1000px){{.g3{{grid-template-columns:repeat(2,minmax(0,1fr))}}}} @media(max-width:560px){{.g3,.g2{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal;margin:1px 2px 1px 0}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.muted{{color:var(--muted);border-color:var(--line);background:#0f1620}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}pre{{white-space:pre-wrap;overflow-wrap:anywhere}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">GO_HIVE · QUEEN MAIL · MCP HTTP · REQUIREMENTS → DESIGN</div>
<h1>qmail MCP HTTP <span>需求—设计链</span></h1>
<p class="lede">把 stdio 的两个 queen-mail MCP 工具通过一个可选 HTTP 端点暴露给外部 agent/harness。配置采用 queen_mail.mcp_http.&lt;id&gt;，URL 使用 /mcp/queen-mail/&lt;id&gt;/&lt;conversation_ref&gt;，令牌只放在 Authorization header。webhook 是可选的回信/新信推送：缺失或 webhook.enabled=false 时 seat 只做入站；启用后才发送 signed webhook。v1 不做 pull，也不增加额外 MCP 工具。</p>
<div class="meta">{meta}<span>协议：<b>{ESC(data['protocol']['transport'])}</b></span></div></header>

<section id="problem"><h2><span class="num">00</span>目标与非目标</h2>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul>
<h3>前提</h3><ul>{lis(data['assumptions'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">每条需求都落到设计项和验收项；R2/R3/R4 是配置、身份和工具面边界。</p>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:7%">优先级</th><th style="width:38%">需求</th><th style="width:34%">验收</th><th style="width:14%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div></section>

<section id="options"><h2><span class="num">02</span>方案决策</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:25%">方案</th><th style="width:9%">结论</th><th style="width:59%">理由</th></tr></thead><tbody>{opt_rows}</tbody></table></div>
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<h3>Package 与工具边界</h3>
<div class="callout info"><b>核心：</b><code>{ESC(data['package_layout']['core'])}</code> <b>Webhook：</b><code>{ESC(data['package_layout']['webhook'])}</code> <b>适配器：</b><code>{ESC(data['package_layout']['adapter'])}</code> <b>既有：</b><code>{ESC(data['package_layout']['existing'])}</code><br>{ESC(data['package_layout']['rule'])}<br><b>维护：</b>{ESC(data['package_layout']['maintenance'])}</div>
<div class="tablewrap"><table><thead><tr><th style="width:24%">工具</th><th style="width:20%">类型</th><th style="width:18%">暴露面</th><th style="width:22%">授权</th><th style="width:16%">审批</th></tr></thead><tbody>{tool_rows}</tbody></table></div>
<div class="callout bad"><b>工具面严格等于 stdio：</b>只有 <code>list_queens</code> 和 <code>message_queen</code>。不增加 <code>read_letters</code>、<code>reply_letter</code>、inbox 或 admin；本页不把远程端点接入本地默认工具目录。</div></section>

<section id="protocol"><h2><span class="num">04</span>MCP HTTP 协议</h2>
<div class="grid g3">
<div class="card"><div class="k">传输</div><h3>{ESC(data['protocol']['transport'])}</h3><p>端点 <code>{ESC(data['protocol']['endpoint'])}</code>；POST，GET 可选 SSE，DELETE 终止。</p></div>
<div class="card"><div class="k">版本</div><h3>{ESC(versions)}</h3><p>HTTP 端点拒绝 2024-11-05 的旧 HTTP+SSE；stdio 继续接受 <code>{ESC(stdio_versions)}</code>。</p></div>
<div class="card"><div class="k">认证</div><h3>{ESC(data['protocol']['auth_header'])}</h3><p>token 只在 header；path 只做路由，不能替代 token。</p></div>
</div>
<div class="callout info"><b>Legacy：</b>{ESC(data['protocol']['legacy'])} <b>会话：</b><code>{ESC(data['protocol']['session_header'])}</code>；后续请求带 <code>{ESC(data['protocol']['protocol_header'])}</code>。</div>
<h3>initialize</h3><pre>{ESC(init_text)}</pre>
<h3>tools/call</h3><pre>{ESC(call_text)}</pre>
<p><b>会话生命周期：</b>{ESC(' → '.join(data['transport_contract']['session']))}</p>
<p><b>响应：</b>{ESC('；'.join(data['transport_contract']['responses']))}</p></section>

<section id="webhook"><h2><span class="num">05</span>Webhook push（可选）</h2>
<div class="callout good"><b>默认关闭：</b>{ESC(data['webhook_contract']['optional'])} <br><b>关闭行为：</b>{ESC(data['webhook_contract']['disabled_behavior'])}</div>
<div class="grid g3">
<div class="card"><div class="k">默认值</div><h3>webhook.enabled = false</h3><p>缺失 webhook 对象与显式 false 等价；URL、secret、retry 均不是必填。</p></div>
<div class="card"><div class="k">启用后方向</div><h3>Hive queen → remote seat</h3><p>{ESC(data['webhook_contract']['direction'])} 新信和回信落到该 seat 的 outbox，不经过 MCP 工具返回。</p></div>
<div class="card"><div class="k">事件 / 签名</div><h3>{ESC(' / '.join(data['webhook_contract']['events']))}</h3><p>{ESC(data['webhook_contract']['signature'])}</p></div>
</div>
<h3>请求头（仅启用时）</h3><ul>{webhook_header_items}</ul>
<h3>payload 示例（仅启用时）</h3><pre>{ESC(webhook_payload_text)}</pre>
<h3>retry / ordering（仅启用时）</h3><pre>{ESC(webhook_retry_text)}</pre>
<h3>安全（仅启用时）</h3><ul>{webhook_security_items}</ul>
</section>

<section id="config"><h2><span class="num">06</span>配置与身份</h2>
<div class="callout info"><b>配置：</b><code>{ESC(data['config_contract']['config'])}</code> <b>状态：</b><span class="pill warn">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])}</div>
<div class="tablewrap"><table><thead><tr><th style="width:15%">层级</th><th style="width:45%">路径</th><th style="width:40%">负责内容</th></tr></thead><tbody>{config_rows}</tbody></table></div>
<h3>完整示例（webhook 关闭）</h3><pre>{ESC(config_text)}</pre>
<h3>启用 webhook 示例</h3><pre>{ESC(enabled_config_text)}</pre>
<h3>字段</h3><div class="tablewrap"><table><thead><tr><th style="width:24%">字段</th><th style="width:22%">必填</th><th style="width:54%">含义</th></tr></thead><tbody>{field_rows}</tbody></table></div>
<p><b>生效：</b>{ESC(data['config_contract']['reload'])}</p>
<h3>身份绑定</h3><div class="tablewrap"><table><thead><tr><th style="width:24%">字段</th><th style="width:76%">行为</th></tr></thead><tbody>{identity_rows}</tbody></table></div>
<div class="callout bad"><b>不是 participants.json：</b>每个 <code>mcp_http.&lt;id&gt;</code> 条目就是参与者记录；不创建 <code>participants.json</code>、inbox 或第二套令牌表。</div></section>

<section id="failure"><h2><span class="num">07</span>失败、限额与安全</h2>
<div class="tablewrap"><table><thead><tr><th style="width:42%">场景</th><th style="width:58%">结果</th></tr></thead><tbody>{failure_rows}</tbody></table></div>
<div class="grid g2"><div class="card"><div class="k">Limits</div><ul>{limit_items}</ul></div><div class="card"><div class="k">安全底线</div><ul><li>默认关闭；只有存在 enabled 的 <code>mcp_http</code> 条目才挂载。</li><li>token 只在 Authorization header；禁止 query/path token。</li><li>不记录 token、Authorization 或正文；path 只记录 id/ref 元数据。</li><li>Origin/Host 校验、请求体上限、无 CORS 通配。</li><li>远程使用必须经 TLS 反向代理；不复用 <code>HIVE_DESKTOP_TOKEN</code>。</li></ul></div></div></section>

<section id="acceptance"><h2><span class="num">08</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:8%">ID</th><th style="width:30%">测试</th><th style="width:62%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div>
<h3>Review Focus</h3>
<div class="tablewrap"><table><thead><tr><th style="width:35%">未覆盖输入 / 失败模式</th><th style="width:50%">期望</th><th style="width:15%">Owner</th></tr></thead><tbody>{review_rows}</tbody></table></div>
<div class="callout bad"><b>设计边界：</b>status 仍是 <code>design</code>。没有真实 HTTP E2E、stdio 回归和全量 CI 结果前，不得把本页当作实现或发布证据。</div></section>

<section id="tasks"><h2><span class="num">09</span>实施任务</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:10%">依赖</th><th style="width:42%">写入范围</th><th style="width:42%">完成检查</th></tr></thead><tbody>{task_rows}</tbody></table></div>
<div class="callout info"><b>先决条件：</b>先确认 <code>&lt;id&gt;</code> 是远端 queen 还是 participant 键、token/secret 存储方式；webhook 默认 false，只有决定启用时才需要拍板 URL/events/retry，并确认接受启用后的 push-only 无 pull。</div></section>

<section id="risks"><h2><span class="num">10</span>风险、待拍板与来源</h2>
<div class="tablewrap"><table><thead><tr><th style="width:28%">风险</th><th style="width:27%">影响</th><th style="width:45%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>qmail-mcp-http design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_qmail_mcp_http_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
</div></body></html>
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    json_text = json.dumps(DATA, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(DATA)
    if args.check:
        drift = []
        for path, expected in ((OUT_JSON, json_text), (OUT_HTML, html_text)):
            if not path.exists() or path.read_text(encoding="utf-8") != expected:
                drift.append(str(path))
        if drift:
            print("qmail-mcp-http-design: DRIFT: " + ", ".join(drift))
            return 1
        print(f"qmail-mcp-http-design: OK ({len(DATA['requirements'])} requirements, {len(DATA['acceptance'])} acceptance rows)")
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"qmail-mcp-http-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
