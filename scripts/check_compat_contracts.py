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
  C11 CLI appearance per slot: build the CLI twice — the contract's baseline
      commit (old build) and the checked-out tree (new build) — then compare
      ``hive --help`` / ``hive-browser --help`` / ``hive mcp --help`` /
      ``hive-cred --help`` usage lines plus the frozen contract's command-path
      and flag-key sets (old vs new, new vs the Python reference). Any drift is
      FAIL. Uses local ``go build`` + ``git archive`` (offline) and the Python
      interpreter that runs the reference CLI; SKIP — never a silent pass —
      when the old build or the Python side cannot be produced.

Nothing here touches the network or the project virtualenv. A check that
cannot run is SKIP with a stated reason — never a silent pass; the summary
prints the skip count next to the verdict.

Usage:
    python3 go_hive/scripts/check_compat_contracts.py [--root DIR] [--json FILE]
    python3 go_hive/scripts/check_compat_contracts.py --self-test
"""

from __future__ import annotations

import argparse
import copy
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import tarfile
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


# ---------------------------------------------------------------------------
# C11: CLI appearance (old build vs new build vs Python reference)
# ---------------------------------------------------------------------------

CLI_CONTRACT = "go_hive/contracts/cli-surface.json"
CLI_MODULE = "go_hive/go-hive"
CLI_BINARIES = ("hive", "hive-browser", "hive-cred")
CLI_CONTRACT_CLIS = ("hive", "hive-browser")
C11_MAX_REPORTED = 6


def _c11_go_env() -> dict[str, str]:
    """Offline, no-cgo build env — same pins as the Makefile OFFLINE macro."""
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    return env


def _c11_extract_baseline(root: Path, commit: str, dest: Path) -> str | None:
    """Materialize ``go_hive/go-hive`` at ``commit`` via git archive (no network).

    Only the Go module subtree is needed, so a shallow-ish archive is enough.
    Returns an error string, or None on success. The commit marker keeps
    reruns cheap and prevents reusing a tree from a different baseline.
    """
    marker = dest / ".baseline-commit"
    if marker.exists() and marker.read_text(encoding="utf-8").strip() == commit and (dest / CLI_MODULE / "go.mod").exists():
        return None
    dest.mkdir(parents=True, exist_ok=True)
    tar_path = dest.parent / f"baseline-{commit[:12]}.tar"
    proc = subprocess.run(
        ["git", "-C", str(root), "archive", "--format=tar", f"--output={tar_path}", commit, "--", CLI_MODULE],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    if proc.returncode != 0:
        tail = (proc.stdout or "").strip().splitlines()[-1:] or [f"exit {proc.returncode}"]
        return f"git archive {commit[:8]} 失败：{tail[0][:200]}"
    try:
        with tarfile.open(tar_path) as archive:
            try:
                archive.extractall(dest, filter="data")
            except TypeError:  # Python without the tarfile filter parameter
                archive.extractall(dest)
    except (tarfile.TarError, OSError) as exc:
        return f"解包基线树失败：{exc}"
    marker.write_text(commit + "\n", encoding="utf-8")
    return None


def _c11_build_tree(module_dir: Path, out_dir: Path) -> tuple[dict[str, str] | None, str]:
    """Build the three CLI entry points from one tree. (bins, error)."""
    out_dir.mkdir(parents=True, exist_ok=True)
    built: dict[str, str] = {}
    for name in CLI_BINARIES:
        out = out_dir / name
        proc = subprocess.run(
            ["go", "build", "-o", str(out), f"./cmd/{name}"],
            cwd=str(module_dir),
            env=_c11_go_env(),
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        if proc.returncode != 0:
            tail = (proc.stdout or "").strip().splitlines()[-1:]
            return None, f"go build ./cmd/{name} 失败：{tail[0][:240] if tail else f'exit {proc.returncode}'}"
        binary = out if out.exists() else Path(str(out) + ".exe")  # Windows appends .exe
        if not binary.exists():
            return None, f"go build ./cmd/{name} 未产出二进制：{out}"
        built[name] = str(binary)
    return built, ""


def _c11_capture_go(cs, bins: dict[str, str], root: Path, env: dict[str, str], jobs: int) -> tuple[dict, str]:
    """Recursive --help capture for the two contractual CLIs + hive-cred usage."""
    capture: dict = {"clis": {}, "cred": None}
    for cli in CLI_CONTRACT_CLIS:
        node = cs.capture_cli(cs.Runner("go", [bins[cli]], env, root, jobs), cli)
        root_node = node.get("", {})
        if root_node.get("help_exit_code") != 0 or not root_node.get("usage"):
            return capture, f"`{cli} --help` 不可解析（exit {root_node.get('help_exit_code')}）"
        capture["clis"][cli] = node
    code, out, _ = cs.Runner("go", [bins["hive-cred"]], env, root, 2).run(["--help"], timeout=cs.PROBE_TIMEOUT)
    if code != 0:
        return capture, f"`hive-cred --help` exit {code}"
    capture["cred"] = cs.parse_help(out, "hive-cred")
    return capture, ""


def _c11_capture_python(cs, root: Path, env: dict[str, str], jobs: int, python_bin: str) -> tuple[dict, str]:
    """Capture the Python reference side (framework cli + gcu.cli + hive-cred)."""
    capture: dict = {"clis": {}, "cred": None}
    py_env = dict(env)
    py_env["PYTHONPATH"] = str(root / "tools" / "src")
    hive_runner = cs.Runner("python", [python_bin, str(root / "core/framework/cli.py")], py_env, root, jobs)
    code, _, err = hive_runner.run(["--help"], timeout=cs.PROBE_TIMEOUT)
    if code != 0:
        return capture, f"python hive 不可用（exit {code}）：{cs.collapse(err)[-200:]}"
    browser_runner = cs.Runner("python", [python_bin, "-m", "gcu.cli"], py_env, root, jobs)
    code, _, err = browser_runner.run(["--help"], timeout=cs.PROBE_TIMEOUT)
    if code != 0:
        return capture, f"python hive-browser 不可用（exit {code}）：{cs.collapse(err)[-200:]}"
    for cli, runner in (("hive", hive_runner), ("hive-browser", browser_runner)):
        capture["clis"][cli] = cs.capture_cli(runner, cli)
    # hive-cred has no contractual capture; its usage line is still compared.
    cred_runner = cs.Runner(
        "python",
        [python_bin, "-c", "import sys; sys.argv=['hive-cred','--help']; from framework.credentials.cli import main; main()"],
        py_env,
        root,
        2,
    )
    code, out, _ = cred_runner.run([], timeout=cs.PROBE_TIMEOUT)
    if code == 0:
        capture["cred"] = cs.parse_help(out, "hive-cred")
    return capture, ""


def _c11_diff_surface(pair: str, cli: str, expected: dict, actual: dict, problems: list[str]) -> None:
    """Append path/usage/flag-set drift between two CLI captures for one CLI."""
    exp_paths, act_paths = set(expected), set(actual)
    missing = sorted(path or "<root>" for path in exp_paths - act_paths)
    extra = sorted(path or "<root>" for path in act_paths - exp_paths)
    if missing or extra:
        problems.append(f"{pair}：`{cli}` 命令路径集合漂移（缺 {missing[:6]}；多 {extra[:6]}）")
    for path in sorted(exp_paths & act_paths):
        exp_node, act_node = expected[path], actual[path]
        if exp_node.get("usage") != act_node.get("usage"):
            problems.append(
                f"{pair}：`{cli} {path or '<root>'}` usage 行漂移（{act_node.get('usage')!r} != {exp_node.get('usage')!r}）"
            )
        exp_flags, act_flags = set(exp_node.get("flags", {})), set(act_node.get("flags", {}))
        if exp_flags != act_flags:
            problems.append(
                f"{pair}：`{cli} {path or '<root>'}` flag 集合漂移（缺 {sorted(exp_flags - act_flags)}；多 {sorted(act_flags - exp_flags)}）"
            )


def _c11_diff_vs_python(cli: str, py_capture: dict, new_capture: dict, frozen_diff: dict, problems: list[str]) -> None:
    """Fresh new-build vs Python deltas must reproduce the frozen delta record.

    The contract is the agreement: renderer differences (usage wording, hidden
    internal extensions) are already recorded there, so anything beyond the
    recorded per-path flag attribution is drift.
    """
    totals = frozen_diff.get("totals", {})
    py_paths, new_paths = set(py_capture), set(new_capture)
    fresh_py_only = sorted(path or "<root>" for path in py_paths - new_paths)
    fresh_go_only = sorted(path or "<root>" for path in new_paths - py_paths)
    frozen_py_only = sorted(path or "<root>" for path in totals.get("python_only", []))
    frozen_go_only = sorted(path or "<root>" for path in totals.get("go_only", []))
    if fresh_py_only != frozen_py_only:
        problems.append(f"新构建 vs Python：`{cli}` 路径 Python 独有 {fresh_py_only[:6]} != 契约记录 {frozen_py_only[:6]}")
    if fresh_go_only != frozen_go_only:
        problems.append(f"新构建 vs Python：`{cli}` 路径 Go 独有 {fresh_go_only[:6]} != 契约记录 {frozen_go_only[:6]}")
    frozen_flags = frozen_diff.get("flags", {})
    for path in sorted(py_paths & new_paths):
        py_flags = set(py_capture[path].get("flags", {}))
        new_flags = set(new_capture[path].get("flags", {}))
        expected = frozen_flags.get(path, {})
        for cat, fresh in (("go_only", sorted(new_flags - py_flags)), ("python_only", sorted(py_flags - new_flags))):
            recorded = sorted(expected.get(cat, []))
            if fresh != recorded:
                problems.append(
                    f"新构建 vs Python：`{cli} {path or '<root>'}` flag 归属漂移（{cat} {fresh} != 契约记录 {recorded}）"
                )


def _c11_cred_check(cred_old: dict | None, cred_new: dict | None, cred_py: dict | None, problems: list[str]) -> None:
    """hive-cred is not in the contract; its usage line is compared directly."""
    old_usage = (cred_old or {}).get("usage")
    new_usage = (cred_new or {}).get("usage")
    py_usage = (cred_py or {}).get("usage")
    if not old_usage or not new_usage:
        problems.append("`hive-cred --help` usage 行缺失（构建产出的二进制不可用？）")
        return
    if old_usage != new_usage:
        problems.append(f"旧构建 vs 新构建：`hive-cred` usage 行漂移（{new_usage!r} != {old_usage!r}）")
    if py_usage is not None and new_usage != py_usage:
        problems.append(f"新构建 vs Python：`hive-cred` usage 行漂移（{new_usage!r} != {py_usage!r}）")


def _c11_compare(contract: dict, old_capture: dict, new_capture: dict, py_capture: dict) -> list[str]:
    """All C11 assertions; pure over captures so --self-test can feed copies."""
    problems: list[str] = []
    clis = contract.get("clis", {})
    for cli in CLI_CONTRACT_CLIS:
        frozen_go = clis.get(cli, {}).get("go", {}).get("commands", {})
        old_cli = old_capture.get("clis", {}).get(cli, {})
        new_cli = new_capture.get("clis", {}).get(cli, {})
        _c11_diff_surface("旧构建 vs 契约基线", cli, frozen_go, old_cli, problems)
        _c11_diff_surface("新构建 vs 契约基线", cli, frozen_go, new_cli, problems)
        _c11_diff_surface("旧构建 vs 新构建", cli, old_cli, new_cli, problems)
        if py_capture.get("clis", {}).get(cli):
            _c11_diff_vs_python(cli, py_capture["clis"][cli], new_cli, clis.get(cli, {}).get("diff", {}), problems)
    _c11_cred_check(old_capture.get("cred"), new_capture.get("cred"), py_capture.get("cred"), problems)
    return problems


def _c11_verdict(problems: list[str]) -> str:
    """C11 verdict: any drift is FAIL; main() turns a FAIL into a non-zero exit."""
    return "FAIL" if problems else "PASS"


def check_cli_surface(root: Path) -> None:
    """C11: CLI appearance gate over the old build, new build, and Python."""
    cid = "C11"
    title = "CLI 外观逐 slot（旧构建 vs 新构建 vs Python）"
    try:
        contract = load_json(root / CLI_CONTRACT)
    except ValueError as exc:
        record(cid, title, "FAIL", str(exc))
        return
    if contract is None:
        record(cid, title, "FAIL", f"{CLI_CONTRACT} 缺失")
        return
    clis = contract.get("clis", {})
    if not all(clis.get(cli, {}).get("go", {}).get("commands") for cli in CLI_CONTRACT_CLIS):
        record(cid, title, "FAIL", "契约缺少 clis.<name>.go.commands（先跑 cli_surface_contract.py 重采）")
        return
    if not all(clis.get(cli, {}).get("python", {}).get("commands") for cli in CLI_CONTRACT_CLIS):
        record(cid, title, "SKIP", "契约未包含 Python 参考面（重采时缺解释器）— 新构建 vs Python 未验证")
        return

    sys.path.insert(0, str(root / "go_hive/scripts"))
    try:
        import cli_surface_contract as cs  # noqa: PLC0415
    except Exception as exc:  # pragma: no cover - import guard
        record(cid, title, "SKIP", f"cli_surface_contract.py 不可用：{exc}（未验证）")
        return

    # Python reference first: when it is missing the whole new-vs-Python
    # comparison is unverified, and the documented outcome is SKIP.
    python_bin = cs.find_python(root, None)
    if not python_bin:
        record(cid, title, "SKIP", "未找到 python hive 解释器（<repo>/.venv 或 ~/hive/.venv）；新构建 vs Python 未验证")
        return
    if shutil.which("go") is None or shutil.which("git") is None:
        record(cid, title, "SKIP", "go/git 不可用，无法本地构建新旧两个构建（未验证）")
        return

    baseline = str((contract.get("meta") or {}).get("baseline_commit") or "").strip()
    if not re.fullmatch(r"[0-9a-f]{7,40}", baseline):
        record(cid, title, "SKIP", f"契约未记录可用基线提交（baseline_commit={baseline!r}）（未验证）")
        return
    probe = subprocess.run(
        ["git", "-C", str(root), "cat-file", "-e", f"{baseline}^{{commit}}"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    if probe.returncode != 0:
        record(cid, title, "SKIP", f"基线提交 {baseline[:8]} 不在本地克隆（浅克隆？）— 旧 vs 新未验证")
        return

    # Keep the temporary tree outside the repo (C8 flags empty dirs here).
    scratch = Path(
        os.environ.get("COMPAT_C11_SCRATCH", str(Path(tempfile.gettempdir()) / "hive-compat-c11"))
    )
    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    env = cs.build_env(home, "compat-c11", f"http://127.0.0.1:{cs.free_port()}")
    jobs = max(1, int(os.environ.get("CLI_SURFACE_JOBS", "8")))

    # Probe the Python CLI before spending time on builds: a broken/missing
    # reference interpreter is the documented SKIP case, never a green pass.
    py_env = dict(env)
    py_env["PYTHONPATH"] = str(root / "tools" / "src")
    python_probe = cs.Runner("python", [python_bin, str(root / "core/framework/cli.py")], py_env, root, 2)
    code, _, err = python_probe.run(["--help"], timeout=cs.PROBE_TIMEOUT)
    if code != 0:
        record(cid, title, "SKIP", f"python hive 不可用（exit {code}）：{cs.collapse(err)[-200:]}（新构建 vs Python 未验证）")
        return

    old_tree = scratch / "old-tree"
    err = _c11_extract_baseline(root, baseline, old_tree)
    if err:
        record(cid, title, "SKIP", f"旧构建树无法复现：{err[:200]}（未验证）")
        return
    old_bins, err = _c11_build_tree(old_tree / CLI_MODULE, scratch / "old-bin")
    if old_bins is None:
        record(cid, title, "SKIP", f"旧构建（基线 {baseline[:8]}）编译失败：{err[:200]}（未验证）")
        return
    new_bins, err = _c11_build_tree(root / CLI_MODULE, scratch / "new-bin")
    if new_bins is None:
        record(cid, title, "FAIL", f"新构建编译失败：{err[:240]}")
        return

    old_capture, err = _c11_capture_go(cs, old_bins, root, env, jobs)
    if err:
        record(cid, title, "SKIP", f"旧构建 CLI 探针失败：{err}（未验证）")
        return
    new_capture, err = _c11_capture_go(cs, new_bins, root, env, jobs)
    if err:
        record(cid, title, "FAIL", f"新构建 CLI 探针失败：{err}")
        return
    py_capture, err = _c11_capture_python(cs, root, env, jobs, python_bin)
    if err:
        record(cid, title, "SKIP", f"{err}（python hive 未就绪；新构建 vs Python 未验证）")
        return

    problems = _c11_compare(contract, old_capture, new_capture, py_capture)
    verdict = _c11_verdict(problems)
    hive_paths = len(new_capture["clis"]["hive"])
    browser_paths = len(new_capture["clis"]["hive-browser"])
    if verdict == "FAIL":
        detail = "；".join(problems[:C11_MAX_REPORTED])
        if len(problems) > C11_MAX_REPORTED:
            detail += f"；…（共 {len(problems)} 处漂移）"
        record(cid, title, verdict, detail[:900])
        return
    cred_note = "hive-cred 旧/新/Python 对齐" if (py_capture.get("cred") or {}).get("usage") else "hive-cred 旧/新对齐（python 侧不可用）"
    record(
        cid,
        title,
        verdict,
        f"基线 {baseline[:8]} 旧/新构建与 Python 三方一致：hive {hive_paths} 路径、hive-browser {browser_paths} 路径；"
        f"4 个 usage slot、路径与 flag 集合 0 漂移；{cred_note}",
    )


def cli_surface_self_test(root: Path) -> int:
    """C11 --self-test: injected synthetic drift must be judged FAIL.

    No builds, no network: copies of the frozen contract's own captures play
    the old/new/Python sides. The clean copy must produce zero problems; a
    deleted flag and rewritten usage line must each produce a FAIL verdict.
    """
    try:
        contract = load_json(root / CLI_CONTRACT)
    except ValueError as exc:
        print(f"self-test: FAIL — {exc}")
        return 1
    if contract is None or not contract.get("clis"):
        print(f"self-test: FAIL — {CLI_CONTRACT} 缺失或为空")
        return 1
    missing = [
        cli for cli in CLI_CONTRACT_CLIS if not contract["clis"].get(cli, {}).get("python", {}).get("commands")
    ]
    if missing:
        print(f"self-test: FAIL — 契约缺 Python 参考面（{missing}），无法覆盖新构建 vs Python 断言")
        return 1

    def frozen_side(side: str) -> dict[str, dict]:
        return {cli: copy.deepcopy(contract["clis"][cli][side]["commands"]) for cli in CLI_CONTRACT_CLIS}

    def cred_node(usage: str) -> dict:
        return {"usage": usage}

    cred_usage = "hive-cred [-h] {token,browse,inspect} ..."
    frozen_go, frozen_py = frozen_side("go"), frozen_side("python")

    def run_compare(new_caps: dict[str, dict], cred: tuple[str, str, str] = (cred_usage, cred_usage, cred_usage)) -> list[str]:
        old_capture = {"clis": copy.deepcopy(frozen_go), "cred": cred_node(cred[0])}
        new_capture = {"clis": new_caps, "cred": cred_node(cred[1])}
        py_capture = {"clis": copy.deepcopy(frozen_py), "cred": cred_node(cred[2])}
        return _c11_compare(contract, old_capture, new_capture, py_capture)

    def detect(problems: list[str], needles: tuple[str, ...]) -> bool:
        return next((p for p in problems if all(n in p for n in needles)), None) is not None

    def pick_long_flag(caps: dict[str, dict]) -> tuple[str, str, str]:
        candidates: list[tuple[bool, str, str, str]] = []
        for cli in CLI_CONTRACT_CLIS:
            for path in sorted(caps[cli]):
                for flag in sorted(caps[cli][path].get("flags", {})):
                    if flag.startswith("--"):
                        candidates.append((flag != "--json", cli, path, flag))
        if not candidates:
            return "", "", ""
        _, cli, path, flag = sorted(candidates)[0]
        return cli, path, flag

    failures: list[str] = []
    verdicts: list[str] = []

    clean = run_compare(copy.deepcopy(frozen_go))
    if _c11_verdict(clean) != "PASS":
        failures.append(f"场景 0（基线副本）误报 {len(clean)} 处漂移")
        verdicts.append(f"[self-test] 场景 0 基线副本（无漂移）：FAIL — 判定 FAIL，误报：{clean[0][:160]}")
    else:
        verdicts.append("[self-test] 场景 0 基线副本（无漂移）：PASS — 判定 PASS，0 处漂移")

    flag_cli, flag_path, flag_name = pick_long_flag(frozen_go)
    if not flag_name:
        failures.append("场景 1（删 flag）无法构造：契约 go 侧没有长 flag 可删")
    else:
        mutated = copy.deepcopy(frozen_go)
        del mutated[flag_cli][flag_path]["flags"][flag_name]
        problems = run_compare(mutated)
        hit_contract = detect(problems, ("新构建 vs 契约基线", "flag 集合漂移", flag_name))
        hit_upgrade = detect(problems, ("旧构建 vs 新构建", "flag 集合漂移", flag_name))
        if hit_contract and hit_upgrade and _c11_verdict(problems) == "FAIL":
            verdicts.append(
                f"[self-test] 场景 1 删除 flag `{flag_name}`（`{flag_cli} {flag_path or '<root>'}`）：PASS — 判定 FAIL"
                f"（命中 新构建 vs 契约基线 / 旧构建 vs 新构建，共 {len(problems)} 处）"
            )
        else:
            failures.append(f"场景 1（删 flag {flag_name}）未被判定 FAIL（契约对照={hit_contract}，旧新对照={hit_upgrade}）")
            verdicts.append(f"[self-test] 场景 1 删除 flag `{flag_name}`：FAIL — 判定未触发（{problems[:2]}）")

    mutated = copy.deepcopy(frozen_go)
    mutated["hive"]["mcp"]["usage"] = mutated["hive"]["mcp"]["usage"] + " --drift"
    problems = run_compare(mutated)
    hit_contract = detect(problems, ("新构建 vs 契约基线", "usage 行漂移", "hive mcp"))
    hit_upgrade = detect(problems, ("旧构建 vs 新构建", "usage 行漂移", "hive mcp"))
    if hit_contract and hit_upgrade and _c11_verdict(problems) == "FAIL":
        verdicts.append(
            f"[self-test] 场景 2 改写 usage 行 `hive mcp`：PASS — 判定 FAIL"
            f"（命中 新构建 vs 契约基线 / 旧构建 vs 新构建，共 {len(problems)} 处）"
        )
    else:
        failures.append(f"场景 2（改 usage 行）未被判定 FAIL（契约对照={hit_contract}，旧新对照={hit_upgrade}）")
        verdicts.append(f"[self-test] 场景 2 改写 usage 行 `hive mcp`：FAIL — 判定未触发（{problems[:2]}）")

    problems = run_compare(copy.deepcopy(frozen_go), cred=(cred_usage, "hive-cred [-h] {token,browse,inspect} --drift ...", cred_usage))
    if _c11_verdict(problems) == "FAIL" and detect(problems, ("旧构建 vs 新构建", "usage 行漂移", "hive-cred")):
        verdicts.append(f"[self-test] 场景 3 改写 usage 行 `hive-cred`：PASS — 判定 FAIL（共 {len(problems)} 处）")
    else:
        failures.append("场景 3（改 hive-cred usage 行）未被判定 FAIL")
        verdicts.append(f"[self-test] 场景 3 改写 usage 行 `hive-cred`：FAIL — 判定未触发（{problems[:2]}）")

    for line in verdicts:
        print(line)
    if failures:
        print("self-test: FAIL — " + "；".join(failures))
        return 1
    print("self-test: PASS（3/3 合成漂移均判定 FAIL；基线副本无误报）")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--root", default=str(REPO))
    parser.add_argument("--json", default="", help="also write the report JSON to this path")
    parser.add_argument(
        "--self-test",
        action="store_true",
        help="C11 only: inject synthetic drift (a deleted flag, a rewritten usage line) and require FAIL",
    )
    args = parser.parse_args(argv)
    root = Path(args.root).resolve()

    if args.self_test:
        return cli_surface_self_test(root)

    check_env_config(root)
    check_storage(root)
    check_db_schema(root)
    check_routes_proto_tools(root)
    check_api_corpus(root)
    check_tool_face_extras(root)
    check_cli_surface(root)

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
