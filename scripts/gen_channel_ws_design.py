#!/usr/bin/env python3
"""Generate the go_hive channel WebSocket requirements -> design -> acceptance chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/channel-websocket-design.html
  - go_hive/docs/channel-websocket-design.json

The transport is an opt-in inbound WebSocket adapter for internal/channels.
It is not a second listener and it does not replace the existing stdio host.
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "channel-websocket-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "channel-websocket-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {
    "schema_version": 1,
    "feature": "channel-websocket",
    "status": "design",
    "decision": (
        "Add type=\"ws\" as an inbound transport for channels.<name> and mount "
        "it on the existing hive serve listener at a fixed route derived from "
        "the channel key: /channel/websocket/<name>. The path is not a config "
        "field and a path key is rejected; there is no custom or shared path. "
        "Reuse the existing hive-channel/1 JSON-RPC methods and SessionBackend "
        "semantics, authenticate each connection with a channel-specific bearer "
        "token, and allow exactly one active connection per channel. stdio "
        "entries remain the default and are unchanged."
    ),
    "goal_beyond_request": (
        "Make a channel implementable in any language that has a WebSocket and "
        "JSON client, without putting a process on the Hive host, while keeping "
        "the existing stdio channel path and its upstream-compatible session "
        "semantics intact. The channel becomes a remote peer of the same "
        "ChannelHost contract instead of a Go-specific child process."
    ),
    "feasibility": {
        "verdict": "可行，但不是按当前配置示例原样可行。",
        "why": (
            "The current host is process-oriented: every enabled channels entry "
            "must have command/args, Host.Start spawns one stdio child, and "
            "Host.handlePluginRequest is only reachable from that child. The "
            "example has type=ws, no command, and no route, so today it would "
            "be reported failed with 'no command configured'. There is also no "
            "WebSocket route in hive serve and no WebSocket server dependency "
            "in go.mod. The protocol layer, however, is already a transport-"
            "neutral JSON-RPC contract, so the work is an inbound transport "
            "adapter plus route/auth/lifecycle work, not a new session model."
        ),
        "current_limits": [
            "internal/channels/config.go PluginConfig has Name, Enabled, Command, Args, Cwd, Env and Section; it does not read type or path.",
            "internal/channels/host.go startOne requires Argv(); an enabled entry with no command is failed, as the existing nocommand test proves.",
            "There is no /channel/websocket route; internal/server/app.go registers API routes and a GET / SPA fallback, and go.mod has no WebSocket server module.",
            "internal/channels/subscription.go hard-codes *pluginProcess in delivery; the output path needs a transport-neutral sink.",
            "internal/server/app.go desktopAuth checks every path except /api/queen-mail/ when HIVE_DESKTOP_TOKEN is set; channel auth must not piggyback on it.",
            "hello carries the whole config section and an absolute state_dir, while Output.Files carries absolute host paths; those are not usable by a remote language client.",
        ],
        "reusable_parts": [
            "hive-channel/1 JSON-RPC envelopes and method names in internal/channels/protocol.go.",
            "Host.handlePluginRequest and the SessionBackend interface, which already wrap open_session, send, subscribe, unsubscribe, set_title and judge.",
            "Output kinds, subscription queue, drop-oldest policy and seq gaps in internal/channels/subscription.go.",
            "GET /api/channels state map in internal/server/routes_credentials_channels.go.",
        ],
    },
    "problem": {
        "statement": (
            "A channel is currently a local implementation detail: the server "
            "owns the child process, the child talks JSON-RPC on stdin/stdout, "
            "and the plugin must run on the same host to read files and state. "
            "That is not language-neutral for remote implementations and it does "
            "not give a remote client a real-time duplex link. A WebSocket "
            "endpoint is the natural transport, but the existing config example "
            "does not describe the identity, path, authentication, lifecycle or "
            "file semantics needed to make it safe."
        ),
        "user_value": [
            "Any language with a WebSocket client can implement a channel; no Go process, no stdin framing, no child lifecycle.",
            "The server can push message, question, error, files and idle outputs in real time over the same socket.",
            "The existing session methods and queen semantics stay identical, so a port is a transport port rather than a second channel model.",
            "The stdio wxbot path remains the default and needs no migration.",
            "The route is opt-in and can be mounted without a second port or a separate daemon.",
        ],
        "why_not_now": (
            "This is a Go-only extension; upstream Hive exposes channel plugins "
            "as in-process packages and has no WebSocket channel transport. It "
            "must stay at design status until path, auth, attachment and "
            "single-connection decisions are accepted."
        ),
    },
    "protocol": {
        "name": "Hive channel JSON-RPC",
        "version": "hive-channel/1",
        "transport": "WebSocket (RFC 6455), text frames",
        "endpoint": "/channel/websocket/<name>",
        "subprotocol": "hive-channel/1",
        "auth_header": "Authorization header (Bearer)",
        "framing": "one UTF-8 JSON object per text message; binary messages are rejected; no NDJSON",
        "source": "internal/channels/protocol.go ProtocolVersion and envelope types",
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Mount an opt-in WebSocket channel route on the existing hive serve listener; do not add a second port or a separate daemon.",
            "acceptance": "A configured ws entry is reachable on the same host/port as /api/health; no listener is opened for the channel.",
            "traces": ["D1", "D12", "A1", "A12"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "Extend channels.<name> with type=stdio|ws. type defaults to stdio; command/args/cwd/env remain the stdio path; ws must not require a command or spawn a process.",
            "acceptance": "The existing wxbot section is unchanged; an enabled ws entry with no command mounts; an enabled stdio entry without command still fails as before.",
            "traces": ["D2", "A2", "A13"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Each ws channel has exactly one route derived from its configuration key: /channel/websocket/<name>. The path is not configurable; a path key is rejected, and a shared path cannot select among several channels.",
            "acceptance": "The derived route works for each valid name; a path key, duplicate route, invalid name or traversal string is a startup error; a client cannot select another channel by path.",
            "traces": ["D2", "D3", "A3", "A4"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Authenticate every ws connection with a channel-specific bearer token, bound to the configured channel; do not reuse HIVE_DESKTOP_TOKEN and never put the token in the URL.",
            "acceptance": "Missing, wrong, cross-channel and query/path tokens fail before upgrade; a valid token works; logs contain no token.",
            "traces": ["D3", "D11", "A4", "A5", "A15"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Reuse hive-channel/1 JSON-RPC envelopes, hello/start handshake, request ids, error kinds and output notifications; do not invent an MCP or a second channel semantic model.",
            "acceptance": "The server sends hello then start; a client responds welcome then start result; malformed versions and unknown methods return typed errors.",
            "traces": ["D4", "D5", "A6", "A7"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Expose exactly the existing client-to-host methods open_session, send, subscribe, unsubscribe, set_title and judge, with the same params and result shapes as stdio.",
            "acceptance": "Method tests use the same fixtures as the stdio host; no new channel method becomes available to a model or to a remote client.",
            "traces": ["D4", "D5", "A7", "A13"],
        },
        {
            "id": "R7",
            "priority": "P0",
            "statement": "Keep Output kinds, per-subscription ordering, seq gaps and the bounded drop-oldest queue; do not let a slow WebSocket client block the event bus.",
            "acceptance": "message/question/error/files/idle arrive in order; a full queue increments drops and leaves a visible seq gap; the backend remains responsive.",
            "traces": ["D7", "D10", "A8", "A11"],
        },
        {
            "id": "R8",
            "priority": "P0",
            "statement": "Do not send absolute host paths to a remote ws client. Output attachments use opaque file_refs and an authenticated download route, or the channel explicitly returns an unsupported-attachment error.",
            "acceptance": "Output.Files is not exposed as a host path on ws; file_refs resolve only inside the session attachment root; traversal and oversized files fail closed.",
            "traces": ["D8", "A10", "A15"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "Define a bounded connection lifecycle: one active connection per channel, handshake/start timeouts, ping/pong liveness, close codes and cleanup of every subscription on disconnect.",
            "acceptance": "A second connection is refused or replaces the first according to config; a silent peer is closed after the pong deadline; disconnect unsubscribes all sessions.",
            "traces": ["D6", "A9", "A11"],
        },
        {
            "id": "R10",
            "priority": "P1",
            "statement": "Report ws channel state alongside existing channels: state running/off/failed, transport, connected, connection count and last_error, without breaking the existing GET /api/channels fields.",
            "acceptance": "Configured/disconnected/connected/failed transitions are visible; existing stdio channel entries keep their current shape.",
            "traces": ["D9", "A12", "A13"],
        },
        {
            "id": "R11",
            "priority": "P0",
            "statement": "Redact secrets and host-only state from hello: omit state_dir and token material, expose only a safe config projection, validate Origin when present and require TLS for remote use.",
            "acceptance": "A captured hello has no token, no environment value and no absolute state_dir; browser Origin mismatch is rejected; plaintext remote use is documented as unsupported.",
            "traces": ["D11", "A5", "A15"],
        },
        {
            "id": "R12",
            "priority": "P1",
            "statement": "Keep the pure-Go, CGO_ENABLED=0 release boundary. A new WebSocket dependency requires explicit approval; otherwise implement the minimal RFC 6455 server with the standard library.",
            "acceptance": "CGO=0 builds/tests pass; the dependency decision is recorded; no cgo or bundled runtime is added.",
            "traces": ["D12", "A17"],
        },
        {
            "id": "R13",
            "priority": "P0",
            "statement": "Preserve the existing stdio channel path, configuration shape and upstream-compatible session semantics; ws is an additive transport, not a migration.",
            "acceptance": "The wxbot stdio host still starts; stdio protocol fixtures are byte-identical; no existing channel config needs editing.",
            "traces": ["D12", "A2", "A13"],
        },
        {
            "id": "R14",
            "priority": "P1",
            "statement": "Publish a language-neutral wire contract with Python and Node examples and a fake-client conformance harness.",
            "acceptance": "Two independent clients complete handshake and one open_session/send/subscribe round trip against a fake backend; missing clients remain an external gap.",
            "traces": ["D4", "A14"],
        },
    ],
    "non_goals": [
        "Do not add a second HTTP listener, a sidecar daemon or an external WebSocket proxy requirement.",
        "Do not expose MCP semantics or MCP tools through this endpoint; this is the channel host protocol.",
        "Do not add Hive-to-Hive federation, remote channel discovery or a second identity store.",
        "Do not add hot reload; configuration is read at serve startup like the current channels section.",
        "Do not support multiple active plugin connections for one channel in v1; one bot identity, one connection.",
        "Do not add replay, an inbox or a cursor in v1; a disconnected client must re-open and re-subscribe.",
        "Do not add channel tools to the local model tool catalogue.",
        "Do not change the stdio child protocol or the existing wxbot configuration contract.",
    ],
    "assumptions": [
        "The Hive process is the server; a language client is the channel plugin and connects to /channel/websocket/<name>.",
        "The endpoint is off unless an enabled ws entry exists; no command is spawned for a ws entry.",
        "A channel token is supplied through an environment variable named by token_env and is never printed.",
        "The ws client can send an Authorization header. Browser clients that cannot set one need a same-origin BFF or a cookie design; that is an open decision.",
        "Remote deployment terminates TLS at a trusted reverse proxy and uses wss; plaintext remote WebSocket is unsupported.",
        "The client accepts at-most-once output semantics with seq gaps and re-subscribes after reconnect; durable delivery is a later feature.",
        "The channel client owns its local state; an absolute server state_dir is neither useful nor safe to send.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "type=ws route on the existing hive serve listener, reusing hive-channel/1",
            "verdict": "chosen",
            "reason": "Matches the user request, gives language-neutral real-time transport, avoids a second port and keeps the session backend and protocol vocabulary. Requires a new inbound transport adapter and auth, not just a config field.",
        },
        {
            "id": "O2",
            "name": "Separate WebSocket listener on a dedicated port",
            "verdict": "rejected",
            "reason": "The request explicitly says to share hive serve; a second port adds deployment, discovery, proxy and state-sync surface for no protocol benefit.",
        },
        {
            "id": "O3",
            "name": "Keep stdio and run a local WebSocket-to-stdio bridge process",
            "verdict": "rejected",
            "reason": "Still requires a process on the Hive host; the remote language client would depend on a Go bridge and the host would still own the child lifecycle.",
        },
        {
            "id": "O4",
            "name": "Adopt an approved pure-Go WebSocket library instead of hand-rolling RFC 6455",
            "verdict": "deferred",
            "reason": "The library is safer and shorter, but it changes go.mod and needs explicit approval under the no-new-dependency rule. The transport contract is independent of this choice.",
        },
        {
            "id": "O5",
            "name": "Expose MCP over WebSocket instead of the channel JSON-RPC",
            "verdict": "rejected",
            "reason": "MCP is an agent/tool protocol, not the ChannelHost object model. Reusing MCP would create a second session/tool layer and break the existing channel semantics.",
        },
        {
            "id": "O6",
            "name": "One shared /channel/websocket/ path for every channel",
            "verdict": "rejected",
            "reason": "The path alone cannot identify the configured channel or its token. A later subprotocol or first-frame selector is possible, but it makes pre-upgrade 404/401/403 impossible and increases ambiguity.",
        },
    ],
    "design": {
        "D1": {
            "title": "Route placement",
            "text": (
                "Mount one handler per enabled ws entry at "
                "/channel/websocket/<name> on the existing http.Server. Register "
                "the route before the GET / SPA fallback. The handler upgrades "
                "only after path, token and Origin checks; the route is not "
                "under /api and does not inherit the desktop token policy. No "
                "second listener is opened."
            ),
        },
        "D2": {
            "title": "Configuration and validation",
            "text": (
                "Extend PluginConfig with Type (stdio default), TokenEnv, "
                "AllowedOrigins, MaxConnections, Takeover and timeout/limit fields. "
                "Parse type before the stdio command checks. For ws, do not require "
                "command, do not spawn a process, validate the name and derive the "
                "route as /channel/websocket/<name>; a path key is rejected. Require "
                "token_env and a unique route. Invalid entries are startup failures, "
                "not silent disables. Read at serve startup; changing an entry "
                "requires restart."
            ),
        },
        "D3": {
            "title": "Authentication and connection identity",
            "text": (
                "The client sends an Authorization header with the bearer "
                "scheme; the token is read from token_env. The server compares "
                "in constant time, binds the token to the configured channel "
                "name and rejects a token presented on another channel. "
                "Missing/wrong token is 401/403 before 101. "
                "If Origin is present it must match allowed_origins; native "
                "clients without Origin are allowed only with a valid token. "
                "HIVE_DESKTOP_TOKEN is never accepted as a channel token and the "
                "token never appears in the URL, query string or hello config."
            ),
        },
        "D4": {
            "title": "Wire protocol",
            "text": (
                "Use WebSocket text frames carrying one UTF-8 JSON-RPC 2.0 "
                "object each. Negotiate subprotocol hive-channel/1 during the "
                "upgrade. Reuse hello, start, output, open_session, send, "
                "subscribe, unsubscribe, set_title and judge exactly as "
                "internal/channels/protocol.go defines them. Binary frames are "
                "rejected. No MCP methods and no per-channel custom model are "
                "introduced."
            ),
        },
        "D5": {
            "title": "Backend and request reuse",
            "text": (
                "The ws transport calls the same request dispatcher as the "
                "stdio host instead of duplicating session logic. Extract the "
                "unexported handler behind a small handler/sink seam so both "
                "transports share open_session, send, subscribe, unsubscribe, "
                "set_title and judge. The channel backend remains SessionBackend; "
                "the ws layer must not import internal/server, internal/cli or "
                "internal/tools/builtin."
            ),
        },
        "D6": {
            "title": "Lifecycle and close semantics",
            "text": (
                "States are CONNECTED, HELLO_SENT, START_SENT, READY and CLOSED. "
                "After 101 the server sends hello (id srv-1); the client must "
                "answer within handshake_timeout_seconds; the server sends start "
                "(id srv-2); the client answers; only then are client requests "
                "accepted. A request before READY gets -32000 with kind "
                "not_ready. On stop, send a bounded stop request then close 1001. "
                "Disconnect closes every subscription and releases the channel "
                "slot."
            ),
        },
        "D7": {
            "title": "Output and backpressure",
            "text": (
                "Reuse Output and outputParams. Each subscription has one "
                "ordered queue of queue_size and one writer; when the queue is "
                "full, drop the oldest output, increment drops and let seq gaps "
                "tell the client. A slow ws peer must never block the event bus. "
                "v1 is at-most-once: no replay buffer and no cursor. Reconnect "
                "means open_session/subscribe again."
            ),
        },
        "D8": {
            "title": "Remote attachments",
            "text": (
                "For ws, do not expose Output.Files as absolute host paths. Add "
                "an optional file_refs array with an opaque id, name, size, mime "
                "and sha256; the client downloads through an authenticated "
                "GET /channel/artifacts/<id> route scoped to the session "
                "attachment root. Input send.images stays the existing base64 "
                "form. If attachment transfer is not accepted, v1 must return a "
                "typed unsupported_attachment error instead of a useless path."
            ),
        },
        "D9": {
            "title": "State and observability",
            "text": (
                "Extend GET /api/channels entries for ws with transport=ws, "
                "connected, connections and last_error while keeping name and "
                "state. state is off when disabled, running when the route is "
                "mounted, failed on config/auth/listener errors. Logs contain "
                "channel name, connection generation, close code and counts, "
                "never Authorization, token, text or attachment bytes."
            ),
        },
        "D10": {
            "title": "Limits and liveness",
            "text": (
                "Enforce max_frame_bytes before JSON decode; reject binary "
                "frames; no permessage-deflate; require masked client frames; "
                "send ping every ping_interval_seconds and require pong within "
                "pong_timeout_seconds; bound in-flight requests and handshake "
                "time. One active connection per channel by default. These are "
                "transport checks, not new channel semantics."
            ),
        },
        "D11": {
            "title": "Hello redaction and remote safety",
            "text": (
                "Build the hello config from a safe projection: enabled, type, "
                "path and non-secret plugin options only. Omit state_dir for ws "
                "and omit token_env's value, Authorization, env values and "
                "absolute paths. Validate the channel name before joining it "
                "to the route. Remote use must be wss behind a trusted proxy; "
                "do not bind 0.0.0.0 as a shortcut and do not reuse the desktop "
                "token."
            ),
        },
        "D12": {
            "title": "Package and dependency boundary",
            "text": (
                "Keep the transport-neutral protocol and host seam in "
                "internal/channels (ws_*.go in the same package is acceptable "
                "to reuse unexported helpers); mount it from a thin server "
                "adapter. Do not import internal/server, internal/cli or "
                "internal/tools/builtin from the channel transport. The default "
                "choice is a minimal standard-library RFC 6455 server; the "
                "approved-library option is recorded in O4 and needs explicit "
                "dependency approval before code."
            ),
        },
    },
    "config_contract": {
        "config": "channels",
        "status": "partly existing / partly proposed",
        "authority": "internal/channels/config.go and internal/cli/loader_channels.go",
        "path_rule": "The route is derived from the channel key as /channel/websocket/<name>. Path is not configurable and a path key is rejected; there is no custom or shared path.",
        "files": [
            {"level": "existing", "path": "channels.<name>.enabled", "own": "truthy gate; already parsed"},
            {"level": "existing", "path": "channels.<name>.command/args/cwd/env", "own": "stdio child only; already parsed"},
            {"level": "proposed", "path": "channels.<name>.type", "own": "stdio default or ws; not read at HEAD"},
            {"level": "derived", "path": "route", "own": "/channel/websocket/<name>; not a config key, no path override"},
            {"level": "proposed", "path": "channels.<name>.token_env", "own": "name of the env var holding the ws token"},
            {"level": "proposed", "path": "channels.<name>.allowed_origins", "own": "optional browser Origin allowlist"},
            {"level": "proposed", "path": "channels.<name>.max_connections", "own": "default 1; v1 does not fan out"},
            {"level": "proposed", "path": "channels.<name>.takeover", "own": "default false; explicit 4004 replacement when true"},
        ],
        "sample": {
            "channels": {
                "ws-name": {
                    "enabled": True,
                    "type": "ws",
                    "token_env": "HIVE_CHANNEL_WS_NAME_TOKEN",
                    "allowed_origins": ["https://ops.example"],
                    "max_connections": 1,
                    "takeover": False,
                    "handshake_timeout_seconds": 10,
                    "ping_interval_seconds": 25,
                    "pong_timeout_seconds": 60,
                    "max_frame_bytes": 16777216,
                    "queue_size": 256,
                    "max_file_bytes": 33554432,
                }
            }
        },
        "fields": [
            {"name": "enabled", "required": "yes (existing)", "meaning": "truthy gate; with type=ws mounts the route"},
            {"name": "type", "required": "no (default stdio)", "meaning": "stdio or ws; unknown values fail startup"},
            {"name": "token_env", "required": "yes for ws", "meaning": "environment variable name; the value is never written to hello"},
            {"name": "allowed_origins", "required": "no", "meaning": "if Origin is present it must match; empty rejects browser origins"},
            {"name": "max_connections", "required": "no", "meaning": "1 in v1; a second connection is 409 unless takeover is enabled"},
            {"name": "takeover", "required": "no", "meaning": "false by default; true closes the old connection with 4004"},
            {"name": "handshake_timeout_seconds", "required": "no", "meaning": "hello/start reply budget; default 10"},
            {"name": "ping_interval_seconds", "required": "no", "meaning": "server ping cadence; default 25"},
            {"name": "pong_timeout_seconds", "required": "no", "meaning": "silent peer close deadline; default 60"},
            {"name": "max_frame_bytes", "required": "no", "meaning": "text frame cap; default 16777216"},
            {"name": "queue_size", "required": "no", "meaning": "per-subscription output queue; default 256"},
            {"name": "max_file_bytes", "required": "no", "meaning": "file_refs download cap; default 33554432"},
        ],
        "reload": "read at serve startup; changing a ws entry requires restarting hive serve, matching the current channels section",
    },
    "identity_contract": {
        "channel": "The route name and token are the channel identity; the token may not be presented on another channel.",
        "client": "The client is a channel plugin, not a model user and not a desktop browser; it never supplies from_queen or an authorization role.",
        "token": "Authorization header only; token_env names the source; no URL/query token and no HIVE_DESKTOP_TOKEN.",
        "hello": "Contains name, protocol and a redacted config projection; never state_dir or token material.",
        "session": "session_id is returned by open_session and is the only key used by send/subscribe/unsubscribe/set_title.",
        "files": "file_refs ids are opaque and scoped to the channel/session; absolute host paths are not disclosed.",
    },
    "protocol_contract": {
        "sequence": [
            "HTTP GET /channel/websocket/<name> with Upgrade, Origin (if any), Authorization and Sec-WebSocket-Protocol",
            "HTTP 101 Switching Protocols with the selected subprotocol",
            "server -> client hello request (srv-1)",
            "client -> server result with plugin_version",
            "server -> client start request (srv-2)",
            "client -> server start result; state READY",
            "client requests open_session/send/subscribe/unsubscribe/set_title/judge",
            "server -> client output notifications with subscription seq",
            "server stop request on shutdown; close 1001",
        ],
        "server_frames": [
            "hello: JSON-RPC request with id and params.protocol, params.name, params.config (redacted)",
            "start: JSON-RPC request with id and empty params",
            "output: JSON-RPC notification with subscription_id, session_id, kind, text/questions/files and seq",
            "stop: JSON-RPC request on shutdown, bounded by StopTimeout",
        ],
        "client_methods": [
            "open_session {queen_id, session_id?, title?}",
            "send {session_id, text, images?, display_text?, correlation_id?}",
            "subscribe {session_id}",
            "unsubscribe {subscription_id}",
            "set_title {session_id, title}",
            "judge {question, state, criteria?}",
        ],
        "close_codes": [
            {"code": 1000, "meaning": "normal close"},
            {"code": 1001, "meaning": "server going away"},
            {"code": 1008, "meaning": "policy violation (auth/origin)"},
            {"code": 1009, "meaning": "message too big"},
            {"code": 1011, "meaning": "internal error"},
            {"code": 4001, "meaning": "authentication failed or expired"},
            {"code": 4002, "meaning": "protocol violation (malformed frame or unexpected response)"},
            {"code": 4003, "meaning": "request before ready"},
            {"code": 4004, "meaning": "connection replaced"},
            {"code": 4005, "meaning": "host shutting down"},
            {"code": 4006, "meaning": "concurrency or backpressure limit"},
        ],
        "hello_example": {
            "jsonrpc": "2.0",
            "id": "srv-1",
            "method": "hello",
            "params": {
                "protocol": "hive-channel/1",
                "name": "ws-name",
                "config": {
                    "enabled": True,
                    "type": "ws",
                    "allowed_origins": ["https://ops.example"],
                },
            },
        },
        "welcome_example": {
            "jsonrpc": "2.0",
            "id": "srv-1",
            "result": {"plugin_version": "0.1.0", "capabilities": ["text", "questions"]},
        },
        "open_session_example": {
            "jsonrpc": "2.0",
            "id": "c1",
            "method": "open_session",
            "params": {"queen_id": "queen_custom_37908", "title": "Telegram ops"},
        },
        "output_example": {
            "jsonrpc": "2.0",
            "method": "output",
            "params": {
                "subscription_id": "sub_1",
                "session_id": "session_20261124_...",
                "kind": "message",
                "text": "Done.",
                "seq": 1,
            },
        },
    },
    "limits": {
        "max_frame_bytes": "16 MiB per text message; binary messages rejected",
        "max_images_per_send": "8, same as the stdio protocol",
        "max_image_bytes": "4 MiB decoded, same as stdio",
        "max_images_total_bytes": "8 MiB per send, same as stdio",
        "max_file_bytes": "32 MiB per file_refs download",
        "queue_size": "256 outputs per subscription; drop-oldest",
        "handshake_timeout": "10 seconds for welcome and start",
        "request_timeout": "60 seconds per client request",
        "ping_interval": "25 seconds",
        "pong_timeout": "60 seconds",
        "max_connections_per_channel": "1 in v1",
        "max_inflight_requests": "16 per connection",
        "compression": "permessage-deflate disabled; no extensions",
    },
    "failure_modes": [
        {"case": "unknown or disabled channel path", "result": "404 before upgrade; no socket and no channel state mutation"},
        {"case": "path key present in a ws entry", "result": "startup config error; route is derived from the channel key and cannot be overridden"},
        {"case": "missing or wrong token", "result": "401/403 before upgrade; no socket; no token echo"},
        {"case": "token valid on another channel", "result": "403; token bound to one route"},
        {"case": "Origin present but not allowed", "result": "403 before upgrade; no browser cross-site connection"},
        {"case": "wrong or missing Sec-WebSocket-Protocol", "result": "426 before upgrade"},
        {"case": "second connection while one is active", "result": "409, or 4004 takeover when explicitly enabled"},
        {"case": "hello/start not answered in time", "result": "close 4002; slot released"},
        {"case": "client request before READY", "result": "-32000 JSON-RPC error, kind=not_ready; connection stays open"},
        {"case": "unknown method", "result": "-32601 method not found; same as stdio"},
        {"case": "malformed JSON or oversized frame", "result": "close 4002 or 1009; no partial dispatch"},
        {"case": "output queue full", "result": "drop oldest, increment drops, leave a seq gap; backend never blocks"},
        {"case": "client disconnects", "result": "close all subscriptions; channel slot returns to disconnected"},
        {"case": "server shutdown", "result": "bounded stop request then close 1001"},
        {"case": "file_ref id traversal or too large", "result": "403/404/413; no path disclosure"},
    ],
    "acceptance": [
        {"id": "A1", "test": "Mount and health", "pass": "enabled ws entry is reachable on the same serve host/port; /api/health still works; no second listener is opened."},
        {"id": "A2", "test": "Config compatibility", "pass": "type defaults to stdio; wxbot config is unchanged; ws without command mounts; stdio without command still fails."},
        {"id": "A3", "test": "Derived route identity", "pass": "each valid channel name gets /channel/websocket/<name>; a path key is rejected; invalid name/duplicate route/traversal fails startup; a client cannot select another channel."},
        {"id": "A4", "test": "Token binding", "pass": "valid, missing, wrong, cross-channel, query and path token cases all fail or succeed as specified; no token in logs."},
        {"id": "A5", "test": "Origin and transport", "pass": "allowed Origin upgrades; disallowed Origin fails; native no-Origin with valid token upgrades; plaintext remote use is documented as unsupported."},
        {"id": "A6", "test": "Handshake sequence", "pass": "101, hello, welcome, start, start result and READY happen in order; timeout and malformed version fail with typed close/error."},
        {"id": "A7", "test": "Method parity", "pass": "open_session, send, subscribe, unsubscribe, set_title and judge use the same params/results/errors as the stdio fixtures."},
        {"id": "A8", "test": "Output stream", "pass": "message/question/error/files/idle and seq are correct; a slow consumer gets a seq gap and does not block the backend."},
        {"id": "A9", "test": "Connection lifecycle", "pass": "second connection policy, ping/pong timeout, disconnect cleanup and shutdown close 1001 all pass."},
        {"id": "A10", "test": "Attachments", "pass": "ws output does not expose absolute paths; file_refs download works with bearer auth; traversal/oversize fails closed."},
        {"id": "A11", "test": "Limits and backpressure", "pass": "frame, image, in-flight and queue limits behave as documented; no goroutine or subscription leak on abort."},
        {"id": "A12", "test": "State", "pass": "off/running/failed, connected/connections and last_error are visible; existing stdio entries keep their fields."},
        {"id": "A13", "test": "Stdio regression", "pass": "existing host, wxbot config, JSON-lines frames and GET /api/channels shape are unchanged; no local model tool is added."},
        {"id": "A14", "test": "Language conformance", "pass": "Python and Node fake clients complete handshake + open_session + send + subscribe against a real serve; missing client is an external gap."},
        {"id": "A15", "test": "Security redaction", "pass": "hello has no token/state_dir/env value; logs and errors have no Authorization or message text; path validation is strict."},
        {"id": "A16", "test": "Design artifact drift", "pass": "generator --check returns 0; a deliberate HTML edit makes it return 1; 1440/390 render has no page-level overflow."},
        {"id": "A17", "test": "Build and dependency audit", "pass": "CGO_ENABLED=0 package tests and six-platform builds pass; go.mod unchanged or the approved dependency is recorded; no bundled runtime."},
    ],
    "review_focus": [
        {"input": "client sends open_session before hello/start completes", "expected": "typed not_ready error; no session is created", "owner": "T3"},
        {"input": "token_env is missing at serve startup", "expected": "the ws entry is failed/not mounted; no anonymous route", "owner": "T1"},
        {"input": "second client races the first connection", "expected": "409 or explicit 4004 takeover; no two writers for one channel", "owner": "T5"},
        {"input": "slow consumer while the queen emits a burst", "expected": "drop-oldest + seq gap, event bus stays responsive", "owner": "T5"},
        {"input": "file_ref id is crafted or points outside the attachment root", "expected": "404/403; no path disclosure and no traversal", "owner": "T6"},
    ],
    "tasks": [
        {"id": "T1", "depends_on": [], "writes": ["internal/channels/config.go", "internal/channels/ws_config.go", "internal/channels/ws_config_test.go"], "done": "type/token_env/timeouts parse and validate; route is derived from the name; a path key is rejected; stdio defaults unchanged; invalid ws fails startup."},
        {"id": "T2", "depends_on": ["T1"], "writes": ["internal/channels/ws_handshake.go", "internal/channels/ws_handshake_test.go"], "done": "upgrade, subprotocol, Origin, bearer, derived route and 401/403/404/409/426 cases pass before 101."},
        {"id": "T3", "depends_on": ["T1"], "writes": ["internal/channels/ws_protocol.go", "internal/channels/ws_protocol_test.go"], "done": "hello/start state machine, request ids, not_ready, method errors and close codes pass."},
        {"id": "T4", "depends_on": ["T3"], "writes": ["internal/channels/host.go", "internal/channels/subscription.go", "internal/channels/request_seam.go"], "done": "stdio and ws share the request handler; subscription delivery is transport-neutral; stdio fixtures stay byte-identical."},
        {"id": "T5", "depends_on": ["T3", "T4"], "writes": ["internal/channels/ws_lifecycle.go", "internal/channels/ws_lifecycle_test.go"], "done": "single connection, takeover policy, ping/pong, disconnect cleanup and queue seq/drop tests pass."},
        {"id": "T6", "depends_on": ["T4"], "writes": ["internal/channels/ws_files.go", "internal/channels/ws_files_test.go", "internal/server/channel_artifact_route.go"], "done": "file_refs metadata, authenticated download, traversal/oversize rejection and text-only fallback pass."},
        {"id": "T7", "depends_on": ["T2", "T5"], "writes": ["internal/server/channels_ws_adapter.go", "internal/cli/loader_channels.go", "internal/server/routes_credentials_channels.go"], "done": "route is mounted only for enabled ws entries; state is visible; no second listener; desktop token is not reused."},
        {"id": "T8", "depends_on": ["T6", "T7"], "writes": ["docs/channel-websocket-design.md", "scripts/channel_ws_client_example.py", "scripts/channel_ws_client_example.mjs"], "done": "Python and Node clients pass against a real serve; docs match the frozen JSON contract."},
        {"id": "T9", "depends_on": ["T8"], "writes": [], "done": "CGO=0 package tests, full CI, six-platform build and stdio regression pass; dependency decision is recorded."},
    ],
    "risks": [
        {"risk": "The derived /channel/websocket/<name> URL is not what an existing client expects", "impact": "a remote client cannot connect with a custom path", "mitigation": "document the one canonical route; do not add a path key; a future shared-path selector needs its own pre-upgrade identity design and tests."},
        {"risk": "A hand-written RFC 6455 server has parser/security bugs", "impact": "invalid frames, DoS or protocol confusion", "mitigation": "keep the frame subset minimal, test masking/fragmentation/close/ping, fuzz frames, and prefer an approved library if the dependency gate opens."},
        {"risk": "The ws route is outside /api and bypasses desktopAuth", "impact": "a new unauthenticated route on a remotely bound server", "mitigation": "mandatory per-channel token, Origin check, TLS boundary and a startup refusal when token_env is absent."},
        {"risk": "Remote clients cannot read absolute Output.Files paths", "impact": "images/documents silently break or leak host paths", "mitigation": "file_refs + authenticated download, or a typed unsupported_attachment error; never send host paths."},
        {"risk": "At-most-once output with drop-oldest loses real replies", "impact": "a slow or disconnected channel misses messages", "mitigation": "document seq gaps; resubscribe after reconnect; durable inbox/replay is a later design."},
        {"risk": "hello carries the whole configuration section", "impact": "tokens, env values or absolute paths leak to a remote client", "mitigation": "build a safe config projection and omit state_dir for ws; test captured hello."},
        {"risk": "Single active connection becomes a reconnect race", "impact": "two clients write to one channel or a reconnect is refused", "mitigation": "generation ids, explicit takeover policy and a test for both refusal and replacement."},
    ],
    "open_questions": [
        "Should the server be hand-written on net/http Hijacker (no dependency) or use an approved pure-Go WebSocket library? O4 is deferred until dependency approval.",
        "How should a browser client authenticate, given that the WebSocket API cannot set an Authorization header? A same-origin BFF/cookie design is different from the native-client header path.",
        "Is file transfer in v1 required? If yes, choose file_refs plus an authenticated GET /channel/artifacts/<id>; if no, v1 must return a typed unsupported_attachment error for Output.Files.",
        "What is the second-connection policy: reject with 409, or replace the old connection with close 4004? The default proposal is reject; takeover needs an explicit config.",
        "Should a mounted ws route with no connected client report state running (endpoint ready) or off? The proposed contract keeps state=running and adds connected=false.",
        "Do you need replay/inbox/at-least-once delivery? The current design is at-most-once with seq gaps and no cursor, because the existing channel queue is drop-oldest.",
    ],
    "source_refs": [
        {"label": "channel config", "path": "go_hive/go-hive/internal/channels/config.go", "use": "PluginConfig and Plugins; no type/path/token fields at HEAD"},
        {"label": "channel host", "path": "go_hive/go-hive/internal/channels/host.go", "use": "startOne requires command; hello/start handshake; handlePluginRequest"},
        {"label": "channel protocol", "path": "go_hive/go-hive/internal/channels/protocol.go", "use": "hive-channel/1 JSON-RPC envelopes and Output kinds"},
        {"label": "channel subscription", "path": "go_hive/go-hive/internal/channels/subscription.go", "use": "hard-coded pluginProcess, queue, drop-oldest, seq"},
        {"label": "serve middleware", "path": "go_hive/go-hive/internal/server/app.go", "use": "desktopAuth and route wrapping; no WebSocket route"},
        {"label": "serve listener", "path": "go_hive/go-hive/internal/cli/loader.go", "use": "http.Server and channel host lifecycle"},
        {"label": "channels API", "path": "go_hive/go-hive/internal/server/routes_credentials_channels.go", "use": "GET /api/channels reads Host.States"},
        {"label": "module dependencies", "path": "go_hive/go-hive/go.mod", "use": "no WebSocket server module"},
        {"label": "RFC 6455", "path": "https://www.rfc-editor.org/rfc/rfc6455", "use": "WebSocket handshake, framing, masking, ping/pong and close"},
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
    config_rows = "".join(
        f"<tr><td>{ESC(row['level'])}</td><td><code>{ESC(row['path'])}</code></td><td>{ESC(row['own'])}</td></tr>"
        for row in data["config_contract"]["files"]
    )
    field_rows = "".join(
        f"<tr><td><code>{ESC(f['name'])}</code></td><td>{ESC(f['required'])}</td><td>{ESC(f['meaning'])}</td></tr>"
        for f in data["config_contract"]["fields"]
    )
    identity_rows = "".join(
        f"<tr><td><code>{ESC(k)}</code></td><td>{ESC(v)}</td></tr>" for k, v in data["identity_contract"].items()
    )
    close_rows = "".join(
        f"<tr><td><code>{ESC(c['code'])}</code></td><td>{ESC(c['meaning'])}</td></tr>"
        for c in data["protocol_contract"]["close_codes"]
    )
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
    config_text = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    hello_text = json.dumps(data["protocol_contract"]["hello_example"], ensure_ascii=False, indent=2)
    welcome_text = json.dumps(data["protocol_contract"]["welcome_example"], ensure_ascii=False, indent=2)
    open_text = json.dumps(data["protocol_contract"]["open_session_example"], ensure_ascii=False, indent=2)
    output_text = json.dumps(data["protocol_contract"]["output_example"], ensure_ascii=False, indent=2)
    sequence_items = lis(data["protocol_contract"]["sequence"])
    server_frame_items = lis(data["protocol_contract"]["server_frames"])
    client_method_items = lis(data["protocol_contract"]["client_methods"])
    meta = (
        f"<span>状态：<b>{ESC(data['status'])}</b></span>"
        f"<span>需求：<b>{len(data['requirements'])}</b></span>"
        f"<span>设计项：<b>{len(data['design'])}</b></span>"
        f"<span>验收：<b>{len(data['acceptance'])}</b></span>"
        f"<span>传输：<b>{ESC(data['protocol']['transport'])}</b></span>"
    )
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>channel-websocket · 需求—设计链</title>
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
<header><div class="eyebrow">GO_HIVE · CHANNELS · WEBSOCKET · REQUIREMENTS → DESIGN</div>
<h1>channel WebSocket <span>需求—设计链</span></h1>
<p class="lede">为 internal/channels 增加一个可选的 WebSocket 入站传输：语言无关的 JSON-RPC、实时双向输出，复用现有 hive serve 和会话后端。默认仍是 stdio；ws route 默认按 channel 名区分，令牌只在 Authorization header。</p>
<div class="meta">{meta}</div></header>

<section id="feasibility"><h2><span class="num">00</span>可行性判断</h2>
<div class="callout bad"><b>结论：</b>{ESC(data['feasibility']['verdict'])} <br><b>原因：</b>{ESC(data['feasibility']['why'])}</div>
<div class="grid g2"><div class="card"><div class="k">当前卡点</div><ul>{lis(data['feasibility']['current_limits'])}</ul></div><div class="card"><div class="k">可复用部分</div><ul>{lis(data['feasibility']['reusable_parts'])}</ul></div></div>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul>
<h3>前提</h3><ul>{lis(data['assumptions'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">R2/R3/R4 是配置、路径和身份边界；R5/R6 复用 stdio 语义；R8 是远程文件的关键边界。</p>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:6%">优先级</th><th style="width:38%">需求</th><th style="width:34%">验收</th><th style="width:16%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div></section>

<section id="options"><h2><span class="num">02</span>方案决策</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:30%">方案</th><th style="width:9%">结论</th><th style="width:55%">理由</th></tr></thead><tbody>{opt_rows}</tbody></table></div>
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>包边界：</b>协议与 host seam 留在 <code>internal/channels</code>；server 只做薄适配器；transport 不得 import <code>internal/server</code>、<code>internal/cli</code> 或 <code>internal/tools/builtin</code>。默认选择标准库最小 RFC 6455 实现；新增库需批准。</div></section>

<section id="protocol"><h2><span class="num">04</span>WebSocket 协议</h2>
<div class="grid g3">
<div class="card"><div class="k">传输</div><h3>{ESC(data['protocol']['transport'])}</h3><p>端点 <code>{ESC(data['protocol']['endpoint'])}</code>；子协议 <code>{ESC(data['protocol']['subprotocol'])}</code>。</p></div>
<div class="card"><div class="k">帧</div><h3>Text / JSON-RPC 2.0</h3><p>{ESC(data['protocol']['framing'])}</p></div>
<div class="card"><div class="k">认证</div><h3>{ESC(data['protocol']['auth_header'])}</h3><p>token 不在 URL；每个 channel 独立绑定。</p></div>
</div>
<h3>连接顺序</h3><ol>{sequence_items}</ol>
<h3>服务端帧</h3><ul>{server_frame_items}</ul>
<h3>客户端方法</h3><ul>{client_method_items}</ul>
<h3>hello</h3><pre>{ESC(hello_text)}</pre>
<h3>welcome</h3><pre>{ESC(welcome_text)}</pre>
<h3>open_session</h3><pre>{ESC(open_text)}</pre>
<h3>output</h3><pre>{ESC(output_text)}</pre>
<h3>关闭码</h3><div class="tablewrap"><table><thead><tr><th style="width:18%">Code</th><th style="width:82%">含义</th></tr></thead><tbody>{close_rows}</tbody></table></div></section>

<section id="config"><h2><span class="num">05</span>配置与身份</h2>
<div class="callout info"><b>配置：</b><code>{ESC(data['config_contract']['config'])}</code> <b>状态：</b><span class="pill warn">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])}</div>
<div class="tablewrap"><table><thead><tr><th style="width:14%">状态</th><th style="width:38%">路径</th><th style="width:48%">负责内容</th></tr></thead><tbody>{config_rows}</tbody></table></div>
<div class="callout info"><b>路径：</b>{ESC(data['config_contract']['path_rule'])}</div>
<h3>合法 JSON 示例</h3><pre>{ESC(config_text)}</pre>
<div class="callout bad"><b>注意：</b>用户给出的 <code>type: ws</code> 片段不是合法 JSON；字段必须写成 <code>"type": "ws"</code>，并且不能有尾逗号。不要再写 <code>path</code>；URL 固定由 channel key 派生为 <code>/channel/websocket/&lt;name&gt;</code>。</div>
<h3>字段</h3><div class="tablewrap"><table><thead><tr><th style="width:24%">字段</th><th style="width:20%">必填</th><th style="width:56%">含义</th></tr></thead><tbody>{field_rows}</tbody></table></div>
<p><b>生效：</b>{ESC(data['config_contract']['reload'])}</p>
<h3>身份绑定</h3><div class="tablewrap"><table><thead><tr><th style="width:20%">字段</th><th style="width:80%">行为</th></tr></thead><tbody>{identity_rows}</tbody></table></div></section>

<section id="failure"><h2><span class="num">06</span>失败、限额与安全</h2>
<div class="tablewrap"><table><thead><tr><th style="width:42%">场景</th><th style="width:58%">结果</th></tr></thead><tbody>{failure_rows}</tbody></table></div>
<div class="grid g2"><div class="card"><div class="k">Limits</div><ul>{limit_items}</ul></div><div class="card"><div class="k">安全底线</div><ul><li>默认关闭；只有 enabled 且 type=ws 的条目才挂载 route。</li><li>token 只在 Authorization header；禁止 query/path token，不复用 HIVE_DESKTOP_TOKEN。</li><li>Origin 存在时必须匹配 allowlist；远程必须 wss 经可信代理。</li><li>无压缩、拒绝 binary、校验 client mask、限制帧/并发/心跳。</li><li>hello 不发 state_dir、token、env value 或绝对路径。</li><li>日志不记录 Authorization、正文、附件字节。</li></ul></div></div></section>

<section id="acceptance"><h2><span class="num">07</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:28%">测试</th><th style="width:65%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div>
<h3>Review Focus</h3>
<div class="tablewrap"><table><thead><tr><th style="width:42%">未覆盖输入 / 失败模式</th><th style="width:43%">期望</th><th style="width:15%">Owner</th></tr></thead><tbody>{review_rows}</tbody></table></div>
<div class="callout bad"><b>设计边界：</b>status 仍是 <code>design</code>。没有真实 WebSocket E2E、Python/Node 客户端、stdio 回归和全量 CI 结果前，不得把本页当作实现或发布证据。</div></section>

<section id="tasks"><h2><span class="num">08</span>实施任务</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:10%">依赖</th><th style="width:44%">写入范围</th><th style="width:40%">完成检查</th></tr></thead><tbody>{task_rows}</tbody></table></div>
<div class="callout info"><b>先决条件：</b>先拍板 WebSocket 依赖、浏览器认证、file_refs 是否在 v1、第二连接策略；路径已固定为按 channel key 派生，不需要再拍板。</div></section>

<section id="risks"><h2><span class="num">09</span>风险、待拍板与来源</h2>
<div class="tablewrap"><table><thead><tr><th style="width:28%">风险</th><th style="width:27%">影响</th><th style="width:45%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>channel-websocket design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_channel_ws_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
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
            print("channel-websocket-design: DRIFT: " + ", ".join(drift))
            return 1
        print(
            f"channel-websocket-design: OK ({len(DATA['requirements'])} requirements, "
            f"{len(DATA['design'])} design, {len(DATA['acceptance'])} acceptance rows)"
        )
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"channel-websocket-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
