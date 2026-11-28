#!/usr/bin/env python3
"""Generate the go_hive ACP client requirements -> design -> acceptance chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/acp-client-design.html
  - go_hive/docs/acp-client-design.json

Usage:
  python3 go_hive/scripts/gen_acp_client_design.py
  python3 go_hive/scripts/gen_acp_client_design.py --check
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "acp-client-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "acp-client-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {
    "schema_version": 1,
    "feature": "acp_client",
    "status": "design",
    "decision": (
        "Implement a pure-Go ACP v1 client as the independent internal/acp package, "
        "expose a small independent acp_* tool family through a thin builtin adapter, "
        "and keep acpx as a behavioral reference rather than a Node dependency."
    ),
    "research": {
        "acpx_version": "0.22.0",
        "acpx_commit": "e6a5e17",
        "acpx_maturity": "pre-1.0 alpha; CLI/runtime interfaces may evolve",
        "acpx_runtime": "Node.js 22.13+ and @agentclientprotocol/sdk; not a go_hive release dependency",
        "acp_wire": "1",
        "acp_schema": "1.34.1",
        "acp_transport": "stdio NDJSON; ACP remote HTTP/WebSocket is a draft and out of v1",
        "go_sdk_candidate": "github.com/coder/acp-go-sdk v1.9.0 (schema 1.34.1, Apache-2.0, no require directives); reference/fallback, not adopted here",
        "current_go_hive": "No ACP client or acp_* tool exists at HEAD; the existing Claude Code queen runner is a Claude-specific CLI adapter, not ACP.",
        "existing_design": "go_hive/docs/agent-runner-protocol.html is a draft for a unified Runner/ACP layer, not an implementation.",
    },
    "package_layout": {
        "package": "internal/acp",
        "adapter": "internal/tools/builtin/acp_tools.go",
        "doc": "internal/acp/doc.go",
        "rule": (
            "The core package owns the ACP wire client, agent registry, session store, queue, "
            "permissions, filesystem/terminal callbacks, events and limits. It must not import "
            "internal/server, internal/cli or internal/tools/builtin. Tool schemas, approval "
            "metadata and registration live only in the adapter."
        ),
        "maintenance": (
            "Maintain acp as an independent Go package: its own API, doc.go, fake-agent tests and "
            "package gates. It must be usable without a session, HTTP server or tool registry."
        ),
    },
    "goal_beyond_request": (
        "Make external ACP coding agents first-class, independently testable tools: go_hive can "
        "delegate a bounded task to Claude, Codex, Gemini, OpenCode, Pi or Hermes without PTY "
        "scraping, Node running inside the release binary, or one bespoke adapter per agent."
    ),
    "source_refs": [
        {
            "label": "acpx overview",
            "url": "https://acpx.sh/",
            "use": "headless ACP client intent, persistent sessions, queueing, permissions, structured output",
        },
        {
            "label": "acpx sessions and queueing",
            "url": "https://acpx.sh/sessions.html",
            "use": "session scope, queue-owner lease, crash recovery, cancellation and status",
        },
        {
            "label": "acpx prompting and watches",
            "url": "https://acpx.sh/prompting.html",
            "use": "prompt/exec split, --no-wait, queue semantics, output cursors",
        },
        {
            "label": "acpx permissions",
            "url": "https://acpx.sh/permissions.html",
            "use": "approve-all/approve-reads/deny-all, per-tool policy, non-interactive deny/fail, fs/terminal guardrails",
        },
        {
            "label": "acpx config and agents",
            "url": "https://acpx.sh/config.html",
            "use": "global/project precedence, argv, auth map, MCP servers, env policy",
        },
        {
            "label": "acpx runtime lifecycle",
            "url": "https://acpx.sh/runtime-process-lifecycle.html",
            "use": "process admission, exit observation, timeouts and cleanup",
        },
        {
            "label": "acpx exit codes",
            "url": "https://acpx.sh/exit-codes.html",
            "use": "stable error categories for wrappers",
        },
        {
            "label": "acpx vision",
            "url": "https://acpx.sh/VISION.html",
            "use": "what belongs in an ACP client versus an orchestration layer",
        },
        {
            "label": "ACP v1 overview",
            "url": "https://agentclientprotocol.com/protocol/v1/overview",
            "use": "client/agent roles, baseline methods, JSON-RPC model",
        },
        {
            "label": "ACP initialization",
            "url": "https://agentclientprotocol.com/protocol/v1/initialization",
            "use": "protocolVersion, capabilities, implementation info",
        },
        {
            "label": "ACP session setup",
            "url": "https://agentclientprotocol.com/protocol/v1/session-setup",
            "use": "new/load/resume/close, cwd, mcpServers, systemPrompt, additionalDirectories",
        },
        {
            "label": "ACP prompt turn",
            "url": "https://agentclientprotocol.com/protocol/v1/prompt-turn",
            "use": "session/update variants, stop reasons, cancellation, usage and compaction",
        },
        {
            "label": "ACP permissions and tools",
            "url": "https://agentclientprotocol.com/protocol/v1/tool-calls",
            "use": "session/request_permission, tool call lifecycle and security boundary",
        },
        {
            "label": "ACP file system and terminals",
            "url": "https://agentclientprotocol.com/protocol/v1/file-system",
            "use": "fs/read_text_file, fs/write_text_file, workspace root set",
        },
        {
            "label": "ACP security",
            "url": "https://agentclientprotocol.com/protocol/v1/security",
            "use": "trust model, untrusted agent output, credentials and workspace roots",
        },
        {
            "label": "ACP schema",
            "url": "https://agentclientprotocol.com/protocol/v1/schema",
            "use": "stable wire types and method names for schema 1.34.1",
        },
        {
            "label": "coder/acp-go-sdk",
            "url": "https://github.com/coder/acp-go-sdk",
            "use": "reference Go SDK; schema pin, transport and capability patterns",
        },
        {
            "label": "acpx source",
            "url": "https://github.com/openclaw/acpx",
            "use": "runtime/queue/permission implementation evidence at commit e6a5e17",
        },
        {
            "label": "go_hive agent-runner design draft",
            "url": "agent-runner-protocol.html",
            "use": "existing Runner interface and ACP rollout intent; not implementation evidence",
        },
    ],
    "problem": {
        "statement": (
            "go_hive can talk to Claude Code through one Claude-shaped CLI adapter and can talk to "
            "MCP tool servers, but it has no generic ACP client. Every new coding agent would need "
            "another process/argument/transcript parser, and none of those agents can use the same "
            "session, permission, filesystem or event contracts."
        ),
        "user_value": [
            "Run one ACP adapter and reach Claude, Codex, Gemini, OpenCode, Pi, Hermes and custom ACP agents.",
            "Keep multi-turn coding sessions, queue prompts, cancel turns and inspect tool/plan events without PTY scraping.",
            "Apply one permission policy and one workspace-root policy instead of one security story per CLI.",
            "Use the capability as an opt-in tool family; the existing Claude Code queen runner stays untouched in v1.",
        ],
        "why_not_now": (
            "This is a post-1.0, opt-in capability. ACP is not in the current parity claim, and go_hive "
            "must not claim ACP support before the package, fake-agent tests and at least one real adapter E2E exist."
        ),
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Ship an independent internal/acp Go package with no imports of internal/server, internal/cli or internal/tools/builtin; it must be usable without a session, HTTP server or tool registry.",
            "acceptance": "Import-boundary test, package doc and package-level tests pass; no cgo and no new dependency.",
            "traces": ["D1", "A1"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "Implement ACP v1 over stdio with newline-delimited JSON-RPC, process lifecycle, bounded frames, request IDs, cancellation, stderr draining and process-tree shutdown.",
            "acceptance": "Fake ACP agent covers initialize, split frames, oversized line, EOF, cancel, crash and restart without orphan processes.",
            "traces": ["D2", "D3", "A2", "A3"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Negotiate protocol version and capabilities honestly: advertise only the client fs/terminal/auth/elicitation capabilities the implementation actually serves; reject unsupported versions and methods.",
            "acceptance": "Capability matrix, version mismatch, unknown method and unsupported optional method tests pass.",
            "traces": ["D3", "A2"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Provide a built-in agent registry and JSON config with argv-only launches; the model may select a registered agent name but may never supply an arbitrary executable, argv or environment.",
            "acceptance": "Config precedence, argv validation, command injection, missing agent and custom-agent tests pass.",
            "traces": ["D4", "A4"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Persist ACP sessions under HIVE_HOME with the scope key (resolved agent argv, absolute cwd, optional name); records are atomic, owner-only and resumable without storing callbacks, policies or secrets.",
            "acceptance": "Create/ensure/list/close, duplicate scope, corrupt record, restart and cleanup tests pass.",
            "traces": ["D5", "A5"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Support session/new, session/load and session/resume according to advertised capabilities; a failed load/resume must never silently create a different conversation.",
            "acceptance": "Capability-present and capability-absent paths, -32603 failure, no silent new-session test pass.",
            "traces": ["D6", "A5"],
        },
        {
            "id": "R7",
            "priority": "P0",
            "statement": "Run prompt turns with a stable turn id, ordered session/update handling, stop reasons, queueing, cooperative cancel and bounded timeouts; a turn must not block the whole server.",
            "acceptance": "Prompt/update/stop/cancel/timeout/queue tests and concurrent-session tests pass.",
            "traces": ["D7", "A6"],
        },
        {
            "id": "R8",
            "priority": "P0",
            "statement": "Apply an explicit ACP permission policy: approve-all, approve-reads or deny-all plus per-tool rules; non-interactive defaults deny; escalations go to the host, never to the model that requested them.",
            "acceptance": "Permission matrix, late approval after cancel, prompt-unavailable, policy precedence and no-model-self-approval tests pass.",
            "traces": ["D8", "A7"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "Serve fs/read_text_file and fs/write_text_file only inside the declared root set; reject symlink escapes, special files, hardlink writes and writes that do not recheck request lifetime.",
            "acceptance": "Path traversal, symlink, hardlink, special-file, atomic-write and disabled-fs tests pass.",
            "traces": ["D9", "A8"],
        },
        {
            "id": "R10",
            "priority": "P0",
            "statement": "Serve terminal/* only when advertised; bind terminal creation to the admitted prompt, enforce cwd/root rules, drain output, kill process groups and revoke late requests.",
            "acceptance": "Create/output/wait/kill/release, cwd escape, cancellation, late-request and disabled-terminal tests pass.",
            "traces": ["D10", "A9"],
        },
        {
            "id": "R11",
            "priority": "P0",
            "statement": "Map ACP session/update into a bounded, typed go_hive event surface: text/thought deltas, tool calls, plans, modes, config, usage, notices, compaction and session info; preserve raw structured data for audit but redact secrets.",
            "acceptance": "Event goldens, cursor ordering, backpressure, unknown-variant preservation and redaction tests pass.",
            "traces": ["D11", "A10"],
        },
        {
            "id": "R12",
            "priority": "P0",
            "statement": "Expose a small independent acp_* tool family through the builtin adapter: list agents, session lifecycle, prompt, status, cancel and control. Do not expose raw ACP methods or fs/terminal primitives as model tools.",
            "acceptance": "Schemas, registration, approval metadata, async prompt/status/cancel and no-raw-request tests pass.",
            "traces": ["D12", "A11"],
        },
        {
            "id": "R13",
            "priority": "P0",
            "statement": "Keep credentials and the host environment out of model output and logs: env allow/block lists, auth policy, no token in argv, no secret in events or errors, and no automatic action from untrusted agent text.",
            "acceptance": "Env/credential redaction, link/command refusal, process environment and log audit tests pass.",
            "traces": ["D13", "A12"],
        },
        {
            "id": "R14",
            "priority": "P1",
            "statement": "Bound every operation: initialize/session/prompt timeouts, line/stderr/event caps, max sessions, idle close, crash backoff and stable error taxonomy.",
            "acceptance": "Timeout, line cap, stderr cap, max-session, idle-close, crash-backoff and error-taxonomy tests pass.",
            "traces": ["D14", "A13"],
        },
        {
            "id": "R15",
            "priority": "P0",
            "statement": "Do not add a Node/JS runtime, bundle an ACP adapter, migrate the existing Claude Code queen runner, or expose remote ACP HTTP/WS in v1.",
            "acceptance": "Dependency audit, release-artifact audit and existing Claude runner regression suite pass.",
            "traces": ["D1", "D14", "A14", "A15"],
        },
    ],
    "non_goals": [
        "Do not port acpx flows, the TypeScript workflow engine, flow replay viewer, compare command or embedded acpx runtime.",
        "Do not implement a multi-process queue owner/shared CLI session layer in v1; one go_hive process owns its ACP sessions.",
        "Do not implement ACP remote HTTP/WebSocket transport; ACP documents it as a draft, and v1 is stdio only.",
        "Do not implement ACP v2; v1 wire protocol is the stable target. Revisit after an upstream stable release.",
        "Do not bundle Node, npx, acpx, @agentclientprotocol/sdk or any agent adapter in the release archive.",
        "Do not migrate the existing Claude Code queen runner to ACP in this design; that is a later, separately accepted migration.",
        "Do not expose raw acp_request, fs/read, fs/write or terminal tools to the model; only the curated tool family is public.",
        "Do not claim OS sandboxing. ACP is a protocol boundary; the agent binary and its own tools remain trusted processes.",
        "Do not read acpx's ~/.acpx/config.json implicitly; go_hive owns its own config and migration is explicit.",
    ],
    "assumptions": [
        "The user installs and authenticates an ACP-capable agent; absence of a binary or credential is a typed error, not a silent fallback.",
        "ACP v1 and schema 1.34.1 are the stable target; optional capabilities are treated as unsupported when omitted.",
        "Agent processes are trusted local binaries launched from registry/config, not arbitrary model output.",
        "The go_hive server can keep long-lived child processes and per-session queues; no PTY is required.",
        "The effective workspace root is controlled by the host; ACP permission and fs checks are guardrails, not an OS sandbox.",
        "The existing internal/claudecode runner remains the default Claude Code queen path until a separate migration is accepted.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "Native pure-Go ACP v1 client in internal/acp (stdlib only)",
            "verdict": "chosen",
            "reason": "Keeps CGO_ENABLED=0, no Node/JS dependency, full control over process lifecycle, permissions, session storage and event mapping; matches the repository's independent-package pattern used by lsp/astgrep.",
        },
        {
            "id": "O2",
            "name": "Use github.com/coder/acp-go-sdk",
            "verdict": "deferred",
            "reason": "Strong typed SDK with schema 1.34.1 and no require directives, but adds an external dependency and an API that is still pre-1.0. Revisit only with explicit dependency approval; a native core keeps the package independent.",
        },
        {
            "id": "O3",
            "name": "Wrap the acpx CLI / Node runtime",
            "verdict": "rejected",
            "reason": "Adds Node 22.13+, a JS package, adapter installation and two layers of process/session management. acpx is a behavioral reference, not a release dependency.",
        },
        {
            "id": "O4",
            "name": "Embed the ACP client inside the server/queen runtime",
            "verdict": "rejected",
            "reason": "Would couple protocol lifecycle, permissions and tests to internal/server or internal/cli, breaking the independent package and making fake-agent tests impossible without a full server.",
        },
        {
            "id": "O5",
            "name": "Remote ACP over HTTP/WebSocket in v1",
            "verdict": "deferred",
            "reason": "ACP remote transport is still a draft; v1 uses stdio, which is the stable transport and fits local coding agents.",
        },
        {
            "id": "O6",
            "name": "ACP v2",
            "verdict": "deferred",
            "reason": "v2 is preview/draft. v1's integer protocol version and capabilities are the stable contract; v2 should be a separate adapter after upstream stabilizes.",
        },
    ],
    "acpx_features": [
        {"area": "Persistent sessions", "acpx": "JSON record + live queue owner; multi-turn across invocations", "decision": "adopt", "go_hive": "ACP session store under HIVE_HOME; one in-process owner per session"},
        {"area": "Queue-aware prompts", "acpx": "queue while a turn runs; --no-wait", "decision": "adopt", "go_hive": "per-session FIFO queue; prompt returns turn_id immediately"},
        {"area": "Crash detection and reload", "acpx": "detect dead agent, reuse saved provider session", "decision": "adopt", "go_hive": "reap process, fail active turn, resume only when capability exists; no silent new session"},
        {"area": "Structured output", "acpx": "text/json/quiet; NDJSON ACP messages", "decision": "adopt", "go_hive": "typed events + JSON tool results; raw ACP retained only in debug/audit with redaction"},
        {"area": "Permission modes", "acpx": "approve-all/approve-reads/deny-all; per-tool policy", "decision": "adopt", "go_hive": "same model, but non-interactive default deny and no model self-approval"},
        {"area": "Filesystem/terminal guardrails", "acpx": "--cwd root set, symlink/hardlink checks, --no-fs/--no-terminal", "decision": "adopt", "go_hive": "same boundary in the core; capability toggles in config"},
        {"area": "Session controls", "acpx": "cancel, set-mode, set config/model, status", "decision": "adopt", "go_hive": "acp_cancel/acp_control; configOptions preferred over deprecated modes"},
        {"area": "Session watch/cursor", "acpx": "message/turn_started/turn_result with cursor", "decision": "adopt", "go_hive": "internal event cursor; adapter can publish to session events"},
        {"area": "Auth/env policy", "acpx": "ACPX_AUTH_*, auth map, env allow/block", "decision": "adopt", "go_hive": "HIVE_ACP_AUTH_*, envAllow/envBlock; no secrets in logs"},
        {"area": "Agent registry/config", "acpx": "built-in names, argv, global/project precedence", "decision": "adopt", "go_hive": "small embedded registry + HIVE_HOME/project/local JSON; argv only"},
        {"area": "Exit codes", "acpx": "0/1/2/3/4/5/130", "decision": "map", "go_hive": "typed Go errors; optional future CLI wrapper maps to the same categories"},
        {"area": "Flows / replay viewer", "acpx": "TypeScript workflow engine and run bundles", "decision": "defer", "go_hive": "not in v1; do not make ACP the orchestration engine"},
        {"area": "Shared sessions / queue daemon", "acpx": "createSharedAcpRuntime and multi-process owner", "decision": "defer", "go_hive": "in-process owner only; one go_hive process owns sessions"},
        {"area": "Compare / embedded runtime API", "acpx": "multi-agent compare and public acpx/runtime", "decision": "defer", "go_hive": "only the independent core and tool adapters in v1"},
        {"area": "Raw --agent / unknown command", "acpx": "Unix escape hatch", "decision": "reject", "go_hive": "model may select registered names only; custom agents are config-administered argv"},
        {"area": "Node runtime", "acpx": "requires Node 22.13+", "decision": "reject", "go_hive": "pure Go; adapters are external user-installed processes"},
    ],
    "design": {
        "D1": {
            "title": "Package boundary",
            "text": "internal/acp is a stdlib-only client library. It exposes Config, Registry, Client, Session, Turn, Event, PermissionBroker and Store. It imports no server, CLI or builtin-tool package, so fake-agent tests and a future CLI can reuse it. The adapter at internal/tools/builtin/acp_tools.go owns model schemas, approval metadata, registration and event publication.",
        },
        "D2": {
            "title": "Transport and process lifecycle",
            "text": "Launch the agent with exec.CommandContext and explicit argv. Use stdin/stdout pipes and a line scanner bounded to 4 MiB; stderr is drained into a bounded ring for diagnostics. One reader goroutine routes responses by JSON-RPC id and server requests to the client router; one writer mutex serializes outbound frames. Close stdin first, then terminate the process group, then kill after a bounded grace. Track a launchId and never let a late write target a retired process.",
        },
        "D3": {
            "title": "Protocol and capability negotiation",
            "text": "Speak ACP v1 JSON-RPC 2.0. Initialize with protocolVersion 1, clientInfo and only implemented clientCapabilities (fs read/write, terminal, elicitation, plan, session notices/compaction/subagents as applicable). Treat omitted capabilities as unsupported. Reject protocol mismatch, unknown methods and capability-gated requests with -32601/-32602. Do not expose a raw request path.",
        },
        "D4": {
            "title": "Agent registry and config",
            "text": "Ship a small embedded registry of friendly names (claude, codex, gemini, opencode, pi, hermes, cursor, copilot) with argv templates. User config merges built-in < $HIVE_HOME/acp.json < <cwd>/.hive/acp.json < <cwd>/.hive/acp.local.json. argv is required for custom agents; no shell string, no model-supplied executable, no model-supplied env. Missing binaries are a typed unavailable error.",
        },
        "D5": {
            "title": "Session store and scope",
            "text": "Store records under $HIVE_HOME/acp/sessions/<record-id>/record.json with 0600 files and 0700 directories. Scope is (resolved argv, absolute cwd, optional name). An ensure finds or creates exactly one open record for that scope. Record fields: local id, name, agent name, argv, cwd, ACP session id, optional provider session id, created/updated time, closed flag and last turn summary. Never persist permission callbacks, resolved policy or credentials.",
        },
        "D6": {
            "title": "Session lifecycle",
            "text": "session/new is the baseline. If the agent advertises sessionCapabilities.resume, prefer session/resume for a quiet reconnect; otherwise use loadSession/session/load for history replay. If neither is available, a new prompt may only continue on the live connection. A failed load/resume returns a typed error and never silently creates a replacement conversation. session/close is used when advertised; otherwise close the process locally and mark the record closed.",
        },
        "D7": {
            "title": "Prompt turn and queue",
            "text": "Every prompt gets a caller-generated turn_id. One active turn per session; additional prompts queue FIFO and return queued status. session/prompt receives content blocks and returns a stop reason (end_turn, max_tokens, max_turn_requests, refusal, cancelled). session/update is processed in wire order until the prompt response. Cancel sends session/cancel, marks unfinished tool calls cancelled, resolves pending permissions as cancelled, waits for the terminal stop reason, then retires the connection if needed.",
        },
        "D8": {
            "title": "Permissions and authentication",
            "text": "PermissionPolicy has mode (approve-all, approve-reads, deny-all), per-tool autoApprove/autoDeny/escalate/defaultAction, and nonInteractivePermissions (deny or fail). Rule precedence is deny, approve, escalate, default, mode. In a server there is no TTY, so an escalation without a host approval broker becomes deny/fail, not an implicit allow. Permission replies are a host/UI concern; the model that called acp_prompt must never approve its own request. Authentication resolves from HIVE_ACP_AUTH_<METHOD> or the credential store; authPolicy skip/fail is explicit.",
        },
        "D9": {
            "title": "Filesystem callbacks",
            "text": "Serve fs/read_text_file and fs/write_text_file only when advertised. Resolve against the session root set (cwd plus additionalDirectories), compare components after symlink resolution, reject symlink escapes, special files, hardlink writes and non-regular files. Writes use a temp file in the same directory, fsync where available, chmod from the existing file or 0600 for new files, and atomic rename. Recheck request lifetime immediately before mutation.",
        },
        "D10": {
            "title": "Terminal callbacks",
            "text": "Serve terminal/create, terminal/output, terminal/wait_for_exit, terminal/kill and terminal/release only when the terminal capability is enabled. Bind each terminal to the admitted prompt and cwd; launch argv without a shell; cap output; drain pipes; kill the whole process group on cancel; revoke late create/kill requests. --no-terminal advertises terminal=false and returns method-not-found for terminal calls.",
        },
        "D11": {
            "title": "Event mapping",
            "text": "Define a small internal Event union: text_delta, thought_delta, tool_call, tool_call_update, plan, mode_changed, config_changed, usage, notice, compaction, session_info and permission_request. Preserve unknown variants and raw _meta as an audit blob with redaction. Apply a per-session cursor and bounded queue; a slow consumer drops/queues according to policy rather than blocking the agent reader. Never write secrets or full environment into events.",
        },
        "D12": {
            "title": "Independent tool family",
            "text": "The adapter registers acp_agents, acp_session, acp_prompt, acp_status, acp_cancel and acp_control. acp_prompt is async and returns {session_id, turn_id, status}; acp_status reads state, last text, usage, pending permission and errors; acp_cancel targets one turn; acp_control uses set_config_option or set_mode. No raw request, fs, terminal or permission tool is model-facing. Permission escalations go to the host broker/UI, not the model.",
        },
        "D13": {
            "title": "Security and trust",
            "text": "Agent argv and env come only from the registry/config; the model selects a name and optional session name/cwd under the workspace. Apply envAllow/envBlock and never place tokens in argv or logs. Treat all agent-authored text, paths, URIs, commands and tool output as untrusted; do not auto-open links, run commands from agent text, or fetch remote resources. The ACP agent can still act through its own tools; ACP is not an OS sandbox.",
        },
        "D14": {
            "title": "Limits, recovery and observability",
            "text": "Defaults: initialize 15s, session/new 30s, prompt 30min, cancel grace 5s, idle close 300s, max 4 sessions, max line 4MiB, stderr 64KiB, event queue 512, pending permissions 8, max 20 events per status read. Detect EOF/exit, fail pending requests, mark the session dead and apply bounded restart only on the next prompt. Emit structured logs with agent name, record id, turn id and error code; never credentials.",
        },
    },
    "protocol": {
        "wire": "JSON-RPC 2.0; one UTF-8 JSON object per line; newline-delimited; stderr is logs only",
        "version": "ACP protocolVersion 1; schema target 1.34.1",
        "lifecycle": [
            "spawn agent",
            "initialize",
            "authenticate (optional, by advertised method)",
            "session/new | session/load | session/resume",
            "session/prompt",
            "session/update notifications (text, tool, plan, usage, notices)",
            "session/request_permission (optional)",
            "session/cancel (optional)",
            "prompt response with stopReason",
            "session/close or process shutdown",
        ],
        "client_methods": [
            "initialize",
            "authenticate",
            "session/new",
            "session/load",
            "session/resume",
            "session/close",
            "session/prompt",
            "session/cancel",
            "session/set_mode",
            "session/set_config_option",
            "logout (if advertised)",
        ],
        "agent_callbacks": [
            "session/update",
            "session/request_permission",
            "fs/read_text_file",
            "fs/write_text_file",
            "terminal/create",
            "terminal/output",
            "terminal/wait_for_exit",
            "terminal/kill",
            "terminal/release",
            "elicitation/create",
            "elicitation/complete",
        ],
        "stop_reasons": ["end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled"],
        "content_blocks": ["text", "resource_link", "image (when advertised)", "audio (when advertised)", "resource (when advertised)"],
        "permission_options": ["allow_once", "allow_always", "reject_once", "reject_always", "cancel"],
        "error_codes": {
            "-32700": "parse error",
            "-32600": "invalid request / before initialization",
            "-32601": "method not found",
            "-32602": "invalid params",
            "-32603": "internal error",
            "-32800": "request cancelled",
            "-32000": "authentication required",
            "-32002": "resource/session not found",
        },
        "transport_note": "ACP remote HTTP/WebSocket is a draft; v1 is stdio only. The package exposes a transport interface so a future remote adapter does not change the session/tool API.",
    },
    "tool_contract": [
        {
            "name": "acp_agents",
            "purpose": "List configured agent names, descriptions, availability and capability summary.",
            "risk": "read",
            "phase": "v1",
            "inputs": "none",
            "returns": "agents[] with name, title, available, capabilities and last_error",
        },
        {
            "name": "acp_session",
            "purpose": "Create, load/resume, inspect, list or close a local ACP session.",
            "risk": "control",
            "phase": "v1",
            "inputs": "action=new|load|resume|list|close; agent; name?; cwd?; session_id?",
            "returns": "session record and capability/state summary",
        },
        {
            "name": "acp_prompt",
            "purpose": "Queue one prompt turn and return immediately with a turn id.",
            "risk": "high",
            "phase": "v1",
            "inputs": "session_id; prompt text; optional text/resource content blocks",
            "returns": "{session_id, turn_id, status: queued|running} (async)",
        },
        {
            "name": "acp_status",
            "purpose": "Read current state, last text, stop reason, usage, pending permission and bounded recent events.",
            "risk": "read",
            "phase": "v1",
            "inputs": "session_id; turn_id?; event_cursor?",
            "returns": "state, last_text, usage, pending_permission, recent_events, error",
        },
        {
            "name": "acp_cancel",
            "purpose": "Cancel one queued or active turn without closing the session.",
            "risk": "control",
            "phase": "v1",
            "inputs": "session_id; turn_id?",
            "returns": "{cancelled, uncertain?, stop_reason?}",
        },
        {
            "name": "acp_control",
            "purpose": "Set an advertised mode or config option/model for a session.",
            "risk": "control",
            "phase": "v1",
            "inputs": "session_id; mode?; key?; value?; model?",
            "returns": "accepted mode/config values and remaining options",
        },
        {
            "name": "acp_permission",
            "purpose": "Host/UI decision for a pending ACP permission request.",
            "risk": "human-only",
            "phase": "v1 host API",
            "inputs": "session_id; turn_id; option_id or outcome",
            "returns": "accepted/denied/cancelled; never advertised to the model",
        },
    ],
    "config_contract": {
        "status": "frozen",
        "authority": "ACP settings are owned by the independent internal/acp package. configuration.json is not an ACP config source in v1.",
        "format": "JSON only in v1. No YAML and no implicit acpx config compatibility.",
        "files": [
            {"level": "built-in", "path": "embedded agent registry", "condition": "always", "note": "friendly names and argv templates; not a claim that every adapter is installed."},
            {"level": "user", "path": "$HIVE_HOME/acp.json", "condition": "read when present"},
            {"level": "project", "path": "<cwd>/.hive/acp.json", "condition": "read when present"},
            {"level": "local", "path": "<cwd>/.hive/acp.local.json", "condition": "gitignored machine-local override"},
        ],
        "precedence": "built-in < user < project < local < explicit tool arguments (allowed fields only)",
        "merge": "Top-level partial override. agents and auth merge by key; a same-named project agent replaces the whole agent object. Arrays are replaced. settings is shallow-merged. No deep merge.",
        "reload": "Config changes affect new sessions only. A live ACP connection keeps its resolved argv, env, cwd and permission policy; close/recreate the session to change them.",
        "settings": {
            "enabled": False,
            "defaultAgent": "claude",
            "permissionMode": "approve-reads",
            "nonInteractivePermissions": "deny",
            "authPolicy": "skip",
            "promptTimeoutSeconds": 1800,
            "initializeTimeoutSeconds": 15,
            "sessionTimeoutSeconds": 30,
            "idleTimeoutSeconds": 300,
            "maxSessions": 4,
            "maxLineBytes": 4194304,
            "maxStderrBytes": 65536,
            "maxEventQueue": 512,
            "fsEnabled": True,
            "terminalEnabled": True,
        },
        "agent_fields": {
            "required": ["argv"],
            "optional": ["description", "cwd", "env", "envAllow", "envBlock", "permissionMode", "permissionPolicy", "auth", "timeoutSeconds", "idleSeconds", "maxSessions"],
            "rule": "argv[0] is the executable and every remaining item is one literal argument. Shell strings are rejected. env values are configuration secrets and never enter model output.",
        },
        "sample": {
            "schema_version": 1,
            "settings": {
                "enabled": True,
                "defaultAgent": "claude",
                "permissionMode": "approve-reads",
                "nonInteractivePermissions": "deny",
                "authPolicy": "skip",
                "promptTimeoutSeconds": 1800,
                "idleTimeoutSeconds": 300,
                "maxSessions": 4,
            },
            "agents": {
                "claude": {
                    "description": "Claude Code through the official ACP adapter",
                    "argv": ["npx", "-y", "@agentclientprotocol/claude-agent-acp"],
                    "envAllow": ["ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"],
                    "permissionMode": "approve-reads",
                },
                "codex": {
                    "description": "Codex through the official ACP adapter",
                    "argv": ["npx", "-y", "@agentclientprotocol/codex-acp"],
                    "envAllow": ["OPENAI_API_KEY"],
                    "permissionMode": "approve-reads",
                },
                "local-acp": {
                    "argv": ["./bin/my-acp-agent", "--profile", "ci"],
                    "cwd": "/opt/agents",
                    "envAllow": ["CI"],
                    "permissionMode": "deny-all",
                },
            },
        },
    },
    "session_contract": {
        "scope": "(resolved agent argv, absolute cwd, optional session name)",
        "store": "$HIVE_HOME/acp/sessions/<record-id>/record.json; files 0600, directories 0700",
        "record_fields": ["id", "name", "agent", "agent_argv", "cwd", "acp_session_id", "agent_session_id", "created_at", "updated_at", "closed", "last_turn"],
        "queue": "FIFO per session; one active prompt turn; extra prompts return queued. No cross-session head-of-line blocking.",
        "resume": "Prefer session/resume when advertised; use session/load when loadSession is advertised; otherwise continue only on the live connection. A failed resume never creates a new conversation.",
        "close": "Use session/close when advertised; otherwise close stdin/process and mark the local record closed. Closing is idempotent.",
        "cleanup": "Idle ACP clients close after 300s. Local records are pruned only by an explicit janitor action; no silent deletion.",
        "limits": "max 4 live sessions per process; prompt 30min default; initialize 15s; session setup 30s; cancel grace 5s; max line 4MiB; stderr 64KiB; event queue 512; 8 pending permissions.",
    },
    "permission_policy": {
        "modes": {
            "approve-all": "Approve every request not resolved by a per-tool rule. Never the default; requires explicit config and host approval.",
            "approve-reads": "Approve read/search/non-mutating requests; other requests go to the policy/escalation path. Default.",
            "deny-all": "Deny every request not resolved by a per-tool rule.",
        },
        "rules": ["autoDeny", "autoApprove", "escalate", "defaultAction", "mode"],
        "non_interactive": "The server has no TTY. Unresolved requests deny by default; nonInteractivePermissions=fail returns PERMISSION_PROMPT_UNAVAILABLE.",
        "escalation": "Escalations are emitted to the host approval broker/UI. The model that requested acp_prompt cannot decide its own permission. With no broker, the request is denied or failed closed.",
        "revocation": "Cancellation, timeout, turn completion or connection close revokes pending permission requests; a late answer cannot authorize a new file/terminal operation.",
        "trust": "ACP permission is advisory: the agent executes its own tools. The client only controls its own fs/terminal callbacks. ACP is not an OS sandbox.",
    },
    "acceptance": [
        {"id": "A1", "test": "Package boundary", "pass": "go test ./internal/acp; go list -deps shows no internal/server, internal/cli or internal/tools/builtin; gofmt/vet clean."},
        {"id": "A2", "test": "ACP handshake and capabilities", "pass": "fake agent covers initialize, protocol mismatch, missing capability, unknown method and clientInfo."},
        {"id": "A3", "test": "Transport and process lifecycle", "pass": "split frames, oversized line, EOF, stderr cap, request cancel, crash and process-tree cleanup pass without orphan processes."},
        {"id": "A4", "test": "Config and agent registry", "pass": "precedence, abstract argv, env reject, missing binary, custom agent, local override and no-shell tests pass."},
        {"id": "A5", "test": "Session store and resume", "pass": "create/ensure/list/close, duplicate scope, corrupt record, restart, load/resume capability and no-silent-new-conversation tests pass."},
        {"id": "A6", "test": "Prompt turn, queue and cancel", "pass": "text/tool/plan updates, all stop reasons, queue order, turn timeout, session/cancel and concurrent sessions pass."},
        {"id": "A7", "test": "Permission matrix", "pass": "modes, per-tool precedence, noninteractive deny/fail, late approval, cancel and no-model-self-approval tests pass."},
        {"id": "A8", "test": "Filesystem callbacks", "pass": "read/write, atomic write, traversal, symlink escape, hardlink, special file, disabled fs and request-lifetime tests pass."},
        {"id": "A9", "test": "Terminal callbacks", "pass": "create/output/wait/kill/release, cwd escape, output cap, cancellation and disabled terminal tests pass."},
        {"id": "A10", "test": "Event mapping", "pass": "goldens for text/thought/tool/plan/usage/notice/compaction; unknown variant preserved; cursor and backpressure tests pass."},
        {"id": "A11", "test": "Tool adapters", "pass": "acp_agents/acp_session/acp_prompt/acp_status/acp_cancel/acp_control schemas and approval metadata match; acp_permission is not model-advertised."},
        {"id": "A12", "test": "Security and credentials", "pass": "env allow/block, secret redaction, no token in argv/events/logs, untrusted link/command refusal and process env audit pass."},
        {"id": "A13", "test": "Limits and recovery", "pass": "timeout, line/stderr/event caps, max sessions, idle close, crash backoff and error taxonomy tests pass."},
        {"id": "A14", "test": "Release matrix and dependencies", "pass": "CGO_ENABLED=0 six GOOS/GOARCH builds, go test/race, no Node/JS dependency and no bundled adapter pass."},
        {"id": "A15", "test": "Real adapter E2E", "pass": "with at least one ACP adapter installed/authenticated, new session + prompt + streamed update + cancel pass; versions and evidence recorded. Missing adapters are an external gap, not a pass."},
        {"id": "A16", "test": "Regression and no migration", "pass": "existing internal/claudecode and full CI suites pass; the Claude Code queen runner path is unchanged."},
    ],
    "review_focus": [
        {"input": "Agent exits or closes stdout while a prompt/permission is pending", "expected": "pending requests fail with exit/stderr evidence; session marked dead; no orphan process; next prompt applies bounded recovery", "owner": "T3"},
        {"input": "Permission reply arrives after cancel, timeout or a newer turn", "expected": "late reply is rejected; no file/terminal operation is authorized; turn remains cancelled/failed", "owner": "T5"},
        {"input": "session/load or session/resume is unsupported or returns -32603", "expected": "typed error; no silent session/new; existing record remains inspectable", "owner": "T3"},
        {"input": "Agent sends a path with symlink escape, hardlink, FIFO or a component-order alias", "expected": "fail closed before mutation; no write outside the root set", "owner": "T6"},
        {"input": "Two clients prompt the same session, or a turn request id is reused after queue admission", "expected": "FIFO order, one active turn, duplicate id rejected; no cross-turn event mixing", "owner": "T4"},
    ],
    "tasks": [
        {"id": "T1", "depends_on": [], "writes": ["internal/acp/doc.go", "internal/acp/config.go", "internal/acp/registry.go", "internal/acp/store.go", "internal/acp/config_test.go", "internal/acp/store_test.go"], "done": "package boundary, config precedence, argv validation and session store tests pass"},
        {"id": "T2", "depends_on": ["T1"], "writes": ["internal/acp/jsonrpc.go", "internal/acp/transport.go", "internal/acp/client.go", "internal/acp/transport_test.go"], "done": "fake-agent handshake, framing, request routing, stderr and process cleanup tests pass"},
        {"id": "T3", "depends_on": ["T1", "T2"], "writes": ["internal/acp/session.go", "internal/acp/lifecycle.go", "internal/acp/session_test.go"], "done": "new/load/resume/close, capability gates and no-silent-new-conversation tests pass"},
        {"id": "T4", "depends_on": ["T2", "T3"], "writes": ["internal/acp/turn.go", "internal/acp/queue.go", "internal/acp/events.go", "internal/acp/turn_test.go", "internal/acp/events_test.go"], "done": "prompt/update/stop/cancel/queue/event-golden tests pass"},
        {"id": "T5", "depends_on": ["T2", "T4"], "writes": ["internal/acp/permissions.go", "internal/acp/auth.go", "internal/acp/permissions_test.go"], "done": "policy matrix, noninteractive deny/fail, late approval and auth tests pass"},
        {"id": "T6", "depends_on": ["T2", "T4"], "writes": ["internal/acp/fs.go", "internal/acp/terminal.go", "internal/acp/fs_test.go", "internal/acp/terminal_test.go"], "done": "root-set, symlink/hardlink, atomic write, terminal lifecycle and cancel tests pass"},
        {"id": "T7", "depends_on": ["T3", "T4", "T5", "T6"], "writes": ["internal/tools/builtin/acp_tools.go", "internal/tools/builtin/acp_tools_test.go", "internal/tools/builtin/builtin.go"], "done": "six tool schemas, async prompt/status/cancel, approval metadata and no-raw-request tests pass"},
        {"id": "T8", "depends_on": ["T7"], "writes": ["internal/acp/e2e_test.go", "docs/acp-client-evidence.md"], "done": "fake ACP E2E plus at least one real adapter E2E recorded with versions and evidence; absent adapters recorded as external gaps"},
        {"id": "T9", "depends_on": ["T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8"], "writes": [], "done": "CGO=0 six-platform builds, full CI, dependency/Node audit, security review and independent review clean; fix rounds rerun this gate"},
    ],
    "risks": [
        {"risk": "ACP adapters differ in capabilities and event shapes", "impact": "a feature works on Claude but fails on Codex/Gemini", "mitigation": "capability negotiation, adapter matrix, unknown-variant preservation, representative fake and real E2E."},
        {"risk": "Child process leaks or a stale write reaches a retired agent", "impact": "zombie processes, cross-turn corruption", "mitigation": "launchId, process groups, close-before-kill, pending-request rejection, reaper tests."},
        {"risk": "Permission requests are mistaken for a sandbox", "impact": "false safety claim; agent executes its own tools", "mitigation": "document advisory nature, noninteractive deny, host approval, no approve-all default, security tests."},
        {"risk": "ACP schema and v2 churn", "impact": "typed structs drift; adapters break", "mitigation": "pin schema 1.34.1, v1 only, generated/handwritten boundary tests, v2 as separate future adapter."},
        {"risk": "Queue/owner complexity from acpx's multi-process model", "impact": "leaks, stale leases, duplicate turns", "mitigation": "in-process owner v1; no shared CLI runtime; explicit session scope and one active turn."},
        {"risk": "Filesystem/terminal callbacks can mutate the workspace", "impact": "data loss or escape", "mitigation": "root set, symlink/hardlink checks, atomic writes, request-lifetime checks, disabled capabilities."},
        {"risk": "Credentials leak through env, argv, events or debug logs", "impact": "account compromise", "mitigation": "env allow/block, credential store, redaction, no token in argv, log audit."},
        {"risk": "Tool family bloats the model context", "impact": "worse tool selection", "mitigation": "six high-level tools, no raw methods; feature flag and lazy registration."},
        {"risk": "No remote ACP transport", "impact": "cannot run agents on another host", "mitigation": "stdio v1; design a transport interface and defer remote HTTP/WS until stable."},
        {"risk": "Windows process/capability differences", "impact": "kill/terminal behavior diverges", "mitigation": "platform files, Windows-specific process-tree tests, evidence gap if no real Windows host."},
    ],
    "open_questions": [
        "Should v1 stay stdlib-only, or is coder/acp-go-sdk allowed as an approved dependency if the native protocol code grows too large?",
        "Which real adapters are mandatory for acceptance: Claude + Codex, or only one adapter plus a fake-agent conformance suite?",
        "Should acp_prompt require explicit human approval for every first use per agent/cwd, or is the session permission policy enough?",
        "Should the far future migrate the existing Claude Code queen runner to ACP, or keep the native Claude adapter as the stable fallback?",
        "Should v1 accept image/resource content blocks, or ship text-only prompts and add multimodal content after the core turn path is proven?",
    ],
}


def render_html(data: dict) -> str:
    def lis(items):
        return "".join(f"<li>{ESC(x)}</li>" for x in items)

    def table(headers, widths, rows):
        th = "".join(f"<th style=\"width:{ESC(w)}\">{ESC(h)}</th>" for h, w in zip(headers, widths))
        return (
            "<div class=\"tablewrap\"><table><thead><tr>"
            + th
            + "</tr></thead><tbody>"
            + "".join(rows)
            + "</tbody></table></div>"
        )

    req_rows = "".join(
        f"<tr><td><code>{ESC(r['id'])}</code></td><td>{ESC(r['priority'])}</td>"
        f"<td>{ESC(r['statement'])}</td><td>{ESC(r['acceptance'])}</td>"
        f"<td>{ESC(', '.join(r['traces']))}</td></tr>"
        for r in data["requirements"]
    )
    opt_rows = "".join(
        f"<tr><td><code>{ESC(o['id'])}</code></td><td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] == 'deferred' else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td></tr>"
        for o in data["options"]
    )
    acpx_rows = "".join(
        f"<tr><td>{ESC(r['area'])}</td><td>{ESC(r['acpx'])}</td>"
        f"<td><span class=\"pill {'ok' if r['decision'] in ('adopt', 'map') else 'warn' if r['decision'] == 'defer' else 'muted'}\">{ESC(r['decision'])}</span></td>"
        f"<td>{ESC(r['go_hive'])}</td></tr>"
        for r in data["acpx_features"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(k)}</div><h3>{ESC(v['title'])}</h3><p>{ESC(v['text'])}</p></div>"
        for k, v in data["design"].items()
    )
    config_file_rows = "".join(
        f"<tr><td><code>{ESC(row['level'])}</code></td><td><code>{ESC(row['path'])}</code></td>"
        f"<td>{ESC(row['condition'])}</td><td>{ESC(row.get('note', ''))}</td></tr>"
        for row in data["config_contract"]["files"]
    )
    tool_rows = "".join(
        f"<tr><td><code>{ESC(t['name'])}</code></td><td>{ESC(t['purpose'])}</td>"
        f"<td>{ESC(t['risk'])}</td><td>{ESC(t['phase'])}</td>"
        f"<td>{ESC(t['inputs'])}</td><td>{ESC(t['returns'])}</td></tr>"
        for t in data["tool_contract"]
    )
    acc_rows = "".join(
        f"<tr><td><code>{ESC(a['id'])}</code></td><td>{ESC(a['test'])}</td><td>{ESC(a['pass'])}</td></tr>"
        for a in data["acceptance"]
    )
    task_rows = "".join(
        f"<tr><td><code>{ESC(t['id'])}</code></td><td>{ESC(', '.join(t['depends_on']) or '—')}</td>"
        f"<td><code>{ESC(', '.join(t['writes']) or '—')}</code></td><td>{ESC(t['done'])}</td></tr>"
        for t in data["tasks"]
    )
    review_rows = "".join(
        f"<tr><td>{ESC(r['input'])}</td><td>{ESC(r['expected'])}</td><td><code>{ESC(r['owner'])}</code></td></tr>"
        for r in data["review_focus"]
    )
    risk_rows = "".join(
        f"<tr><td>{ESC(r['risk'])}</td><td>{ESC(r['impact'])}</td><td>{ESC(r['mitigation'])}</td></tr>"
        for r in data["risks"]
    )
    source_items = "".join(
        f"<li><a href=\"{ESC(s['url'])}\">{ESC(s['label'])}</a> — {ESC(s['use'])}</li>"
        for s in data["source_refs"]
    )
    method_list = "".join(f"<li><code>{ESC(x)}</code></li>" for x in data["protocol"]["client_methods"])
    callback_list = "".join(f"<li><code>{ESC(x)}</code></li>" for x in data["protocol"]["agent_callbacks"])
    stop_list = "".join(f"<li><code>{ESC(x)}</code></li>" for x in data["protocol"]["stop_reasons"])
    permission_list = "".join(f"<li><code>{ESC(x)}</code></li>" for x in data["protocol"]["permission_options"])
    content_list = "".join(f"<li><code>{ESC(x)}</code></li>" for x in data["protocol"]["content_blocks"])
    error_rows = "".join(
        f"<tr><td><code>{ESC(k)}</code></td><td>{ESC(v)}</td></tr>"
        for k, v in data["protocol"]["error_codes"].items()
    )
    settings_json = json.dumps(data["config_contract"]["settings"], ensure_ascii=False, indent=2)
    sample_json = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    agent_fields = data["config_contract"]["agent_fields"]
    lifecycle = " → ".join(data["protocol"]["lifecycle"])

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ACP client · 需求—设计链</title>
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
@media(max-width:1000px){{.g3{{grid-template-columns:repeat(2,minmax(0,1fr))}}}} @media(max-width:560px){{.g2,.g3{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
.card{{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0}} .card .k{{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700}} .card .d{{color:var(--muted);font-size:12px;margin-top:5px}}
.tablewrap{{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}}
table{{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed}} th,td{{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word}} th{{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1}} tr:hover td{{background:#121a25}} tr:last-child td{{border-bottom:0}}
code{{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}}
pre{{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2}}
.pill{{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal;margin:1px 2px 1px 0}} .pill.ok{{color:var(--ok);border-color:#1d3b26;background:#0f1a13}} .pill.warn{{color:var(--warn);border-color:#3d3013;background:#1a150a}} .pill.muted{{color:var(--muted);border-color:var(--line);background:#0f1620}}
.callout{{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4}} .callout.info{{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}} .callout.bad{{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}}
ul,ol{{margin:7px 0 7px 20px;padding:0}} li{{margin:4px 0;color:#c6d2df}} footer{{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}}
@media(max-width:560px){{table{{font-size:12px}}th,td{{padding:7px 6px}}}}
</style></head><body><div class="wrap">
<header><div class="eyebrow">GO_HIVE · ACP CLIENT · REQUIREMENTS → DESIGN</div>
<h1>ACP client <span>需求—设计链</span></h1>
<p class="lede">把外部 ACP agent 变成 go_hive 的独立工具面：纯 Go、stdio JSON-RPC、独立 internal/acp 包、独立 acp_* tools；acpx 只作为行为参考，不作为 Node 运行时依赖。</p>
<div class="meta"><span>状态：<b>{ESC(data['status'])}</b></span><span>包：<b>{ESC(data['package_layout']['package'])}</b></span><span>协议：<b>ACP v1 / schema {ESC(data['research']['acp_schema'])}</b></span><span>传输：<b>stdio only</b></span></div></header>

<section id="problem"><h2><span class="num">00</span>目标与非目标</h2>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<div class="callout"><b>研究基线：</b>acpx {ESC(data['research']['acpx_version'])} @ <code>{ESC(data['research']['acpx_commit'])}</code>（{ESC(data['research']['acpx_maturity'])}）；ACP wire {ESC(data['research']['acp_wire'])} / schema {ESC(data['research']['acp_schema'])}；go_hive 当前{ESC(data['research']['current_go_hive'])}</div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">每条需求必须落到设计项和至少一个验收项。R1/R15 是包边界与发布边界，不能当成实现细节。</p>
{table(["ID","优先级","需求","验收","链路"],["7%","7%","38%","35%","13%"],[req_rows])}</section>

<section id="options"><h2><span class="num">02</span>决策链</h2>
{table(["ID","方案","结论","理由"],["7%","27%","10%","56%"],[opt_rows])}
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="acpx"><h2><span class="num">03</span>acpx 特性吸收表</h2>
<p class="sub">acpx 是“ACP client 应该做到什么”的最佳行为参考；它不是要移植的运行时。下表的 adopt 是语义吸收，不是复制 TypeScript/Node 实现。</p>
{table(["领域","acpx 行为","决定","go_hive v1"],["17%","31%","8%","44%"],[acpx_rows])}
<div class="callout info"><b>边界：</b>不移植 flows、replay viewer、shared runtime、compare、embedded runtime；这些属于编排层，不属于独立 ACP client 包。</div></section>

<section id="design"><h2><span class="num">04</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>Package 边界：</b><code>{ESC(data['package_layout']['package'])}</code> 是独立可测核心；<code>{ESC(data['package_layout']['adapter'])}</code> 只做 schema / 审批 / 注册。{ESC(data['package_layout']['rule'])} <b>维护：</b>{ESC(data['package_layout']['maintenance'])}</div></section>

<section id="protocol"><h2><span class="num">05</span>ACP 协议与生命周期</h2>
<div class="grid g2">
<div class="card"><div class="k">Wire</div><h3>JSON-RPC 2.0 / NDJSON</h3><p>{ESC(data['protocol']['wire'])}</p></div>
<div class="card"><div class="k">Version</div><h3>ACP v1 / schema {ESC(data['research']['acp_schema'])}</h3><p>{ESC(data['protocol']['transport_note'])}</p></div>
</div>
<h3>生命周期</h3><pre>{ESC(lifecycle)}</pre>
<div class="grid g3">
<div class="card"><div class="k">Client → Agent</div><ul>{method_list}</ul></div>
<div class="card"><div class="k">Agent → Client</div><ul>{callback_list}</ul></div>
<div class="card"><div class="k">Stop / Permission</div><p class="sub">Stop reasons</p><ul>{stop_list}</ul><p class="sub">Permission options</p><ul>{permission_list}</ul></div>
</div>
<h3>Content blocks</h3><ul>{content_list}</ul>
<h3>ACP error codes</h3>{table(["code","meaning"],["12%","88%"],[error_rows])}</section>

<section id="tools"><h2><span class="num">06</span>独立 Tools</h2>
<p class="sub">工具面是独立包之上的薄适配器；v1 只暴露高层动作，不暴露 raw ACP method、fs/terminal primitive。acp_permission 只给 host/UI，不给模型。</p>
{table(["tool","用途","风险","阶段","输入","返回"],["18%","30%","9%","10%","18%","15%"],[tool_rows])}
<div class="callout bad"><b>安全线：</b>调用 <code>acp_prompt</code> 的模型不能批准自己触发的 ACP permission；无 host approval broker 时，escalation 必须 deny/fail closed。</div></section>

<section id="config"><h2><span class="num">07</span>ACP Config Scheme</h2>
<div class="callout info"><b>Config Scheme：</b><span class="pill ok">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])} <b>格式：</b>{ESC(data['config_contract']['format'])}</div>
<div class="grid g3">
<div class="card"><div class="k">文件格式</div><h3>JSON only v1</h3><p>不引入 YAML 依赖；不隐式读取 ~/.acpx/config.json。</p></div>
<div class="card"><div class="k">优先级</div><h3>{ESC(data['config_contract']['precedence'])}</h3><p>所有存在文件参与合并；agents/auth 按 key 合并，其余按层替换。</p></div>
<div class="card"><div class="k">重载</div><h3>新 session 生效</h3><p>live ACP connection 保留原 argv/env/cwd/policy；改配置后 close/recreate。</p></div>
</div>
<h3>文件与优先级</h3>{table(["层级","路径","生效条件","说明"],["12%","35%","22%","31%"],[config_file_rows])}
<h3>settings</h3><pre>{ESC(settings_json)}</pre>
<h3>Agent 字段</h3>
<p class="sub">required: <code>{ESC(', '.join(agent_fields['required']))}</code> · optional: <code>{ESC(', '.join(agent_fields['optional']))}</code></p>
<p>{ESC(agent_fields['rule'])}</p>
<h3>完整示例</h3><pre>{ESC(sample_json)}</pre>
<h3>Session 契约</h3>
<div class="tablewrap"><table><tbody>
<tr><td><b>scope</b></td><td>{ESC(data['session_contract']['scope'])}</td></tr>
<tr><td><b>store</b></td><td>{ESC(data['session_contract']['store'])}</td></tr>
<tr><td><b>record</b></td><td><code>{ESC(', '.join(data['session_contract']['record_fields']))}</code></td></tr>
<tr><td><b>queue</b></td><td>{ESC(data['session_contract']['queue'])}</td></tr>
<tr><td><b>resume</b></td><td>{ESC(data['session_contract']['resume'])}</td></tr>
<tr><td><b>close</b></td><td>{ESC(data['session_contract']['close'])}</td></tr>
<tr><td><b>cleanup</b></td><td>{ESC(data['session_contract']['cleanup'])}</td></tr>
<tr><td><b>limits</b></td><td>{ESC(data['session_contract']['limits'])}</td></tr>
</tbody></table></div></section>

<section id="permissions"><h2><span class="num">08</span>权限与安全</h2>
<div class="grid g3">
<div class="card"><div class="k">approve-all</div><p>{ESC(data['permission_policy']['modes']['approve-all'])}</p></div>
<div class="card"><div class="k">approve-reads</div><p>{ESC(data['permission_policy']['modes']['approve-reads'])}</p></div>
<div class="card"><div class="k">deny-all</div><p>{ESC(data['permission_policy']['modes']['deny-all'])}</p></div>
</div>
<p><b>规则顺序：</b><code>{ESC(' > '.join(data['permission_policy']['rules']))}</code></p>
<p><b>非交互：</b>{ESC(data['permission_policy']['non_interactive'])}</p>
<p><b>升级：</b>{ESC(data['permission_policy']['escalation'])}</p>
<p><b>撤销：</b>{ESC(data['permission_policy']['revocation'])}</p>
<p><b>信任边界：</b>{ESC(data['permission_policy']['trust'])}</p>
<div class="callout bad"><b>不要宣称 sandbox：</b>ACP 只约束 client 的 fs/terminal 回调；agent 自己的工具仍然按它自己的权限运行。用户必须信任所配置的 agent 二进制。</div></section>

<section id="acceptance"><h2><span class="num">09</span>验收链</h2>
{table(["ID","测试","通过条件"],["8%","28%","64%"],[acc_rows])}
<h3>Review Focus</h3>
{table(["未覆盖输入 / 失败模式","期望","Owner"],["35%","50%","15%"],[review_rows])}</section>

<section id="tasks"><h2><span class="num">10</span>实施任务</h2>
{table(["ID","依赖","写入范围","完成检查"],["6%","10%","40%","44%"],[task_rows])}
<div class="callout bad"><b>关闭门：</b>T9 依赖全部任务；必须以真实命令结果收口，不能以设计页、代码存在或 fake agent 代替真实 adapter E2E。</div></section>

<section id="risks"><h2><span class="num">11</span>风险、待拍板与来源</h2>
{table(["风险","影响","缓解"],["28%","27%","45%"],[risk_rows])}
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>ACP client design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_acp_client_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
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
            print("acp-client-design: DRIFT: " + ", ".join(drift))
            return 1
        print("acp-client-design: OK")
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"acp-client-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
