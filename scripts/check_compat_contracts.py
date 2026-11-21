#!/usr/bin/env python3
"""compat-check: frozen-contract self-consistency gate (offline, stdlib only).

The compatibility contracts under ``go_hive/contracts/`` are the frozen
surface a delivery promises to keep. Their full ``--check`` regeneration
lives in the freeze lanes; this gate is the cheap, standing CI check that the
*committed* contracts are internally consistent and still match the tree:

  C1  env-vars.json / config-keys.json structure: schema version, inventory
      <-> variables[], status vocabulary, unique config key paths
  C2  env audit counts reproduce the pinned expectation (python / go / common
      / python_only / go_only)
  C3  configuration.json contract: every reader anchor still resolves in the
      checked-out tree (a moved/deleted reader is drift)
  C4  storage-fields.json: unique classes, allowed presence values, non-empty
      evidence, file_inventory partition (shared / python_only / go_only)
  C5  DB schema: tracker ``SCHEMA_VERSION`` (python) == ``SchemaVersion`` (go)
      and both bootstrap DDLs create the same table set
  C6  API routes: ``export_api_routes.py --check``
  C7  API response shapes: ``check_api_proto.py``
  C8  tool definitions: ``freeze_tool_contract.py check``
  C9  api-responses corpus: index <-> diff <-> files on disk
  C10 tool-face extras (tool-surface-flat / browser tool defs / tool-output
      golden): needs ``uv``; SKIP with the reason when it is unavailable

Nothing here touches the network or the project virtualenv. A check that
cannot run is SKIP with a stated reason — never a silent pass; the summary
prints the skip count next to the verdict.

Usage:
    python3 go_hive/scripts/check_compat_contracts.py [--root DIR] [--json FILE]
"""

from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

ENV_STATUS_VOCAB = {
    "aligned",
    "default_differs",
    "default_and_parse_differ",
    "parse_differs",
    "python_only",
    "go_only",
    "python_dev_only",
    "go_dev_only",
    "python_test_only",
    "go_test_only",
    "dev_only",
    "test_only",
    "mention_only",
    "literal_only",
    "dev_eval",
}
CFG_STATUS_VOCAB = {
    "aligned",
    "go_partial",
    "python_only",
    "go_only",
    "unread",
    "default_differs",
    "type_differs",
    "parse_differs",
}
PRESENCE_VOCAB = {"both", "both_type_diff", "python_only", "go_only"}

results: list[dict] = []


def record(cid: str, title: str, status: str, detail: str) -> None:
    results.append({"id": cid, "title": title, "status": status, "detail": detail})


def load_json(path: Path):
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    except json.JSONDecodeError as exc:
        raise ValueError(f"{path}: invalid JSON: {exc}") from exc


def run_script(args: list[str], cwd: Path) -> tuple[int, str]:
    proc = subprocess.run(
        [sys.executable, *args],
        cwd=str(cwd),
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    return proc.returncode, proc.stdout.strip()


def check_env_config(root: Path) -> None:
    """C1-C3: env/config manifest consistency and tree anchors."""
    problems: list[str] = []
    try:
        env = load_json(root / "go_hive/contracts/env-vars.json")
        cfg = load_json(root / "go_hive/contracts/config-keys.json")
    except ValueError as exc:
        record("C1", "env/config manifests parse", "FAIL", str(exc))
        return
    if env is None or cfg is None:
        record("C1", "env/config manifests parse", "FAIL", "env-vars.json or config-keys.json missing")
        return

    if env.get("schema_version") != 1 or cfg.get("schema_version") != 1:
        problems.append(f"schema_version env={env.get('schema_version')} cfg={cfg.get('schema_version')} (want 1)")
    variables = env.get("variables", [])
    inventory = env.get("inventory", {})
    inv_names = set(inventory.get("common", [])) | set(inventory.get("python_only", [])) | set(inventory.get("go_only", []))
    var_names = {v.get("name") for v in variables}
    if len(var_names) != len(variables):
        problems.append("duplicate env names in variables[]")
    if inv_names - var_names:
        problems.append(f"inventory names missing from variables[]: {sorted(inv_names - var_names)[:6]}")
    if var_names - inv_names:
        problems.append(f"variables[] names outside the inventory: {sorted(var_names - inv_names)[:6]}")
    bad_status = sorted({v.get("status") for v in variables} - ENV_STATUS_VOCAB)
    if bad_status:
        problems.append(f"unknown env status values: {bad_status}")
    for v in variables:
        if not v.get("python") or not v.get("go"):
            problems.append(f"env {v.get('name')} lacks a per-side evidence block")
            break
    if problems:
        record("C1", "env/config manifests self-consistent", "FAIL", "; ".join(problems))
    else:
        record(
            "C1",
            "env/config manifests self-consistent",
            "PASS",
            f"{len(variables)} env names, inventory {len(inv_names)}; config schema_version=1",
        )

    # C2: counts vs the pinned expectation.
    sys.path.insert(0, str(root / "go_hive/scripts"))
    try:
        import freeze_env_config_contract as freeze  # noqa: PLC0415
    except Exception as exc:  # pragma: no cover - import guard
        record("C2", "env audit counts reproduce the pinned expectation", "SKIP", f"freeze scanner unavailable: {exc}")
        record("C3", "configuration.json reader anchors resolve", "SKIP", f"freeze scanner unavailable: {exc}")
        return

    audit = freeze.scan_audit_literals(root)
    reproduced = {
        "python": len(audit["python"]),
        "go": len(audit["go"]),
        "common": len(audit["common"]),
        "python_only": len(audit["python_only"]),
        "go_only": len(audit["go_only"]),
    }
    expected = env.get("verification", {}).get("expected", freeze.AUDIT_EXPECTED)
    if reproduced != expected:
        record(
            "C2",
            "env audit counts reproduce the pinned expectation",
            "FAIL",
            f"tree={reproduced} expected={expected} (regenerate the manifest when the env surface changes)",
        )
    else:
        record("C2", "env audit counts reproduce the pinned expectation", "PASS", json.dumps(reproduced, sort_keys=True))

    sys.path.insert(0, str(root / "go_hive/scripts"))
    try:
        import env_config_contract_data as data  # noqa: PLC0415
    except Exception as exc:  # pragma: no cover - import guard
        record("C3", "configuration.json reader anchors resolve", "SKIP", f"contract data unavailable: {exc}")
        return
    stale: list[str] = []
    for entry in data.CONFIG_KEYS:
        for side in ("py", "go"):
            for anchor in entry.get(side, []):
                try:
                    freeze.resolve_evidence(root, anchor)
                except SystemExit as exc:
                    stale.append(f"{entry.get('path')} [{side}] {exc}")
    if stale:
        record("C3", "configuration.json reader anchors resolve", "FAIL", f"{len(stale)} stale anchor(s): {stale[:3]}")
    else:
        record(
            "C3",
            "configuration.json reader anchors resolve",
            "PASS",
            f"{len(data.CONFIG_KEYS)} config keys, every reader anchor resolves",
        )


def check_storage(root: Path) -> None:
    """C4: storage-fields.json self-consistency."""
    try:
        storage = load_json(root / "contracts/storage-fields.json")
    except ValueError as exc:
        record("C4", "storage contract self-consistent", "FAIL", str(exc))
        return
    if storage is None:
        record("C4", "storage contract self-consistent", "FAIL", "contracts/storage-fields.json missing")
        return
    problems: list[str] = []
    classes = list(storage.get("observed_classes", [])) + list(storage.get("code_classes", []))
    names = [c.get("name") for c in classes]
    if len(set(names)) != len(names):
        problems.append("duplicate class names")
    for cls in classes:
        if not cls.get("path_glob") or not cls.get("code_refs"):
            problems.append(f"class {cls.get('name')!r} lacks path_glob/code_refs")
        for field, meta in (cls.get("fields") or {}).items():
            if meta.get("presence") not in PRESENCE_VOCAB:
                problems.append(f"{cls.get('name')}.{field}: presence={meta.get('presence')!r}")
                break
    inv = storage.get("file_inventory", {})
    shared, py_only, go_only = set(inv.get("shared", [])), set(inv.get("python_only", [])), set(inv.get("go_only", []))
    overlap = (shared & py_only) | (shared & go_only) | (py_only & go_only)
    if overlap:
        problems.append(f"file_inventory overlap: {sorted(overlap)[:5]}")
    counts = inv.get("counts", {})
    if counts:
        pairs = (("both", shared), ("python_only", py_only), ("go_only", go_only))
        for key, names in pairs:
            if key in counts and counts[key] != len(names):
                problems.append(f"file_inventory counts[{key}]={counts[key]} != {len(names)}")
        if "python" in counts and counts["python"] != len(shared) + len(py_only):
            problems.append(f"file_inventory counts[python]={counts['python']} != both+python_only")
        if "go" in counts and counts["go"] != len(shared) + len(go_only):
            problems.append(f"file_inventory counts[go]={counts['go']} != both+go_only")
    if problems:
        record("C4", "storage contract self-consistent", "FAIL", "; ".join(problems[:4]))
    else:
        record(
            "C4",
            "storage contract self-consistent",
            "PASS",
            f"{len(classes)} file classes, fields/presence vocabulary ok, inventory partition ok",
        )


def check_db_schema(root: Path) -> None:
    """C5: tracker schema version + bootstrap DDL table set across implementations."""
    py_path = root / "core/framework/host/tracker_db.py"
    go_path = root / "go_hive/go-hive/internal/tracker/tracker.go"
    if not py_path.exists() or not go_path.exists():
        record("C5", "DB schema versions agree (python vs go)", "SKIP", "tracker sources not both present")
        return
    py_text = py_path.read_text(encoding="utf-8", errors="replace")
    go_text = go_path.read_text(encoding="utf-8", errors="replace")
    py_m = re.search(r"\bSCHEMA_VERSION\s*=\s*(\d+)", py_text)
    go_m = re.search(r"\bSchemaVersion\s*=\s*(\d+)", go_text)
    problems: list[str] = []
    if not py_m or not go_m:
        problems.append("schema version constant not found (python SCHEMA_VERSION / go SchemaVersion)")
    elif py_m.group(1) != go_m.group(1):
        problems.append(f"schema version drift: python={py_m.group(1)} go={go_m.group(1)}")

    def ddl_tables(text: str, marker: str) -> set[str]:
        chunk = text[text.find(marker): text.find(marker) + 20000]
        return set(re.findall(r"CREATE TABLE(?: IF NOT EXISTS)?\s+([A-Za-z_][A-Za-z0-9_]*)", chunk))

    py_tables = ddl_tables(py_text, "_BOOTSTRAP_SCHEMA")
    go_tables = ddl_tables(go_text, "bootstrapSchema")
    if not py_tables or not go_tables:
        problems.append("bootstrap DDL table list not found")
    elif py_tables != go_tables:
        problems.append(f"bootstrap DDL tables differ: only-python={sorted(py_tables - go_tables)} only-go={sorted(go_tables - py_tables)}")
    if problems:
        record("C5", "DB schema versions agree (python vs go)", "FAIL", "; ".join(problems))
    else:
        record(
            "C5",
            "DB schema versions agree (python vs go)",
            "PASS",
            f"tracker schema_version={py_m.group(1) if py_m else '?'} both sides; bootstrap tables={sorted(py_tables)}",
        )


def check_routes_proto_tools(root: Path) -> None:
    checks = [
        ("C6", "API routes frozen (export_api_routes --check)", ["go_hive/scripts/export_api_routes.py", "--check"]),
        ("C7", "API response shapes <-> proto (check_api_proto)", ["go_hive/scripts/check_api_proto.py"]),
        ("C8", "tool definitions frozen (freeze_tool_contract check)", ["go_hive/scripts/freeze_tool_contract.py", "check"]),
    ]
    for cid, title, argv in checks:
        script = root / argv[0]
        if not script.exists():
            record(cid, title, "SKIP", f"{argv[0]} not present")
            continue
        rc, output = run_script([str(script), *argv[1:]], cwd=root)
        tail = " | ".join(line for line in output.splitlines()[-2:]) if output else ""
        if rc == 0:
            record(cid, title, "PASS", tail[:300])
        else:
            record(cid, title, "FAIL", f"exit {rc}: {tail[:300]}")


def check_api_corpus(root: Path) -> None:
    """C9: api-responses index <-> diff <-> files on disk."""
    base = root / "go_hive/contracts/api-responses"
    try:
        index = load_json(base / "index.json")
        diff = load_json(base / "diff.json")
    except ValueError as exc:
        record("C9", "api-responses corpus self-consistent", "FAIL", str(exc))
        return
    if index is None or diff is None:
        record("C9", "api-responses corpus self-consistent", "FAIL", "index.json or diff.json missing")
        return
    problems: list[str] = []
    endpoints = index.get("endpoints", [])
    names = [e.get("endpoint") for e in endpoints]
    if len(set(names)) != len(names):
        problems.append("duplicate endpoint ids in index.json")
    missing_files: list[str] = []
    for e in endpoints:
        for key in ("reference_file", "target_file", "reference_shape", "target_shape"):
            rel = e.get(key)
            if rel and not (base / rel).exists():
                missing_files.append(f"{e.get('endpoint')}:{rel}")
    if missing_files:
        problems.append(f"{len(missing_files)} referenced file(s) missing: {missing_files[:3]}")
    diff_names = {e.get("endpoint") for e in diff.get("endpoints", [])}
    if diff_names != set(names):
        problems.append(f"diff endpoints != index endpoints (only-index={sorted(set(names) - diff_names)[:3]}, only-diff={sorted(diff_names - set(names))[:3]})")
    totals = index.get("totals", {})
    if isinstance(totals, dict) and "endpoints" in totals and totals["endpoints"] != len(endpoints):
        problems.append(f"index totals.endpoints={totals['endpoints']} != {len(endpoints)}")
    if problems:
        record("C9", "api-responses corpus self-consistent", "FAIL", "; ".join(problems[:3]))
    else:
        record("C9", "api-responses corpus self-consistent", "PASS", f"{len(endpoints)} endpoints, index/diff/files agree")


def check_tool_face_extras(root: Path) -> None:
    """C10: tool-face extras that need uv; SKIP (with reason) without it."""
    if shutil.which("uv") is None:
        record("C10", "tool-face extras (tool-surface/browser-tools/tool-output)", "SKIP", "uv unavailable")
        return
    checks = [
        ("tool-surface-flat", ["scripts/gen_tool_surface_flat.py", "--check"]),
        ("browser-tool-defs", ["scripts/gen_browser_tool_defs.py", "--check"]),
        ("tool-output-golden", ["scripts/tool_output_golden.py", "report", "--check"]),
    ]
    failures: list[str] = []
    for label, argv in checks:
        proc = subprocess.run(
            ["uv", "run", "--no-project", "python", *argv],
            cwd=str(root / "go_hive"),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        if proc.returncode != 0:
            failures.append(f"{label} (exit {proc.returncode}): {proc.stdout.strip().splitlines()[-1][:160] if proc.stdout.strip() else ''}")
    if failures:
        record("C10", "tool-face extras (tool-surface/browser-tools/tool-output)", "FAIL", "; ".join(failures))
    else:
        record("C10", "tool-face extras (tool-surface/browser-tools/tool-output)", "PASS", "3 checks clean (uv)")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", default=str(REPO))
    parser.add_argument("--json", default="", help="also write the report JSON to this path")
    args = parser.parse_args(argv)
    root = Path(args.root).resolve()

    check_env_config(root)
    check_storage(root)
    check_db_schema(root)
    check_routes_proto_tools(root)
    check_api_corpus(root)
    check_tool_face_extras(root)

    for row in results:
        print(f"[{row['status']}] {row['id']} {row['title']} — {row['detail']}")
    passed = sum(1 for r in results if r["status"] == "PASS")
    failed = sum(1 for r in results if r["status"] == "FAIL")
    skipped = sum(1 for r in results if r["status"] == "SKIP")
    verdict = "FAIL" if failed else "PASS"
    note = f" ({skipped} checks SKIPPED — not verified)" if skipped else ""
    print(f"compat-check: pass={passed} fail={failed} skip={skipped} verdict={verdict}{note}")
    if args.json:
        Path(args.json).write_text(
            json.dumps({"checks": results, "summary": {"pass": passed, "fail": failed, "skip": skipped}, "verdict": verdict}, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
