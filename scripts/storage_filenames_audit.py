#!/usr/bin/env python3
"""SC-08: verify the single-side filename lists in
contracts/storage-fields.json#file_inventory, name by name.

The contract's file inventory scans a fixed set of module directories on each
side. A name that only appears in a module outside those roots is reported as
"python_only" / "go_only" even though the other implementation may use the same
filename elsewhere, or the file may be a packaged resource rather than
HIVE_HOME user data. This audit re-scans both trees, records real hit sites,
and applies a curated verdict per name:

  兼容          same filename semantics on both sides, a rebuildable cache, a
                packaged (non-HIVE_HOME) resource, or additive single-side state
                the other runtime neither reads nor writes
  单向兼容      one direction works only (e.g. Go writes a format Python does
                not read); data is not damaged but the feature is invisible to
                the other runtime
  不兼容        the other implementation has no equivalent; a Python-only user
                data file that Go ignores (feature gap, not a migration blocker)

Verdicts are curated from code reads; the hit sites are recomputed on every run
so the evidence stays traceable. Stdlib only.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

VERDICTS: dict[str, dict[str, str]] = {
    # ---- contract "python_only" list ------------------------------------
    ".janitor.lock": {
        "verdict": "兼容",
        "note": "Both sides take this session lock (Python retention.py/session_manager.py; Go internal/maintenance/retention.go JanitorLockName).",
    },
    "0000000000.json": {
        "verdict": "兼容",
        "note": "Conversation part filename is `{seq:010d}.json` on both sides (Python conversation_store.py:77; Go writes the same 10-digit names, proven by the storage-compat parts diff). The literal only appears in a Python docstring/skip check.",
    },
    "agent.json": {
        "verdict": "兼容",
        "note": "Repo-packaged built-in agent descriptor read by the Python loader/credential setup; not HIVE_HOME user data, so Go needs no reader.",
    },
    "attached_credentials.json": {
        "verdict": "兼容",
        "note": "Pinned credentials per session at HIVE_HOME/sessions/<sid>/attached_credentials.json. Python writes it (agent_loop/internals/credential_tool.py:311); Go reads and writes the same path and shape (internal/tools/credentials_tool.go, wired from internal/server/chat_credentials_tool.go), and migrates the older session-local file on first write. Tests: internal/server/chat_credentials_tool_test.go, internal/tools/credentials_tool_test.go.",
    },
    "conversations.json": {
        "verdict": "兼容",
        "note": "Claude-Code queen conversation index, used by both sides (Python server/claude_code_queens.py; Go internal/claudecode + server/claude_code_queens.go). The Go hit lives outside the contract's scanned roots, which is why it looked single-side.",
    },
    "mail.jsonl": {
        "verdict": "兼容",
        "note": "Queen Mail store, used by both sides (Python server/queen_mail.py; Go internal/queenmail). Outside the contract's Go roots.",
    },
    "mcp_servers.json": {
        "verdict": "兼容",
        "note": "Legacy per-agent MCP server list read by both pipeline stages (Python pipeline/stages/mcp_registry.py; Go internal/pipeline/stage_mcp_registry.go).",
    },
    "model_catalog.json": {
        "verdict": "兼容",
        "note": "Curated model metadata is a packaged resource, not HIVE_HOME data: Python loads the file next to llm/model_catalog.py; Go embeds its copy via go:embed.",
    },
    "notifications.json": {
        "verdict": "兼容",
        "note": "Sentinel notification store, both sides (Python sentinel/store.py; Go internal/sentinel/store.go).",
    },
    "report_manifest.json": {
        "verdict": "兼容",
        "note": "Session report bundle manifest, both sides (Python server/_session_report.py; Go internal/sessionreport).",
    },
    "runs.jsonl": {
        "verdict": "兼容",
        "note": "Run lifecycle log: Python appends (host/execution_manager.py) and discovers (agents/discovery.py); Go reads it in agents/discovery.go but does not append (its chat turns have no graph run_id, and its worker dirs are worker_* which Python's session_ filter excludes). One-way read compatibility is documented and tested; no conversion exists.",
    },
    "user_report.json": {
        "verdict": "兼容",
        "note": "User-facing session report, both sides (Python server/_session_report.py; Go internal/sessionreport).",
    },
    # ---- contract "go_only" list ----------------------------------------
    ".credentials.json": {
        "verdict": "兼容",
        "note": "External Claude credential file (~/.claude/.credentials.json), read by both sides; not HIVE_HOME data.",
    },
    ".play.json": {
        "verdict": "单向兼容",
        "note": "Go-native playbook definition at colonies/<id>/playbooks/<name>.play.json (internal/playbook/rows.go, colony/playbook_tools.go). Go also reads Python's .play.py, and now writes a generated .play.py mirror beside a native playbook (internal/playbook/python_mirror.go): Python can list/run it, a hand-written mirror is never overwritten, and .play.json stays authoritative on the Go side. Tests: internal/playbook/python_mirror_test.go.",
    },
    "_tools.json": {
        "verdict": "兼容",
        "note": "Go native mcp2cli tool cache (internal/mcpregistry/nativecli/cache.go); Python drives the external mcp2cli with its own cache. Rebuildable cache, no user data.",
    },
    "antigravity-accounts.json": {
        "verdict": "兼容",
        "note": "Antigravity account list at HIVE_HOME/antigravity-accounts.json, read by both sides (Python loader/agent_loader.py; Go internal/server/llm_config.go).",
    },
    "auth.json": {
        "verdict": "兼容",
        "note": "External Codex auth file (~/.codex/auth.json), read/refreshed by both sides; not HIVE_HOME data.",
    },
    "catalog.json": {
        "verdict": "兼容",
        "note": "queens/catalog.json (removed-queen registry), both sides (Python agents/queen/queen_profiles.py; Go internal/queen/profiles.go + routes_queens.go).",
    },
    "config.json": {
        "verdict": "兼容",
        "note": "MCP registry config at mcp_registry/config.json, both sides (Python loader/mcp_registry.py; Go internal/mcpregistry/registry.go).",
    },
    "installed.json": {
        "verdict": "兼容",
        "note": "MCP registry installed-server list, both sides (Python loader/mcp_registry.py + routes_mcp.py; Go internal/mcpregistry).",
    },
    "last_fetched.json": {
        "verdict": "兼容",
        "note": "MCP registry index-fetch stamp, both sides.",
    },
    "registry_index.json": {
        "verdict": "兼容",
        "note": "MCP registry static index cache, both sides (Python loader/mcp_registry*.py; Go internal/mcpregistry/registry.go + cli.go).",
    },
    "signals.jsonl": {
        "verdict": "兼容",
        "note": "Go-only improvement/reflexion signals (internal/colony/improvement.go); Python has no counterpart store. Additive runtime telemetry, ignored by Python.",
    },
    "usage.json": {
        "verdict": "兼容",
        "note": "Go-only usage caches (nativecli cache + sender rotation); non-authoritative and rebuildable, no Python reader needed.",
    },
}

SKIP_DIRS = {"__pycache__", "node_modules", ".git"}
LINE_CAP = 3
FILE_SKIP = re.compile(r"(^|/)(tests?|_test)(/|\.|$)")


def scan(root: Path, needle: str, limit: int = LINE_CAP) -> list[str]:
    hits: list[str] = []
    if not root.is_dir():
        return hits
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.suffix not in (".py", ".go"):
            continue
        rel = path.relative_to(root)
        if any(part in SKIP_DIRS for part in rel.parts):
            continue
        if FILE_SKIP.search(str(rel)) or path.name.endswith("_test.go"):
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        if needle not in text:
            continue
        for lineno, line in enumerate(text.splitlines(), 1):
            if needle in line:
                hits.append(f"{path}:{lineno}: {line.strip()[:180]}")
                break
        if len(hits) >= limit:
            break
    return hits


def within(roots: list[str], hit: str, base: Path) -> bool:
    try:
        rel = str(Path(hit.split(":", 1)[0]).resolve().relative_to(base.resolve()))
    except ValueError:
        return False
    return any(rel == root or rel.startswith(root.rstrip("/") + "/") for root in roots)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=".", help="repository root")
    parser.add_argument("--contract", default="contracts/storage-fields.json")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    root = Path(args.root).resolve()
    contract = json.loads((root / args.contract).read_text(encoding="utf-8"))
    inventory = contract["file_inventory"]
    py_roots = inventory["python_roots"]
    go_roots = inventory["go_roots"]

    py_base = root / "core"
    go_base = root / "go_hive" / "go-hive"
    names = [("python_only", name) for name in inventory["python_only"]]
    names += [("go_only", name) for name in inventory["go_only"]]

    results = []
    missing_verdict = []
    for side, name in names:
        curated = VERDICTS.get(name)
        if curated is None:
            missing_verdict.append(name)
            curated = {"verdict": "UNREVIEWED", "note": ""}
        py_hits = scan(root / "core" / "framework", name)
        go_hits = scan(root / "go_hive" / "go-hive" / "internal", name)
        results.append({
            "name": name,
            "contract_side": side,
            "python_hits": py_hits,
            "go_hits": go_hits,
            "python_hit_inside_contract_roots": any(within(py_roots, hit, py_base) for hit in py_hits),
            "go_hit_inside_contract_roots": any(within(go_roots, hit, go_base) for hit in go_hits),
            "verdict": curated["verdict"],
            "note": curated["note"],
        })

    counts: dict[str, int] = {}
    for item in results:
        counts[item["verdict"]] = counts.get(item["verdict"], 0) + 1

    # How many of the contract's "single-side" names are actually used by the
    # other side outside the contract's scanned roots?
    other_side_hits = [
        item["name"] for item in results
        if (item["contract_side"] == "python_only" and item["go_hits"])
        or (item["contract_side"] == "go_only" and item["python_hits"])
    ]

    report = {
        "contract": args.contract,
        "method": (
            "Re-scan core/framework (Python) and go_hive/go-hive/internal (Go) "
            "for each name in contracts/storage-fields.json#file_inventory "
            "single-side lists; verdicts are curated from the hit sites. "
            "'*_hit_inside_contract_roots' shows whether the hit would have "
            "been visible to the contract's narrower scan roots."
        ),
        "summary": {
            "names": len(results),
            "verdicts": counts,
            "names_also_used_by_the_other_side": len(other_side_hits),
            "names_also_used_by_the_other_side_list": other_side_hits,
        },
        "names": results,
    }
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2, sort_keys=True), encoding="utf-8")

    print(json.dumps(report["summary"], indent=2, ensure_ascii=False))
    if missing_verdict:
        print("missing verdicts:", missing_verdict, file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
