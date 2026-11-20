#!/usr/bin/env python3
"""Report-only gate for the frozen env/config contract.

Reads:
  go_hive/contracts/env-vars.json
  go_hive/contracts/config-keys.json
  go_hive/docs/env-config-contract-diff.md

and compares them with a fresh, read-only scan of the checked-out tree.

Default mode is REPORT ONLY (exit 0) by design: the freeze landed before the
gaps it measures are fixed, so a hard failure would block every unrelated lane.
`--strict` turns drift into exit 1 for release-time use.

Reports:
  * env counts vs the published audit rule (89/96/66/23/30) and per-name
    missing/extra relative to the manifest inventory;
  * configuration.json keys by status (aligned / go_partial / python_only /
    go_only / unread / *_differs) — the "missing", "extra" and "type" buckets;
  * anchors that no longer resolve (a reader moved or was deleted);
  * manifest staleness (fresh generation differs from the committed files).

Parity-exempt rationale: this is a Go-side governance artifact over generated
JSON manifests. The Python reference has no equivalent test or gate; nothing in
it exercises runtime behaviour, so there is no upstream test to bind to.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import env_config_contract_data as data  # noqa: E402
import freeze_env_config_contract as freeze  # noqa: E402


def load(path: Path) -> dict | None:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    except json.JSONDecodeError as exc:
        print(f"ERROR: {path} is not valid JSON: {exc}")
        return {}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument("--strict", action="store_true", help="exit 1 on drift / count mismatch")
    args = parser.parse_args(argv)

    root = Path(args.root).resolve()
    problems: list[str] = []

    env_path = root / "go_hive/contracts/env-vars.json"
    cfg_path = root / "go_hive/contracts/config-keys.json"
    doc_path = root / "go_hive/docs/env-config-contract-diff.md"
    env = load(env_path)
    cfg = load(cfg_path)

    print("== env/config contract gate (report-only unless --strict) ==")
    if env is None or cfg is None:
        msg = "manifest(s) missing: " + ", ".join(str(p) for p in (env_path, cfg_path) if not p.exists())
        print(msg)
        return 1 if args.strict else 0
    if env == {} or cfg == {}:
        problems.append("manifest is not valid JSON")
        env = env or {}
        cfg = cfg or {}

    # ---- env: reproduce the published audit counts ------------------------
    audit = freeze.scan_audit_literals(root)
    reproduced = {
        "python": len(audit["python"]),
        "go": len(audit["go"]),
        "common": len(audit["common"]),
        "python_only": len(audit["python_only"]),
        "go_only": len(audit["go_only"]),
    }
    expected = env.get("verification", {}).get("expected", freeze.AUDIT_EXPECTED)
    counts_match = reproduced == expected
    print(f"env audit reproduced: {reproduced} (expected {expected}) -> {'MATCH' if counts_match else 'MISMATCH'}")
    if not counts_match:
        problems.append("env audit counts no longer match the published audit")

    manifest_names = {v["name"] for v in env.get("variables", [])}
    inventory = env.get("inventory", {})
    inv_names = set(inventory.get("common", [])) | set(inventory.get("python_only", [])) | set(inventory.get("go_only", []))
    if inv_names - manifest_names:
        print(f"env MISSING from variables[]: {sorted(inv_names - manifest_names)}")
        problems.append("env inventory names missing from variables[]")
    if manifest_names - inv_names:
        print(f"env EXTRA in variables[] (not in inventory): {sorted(manifest_names - inv_names)}")
        problems.append("env variables[] contains names outside the audit inventory")
    status_counts = Counter(v.get("status") for v in env.get("variables", []))
    print(f"env read-evidence status: {dict(sorted(status_counts.items()))}")

    # ---- config: anchors, counts, type/default buckets -------------------
    stale_anchors = []
    for entry in data.CONFIG_KEYS:
        for side in ("py", "go"):
            for anchor in entry[side]:
                try:
                    freeze.resolve_evidence(root, anchor)
                except SystemExit as exc:
                    stale_anchors.append(f"{entry['path']} [{side}] {exc}")
    if stale_anchors:
        print(f"config STALE ANCHORS ({len(stale_anchors)}):")
        for line in stale_anchors[:20]:
            print("  " + line)
        problems.append("configuration.json contract has stale reader anchors")

    cfg_status = Counter()
    for section in cfg.get("sections", []):
        for key in section.get("keys", []):
            cfg_status[key.get("status")] += 1
    total_keys = sum(cfg_status.values())
    print(f"config keys: {total_keys} -> {dict(sorted(cfg_status.items()))}")
    print(
        "  missing (python_only)={} extra (go_only)={} runtime-partial={} "
        "type/default/parse differences={}".format(
            cfg_status.get("python_only", 0),
            cfg_status.get("go_only", 0),
            cfg_status.get("go_partial", 0),
            cfg_status.get("type_differs", 0) + cfg_status.get("default_differs", 0) + cfg_status.get("parse_differs", 0),
        )
    )
    for status in ("python_only", "go_only"):
        names = [
            k["path"]
            for section in cfg.get("sections", [])
            for k in section.get("keys", [])
            if k.get("status") == status
        ]
        print(f"  {status}: {names}")

    # ---- staleness -------------------------------------------------------
    py_occ = freeze.scan_python(root)
    go_occ = freeze.scan_go(root)
    fresh_env = freeze.build_env_manifest(root, py_occ, go_occ)
    fresh_cfg = freeze.build_config_manifest(root)
    fresh_doc = freeze.build_doc(fresh_env, fresh_cfg)
    if json.loads(json.dumps(fresh_env)) != env:
        print("STALE: contracts/env-vars.json differs from a fresh build")
        problems.append("env-vars.json staleness")
    if json.loads(json.dumps(fresh_cfg)) != cfg:
        print("STALE: contracts/config-keys.json differs from a fresh build")
        problems.append("config-keys.json staleness")
    if not doc_path.exists() or doc_path.read_text(encoding="utf-8") != fresh_doc:
        print("STALE: docs/env-config-contract-diff.md differs from a fresh build")
        problems.append("diff doc staleness")

    if problems:
        print("RESULT: REPORT — " + "; ".join(sorted(set(problems))))
        return 1 if args.strict else 0
    print("RESULT: OK — manifests match the tree and the published env audit")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
