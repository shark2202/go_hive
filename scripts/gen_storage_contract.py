#!/usr/bin/env python3
"""Build contracts/storage-fields.json: per-file, per-field storage contract.

Two evidence sources, both recorded per entry:

1. ``observed`` — structures extracted mechanically from a finished
   storage-compat double run (docs/storage-compat/*.json plus the run's
   scratch trees). Fields get presence ``both|python_only|go_only``.
2. ``code`` — writer/reader source references for file classes the harness
   does not exercise end to end (session meta, colony sidecars, tasks...).
   These entries also carry the field list read out of the fixture seeds.

Usage:
  python3 gen_storage_contract.py --work <run scratch dir> --out contracts/storage-fields.json
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

# ---------------------------------------------------------------------------
# file-name inventory
# ---------------------------------------------------------------------------

PY_SCAN_ROOTS = [
    "core/framework/storage",
    "core/framework/host",
    "core/framework/credentials",
    "core/framework/tasks",
    "core/framework/agent_loop",
    "core/framework/memory",
    "core/framework/server",
    "core/framework/skills",
]
GO_SCAN_ROOTS = [
    "go_hive/go-hive/internal/storage",
    "go_hive/go-hive/internal/colony",
    "go_hive/go-hive/internal/tracker",
    "go_hive/go-hive/internal/credentials",
    "go_hive/go-hive/internal/tasks",
    "go_hive/go-hive/internal/agentloop",
    "go_hive/go-hive/internal/session",
    "go_hive/go-hive/internal/memory",
    "go_hive/go-hive/internal/server",
    "go_hive/go-hive/internal/skills",
    "go_hive/go-hive/internal/mcpregistry",
]

FILE_RE = re.compile(r"""["'`]([A-Za-z0-9_.-]+\.(?:json|jsonl|db|enc|lock))["'`]""")
# Names that are developer-facing project files rather than HIVE_HOME storage.
EXCLUDE_NAMES = {
    "package.json",
    "package-lock.json",
    "tsconfig.json",
    "tsconfig.node.json",
    "go.mod",
    "go.sum",
    "pyproject.toml",
    "uv.lock",
    "checkpoint.sh",
}


def scan_names(root: Path, suffixes: tuple[str, ...]) -> set[str]:
    names: set[str] = set()
    if not root.is_dir():
        return names
    for path in sorted(root.rglob("*")):
        if path.suffix not in suffixes or not path.is_file():
            continue
        if path.name.endswith("_test.go") or "/tests/" in str(path) or path.name.startswith("test_"):
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for line in text.splitlines():
            if any(marker in line for marker in ("TempDir", "mkstemp", "NamedTemporaryFile", "tempfile.")):
                continue
            for match in FILE_RE.finditer(line):
                name = match.group(1)
                if name in EXCLUDE_NAMES:
                    continue
                names.add(name)
    return names


def inventory() -> dict:
    py: set[str] = set()
    go: set[str] = set()
    for rel in PY_SCAN_ROOTS:
        py |= scan_names(REPO / rel, (".py",))
    for rel in GO_SCAN_ROOTS:
        go |= scan_names(REPO / rel, (".go",))
    return {
        "method": "quoted filename literals in storage-relevant modules (see module lists in gen_storage_contract.py)",
        "python_roots": PY_SCAN_ROOTS,
        "go_roots": GO_SCAN_ROOTS,
        "counts": {"python": len(py), "go": len(go), "both": len(py & go), "python_only": len(py - go), "go_only": len(go - py)},
        "shared": sorted(py & go),
        "python_only": sorted(py - go),
        "go_only": sorted(go - py),
    }


# ---------------------------------------------------------------------------
# observed structures
# ---------------------------------------------------------------------------


def flatten(value, prefix: str, out: dict[str, str]) -> None:
    if value is None:
        out[prefix or "$"] = "null"
    elif isinstance(value, bool):
        out[prefix or "$"] = "bool"
    elif isinstance(value, int):
        out[prefix or "$"] = "int"
    elif isinstance(value, float):
        out[prefix or "$"] = "float"
    elif isinstance(value, str):
        out[prefix or "$"] = "string"
    elif isinstance(value, list):
        out[prefix or "$"] = "array"
        if value:
            flatten(value[0], (prefix + "[]") if prefix else "[]", out)
    elif isinstance(value, dict):
        out[prefix or "$"] = "object"
        for key in sorted(value):
            flatten(value[key], f"{prefix}.{key}" if prefix else key, out)


def load_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError):
        return None


def observed_fields(tree: Path, rel_candidates: list[str]) -> dict[str, str]:
    for rel in rel_candidates:
        path = tree / rel
        if path.exists():
            data = load_json(path)
            if data is not None:
                out: dict[str, str] = {}
                flatten(data, "", out)
                return out
    return {}


def presence_diff(py_fields: dict, go_fields: dict) -> dict:
    fields = {}
    for key in sorted(set(py_fields) | set(go_fields)):
        py_type = py_fields.get(key)
        go_type = go_fields.get(key)
        if py_type and go_type:
            presence = "both" if py_type == go_type else "both_type_diff"
        elif py_type:
            presence = "python_only"
        else:
            presence = "go_only"
        fields[key] = {"python": py_type, "go": go_type, "presence": presence}
    return fields


def probe_entries(work: Path) -> list[dict]:
    probe_py = work / "probes-py"
    probe_go = work / "probes-go"
    base_py = "queens/queen_growth/sessions/session_compat_probe_py"
    base_go = "queens/queen_growth/sessions/session_compat_probe_go"
    entries = []

    def entry(name: str, glob: str, py_rel: str, go_rel: str, refs: list[str]) -> dict:
        py_fields = observed_fields(probe_py, [py_rel])
        go_fields = observed_fields(probe_go, [go_rel])
        return {
            "name": name,
            "path_glob": glob,
            "evidence": "observed (probe double run)",
            "code_refs": refs,
            "fields": presence_diff(py_fields, go_fields),
        }

    entries.append(entry(
        "session_state",
        "queens/<queen>/sessions/<sid>/state.json",
        f"{base_py}/state.json",
        f"{base_go}/state.json",
        ["framework/schemas/session_state.py::SessionState", "internal/schemas/session_state.go::SessionState"],
    ))
    entries.append(entry(
        "conversation_meta",
        ".../conversations/meta.json",
        f"{base_py}/conversations/meta.json",
        f"{base_go}/conversations/meta.json",
        ["framework/agent_loop/conversation.py::_persist_meta", "internal/agentloop/conversation.go::persistMeta"],
    ))
    entries.append(entry(
        "conversation_cursor",
        ".../conversations/cursor.json",
        f"{base_py}/conversations/cursor.json",
        f"{base_go}/conversations/cursor.json",
        [
            "framework/agent_loop/conversation.py::_write_next_seq",
            "framework/agent_loop/internals/cursor_persistence.py (recent_responses/recent_tool_fingerprints/pending_input/user_stopped)",
            "internal/agentloop/store.go::writeCursorNextSeq",
            "internal/server/chat_park.go (pending_input)",
        ],
    ))
    for seq, note in [("0000000000", "user/client-input part"), ("0000000001", "assistant part with tool_calls"), ("0000000002", "tool result with is_skill_content")]:
        entries.append(entry(
            f"conversation_part_{seq}",
            f".../conversations/parts/{seq}.json ({note})",
            f"{base_py}/conversations/parts/{seq}.json",
            f"{base_go}/conversations/parts/{seq}.json",
            [
                "framework/agent_loop/conversation.py::Message.to_storage_dict / from_storage_dict",
                "internal/agentloop/message.go::ToStorageDict / MessageFromStorageDict",
            ],
        ))
    entries.append(entry(
        "checkpoint_index",
        ".../checkpoints/index.json",
        f"{base_py}/checkpoints/index.json",
        f"{base_go}/checkpoints/index.json",
        ["framework/storage/checkpoint_store.py::_update_index_add", "internal/storage/checkpoint_store.go::updateIndexAdd"],
    ))
    entries.append(entry(
        "checkpoint_file",
        ".../checkpoints/cp_*.json",
        f"{base_py}/checkpoints/cp_node_complete_start_20260101T000000.json",
        f"{base_go}/checkpoints/cp_node_complete_start_20260101T000000.json",
        ["framework/schemas/checkpoint.py::Checkpoint", "internal/schemas/checkpoint.go::Checkpoint"],
    ))
    entries.append(entry(
        "colony_metadata",
        "colonies/<colony>/metadata.json",
        "colonies/probe_colony/metadata.json",
        "colonies/probe_colony/metadata.json",
        ["framework/host/colony_metadata.py", "internal/server/routes_colonies.go::ensureMinimalColony"],
    ))
    entries.append(entry(
        "credentials_index",
        "credentials/metadata/index.json",
        "credentials/metadata/index.json",
        "credentials/metadata/index.json",
        [
            "framework/credentials/storage.py::_index_entry_for (INDEX_VERSION 2.0)",
            "internal/credentials/storage.go::indexEntryFor (IndexVersion 2.0)",
        ],
    ))
    return entries


# ---------------------------------------------------------------------------
# code-referenced classes (not exercised end to end by the probes)
# ---------------------------------------------------------------------------

CODE_CLASSES = [
    {
        "name": "queen_session_meta",
        "path_glob": "queens/<queen>/sessions/<sid>/meta.json (and colony overseer variants)",
        "evidence": "fixture-seeded + writer code read",
        "code_refs": [
            "framework/server/session_manager.py (session_id/created_at/updated_at/agent_path/colony_id/queen_id/title...)",
            "internal/server/routes_*.go + internal/session/session.go",
        ],
        "fields_note": "Both sides read tolerant dicts; no fixed schema class. Fields observed in the migration fixtures: session_id, created_at, updated_at, agent_path, colony_fork, queen_id, colony_id.",
    },
    {
        "name": "colony_worker_json",
        "path_glob": "colonies/<colony>/worker.json",
        "evidence": "writer code read",
        "code_refs": [
            "framework/server/session_manager.py::_ensure_minimal_colony",
            "internal/server/routes_colonies.go::ensureMinimalColony",
        ],
        "fields_note": "Both writers emit name, description, tools[], goal{description}, optional queen_name — identical key sets.",
    },
    {
        "name": "colony_tools_json",
        "path_glob": "colonies/<colony>/tools.json",
        "evidence": "writer code read",
        "code_refs": ["framework/host/colony_tools_config.py::_make_sidecar_payload", "internal/server/routes_colony_tools.go"],
        "fields_note": "enabled_mcp_tools, updated_at, saved_on_version (both sides).",
    },
    {
        "name": "colony_triggers_json",
        "path_glob": "colonies/<colony>/triggers.json",
        "evidence": "fixture-seeded (raw list of trigger entries)",
        "code_refs": ["framework/host/triggers.py::TriggerDefinition", "internal/triggers (TriggerDefinition)"],
        "fields_note": "id, trigger_type, trigger_config, description, task, enabled, last_fired_at, next_due_at.",
    },
    {
        "name": "tasks_json",
        "path_glob": "<session>/tasks.json",
        "evidence": "writer code read (harness does not exercise the task store)",
        "code_refs": ["framework/tasks/store.py (TaskListDocument: meta + hwm + tasks)", "internal/tasks/store.go"],
        "fields_note": "Top level: meta, hwm, tasks. Not mechanically diffed in this run.",
    },
    {
        "name": "credential_enc_payload",
        "path_glob": "credentials/credentials/<credential_id>.enc (Fernet token -> JSON)",
        "evidence": "observed via cross-read (both directions succeeded)",
        "code_refs": [
            "framework/credentials/models.py::CredentialObject/CredentialKey",
            "internal/credentials/models.go::CredentialObject/CredentialKey",
        ],
        "fields_note": "Payload fields: id, credential_type, keys{name:{name,value,expires_at,metadata}}, provider_id, last_refreshed, auto_refresh, last_used, use_count, description, tags, created_at, updated_at. Both sides read each other's files (docs/storage-compat/credentials-crossread.json).",
    },
    {
        "name": "memories",
        "path_glob": "memories/**/*.md, memories/**/index.json",
        "evidence": "not part of this harness (covered by the memory double run)",
        "code_refs": ["framework/agents/queen/memory*", "internal/memory/*"],
        "fields_note": "Markdown files; no JSON field contract beyond the memory index (already double-run separately).",
    },
    {
        "name": "mcp_registry_installed",
        "path_glob": "mcp_registry/installed.json, trusted_repos.json",
        "evidence": "writer code read",
        "code_refs": ["framework/server/routes_mcp.py", "internal/mcpregistry"],
        "fields_note": "Registry JSON; no schema class on either side beyond dict passthrough.",
    },
]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", required=True, help="scratch dir from storage_compat_run.sh")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    work = Path(args.work).resolve()
    out_path = Path(args.out).resolve()

    try:
        head = subprocess.run(["git", "rev-parse", "--short", "HEAD"], cwd=REPO, capture_output=True, text=True, check=False).stdout.strip()
    except OSError:
        head = ""

    contract = {
        "meta": {
            "description": "Field-level storage contract for HIVE_HOME, derived from the Python/Go storage-compat double run.",
            "baseline_commit": head,
            "run_artifacts": "docs/storage-compat/",
            "generator": "go_hive/scripts/gen_storage_contract.py",
            "presence_values": {
                "both": "field present on both sides with the same JSON type",
                "both_type_diff": "field present on both sides, types differ",
                "python_only": "present only in Python-produced files",
                "go_only": "present only in Go-produced files",
            },
        },
        "file_inventory": inventory(),
        "observed_classes": probe_entries(work),
        "code_classes": CODE_CLASSES,
    }
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(contract, indent=2, sort_keys=False), encoding="utf-8")

    inv = contract["file_inventory"]["counts"]
    diffs = []
    for entry in contract["observed_classes"]:
        bad = {k: v for k, v in entry["fields"].items() if v["presence"] != "both"}
        if bad:
            diffs.append({"class": entry["name"], "fields": bad})
    print(json.dumps({
        "out": str(out_path),
        "inventory": inv,
        "observed_classes_with_field_diffs": diffs,
    }, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
