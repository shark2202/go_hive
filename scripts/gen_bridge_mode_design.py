#!/usr/bin/env python3
"""Generate the BRIDGE_MODE -> go_hive requirements/design chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/bridge-mode-design.html
  - go_hive/docs/bridge-mode-design.json

This is a design artifact for a post-1.0 optional adapter. It does not claim
that an internal/bridge package, a control-plane client, or any route exists at
HEAD.
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "bridge-mode-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "bridge-mode-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {
    "schema_version": 1,
    "feature": "bridge-mode",
    "status": "design",
    "decision": (
        "Implement BRIDGE_MODE as an optional outbound bridge adapter for "
        "go_hive. The local process registers an environment with a control "
        "plane, long-polls work, acknowledges and heartbeats a lease, maps the "
        "work to a real local Hive session, relays permission events, and "
        "archives the session. It is v2-style env-less, outbound-only, default "
        "off, and post-1.0. It is not a LAN peer transport and it does not "
        "claim compatibility with claude.ai or ByteBroad's proprietary bridge "
        "until their control-plane contract and OAuth access are provided."
    ),
    "goal_beyond_request": (
        "Turn a local go_hive installation into a remotely controlled executor "
        "without exposing an inbound port. The remote control plane chooses "
        "work; the local process retains ownership of session creation, tool "
        "execution, permission decisions and audit. The model never receives a "
        "raw remote prompt that bypasses the queen/colony runtime."
    ),
    "feasibility": {
        "verdict": "传输可移植；控制面兼容性有条件；产品语义不能照搬。",
        "why": (
            "Bridge Mode is an outbound long-poll worker protocol, so Go can "
            "implement the HTTP client, environment registration, lease, "
            "heartbeat, cancellation, session mapping and permission relay with "
            "the standard library. The hard dependency is the external control "
            "plane: its endpoints, OAuth provider, work result contract and "
            "trusted-device semantics are not part of go_hive. A local design "
            "can pin a product-owned contract, but cannot honestly claim "
            "claude.ai or ByteBroad compatibility without that server contract."
        ),
        "reusable_parts": [
            "HTTPS long-poll, bounded backoff and outbound-only networking are straightforward in Go.",
            "hive serve already exposes a real session API: POST /api/sessions, chat, events and DELETE.",
            "hive-cred and the credential store already provide a place for provider credentials; bridge must not invent a plaintext token file.",
            "Sentinel's outbound long-poll listeners are a useful lifecycle and backoff reference, not the bridge protocol itself.",
            "The event bus already carries ask-user and client-input events that can be relayed to a host approval surface.",
        ],
        "not_portable": [
            "The control-plane endpoints, OAuth refresh and trusted-device rules are external; they cannot be fabricated locally.",
            "A remote work id is not a Hive session id; a mapping and ownership check must be designed, not assumed.",
            "Bridge must not expose an inbound port or reuse HIVE_DESKTOP_TOKEN.",
            "Bridge is not LAN Pipes: it has no multicast discovery and no peer-to-peer prompt routing.",
            "The feature is outside upstream Hive 1.0 and outside the current alignment scope.",
        ],
    },
    "problem": {
        "statement": (
            "go_hive already has a local session API and a queen/colony runtime, "
            "but no outbound control-plane client. An operator who wants to use "
            "a web control plane must open an inbound port, copy a desktop token "
            "and manually manage sessions. Bridge Mode describes the missing "
            "pattern: register once, poll work behind NAT, run a real session, "
            "relay approval, and release the lease."
        ),
        "user_value": [
            "A local installation can be driven from a control plane without an inbound port.",
            "Remote work maps to a real Hive session, so queen/colony tools and audit still apply.",
            "Permission and ask-user prompts stay human-approved on the host side.",
            "A crashed or stopped worker does not silently duplicate work or leak a lease.",
            "Operators can see environment, work, session and last error in one status surface.",
        ],
        "why_not_now": (
            "This is an additive remote-control capability, not an upstream Hive "
            "feature. It must remain design-only until the control-plane owner, "
            "credential model, workspace isolation and permission relay are "
            "accepted. A complete-looking page is not implementation evidence."
        ),
    },
    "protocol": {
        "name": "BRIDGE_MODE",
        "provider": "external control plane (not owned by go_hive; contract unpinned)",
        "version": "v2 env-less preferred; v1 env-based only as a compatibility mode",
        "transport": "outbound HTTPS long-poll (documented 10s timeout); no inbound listener",
        "auth": "OAuth bearer for registration, environment secret for work, trusted-device token for v2",
        "scope": "one private workspace, one active work per environment in v1, post-1.0 optional",
        "result_contract": "not specified by the attached document; must be pinned before implementation",
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Keep BRIDGE_MODE opt-in, default off and explicitly post-1.0; no default tool, no always-on listener and no change to the 1.0 parity claim.",
            "acceptance": "With the feature absent/disabled, no bridge client, poller, environment credential, inbound socket or model tool is registered; existing local suites are unchanged.",
            "traces": ["D1", "D12", "A1", "A17"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "Use outbound HTTPS long-poll only. The local process opens no inbound port, does not reuse HIVE_DESKTOP_TOKEN and does not expose the desktop API to the control plane.",
            "acceptance": "A network probe sees only outbound connections; no bridge listener exists; a control-plane request carrying the desktop token is rejected.",
            "traces": ["D2", "D3", "D10", "A2", "A3", "A12"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Pin the control-plane contract before coding: endpoints, version, work payload, result/status channel, error taxonomy and OAuth provider. Unpinned provider behaviour is a blocking external dependency, not a guess.",
            "acceptance": "The design names a concrete control-plane owner/base URL and records the contract version; a fake control plane implements the same schema; an unknown/unsupported version fails closed.",
            "traces": ["D2", "D4", "D5", "A4", "A15"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Store environment secrets and OAuth refresh material in the credential store, never in argv, URLs, logs, plaintext config or the repository; use a dedicated bridge identity, not the desktop token.",
            "acceptance": "Secrets are redacted in logs and errors; config contains a handle/env name only; a missing/expired credential produces a typed error; no token appears in the URL.",
            "traces": ["D3", "D10", "A3", "A11", "A16"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Implement environment lifecycle: register, reuse by id, deregister, and run recovery. Re-registration must be idempotent and must not create a second environment after a crash.",
            "acceptance": "Register returns id/secret; reuse returns the same environment; deregister removes the lease; a crash/restart reuses the persisted id; duplicate registration does not create an orphan.",
            "traces": ["D4", "D8", "A4", "A7", "A14"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Implement work lifecycle with at-most-once side effects: poll, acknowledge, run, heartbeat, stop, archive. Redelivery is deduplicated by work id; ack happens only after local ownership is persisted.",
            "acceptance": "A redelivered work id reuses the existing local session; ack is durable; a duplicate poll does not start a second turn; a lost lease prevents further mutation.",
            "traces": ["D5", "D8", "D9", "A5", "A6", "A9"],
        },
        {
            "id": "R7",
            "priority": "P0",
            "statement": "Map remote work to a real local queen session through a persistent mapping. The remote control plane cannot choose an arbitrary local session, workspace or colony; workspace/profile selection is config-bound.",
            "acceptance": "A work creates or resumes one local session; the mapping survives restart; an unknown remote session id is a typed error; no raw prompt bypasses SessionManager.",
            "traces": ["D6", "D9", "A6", "A8", "A17"],
        },
        {
            "id": "R8",
            "priority": "P0",
            "statement": "Relay ask-user and tool permission requests to the control plane, but let only the host approve. A remote model cannot self-approve; approval after cancel, timeout, close or lease loss is invalid.",
            "acceptance": "Permission request arrives with work/session/request ids; host approval reaches the session; self-approval is impossible; late/duplicate approval is rejected; no approval UI is model-controlled.",
            "traces": ["D7", "D10", "A10", "A16"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "Define heartbeat and cancellation semantics: lease renewal, soft timeout, hard stop, process crash and control-plane disconnect. A stopped or expired work must not keep running behind the control plane's back.",
            "acceptance": "Heartbeat renews a live lease; stop cancels the local turn; lease expiry stops work; a crash resumes or reports a typed lost-lease error; no zombie session remains.",
            "traces": ["D8", "D9", "A7", "A9", "A14"],
        },
        {
            "id": "R10",
            "priority": "P1",
            "statement": "Provide a small operator surface: hive bridge status/register/deregister and GET /api/bridge/status. It is host-only and does not add a model-facing bridge tool.",
            "acceptance": "Status shows environment id, control-plane URL, lease, session id, last error and feature state; register/deregister work; the default tool catalogue is unchanged.",
            "traces": ["D11", "D12", "A1", "A12", "A17"],
        },
        {
            "id": "R11",
            "priority": "P1",
            "statement": "Keep audit and errors compatible with local sessions: environment/work/session ids, permission outcome, stop reason and typed failures are observable without logging tokens or full message bodies.",
            "acceptance": "Audit rows identify the bridge path and outcome; redaction tests pass; a failed work has one typed error and no partial session mutation.",
            "traces": ["D10", "D11", "A11", "A16"],
        },
        {
            "id": "R12",
            "priority": "P0",
            "statement": "Respect the package and tool boundary: internal/bridge is independent, a thin server adapter owns session wiring, and no raw protocol method or model-facing bridge tool is registered.",
            "acceptance": "internal/bridge does not import internal/server, internal/cli or internal/tools/builtin; the adapter is the only wiring point; the frozen default tool surface is unchanged.",
            "traces": ["D12", "A12", "A17"],
        },
        {
            "id": "R13",
            "priority": "P1",
            "statement": "Preserve existing local behaviour: no change to desktop auth, session API shapes, stdio channels, queen mail or colony semantics; bridge is additive and scope-checked.",
            "acceptance": "Existing API/session/channel/queen/colony suites pass; bridge-disabled behaviour is byte-for-byte unchanged; no new route appears without the feature flag.",
            "traces": ["D1", "D12", "A1", "A17"],
        },
        {
            "id": "R14",
            "priority": "P1",
            "statement": "Verify with a fake control plane and a real local session E2E; build with CGO_ENABLED=0; record a real control-plane run as an external gap if no credentials/contract exist.",
            "acceptance": "Fake control-plane tests cover register/poll/ack/heartbeat/stop/archive, crash, duplicate work, permission and redaction; a real session E2E runs; CGO=0 six-platform build passes; external gap is labelled.",
            "traces": ["D12", "A13", "A14", "A15", "A16"],
        },
    ],
    "non_goals": [
        "Do not copy claude.ai or ByteBroad's proprietary control plane or claim compatibility without its contract and OAuth access.",
        "Do not expose an inbound bridge port or reuse HIVE_DESKTOP_TOKEN as the bridge credential.",
        "Do not add a raw remote prompt path that bypasses SessionManager, queen/colony runtime, permissions or audit.",
        "Do not add LAN discovery, multicast, peer-to-peer control or multi-hop federation; that is the separate LAN Pipes design.",
        "Do not add a model-facing bridge_* tool or a new default tool surface.",
        "Do not add multi-tenant isolation, public internet exposure or a hosted SaaS control plane in v1.",
        "Do not make bridge mode a Hive 1.0 parity item or a commercial release gate without explicit approval.",
        "Do not log environment secrets, OAuth tokens, trusted-device tokens or full message bodies.",
    ],
    "assumptions": [
        "The control plane owner provides a pinned HTTP contract, OAuth provider and environment registration policy.",
        "The local process has a dedicated workspace and can run without a shared HIVE_HOME containing unrelated sessions.",
        "A remote work is represented by a stable work id and an explicit session reference; unknown types fail closed.",
        "The host UI or CLI is the permission authority; the remote model is never an approval authority.",
        "The control plane can tolerate a 10s long poll, bounded backoff and duplicate deliveries.",
        "A real control-plane run requires credentials not present in this repository and must be recorded separately.",
        "Bridge is single-hop; multi-hop and LAN P2P are separate designs.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "Outbound bridge adapter over the existing Hive session API",
            "verdict": "chosen",
            "reason": "Keeps session creation, queen/colony semantics, permissions and audit local; works behind NAT; maps cleanly to a v2-style environment and lease.",
        },
        {
            "id": "O2",
            "name": "Expose the existing hive serve API directly to the control plane",
            "verdict": "rejected",
            "reason": "Requires an inbound port and broad desktop-token access; the control plane could reach unrelated APIs. It also ignores environment registration, lease and permission relay.",
        },
        {
            "id": "O3",
            "name": "Reuse LAN Pipes multicast/TCP as the remote control transport",
            "verdict": "rejected",
            "reason": "Bridge is a control-plane-to-executor direction, not peer discovery. LAN Pipes has no environment registration, lease or remote approval model.",
        },
        {
            "id": "O4",
            "name": "SSH reverse tunnel or static reverse proxy to hive serve",
            "verdict": "deferred",
            "reason": "Useful as an emergency deployment, but it provides no environment lifecycle, work queue or remote approval UX and still exposes a broad local API.",
        },
        {
            "id": "O5",
            "name": "Direct claude.ai / ByteBroad bridge compatibility",
            "verdict": "rejected for v1",
            "reason": "The endpoints, OAuth, trusted-device and result contract are external and unpinned; claiming compatibility without them would be fabricated.",
        },
        {
            "id": "O6",
            "name": "In-process delegate_task only",
            "verdict": "rejected",
            "reason": "It solves local fan-out, not remote control behind NAT. It has no environment, lease, cancellation or control-plane permission semantics.",
        },
    ],
    "design": {
        "D1": {
            "title": "Opt-in scope and deployment",
            "text": "Bridge routes and the poller mount only when bridge.enabled is true. Default serve remains loopback; the bridge client is outbound and opens no inbound port. The feature is post-1.0, not part of the frozen route or tool surface, and can be killed without affecting local sessions.",
        },
        "D2": {
            "title": "Control-plane client contract",
            "text": "A single client owns the pinned base URL, API version, OAuth registration, work poll, ack, heartbeat, stop, deregister, archive, permission event and reconnect. It validates server ids with a small allowlist, rejects path traversal, uses bounded timeouts and never reads a per-request URL from work data.",
        },
        "D3": {
            "title": "Identity and credentials",
            "text": "Environment id and secret are issued by registration; OAuth and trusted-device material live in the credential store. Config names a credential handle or env var, never a literal secret. The desktop token is a different principal and is rejected. Logs redact headers, tokens and message bodies.",
        },
        "D4": {
            "title": "Environment lifecycle",
            "text": "Register is idempotent by reuse id; the id/secret pair is persisted atomically before polling. Deregister revokes the lease and stops polling. A restart reuses the persisted environment and does not create an orphan. A failed deregister is reported, not silently ignored.",
        },
        "D5": {
            "title": "Work lifecycle and idempotency",
            "text": "Poll returns one work; the client validates type and required ids, persists a work-to-session mapping, then acks. A redelivered work id reuses that mapping and never starts a second turn. Unknown work types, mismatched environment ids and malformed payloads fail closed before ack.",
        },
        "D6": {
            "title": "Session mapping and ownership",
            "text": "A remote work maps to a local queen session created through the existing SessionManager. The mapping stores environment id, work id, local session id and workspace/profile; it never accepts an arbitrary local session id from the control plane. Colony spawning is allowed through the session's normal runtime and locks the session as usual.",
        },
        "D7": {
            "title": "Permission relay",
            "text": "ask-user and tool-permission events are published to a host approval surface and forwarded as a control-plane event with work, session and request ids. Only a host decision is accepted; request ids expire on cancel, timeout, close or lease loss. A remote model cannot approve its own request.",
        },
        "D8": {
            "title": "Lease, heartbeat and cancellation",
            "text": "A live work renews its lease with heartbeat. Soft timeout asks the session to stop; hard timeout and lease loss cancel the turn. Stop is idempotent and late heartbeats cannot resurrect a lost lease. Process crash recovery either resumes the same session or reports a typed lost-lease error.",
        },
        "D9": {
            "title": "Events, results and archive",
            "text": "Session events are bounded and ordered; the control-plane result channel is pinned before implementation. Archive is idempotent (409 = already archived) and does not delete local audit. An event gap is reported, never silently reset. Local session history remains the durable source.",
        },
        "D10": {
            "title": "Security and isolation",
            "text": "Outbound HTTPS only; control-plane host allowlist; dedicated workspace; no shared HIVE_HOME; no desktop API exposure; no auto-approval; no shell interpolation from remote text; body/event caps; per-environment rate limits; kill switch. The bridge is a remote code-execution surface and is treated as such.",
        },
        "D11": {
            "title": "Operator surface and audit",
            "text": "hive bridge status/register/deregister and GET /api/bridge/status expose enabled, environment id, control-plane URL, lease, work, local session, last error and stop reason. Audit records environment/work/session and permission outcome; tokens and message bodies are redacted.",
        },
        "D12": {
            "title": "Package, tool and dependency boundary",
            "text": "internal/bridge owns transport, protocol, store, lease, runner and policy; internal/server/bridge_adapter.go is the only SessionManager wiring; internal/cli/bridge.go is the operator entry. internal/bridge must not import internal/server, internal/cli or internal/tools/builtin. No model-facing bridge tool is registered; no bundled peer runtime; CGO_ENABLED=0.",
        },
    },
    "config_contract": {
        "status": "proposed / absent at HEAD",
        "config": "bridge",
        "authority": "New optional bridge namespace; do not change desktop auth, channels or queen_mail.",
        "fields": [
            {"name": "bridge.enabled", "required": "no", "meaning": "default false; true starts the outbound client and operator routes"},
            {"name": "bridge.control_plane_url", "required": "yes when enabled", "meaning": "HTTPS base URL; host must be allowlisted"},
            {"name": "bridge.api_version", "required": "yes when enabled", "meaning": "pinned control-plane contract version"},
            {"name": "bridge.environment.reuse_id", "required": "no", "meaning": "stable local id for idempotent registration"},
            {"name": "bridge.oauth.credential_handle", "required": "yes when enabled", "meaning": "credential-store handle; never a literal token"},
            {"name": "bridge.trusted_device.credential_handle", "required": "no", "meaning": "v2 trusted-device handle; absence is allowed only when the control plane does not require it"},
            {"name": "bridge.workspace_root", "required": "yes when enabled", "meaning": "dedicated absolute workspace; no shared HIVE_HOME"},
            {"name": "bridge.queen_profile", "required": "no", "meaning": "profile used for remote sessions; must be explicit if multiple exist"},
            {"name": "bridge.poll_timeout_seconds", "required": "no", "meaning": "default 10; bounded long poll"},
            {"name": "bridge.heartbeat_seconds", "required": "no", "meaning": "lease renewal interval; must be below the server lease"},
            {"name": "bridge.max_sessions", "required": "no", "meaning": "v1 default 1; bounded local concurrency"},
            {"name": "bridge.permission_mode", "required": "no", "meaning": "host_approval default; deny_all is the fail-closed alternative"},
            {"name": "bridge.allowed_control_hosts", "required": "no", "meaning": "explicit host allowlist; empty means the configured URL host only"},
        ],
        "sample": {
            "bridge": {
                "enabled": True,
                "control_plane_url": "https://bridge.example.com",
                "api_version": "v2",
                "environment": {"reuse_id": "hive-office-a"},
                "oauth": {"credential_handle": "bridge/oauth/office-a"},
                "trusted_device": {"credential_handle": "bridge/device/office-a"},
                "workspace_root": "/Users/operator/.hive-bridge",
                "queen_profile": "bridge-worker",
                "poll_timeout_seconds": 10,
                "heartbeat_seconds": 30,
                "max_sessions": 1,
                "permission_mode": "host_approval",
                "allowed_control_hosts": ["bridge.example.com"],
            }
        },
        "reload": "Read at serve startup; credential rotation may be reloaded explicitly, but URL/workspace changes require a restart in v1.",
    },
    "protocol_contract": {
        "sequence": [
            "register environment with OAuth bearer",
            "persist environment id/secret; start outbound poll",
            "poll work; validate environment id and work type",
            "persist work-to-session mapping; ack work",
            "create or resume the local Hive session; run the turn",
            "heartbeat the lease while running",
            "relay ask-user/tool permission event; host approves or denies",
            "post result/status through the pinned control-plane channel",
            "stop on cancel/lease loss; archive; deregister on shutdown",
        ],
        "operations": [
            "POST /v1/environments/bridge",
            "GET /v1/environments/{id}/work/poll",
            "POST /v1/environments/{id}/work/{workId}/ack",
            "POST /v1/environments/{id}/work/{workId}/stop",
            "POST /v1/environments/{id}/work/{workId}/heartbeat",
            "DELETE /v1/environments/bridge/{id}",
            "POST /v1/sessions/{id}/archive",
            "POST /v1/sessions/{id}/events",
            "POST /v1/environments/{id}/bridge/reconnect",
        ],
        "work_payload": {
            "id": "stable work id",
            "type": "session (v1 only)",
            "session_id": "remote session reference; mapped locally, never trusted as a local id",
            "workspace": "optional; must match the configured workspace policy",
            "expires_at": "lease deadline; missing means fail closed",
        },
        "result_gap": "The attached document does not define a result upload endpoint. v1 must pin one (event/result/status) or run without claiming remote result delivery.",
        "rule": "No raw prompt; no arbitrary local route; no per-request control-plane URL; no model self-approval.",
    },
    "failure_modes": [
        {"case": "feature disabled", "result": "no outbound client, no environment credential, no bridge route or tool"},
        {"case": "missing or expired OAuth credential", "result": "typed auth error; no session created; no retry loop"},
        {"case": "control plane returns 401", "result": "refresh OAuth once; if still 401, fatal and stop polling"},
        {"case": "control plane returns 403/404/410", "result": "BridgeFatalError; no retry storm; operator sees a typed error"},
        {"case": "redelivered work id", "result": "reuse existing mapping; no second turn; ack remains idempotent"},
        {"case": "ack lost after local session created", "result": "redelivery resumes the same session; no duplicate side effects"},
        {"case": "heartbeat lease lost", "result": "cancel local turn; no late mutation; report lost lease"},
        {"case": "permission request times out", "result": "deny/fail closed; late approval rejected"},
        {"case": "control-plane result channel unpinned", "result": "block implementation; do not invent a result endpoint"},
        {"case": "process crashes mid-work", "result": "reuse persisted mapping; resume or typed lost-lease error; archive only after reconciliation"},
    ],
    "limits": {
        "max_sessions": "v1 default 1; hard cap configurable, never unbounded",
        "poll_timeout": "10s documented; client timeout includes margin",
        "heartbeat": "must be strictly below the control-plane lease",
        "work_payload": "bounded JSON; unknown fields preserved only where the contract says so",
        "event_queue": "bounded; explicit gap/error, never silent loss",
        "request": "bounded body; path/id allowlist; no control-plane URL from work data",
        "archive": "409 is idempotent success; local audit is not deleted",
        "retry": "bounded backoff; 401 refresh once; 403/404/410 fatal; no infinite loop",
    },
    "acceptance": [
        {"id": "A1", "test": "Feature gate", "pass": "disabled/absent config starts no poller, opens no bridge route and registers no tool; local suite unchanged."},
        {"id": "A2", "test": "Outbound only", "pass": "network probe shows no inbound bridge listener; desktop token cannot authorize bridge work."},
        {"id": "A3", "test": "Token redaction", "pass": "environment secret, OAuth token and trusted-device token absent from logs/errors/URLs/config samples."},
        {"id": "A4", "test": "Environment lifecycle", "pass": "register/reuse/deregister work; duplicate registration is idempotent; restart reuses the same environment."},
        {"id": "A5", "test": "Work idempotency", "pass": "redelivered work id resumes one session; duplicate ack/poll does not start a second turn."},
        {"id": "A6", "test": "Session mapping", "pass": "remote work creates or resumes one local queen session; unknown/mismatched ids fail closed."},
        {"id": "A7", "test": "Heartbeat/stop", "pass": "heartbeat renews; stop cancels; lease expiry stops the turn; late heartbeat cannot resurrect."},
        {"id": "A8", "test": "No raw prompt", "pass": "bridge cannot submit a bare prompt or arbitrary local session id; all execution goes through SessionManager."},
        {"id": "A9", "test": "Crash recovery", "pass": "process crash resumes the mapped session or reports a typed lost-lease error; no zombie work."},
        {"id": "A10", "test": "Permission relay", "pass": "request reaches host; host decision returns; remote self-approval and late approval fail closed."},
        {"id": "A11", "test": "Audit", "pass": "environment/work/session/permission/stop outcomes are observable; bodies and tokens are redacted."},
        {"id": "A12", "test": "Tool/catalogue boundary", "pass": "no model-facing bridge tool; frozen default tool surface unchanged; adapter is the only wiring point."},
        {"id": "A13", "test": "Fake control plane E2E", "pass": "register, poll, ack, session, heartbeat, stop, permission, archive and deregister pass against a fake contract."},
        {"id": "A14", "test": "Real local session E2E", "pass": "a real hive serve session is created and driven end-to-end; no fake session runtime."},
        {"id": "A15", "test": "Real control plane", "pass": "run only with provided credentials/contract; otherwise recorded as an external evidence gap, not PASS."},
        {"id": "A16", "test": "Security negative cases", "pass": "401 refresh once, 403/404/410 fatal, path traversal, wrong environment, duplicate work and late approval all fail safely."},
        {"id": "A17", "test": "Regression and build", "pass": "existing API/session/channel/queen/colony tests pass; CGO_ENABLED=0 six-platform build; design --check passes."},
    ],
    "review_focus": [
        {"input": "ack lost, control plane redelivers work", "expected": "same local session resumes; no second turn or duplicate file write", "owner": "T5"},
        {"input": "remote model approves its own permission request", "expected": "rejected; only host approval is accepted", "owner": "T6"},
        {"input": "OAuth 401 loop or 410 gone", "expected": "one refresh only; fatal error; polling stops cleanly", "owner": "T3"},
        {"input": "process crashes after ack but before session completes", "expected": "resume the mapped session or report lost lease; no orphaned work", "owner": "T7"},
        {"input": "control-plane result contract is missing", "expected": "implementation blocked at contract decision; no invented result endpoint", "owner": "T1"},
    ],
    "tasks": [
        {"id": "T1", "depends_on": [], "writes": ["internal/bridge/config.go", "internal/bridge/config_test.go"], "done": "feature gate, control-plane URL/version, workspace, profile, limits and credential handles validate; disabled starts no client."},
        {"id": "T2", "depends_on": ["T1"], "writes": ["internal/bridge/client.go", "internal/bridge/client_test.go"], "done": "pinned HTTP client implements register/poll/ack/stop/heartbeat/deregister/archive/permission/reconnect with allowlist, timeouts and typed errors."},
        {"id": "T3", "depends_on": ["T1"], "writes": ["internal/bridge/auth.go", "internal/bridge/auth_test.go"], "done": "OAuth credential lookup, one-shot 401 refresh, trusted-device header, redaction and fatal-status handling pass."},
        {"id": "T4", "depends_on": ["T2"], "writes": ["internal/bridge/environment.go", "internal/bridge/environment_test.go"], "done": "environment register/reuse/deregister and restart recovery are idempotent."},
        {"id": "T5", "depends_on": ["T2", "T4"], "writes": ["internal/bridge/work.go", "internal/bridge/work_test.go"], "done": "work validation, work-to-session mapping, ack idempotency and redelivery dedupe pass."},
        {"id": "T6", "depends_on": ["T5"], "writes": ["internal/bridge/permission.go", "internal/bridge/permission_test.go"], "done": "ask-user/tool permission relay is host-approved; self-approval, late approval and duplicate approval fail closed."},
        {"id": "T7", "depends_on": ["T5", "T6"], "writes": ["internal/bridge/lease.go", "internal/bridge/lease_test.go"], "done": "heartbeat, soft/hard timeout, stop, lease loss and crash recovery are deterministic."},
        {"id": "T8", "depends_on": ["T5"], "writes": ["internal/server/bridge_adapter.go", "internal/server/bridge_adapter_test.go", "internal/cli/bridge.go"], "done": "thin SessionManager adapter, hive bridge status/register/deregister and GET /api/bridge/status work; no model tool added."},
        {"id": "T9", "depends_on": ["T3", "T7", "T8"], "writes": ["internal/bridge/e2e_test.go", "docs/bridge-mode-e2e.md"], "done": "fake control-plane E2E and real local session E2E pass; real control-plane gap recorded; build and regression gates pass."},
    ],
    "risks": [
        {"risk": "Control plane is proprietary/unavailable", "impact": "cannot honestly implement or verify compatibility", "mitigation": "pin a product-owned contract or stop; record external gap; no fabricated endpoints."},
        {"risk": "Remote code execution surface", "impact": "compromised control plane can drive tools", "mitigation": "default off, dedicated workspace, host approval, least privilege, allowlists, kill switch, audit."},
        {"risk": "Credential leakage", "impact": "environment secret/OAuth theft", "mitigation": "credential store, env handles, redaction, no URL tokens, no repo secrets, rotation."},
        {"risk": "Duplicate work after lost ack", "impact": "double writes or duplicate messages", "mitigation": "work-id idempotency, mapping before ack, no second turn on redelivery."},
        {"risk": "Permission race", "impact": "late approval changes a completed action", "mitigation": "request ids, expiry, cancel/close invalidation, host-only decision."},
        {"risk": "Session mapping collision", "impact": "remote work lands in the wrong local session", "mitigation": "environment-scoped mapping, random local ids, ownership checks, typed mismatch."},
        {"risk": "Long-poll disconnect / retry storm", "impact": "load and missed work", "mitigation": "bounded backoff, one refresh, jitter, max poll concurrency, health status."},
        {"risk": "Scope creep into 1.0", "impact": "unverified feature surface and parity dilution", "mitigation": "design status, post-1.0 gate, no default tool/route, separate acceptance."},
    ],
    "open_questions": [
        "Who owns the control plane, and what is the exact pinned API/OAuth/result contract? Without this, implementation is blocked.",
        "Should v1 support only one active work per environment, or a bounded pool? The proposal is one.",
        "Should the remote work map to a queen session or a colony session? The proposal creates a queen session and lets normal colony semantics apply.",
        "What is the result/status channel? The attached document names nine operations but not a result upload; this must be pinned.",
        "Should trusted-device v2 be required, or is environment-secret + TLS sufficient for v1? The proposal prefers v2 when available.",
        "Should bridge mode be in the 1.0 release at all? The proposal keeps it post-1.0 and out of parity.",
        "What is the workspace isolation requirement: dedicated OS user, container, or same-user directory? The proposal requires a dedicated workspace and records OS isolation as an open decision.",
    ],
    "source_refs": [
        {"label": "BRIDGE_MODE feature guide", "path": "codes/bytecoder/docs/features/bridge-mode.md", "use": "v1/v2, 9 operations, OAuth, long poll, trusted device, permissions, archive, module layout"},
        {"label": "session routes", "path": "go_hive/go-hive/internal/server/routes_sessions.go", "use": "POST /api/sessions, GET /api/sessions, DELETE, session lifecycle"},
        {"label": "chat and events routes", "path": "go_hive/go-hive/internal/server/chat_routes.go", "use": "POST /api/sessions/{id}/chat and GET /api/sessions/{id}/events"},
        {"label": "desktop auth", "path": "go_hive/go-hive/internal/server/app.go", "use": "desktopAuth, X-Hive-Token; must not be reused as bridge auth"},
        {"label": "serve listener", "path": "go_hive/go-hive/internal/cli/loader.go", "use": "listener and non-loopback token refusal"},
        {"label": "outbound listener pattern", "path": "go_hive/go-hive/internal/sentinel/listeners.go", "use": "long-poll, backoff, outbound-only lifecycle reference"},
        {"label": "credential store", "path": "go_hive/go-hive/internal/credentials", "use": "credential handles and redaction; no plaintext bridge secrets"},
        {"label": "worker isolation", "path": "go_hive/go-hive/internal/colony/worker_definition.go", "use": "worker is not a peer message channel; remote work must not bypass session runtime"},
        {"label": "queen mail", "path": "go_hive/go-hive/internal/queenmail", "use": "asynchronous mail, not a control-plane work queue"},
    ],
}


def render_html(data: dict) -> str:
    def lis(items):
        return "".join(f"<li>{ESC(x)}</li>" for x in items)

    req_rows = "".join(
        f"<tr><td><code>{ESC(r['id'])}</code></td><td>{ESC(r['priority'])}</td><td>{ESC(r['statement'])}</td>"
        f"<td>{ESC(r['acceptance'])}</td><td>{ESC(', '.join(r['traces']))}</td></tr>"
        for r in data["requirements"]
    )
    opt_rows = "".join(
        f"<tr><td><code>{ESC(o['id'])}</code></td><td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] in {'deferred', 'rejected for v1'} else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td></tr>"
        for o in data["options"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(k)}</div><h3>{ESC(v['title'])}</h3><p>{ESC(v['text'])}</p></div>"
        for k, v in data["design"].items()
    )
    field_rows = "".join(
        f"<tr><td><code>{ESC(f['name'])}</code></td><td>{ESC(f['required'])}</td><td>{ESC(f['meaning'])}</td></tr>"
        for f in data["config_contract"]["fields"]
    )
    config_text = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    protocol_items = lis(data["protocol_contract"]["sequence"])
    operation_items = lis(data["protocol_contract"]["operations"])
    work_text = json.dumps(data["protocol_contract"]["work_payload"], ensure_ascii=False, indent=2)
    failure_rows = "".join(
        f"<tr><td>{ESC(f['case'])}</td><td>{ESC(f['result'])}</td></tr>" for f in data["failure_modes"]
    )
    limit_items = "".join(f"<li><code>{ESC(k)}</code>: {ESC(v)}</li>" for k, v in data["limits"].items())
    acc_rows = "".join(
        f"<tr><td><code>{ESC(a['id'])}</code></td><td>{ESC(a['test'])}</td><td>{ESC(a['pass'])}</td></tr>"
        for a in data["acceptance"]
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
        f"<li><code>{ESC(s['path'])}</code> — {ESC(s['use'])}</li>" for s in data["source_refs"]
    )
    meta = (
        f"<span>状态：<b>{ESC(data['status'])}</b></span>"
        f"<span>需求：<b>{len(data['requirements'])}</b></span>"
        f"<span>设计项：<b>{len(data['design'])}</b></span>"
        f"<span>验收：<b>{len(data['acceptance'])}</b></span>"
        f"<span>范围：<b>{ESC(data['protocol']['scope'])}</b></span>"
    )
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>bridge-mode · go_hive 远程控制面适配器 需求—设计链</title>
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
<header><div class="eyebrow">GO_HIVE · BRIDGE MODE · POST-1.0 REQUIREMENTS → DESIGN</div>
<h1>BRIDGE_MODE → go_hive <span>远程控制面适配器 需求—设计链</span></h1>
<p class="lede">把“本地 CLI 注册为远程执行器”的 Bridge 模式移植为 go_hive 的可选出站适配器：注册环境、长轮询取任务、ack/心跳/停止、映射真实 queen session、权限回到宿主审批。默认关闭，出站 only，post-1.0；不声称兼容 claude.ai / ByteBroad，直到控制面合同和 OAuth 访问被确认。</p>
<div class="meta">{meta}</div></header>

<section id="feasibility"><h2><span class="num">00</span>可行性判断</h2>
<div class="callout bad"><b>结论：</b>{ESC(data['feasibility']['verdict'])}<br><b>原因：</b>{ESC(data['feasibility']['why'])}</div>
<div class="grid g2"><div class="card"><div class="k">可复用</div><ul>{lis(data['feasibility']['reusable_parts'])}</ul></div><div class="card"><div class="k">不能直接搬</div><ul>{lis(data['feasibility']['not_portable'])}</ul></div></div>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul><h3>前提</h3><ul>{lis(data['assumptions'])}</ul></section>

<section id="protocol"><h2><span class="num">01</span>协议钉住点</h2>
<div class="grid g3">
<div class="card"><div class="k">Provider</div><h3>{ESC(data['protocol']['provider'])}</h3><p>控制面是外部依赖，不是 go_hive 自带服务。</p></div>
<div class="card"><div class="k">Transport</div><h3>{ESC(data['protocol']['transport'])}</h3><p>出站 HTTPS 长轮询；不开入站端口。</p></div>
<div class="card"><div class="k">Version</div><h3>{ESC(data['protocol']['version'])}</h3><p>v2 优先；v1 仅兼容模式。</p></div>
</div>
<div class="callout bad"><b>结果通道未定义：</b>{ESC(data['protocol']['result_contract'])}。这是实现的阻塞项，不能用猜测补端点。</div>
<h3>操作序列</h3><ol>{protocol_items}</ol><h3>控制面操作</h3><ul>{operation_items}</ul>
<h3>Work 载荷</h3><pre>{ESC(work_text)}</pre>
<div class="callout info"><b>规则：</b>{ESC(data['protocol_contract']['rule'])}</div></section>

<section id="requirements"><h2><span class="num">02</span>需求链</h2>
<p class="sub">R2/R12 是入站面与包边界；R3 是外部契约阻塞；R4 是凭据边界；R6/R9 是租约与幂等；R8 是权限边界；R1/R13 是 1.0 回归边界。</p>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:6%">优先级</th><th style="width:38%">需求</th><th style="width:34%">验收</th><th style="width:16%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div></section>

<section id="options"><h2><span class="num">03</span>方案决策</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:30%">方案</th><th style="width:13%">结论</th><th style="width:51%">理由</th></tr></thead><tbody>{opt_rows}</tbody></table></div>
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="design"><h2><span class="num">04</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>包边界：</b>internal/bridge 负责 transport/protocol/store/lease/runner/policy；internal/server/bridge_adapter.go 只做 SessionManager 接线；internal/cli/bridge.go 只做 operator entry。核心不得 import internal/server、internal/cli 或 internal/tools/builtin；不注册 model-facing bridge 工具。</div></section>

<section id="config"><h2><span class="num">05</span>配置、身份与权限</h2>
<div class="callout info"><b>配置：</b><code>{ESC(data['config_contract']['config'])}</code> <span class="pill warn">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])}</div>
<h3>示例</h3><pre>{ESC(config_text)}</pre>
<h3>字段</h3><div class="tablewrap"><table><thead><tr><th style="width:30%">字段</th><th style="width:19%">必填</th><th style="width:51%">含义</th></tr></thead><tbody>{field_rows}</tbody></table></div>
<p><b>生效：</b>{ESC(data['config_contract']['reload'])}</p>
<div class="callout bad"><b>安全底线：</b>默认关闭；出站 HTTPS；不暴露 hive serve 的入站 API；不复用 desktop token；environment secret/OAuth/trusted-device 只从 credential store 读取；权限由宿主批准；远端不能自批；日志不打印 token 或消息正文。</div></section>

<section id="failure"><h2><span class="num">06</span>失败与限额</h2>
<div class="tablewrap"><table><thead><tr><th style="width:40%">场景</th><th style="width:60%">结果</th></tr></thead><tbody>{failure_rows}</tbody></table></div>
<div class="grid g2"><div class="card"><div class="k">Limits</div><ul>{limit_items}</ul></div><div class="card"><div class="k">实现前必须确认</div><ul><li>控制面 owner、base URL、API 版本、OAuth provider。</li><li>work 结果/状态通道，以及 archive 后的可见性。</li><li>workspace 隔离：专用目录、独立 OS 用户还是容器。</li><li>是否强制 v2 trusted device。</li><li>是否允许 remote work 触发 colony/worker；本设计允许通过正常 session 语义。</li><li>是否进入 1.0 发布；本设计建议 post-1.0。</li></ul></div></div></section>

<section id="acceptance"><h2><span class="num">07</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:28%">测试</th><th style="width:65%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div>
<h3>Review Focus</h3>
<div class="tablewrap"><table><thead><tr><th style="width:42%">未覆盖输入 / 失败模式</th><th style="width:43%">期望</th><th style="width:15%">Owner</th></tr></thead><tbody>{review_rows}</tbody></table></div>
<div class="callout bad"><b>设计边界：</b>status 仍是 <code>design</code>。没有 fake control plane、真实 local session、权限 relay、redaction 和最终全量 CI 前，不把本页当作实现或发布证据。</div></section>

<section id="tasks"><h2><span class="num">08</span>实施任务</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:10%">依赖</th><th style="width:44%">写入范围</th><th style="width:40%">完成检查</th></tr></thead><tbody>{task_rows}</tbody></table></div>
<div class="callout info"><b>先决条件：</b>先拍板控制面合同、OAuth、结果通道、workspace 隔离、权限模式和是否进入 1.0；未拍板前不进入实现 lane。</div></section>

<section id="risks"><h2><span class="num">09</span>风险、待拍板与来源</h2>
<div class="tablewrap"><table><thead><tr><th style="width:28%">风险</th><th style="width:27%">影响</th><th style="width:45%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>bridge-mode design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_bridge_mode_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
</div></body></html>
"""


def validate(data: dict) -> None:
    req_ids = {r["id"] for r in data["requirements"]}
    design_ids = set(data["design"])
    acc_ids = {a["id"] for a in data["acceptance"]}
    task_ids = {t["id"] for t in data["tasks"]}
    for req in data["requirements"]:
        for trace in req["traces"]:
            if trace not in design_ids | acc_ids:
                raise ValueError(f"{req['id']} references unknown {trace}")
    referenced = {trace for req in data["requirements"] for trace in req["traces"]}
    for design in design_ids:
        if design not in referenced:
            raise ValueError(f"{design} is not referenced by a requirement")
    for task in data["tasks"]:
        for dep in task["depends_on"]:
            if dep not in task_ids:
                raise ValueError(f"{task['id']} depends on unknown {dep}")
    if req_ids != {f"R{i}" for i in range(1, len(req_ids) + 1)}:
        raise ValueError("requirements are not contiguous")
    if acc_ids != {f"A{i}" for i in range(1, len(acc_ids) + 1)}:
        raise ValueError("acceptance ids are not contiguous")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    json_text = json.dumps(DATA, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(DATA)
    validate(DATA)
    if args.check:
        drift = []
        for path, expected in ((OUT_JSON, json_text), (OUT_HTML, html_text)):
            if not path.exists() or path.read_text(encoding="utf-8") != expected:
                drift.append(str(path))
        if drift:
            print("bridge-mode-design: DRIFT: " + ", ".join(drift))
            return 1
        print(
            f"bridge-mode-design: OK ({len(DATA['requirements'])} requirements, "
            f"{len(DATA['design'])} design, {len(DATA['acceptance'])} acceptance rows)"
        )
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"bridge-mode-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
