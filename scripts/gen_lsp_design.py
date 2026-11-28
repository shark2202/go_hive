#!/usr/bin/env python3
"""Generate the go_hive LSP requirements -> design -> acceptance chain.

The DATA object is the source of truth. Outputs:
  - go_hive/docs/lsp-design.html
  - go_hive/docs/lsp-design.json

Usage:
  python3 go_hive/scripts/gen_lsp_design.py
  python3 go_hive/scripts/gen_lsp_design.py --check
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "lsp-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "lsp-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


DATA = {
    "schema_version": 1,
    "feature": "lsp",
    "status": "design",
    "decision": "Implement a pure-Go, stdio JSON-RPC LSP client and the read-only lsp tool first; no cgo and no bundled language servers.",
    "package_layout": {
        "package": "internal/lsp",
        "adapter": "internal/tools/builtin/lsp.go",
        "doc": "internal/lsp/doc.go",
        "rule": "The core package owns config/discovery, JSON-RPC transport, client lifecycle, documents, positions, diagnostics and edits. It must not import internal/server, internal/cli or internal/tools/builtin. The tool schema, approval metadata and registration live in the adapter package.",
        "maintenance": "Maintain lsp as an independent Go package: its own API, doc.go, fake-server tests and package gates. It must be usable without a session, HTTP server or tool registry.",
    },
    "goal_beyond_request": "Give go_hive agents IDE-level semantic navigation, diagnostics and auditable refactoring, while keeping the release binary pure Go and the language-server ecosystem an explicit external dependency.",
    "source_refs": [
        {
            "label": "omp Code intelligence",
            "url": "https://omp.sh/docs/code-intelligence",
            "use": "feature intent, lazy/shared server model, settings and limitations",
        },
        {
            "label": "omp LSP configuration",
            "url": "https://github.com/can1357/oh-my-pi/blob/main/docs/lsp-config.md",
            "use": "auto-detection, 55 built-in definitions, precedence, merge and reload semantics",
        },
        {
            "label": "omp lsp tool contract",
            "url": "https://github.com/can1357/oh-my-pi/blob/main/docs/tools/lsp.md",
            "use": "14 actions, inputs, output, approvals, timeout and caps",
        },
        {
            "label": "LSP 3.17 specification",
            "url": "https://microsoft.github.io/language-server-protocol/specifications/lsp/3.17/specification/",
            "use": "stdio framing, JSON-RPC lifecycle, document sync and workspace edit semantics",
        },
    ],
    "problem": {
        "statement": "go_hive has no semantic code-intelligence surface. Text search and AST search can locate syntax, but cannot answer what a symbol means, where it is defined, which files reference it, or whether a rename is type-safe.",
        "user_value": [
            "Ask the same language server an editor uses for definitions, references, types, diagnostics and fixes.",
            "Perform semantic renames that follow imports, re-exports and cross-file references.",
            "Use it only when a matching project marker and server binary exist; never invent a result when no server is available.",
            "Keep the Go core CGO_ENABLED=0; language servers remain external processes selected by the user.",
        ],
        "why_not_now": "This is a post-1.0 opt-in capability. Hive upstream has no lsp tool or LSP client, so this is not part of the current parity claim.",
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "Expose a model-facing tool named lsp whose action enum covers the read-only set first: status, capabilities, diagnostics, definition, type_definition, implementation, references, hover and symbols.",
            "acceptance": "Tool definition and action validation are frozen by a golden test; unknown actions fail closed.",
            "traces": ["D1", "D4", "A1", "A2"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "Implement the client with Go stdlib only: JSON-RPC over stdio, Content-Length framing, request IDs, cancellation, timeouts and process lifecycle.",
            "acceptance": "A fake LSP server exercises initialize, request, notification, response, error, cancel, crash and restart; no new Go module is required.",
            "traces": ["D2", "A3", "A4"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "Auto-detect servers from embedded defaults and user config ($HIVE_HOME/lsp.json): cwd-only root markers by default, an optional absolute per-server rootDir pin, fileTypes, disabled, local-bin then PATH command resolution.",
            "acceptance": "Config precedence, shallow merge, object-field replacement, disabled, unresolved-binary and rootDir parse/validate/rooted cases pass table tests.",
            "traces": ["D3", "A5"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "Map LSP UTF-16 positions to Go byte offsets and back for line/symbol resolution, including CRLF, CJK and emoji.",
            "acceptance": "Round-trip matrix covers ASCII, CJK, emoji, tabs, CRLF and a symbol repeated on the same line with #N.",
            "traces": ["D5", "A6"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "Synchronize only the documents a request needs: didOpen, full-text didChange, didClose after idle, version tracking and disk reconciliation.",
            "acceptance": "Document manager tests cover open, change, stale version, external edit, closed file, symlink and non-regular file refusal.",
            "traces": ["D6", "A7"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "Return a bounded, deterministic result: grouped locations with file:line:col context, diagnostics severity sorting, dedupe, caps and no-match text.",
            "acceptance": "Golden output tests cover definition/references/hover/symbols/diagnostics, truncation and server failure text.",
            "traces": ["D7", "A8"],
        },
        {
            "id": "R7",
            "priority": "P1",
            "statement": "Apply semantic write actions only with explicit approval: rename as a preview by default, then apply; code_actions; rename_file.",
            "acceptance": "Preview and apply goldens, WorkspaceEdit validation, conflicting edits, version mismatch and rollback tests pass.",
            "traces": ["D8", "A9"],
        },
        {
            "id": "R8",
            "priority": "P1",
            "statement": "Bound every request and workspace scan: tool timeout 5..300s, startup, project-load, diagnostics, process count, glob and symbol caps.",
            "acceptance": "Timeout, cancellation, idle shutdown, crash backoff, max-process and oversized-frame tests pass.",
            "traces": ["D9", "D10", "A10"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "Do not expose arbitrary LSP requests to the model in v1. request is deferred until the read and write surfaces are audited.",
            "acceptance": "Schema rejects request; no raw method dispatch path is reachable from the tool.",
            "traces": ["D4", "A2"],
        },
        {
            "id": "R10",
            "priority": "P1",
            "statement": "Keep server configuration and execution trustworthy: model cannot choose binary/args; config comes only from $HIVE_HOME/lsp.json; secrets and server stderr stay out of normal tool output.",
            "acceptance": "Config-to-command path; malicious config; stderr redaction; workspace-root and symlink tests pass.",
            "traces": ["D10", "A11"],
        },
    ],
    "non_goals": [
        "Do not bundle gopls, pyright, rust-analyzer or any language server in the go_hive release archive.",
        "Do not add a DAP/debugger client in this feature; that is a separate protocol and product surface.",
        "Do not implement a full VS Code-compatible client in v1; only the capabilities the tool actions need.",
        "Do not claim parity with omp's 55-server catalog; ship an embedded catalog and test a representative subset, with custom servers supported through config.",
        "Do not support YAML config in v1 unless a YAML dependency is separately approved. JSON is the v1 format.",
        "Do not expose raw LSP request, remote LSP, browser/editor UI, or background workspace indexing in v1.",
    ],
    "assumptions": [
        "A user who wants LSP can install a language server; absence is an explicit unavailable result, not a reason to fake semantics.",
        "The session directory is the default workspace root; root-marker detection follows the omp cwd-only rule, not a parent-directory walk. Configuration is separate: it comes only from $HIVE_HOME/lsp.json (there is no session-level file). A definition may instead pin an absolute rootDir, which becomes that server's workspace root.",
        "Read-only actions are safe to auto-run within the tool policy; rename, code actions and file renames require explicit write approval.",
        "All source positions are converted through one position mapper; no tool path is allowed to use raw byte columns as LSP columns.",
    ],
    "options": [
        {
            "id": "O1",
            "name": "Use a Go LSP library",
            "verdict": "rejected",
            "reason": "The protocol surface needed here is small and stateful; an external library adds dependency and lifecycle semantics we would still have to audit. Stdlib keeps CGO=0 and the release surface controlled.",
        },
        {
            "id": "O2",
            "name": "Embed tree-sitter / ast-grep instead",
            "verdict": "rejected",
            "reason": "Syntax parsing cannot answer type, reference or rename semantics. It is a different feature; ast_grep remains the syntactic fallback.",
        },
        {
            "id": "O3",
            "name": "Pure-Go stdio JSON-RPC client + external servers",
            "verdict": "chosen",
            "reason": "No cgo, no new Go dependency, matches the LSP ecosystem and keeps server installation explicit. The work is in lifecycle, document sync and edit safety.",
        },
        {
            "id": "O4",
            "name": "Shared broker / lspmux in v1",
            "verdict": "deferred",
            "reason": "Useful for multi-session reuse, but adds a daemon, socket protocol and cross-session document cleanup. Private per-project clients first.",
        },
    ],
    "design": {
        "D1": {
            "title": "Tool surface",
            "text": "Register lsp only when $HIVE_HOME/lsp.json settings.enabled is true. V1 registers the read-only actions and returns a typed unsupported-action error for write actions until P1. The full target enum is documented but not exposed as a false promise.",
        },
        "D2": {
            "title": "Transport and lifecycle",
            "text": "One process per (workspace, server) with a reader goroutine and a writer protected by a mutex. Frame with Content-Length; cap frame size; route responses by JSON-RPC id; turn notifications into events; cancel with $/cancelRequest; kill the process group on shutdown. Initialize with processId, clientInfo, rootUri, workspaceFolders and only the capabilities the client implements.",
        },
        "D3": {
            "title": "Config and discovery",
            "text": "The independent internal/lsp loader merges embedded defaults with $HIVE_HOME/lsp.json only. The session directory is the file tools' sandbox root and is agent-writable, so <sessionDir>/.hive/lsp.json and .hive/lsp.local.json are never read (see go_hive/docs/config-roots.html). Shallow merge per server; object-valued fields replace as a whole. Auto-detect requires a workspace root marker and a resolvable command in workspace-local bins first, then PATH; a definition may pin an absolute validated rootDir instead, which roots discovery, local bins, the server process cwd/rootUri, documents and workspace scans there. Invalid new servers are ignored with a warning; invalid overrides retain the previous definition, and a missing rootDir directory reports the server as unrooted with a reason.",
        },
        "D4": {
            "title": "Action router",
            "text": "Resolve file -> server by fileTypes, excluding disabled servers and preferring primary over isLinter. Read-only actions use one client lookup. Workspace forms are explicit: diagnostics/symbols can use file='*'; status ignores file; capabilities may inspect all. No request action in v1.",
        },
        "D5": {
            "title": "Position mapper",
            "text": "LSP positions are zero-based line plus UTF-16 code units. Build a per-line index from UTF-8 bytes to UTF-16 columns and back. Resolve symbol columns with identifier boundaries, exact case first then case-insensitive, occurrence selector name#N, line-local only. Never use Go byte offsets as LSP character offsets.",
        },
        "D6": {
            "title": "Document manager",
            "text": "Track uri, languageId, version, text and last-used time. Open a document before a position request; reconcile from disk when the file changed; send didChange with the full text for v1; close idle documents after five minutes. Refuse non-regular files and paths outside the workspace.",
        },
        "D7": {
            "title": "Diagnostics and results",
            "text": "Keep push diagnostics per URI/version. For a single file wait for a fresh publish or pull diagnostic if supported; do not call an empty stale publish clean. Sort by severity and range; dedupe by range+message. Render deterministic text; cap references and symbols; expose structured details for the UI.",
        },
        "D8": {
            "title": "Workspace edits",
            "text": "Parse both changes and documentChanges. Validate URIs, versions, ranges and overlapping edits before touching disk. Apply per-file with a temp file and atomic rename; keep enough original bytes to roll back the whole operation on failure. rename defaults to preview, apply requires explicit approval; rename_file sends willRenameFiles/didRenameFiles and rolls back reference edits if the filesystem move fails.",
        },
        "D9": {
            "title": "Timeouts and resource bounds",
            "text": "Tool timeout 5..300s, default 20s. Warmup 5s; project-load wait 15s; single-file diagnostics 3s; glob diagnostics 400ms/file; max 20 glob files; max 200 workspace symbols; max 50 contextual references; max 2 workspace checkers. Idle client sweep 60s; open document idle 5min; failed initialization backoff 3min.",
        },
        "D10": {
            "title": "Security and trust",
            "text": "Server command and args come only from embedded defaults or $HIVE_HOME/lsp.json, never from model input. Do not log secrets or full environment. Redact stderr to a bounded tail. Enforce workspace-root and symlink checks. Write actions are approval-gated. Raw request is absent in v1.",
        },
    },
    "protocol": {
        "framing": [
            "Content-Length: <bytes>\\r\\n",
            "Content-Type: application/vscode-jsonrpc; charset=utf-8 (optional on read)",
            "\\r\\n",
            "JSON body",
        ],
        "client_capabilities": [
            "workspace.workspaceFolders",
            "workspace.configuration",
            "workspace.applyEdit",
            "textDocument.synchronization.dynamicRegistration",
            "textDocument.definition.linkSupport",
            "textDocument.hover.contentFormat",
            "textDocument.diagnostic",
            "window.workDoneProgress",
        ],
        "request_examples": {
            "initialize": {"processId": "number|null", "rootUri": "file URI", "capabilities": "object", "workspaceFolders": "array"},
            "definition": {"textDocument": {"uri": "file URI"}, "position": {"line": "0-based", "character": "UTF-16 code units"}},
            "rename": {"textDocument": {"uri": "file URI"}, "position": "Position", "newName": "string"},
            "codeAction": {"textDocument": {"uri": "file URI"}, "range": "Range", "context": {"diagnostics": "array"}},
        },
        "lifecycle": ["spawn", "initialize", "initialized", "didOpen/didChange", "request(s)", "shutdown", "exit", "wait with bounded grace", "kill process group"],
    },
    "config_contract": {
        "status": "frozen",
        "authority": "LSP configuration is owned by the independent internal/lsp package. One operator-level JSON config file ($HIVE_HOME/lsp.json) is merged over embedded defaults; configuration.json and session-directory files are not LSP config sources in v1.",
        "format": "JSON only in v1. YAML is deliberately out of scope until a YAML dependency is separately approved.",
        "files": [
            {"level": "built-in", "path": "embedded defaults", "condition": "always", "note": "55 server definitions shipped as data, not a promise that every server is installed."},
            {"level": "user", "path": "$HIVE_HOME/lsp.json", "condition": "read when present", "note": "The only on-disk level. Session-directory files are agent-writable and are never read (see go_hive/docs/config-roots.html)."},
        ],
        "precedence": "built-in < user",
        "merge": "The user file is merged over the built-in defaults. Per-server merge is shallow: a user server object overrides top-level fields; settings, initOptions, capabilities and workspaceReadyTimings replace as whole objects, never deep-merge. Servers absent from the user file retain built-in values.",
        "cache": "Load and cache the merged config per home. Root-marker and binary discovery are evaluated against the workspace root (session directory) unless a server definition pins rootDir, in which case they use that absolute directory; no parent-directory walk either way.",
        "reload": "After changing $HIVE_HOME/lsp.json, root markers or installed binaries, call lsp action=reload file=*; reload clears the home cache and restarts servers whose identity changed.",
        "top_level": ["schema_version", "settings", "idleTimeoutMs", "servers"],
        "settings": {
            "enabled": True,
            "lazy": True,
            "requestTimeout": 10,
            "formatOnWrite": False,
            "diagnosticsOnWrite": True,
            "diagnosticsOnEdit": False,
            "shared": False,
        },
        "server_required": ["command", "fileTypes", "rootMarkers"],
        "server_optional": ["args", "languageId", "initOptions", "settings", "disabled", "warmupTimeoutMs", "isLinter", "watchFiles", "rootDir"],
        "sample": {
            "schema_version": 1,
            "settings": {
                "enabled": True,
                "lazy": True,
                "requestTimeout": 10,
                "diagnosticsOnWrite": True,
            },
            "idleTimeoutMs": 300000,
            "servers": {
                "gopls": {
                    "command": "gopls",
                    "args": ["serve"],
                    "fileTypes": [".go", ".mod", ".sum"],
                    "rootMarkers": ["go.mod", "go.work", "go.sum"],
                },
                "pyright": {
                    "command": "pyright-langserver",
                    "args": ["--stdio"],
                    "fileTypes": [".py", ".pyi"],
                    "rootMarkers": ["pyproject.toml", "pyrightconfig.json"],
                },
            },
        },
    },
    "catalog": {
        "embedded": 55,
        "tested_representatives": ["gopls", "pyright", "rust-analyzer", "typescript-language-server", "clangd", "yamlls", "bashls"],
        "rule": "Catalog entries are data, not a promise that every server is installed or certified. The acceptance suite tests a representative cross-language subset and records missing binaries as an external evidence gap.",
    },
    "acceptance": [
        {"id": "A1", "test": "Tool schema golden", "pass": "lsp name, action enum, required fields and approval metadata match the frozen contract."},
        {"id": "A2", "test": "Action validation", "pass": "unknown action, missing file/query/new_name, invalid payload and request action all fail closed."},
        {"id": "A3", "test": "Fake LSP lifecycle", "pass": "initialize, initialized, request, response, notification, error, cancel, crash and restart pass."},
        {"id": "A4", "test": "Framing and limits", "pass": "split frames, extra headers, oversized frame, malformed JSON and EOF are handled without panic."},
        {"id": "A5", "test": "Config matrix", "pass": "precedence, shallow merge, object replacement, disabled, missing binary, cwd-only root marker, optional rootDir parse/validate/rooted and custom server pass."},
        {"id": "A6", "test": "UTF-16 matrix", "pass": "ASCII, CJK, emoji, tabs, CRLF and #N round-trip exactly."},
        {"id": "A7", "test": "Document sync", "pass": "open, full change, external edit, stale version, idle close, non-regular and symlink cases pass."},
        {"id": "A8", "test": "Read-only result goldens", "pass": "definition/references/hover/symbols/diagnostics text, sorting, dedupe, caps and no-match text match goldens."},
        {"id": "A9", "test": "WorkspaceEdit", "pass": "preview, apply, overlap, version mismatch, rollback and rename_file pass with no partial mutation."},
        {"id": "A10", "test": "Bounds and cancellation", "pass": "timeouts, $/cancelRequest, process crash backoff, idle close and max-process cap pass."},
        {"id": "A11", "test": "Security", "pass": "config command injection, path escape, symlink escape, stderr redaction and write approval checks pass."},
        {"id": "A12", "test": "No-cgo release matrix", "pass": "CGO_ENABLED=0 go test and six GOOS/GOARCH builds pass."},
        {"id": "A13", "test": "Real server E2E", "pass": "with gopls installed, definition/references/hover/diagnostics/rename run against a fixture module; if absent, recorded as an external gap, not a pass."},
        {"id": "A14", "test": "Dependency audit", "pass": "no new Go module; no bundled server; runtime missing-server text is explicit."},
    ],
    "review_focus": [
        {"input": "CJK/emoji and CRLF in position mapping", "expected": "LSP column stays UTF-16-correct; no off-by-one or invalid range", "owner": "T3"},
        {"input": "diagnostics published before initialize completes", "expected": "queued or ignored by version, never reported as current", "owner": "T4"},
        {"input": "server crash with pending request", "expected": "pending requests fail with exit/stderr evidence; restart is bounded", "owner": "T2"},
        {"input": "WorkspaceEdit overlapping edits or stale document version", "expected": "reject before disk mutation; no partial file write", "owner": "T6"},
        {"input": "config points to a malicious binary or path outside workspace", "expected": "explicit config trust boundary; path checks fail closed", "owner": "T1"},
    ],
    "tasks": [
        {"id": "T1", "depends_on": [], "writes": ["internal/lsp/config.go", "internal/lsp/defaults.json", "internal/lsp/discovery.go", "internal/lsp/config_test.go"], "done": "config/discovery tests pass; embedded catalog loads; forbidden command path rejected"},
        {"id": "T2", "depends_on": ["T1"], "writes": ["internal/lsp/transport.go", "internal/lsp/client.go", "internal/lsp/protocol.go", "internal/lsp/transport_test.go"], "done": "fake server lifecycle/framing/cancel/crash tests pass"},
        {"id": "T3", "depends_on": ["T2"], "writes": ["internal/lsp/document.go", "internal/lsp/position.go", "internal/lsp/document_test.go", "internal/lsp/position_test.go"], "done": "UTF-16/document sync matrix passes"},
        {"id": "T4", "depends_on": ["T2", "T3"], "writes": ["internal/lsp/router.go", "internal/lsp/read_actions.go", "internal/lsp/results.go", "internal/lsp/read_actions_test.go"], "done": "read-only action goldens and diagnostics freshness pass"},
        {"id": "T5", "depends_on": ["T1", "T4"], "writes": ["internal/lsp/workspace.go", "internal/lsp/catalog.go", "internal/lsp/workspace_test.go"], "done": "workspace diagnostics/symbols/status/capabilities and caps pass"},
        {"id": "T6", "depends_on": ["T3", "T4"], "writes": ["internal/lsp/edits.go", "internal/lsp/write_actions.go", "internal/lsp/edits_test.go"], "done": "preview/apply/rollback/version/overlap tests pass"},
        {"id": "T7", "depends_on": ["T5", "T6"], "writes": ["internal/tools/builtin/lsp.go", "internal/tools/builtin/lsp_test.go", "internal/tools/builtin/registry.go"], "done": "lsp tool registration, approvals and schema golden pass"},
        {"id": "T8", "depends_on": ["T7"], "writes": ["internal/lsp/e2e_test.go", "docs/lsp-research.md"], "done": "fake-server E2E plus optional real gopls E2E recorded with evidence"},
        {"id": "T9", "depends_on": ["T8"], "writes": [], "done": "CGO=0 six-platform build, full package tests, schema/approval audit and dependency audit pass; independent review clean"},
    ],
    "risks": [
        {"risk": "LSP servers are heavy and may take seconds to initialize", "impact": "first call latency and user-visible timeouts", "mitigation": "lazy start, bounded project-load wait, idle shutdown, explicit status and no silent fallback."},
        {"risk": "UTF-16 position conversion is easy to get subtly wrong", "impact": "queries hit wrong columns or corrupt edits", "mitigation": "single position mapper, CJK/emoji/CRLF matrix, never use byte columns directly."},
        {"risk": "WorkspaceEdit application can partially mutate a project", "impact": "data loss / broken worktree", "mitigation": "validate all edits first, atomic per-file writes, transaction rollback, preview default for rename."},
        {"risk": "Config execution is a trust boundary", "impact": "arbitrary binary execution", "mitigation": "embedded/user config only, model cannot set command/args, write approval, path and stderr controls."},
        {"risk": "55-server catalog is broader than the tested set", "impact": "untested servers may fail", "mitigation": "call the catalog data; test seven representative servers; missing binaries are evidence gaps, not passes."},
        {"risk": "No shared broker means duplicate servers across sessions", "impact": "memory use", "mitigation": "defer shared mux; cap processes; start private clients only."},
    ],
    "open_questions": [
        "Should v1 read-only ship before write actions, or is rename required in the first release?",
        "Which representative servers are mandatory for acceptance: gopls + pyright, or all seven?",
    ],
}


def render_html(data: dict) -> str:
    def lis(items):
        return "".join(f"<li>{ESC(x)}</li>" for x in items)

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
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(k)}</div><h3>{ESC(v['title'])}</h3><p>{ESC(v['text'])}</p></div>"
        for k, v in data["design"].items()
    )
    config_file_rows = "".join(
        f"<tr><td><code>{ESC(row['level'])}</code></td><td><code>{ESC(row['path'])}</code></td><td>{ESC(row['condition'])}</td><td>{ESC(row.get('note', ''))}</td></tr>"
        for row in data["config_contract"]["files"]
    )
    config_settings = json.dumps(data["config_contract"]["settings"], ensure_ascii=False, indent=2)
    config_sample = json.dumps(data["config_contract"]["sample"], ensure_ascii=False, indent=2)
    action_rows = "".join(
        f"<tr><td><code>{ESC(a)}</code></td><td>{ESC(state)}</td></tr>"
        for a, state in [
            ("status", "v1 read-only"), ("capabilities", "v1 read-only"),
            ("diagnostics", "v1 read-only"), ("definition", "v1 read-only"),
            ("type_definition", "v1 read-only"), ("implementation", "v1 read-only"),
            ("references", "v1 read-only"), ("hover", "v1 read-only"),
            ("symbols", "v1 read-only"), ("rename", "P1 write / approval"),
            ("code_actions", "P1 write / approval"), ("rename_file", "P1 write / approval"),
            ("reload", "P1 control"), ("request", "deferred / not exposed"),
        ]
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
    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>lsp · 需求—设计链</title>
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
.grid{{display:grid;gap:12px}} .g2{{grid-template-columns:repeat(2,minmax(0,1fr))}} .g3,.priority{{grid-template-columns:repeat(3,minmax(0,1fr))}} .grid>*,.priority>*{{min-width:0}} .priority{{display:grid;gap:12px}}
@media(max-width:1000px){{.g3,.priority{{grid-template-columns:repeat(2,minmax(0,1fr))}}}} @media(max-width:560px){{.g3,.g2,.priority{{grid-template-columns:1fr}}.wrap{{padding:30px 16px 60px}}h1{{font-size:24px}}}}
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
<header><div class="eyebrow">GO_HIVE · LSP · REQUIREMENTS → DESIGN</div>
<h1>lsp <span>需求—设计链</span></h1>
<p class="lede">用纯 Go 的 stdio JSON-RPC 客户端接入项目已有的语言服务器，先做只读语义导航与诊断，再按审批边界补齐 rename/code actions。外部语言服务器是运行时依赖，不是 go_hive 发布包的一部分。</p>
<div class="meta"><span>状态：<b>{ESC(data['status'])}</b></span><span>目标：<b>CGO_ENABLED=0</b></span><span>核心动作：<b>9 read-only + 3 write 后置</b></span></div></header>

<section id="problem"><h2><span class="num">00</span>目标与非目标</h2>
<div class="callout info"><b>目标之外：</b>{ESC(data['goal_beyond_request'])}</div>
<p>{ESC(data['problem']['statement'])}</p>
<div class="grid g2"><div class="card"><div class="k">用户价值</div><ul>{lis(data['problem']['user_value'])}</ul></div><div class="card"><div class="k">为什么不是当前 parity</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>非目标</h3><ul>{lis(data['non_goals'])}</ul></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">每条需求必须落到一个设计项和至少一个验收项；R9/R10 是安全与信任边界，不是可选优化。</p>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:7%">优先级</th><th style="width:38%">需求</th><th style="width:35%">验收</th><th style="width:13%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div></section>

<section id="options"><h2><span class="num">02</span>决策链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:27%">方案</th><th style="width:10%">结论</th><th style="width:56%">理由</th></tr></thead><tbody>{opt_rows}</tbody></table></div>
<div class="callout"><b>选择：</b>{ESC(data['decision'])}</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<h3>协议生命周期</h3><pre>{ESC(' → '.join(data['protocol']['lifecycle']))}</pre>
<h3>帧格式</h3><pre>{ESC(json.dumps(data['protocol']['framing'], ensure_ascii=False, indent=2))}</pre></section>

<section id="config"><h2><span class="num">04</span>LSP Config Scheme</h2>
<div class="callout info"><b>Config Scheme：</b><span class="pill ok">{ESC(data['config_contract']['status'])}</span> <b>归属：</b>{ESC(data['config_contract']['authority'])} <b>格式：</b>{ESC(data['config_contract']['format'])}</div>
<div class="grid g3">
<div class="card"><div class="k">文件格式</div><h3>JSON only v1</h3><p>只有一个 operator 级 JSON 文件（$HIVE_HOME/lsp.json）；不引入 YAML 依赖，不读 configuration.json 或会话目录的 LSP 配置。</p></div>
<div class="card"><div class="k">优先级</div><h3>{ESC(data['config_contract']['precedence'])}</h3><p>所有存在文件都参与合并，不是 first-file-only。</p></div>
<div class="card"><div class="k">缓存</div><h3>per-home + reload</h3><p>配置缓存按 HIVE_HOME；修改 $HIVE_HOME/lsp.json、root marker 或二进制后必须 reload。</p></div>
</div>
<h3>文件与优先级</h3>
<div class="tablewrap"><table><thead><tr><th style="width:13%">层级</th><th style="width:32%">路径</th><th style="width:25%">生效条件</th><th style="width:30%">说明</th></tr></thead><tbody>{config_file_rows}</tbody></table></div>
<h3>settings</h3>
<pre>{ESC(config_settings)}</pre>
<h3>完整示例</h3>
<pre>{ESC(config_sample)}</pre>
<p><b>合并规则：</b>{ESC(data['config_contract']['merge'])}</p>
<p><b>发现与缓存：</b>{ESC(data['config_contract']['cache'])} <b>Reload：</b>{ESC(data['config_contract']['reload'])}</p>
<h3>服务器字段</h3>
<p class="sub">新服务器必须有：<code>{ESC(', '.join(data['config_contract']['server_required']))}</code>。可选：<code>{ESC(', '.join(data['config_contract']['server_optional']))}</code>。覆盖内置服务器时 required 字段可继承；无效的新服务器忽略并告警，无效 override 保留旧定义。</p>
<p class="sub">内置目录：{ESC(data['catalog']['embedded'])} 个 omp-style 定义；验收代表：{ESC(', '.join(data['catalog']['tested_representatives']))}。目录是数据，不等于每个服务器都已安装或认证。</p>
<div class="callout info"><b>Package 边界：</b><code>{ESC(data['package_layout']['package'])}</code> 是独立可测的核心包；<code>{ESC(data['package_layout']['adapter'])}</code> 只做工具 schema / 审批 / 注册适配。{ESC(data['package_layout']['rule'])} <b>维护：</b>{ESC(data['package_layout']['maintenance'])}</div></section>

<section id="actions"><h2><span class="num">05</span>动作面与审批</h2>
<div class="tablewrap"><table><thead><tr><th style="width:35%">action</th><th style="width:65%">阶段 / 权限</th></tr></thead><tbody>{action_rows}</tbody></table></div>
<div class="callout info"><b>写操作边界：</b>rename 默认预览，显式 apply 才落盘；code_actions 默认列出，显式 apply 才执行；raw request 在 v1 不暴露。</div></section>

<section id="acceptance"><h2><span class="num">06</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:8%">ID</th><th style="width:28%">测试</th><th style="width:64%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div>
<h3>Review Focus</h3>
<div class="tablewrap"><table><thead><tr><th style="width:35%">未覆盖输入 / 失败模式</th><th style="width:50%">期望</th><th style="width:15%">Owner</th></tr></thead><tbody>{review_rows}</tbody></table></div></section>

<section id="tasks"><h2><span class="num">07</span>实施任务</h2>
<div class="tablewrap"><table><thead><tr><th style="width:6%">ID</th><th style="width:10%">依赖</th><th style="width:40%">写入范围</th><th style="width:44%">完成检查</th></tr></thead><tbody>{task_rows}</tbody></table></div>
<div class="callout bad"><b>关闭门：</b>T9 依赖全部任务；必须以真实命令结果收口，不能以设计页或代码存在代替验证。</div></section>

<section id="risks"><h2><span class="num">08</span>风险与来源</h2>
<div class="tablewrap"><table><thead><tr><th style="width:28%">风险</th><th style="width:27%">影响</th><th style="width:45%">缓解</th></tr></thead><tbody>{risk_rows}</tbody></table></div>
<h3>待拍板</h3><ul>{lis(data['open_questions'])}</ul>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>lsp design · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_lsp_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
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
            print("lsp-design: DRIFT: " + ", ".join(drift))
            return 1
        print("lsp-design: OK")
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"lsp-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
