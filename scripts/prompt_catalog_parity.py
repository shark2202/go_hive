#!/usr/bin/env python3
"""Skill-catalog / reminder rendering parity: Python reference vs Go target.

Drives the same shared fixture through both implementations and compares the
rendered text byte for byte (after canonicalizing machine-specific roots):

  * Python: go_hive/scripts/prompt_catalog_render.py — the queen session's
    SkillsManager (queen_orchestrator's config), rendered for the phase-agnostic,
    independent and colony phases, plus a synthetic description-cap table and
    the <system-reminder> wrapper.
  * Go: `go test ./internal/skills ./internal/agentloop -run <golden tests>`
    with HIVE_PROMPT_CATALOG_CAPTURE_DIR, which writes the same cases.

Outputs (default go_hive/docs/e2e-assets/prompt-catalog):

  python/<case>.txt        Python reference rendering
  go/<case>.txt            Go target rendering
  summary.txt              one-screen result
  diff.json                per-case verdict + first diff hunk

Opt-in; stdlib only; never touches the network.
"""

from __future__ import annotations

import argparse
import difflib
import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_MODULE = REPO_ROOT / "go_hive" / "go-hive"
FIXTURE = GO_MODULE / "internal" / "skills" / "testdata" / "prompt-catalog" / "fixture.json"
DEFAULT_OUT = REPO_ROOT / "go_hive" / "docs" / "e2e-assets" / "prompt-catalog"

CASES = [
    "small.none",
    "small.independent",
    "small.colony",
    "edge.none",
    "edge.independent",
    "edge.colony",
    "caps.none",
    "caps.compact",
    "turn-context",
    "reminder.block",
]

QUEEN_ID = "queen_growth"


def log(message: str) -> None:
    print(f"[catalog-parity] {message}", flush=True)


def write_skill_md(path: Path, dir_name: str, spec: dict) -> None:
    path.mkdir(parents=True, exist_ok=True)
    if "raw_frontmatter" in spec:
        frontmatter = spec["raw_frontmatter"]
    else:
        name = spec.get("name", dir_name)
        lines = [f"name: {name}", f"description: {json.dumps(spec['description'], ensure_ascii=False)}"]
        metadata = spec.get("metadata")
        if metadata:
            lines.append("metadata:")
            for key, value in metadata.items():
                lines.append(f"  {key}: {json.dumps(value, ensure_ascii=False)}")
        frontmatter = "\n".join(lines)
    body = f"Body for {dir_name}.\n"
    (path / "SKILL.md").write_text(f"---\n{frontmatter}\n---\n\n{body}", encoding="utf-8")


def materialize_fixture(fixture: dict, profile: str, root: Path) -> tuple[Path, Path]:
    """Write one profile as <root>/hive (HIVE_HOME) and <root>/home (HOME).

    Skills carrying ``symlink_to`` are created as symlinks after every real
    skill exists (a link must not be written before its target).
    """
    entry = fixture["profiles"][profile]
    home = root / "home"
    hive = root / "hive"
    links: list[tuple[Path, str]] = []

    def emit(base: Path, skills: dict) -> None:
        for dir_name, spec in skills.items():
            if spec.get("symlink_to"):
                links.append((base / dir_name, spec["symlink_to"]))
                continue
            write_skill_md(base / dir_name, dir_name, spec)

    emit(hive / "skills", entry.get("hive_skills", {}))
    emit(hive / "queens" / QUEEN_ID / "skills", entry.get("queen_skills", {}))
    emit(home / ".agents" / "skills", entry.get("agents_skills", {}))
    for link_path, target in links:
        link_path.parent.mkdir(parents=True, exist_ok=True)
        if link_path.is_symlink() or link_path.exists():
            link_path.unlink()
        os.symlink(target, link_path)
    (hive / "configuration.json").write_text("{}\n", encoding="utf-8")
    return home, hive


def run_python_side(fixture: dict, root: Path, out: Path) -> dict[str, Path]:
    uv = shutil.which("uv")
    if uv is None:
        raise SystemExit("uv is not on PATH; the Python side runs with `uv run --offline`")
    raw = out.parent / "python-raw"
    raw.mkdir(parents=True, exist_ok=True)
    written: dict[str, Path] = {}
    for profile in ("small", "edge"):
        home, hive = materialize_fixture(fixture, profile, root / profile)
        env = dict(os.environ)
        env.update(
            {
                "HOME": str(home),
                "HIVE_HOME": str(hive),
                "PYTHONPATH": "core",
                "PYTHONUNBUFFERED": "1",
            }
        )
        proc = subprocess.run(
            [
                uv,
                "run",
                "--offline",
                "python",
                "go_hive/scripts/prompt_catalog_render.py",
                "--fixture",
                str(FIXTURE),
                "--profile",
                profile,
                "--out",
                str(raw),
            ],
            cwd=str(REPO_ROOT),
            env=env,
            capture_output=True,
            text=True,
            timeout=300,
        )
        if proc.returncode != 0:
            raise SystemExit(f"python renderer failed for {profile}:\n{proc.stdout}\n{proc.stderr}")
        log(f"python render ok: {profile}")
    for case in CASES:
        path = raw / f"{case}.txt"
        if not path.is_file():
            raise SystemExit(f"python renderer left no output for {case}")
        written[case] = path
    return written


def run_go_side(capture_dir: Path) -> dict[str, Path]:
    env = dict(os.environ)
    env["HIVE_PROMPT_CATALOG_CAPTURE_DIR"] = str(capture_dir)
    env.setdefault("GOPROXY", "off")
    proc = subprocess.run(
        [
            "go",
            "test",
            "./internal/skills",
            "./internal/agentloop",
            "./internal/server",
            "-run",
            "TestPromptCatalogParityGolden|TestReminderBlockParityGolden|TestTurnContextBodyParityGolden",
            "-count=1",
        ],
        cwd=str(GO_MODULE),
        env=env,
        capture_output=True,
        text=True,
        timeout=900,
    )
    if proc.returncode != 0:
        raise SystemExit(f"go capture failed:\n{proc.stdout}\n{proc.stderr}")
    log("go capture ok")
    written: dict[str, Path] = {}
    for case in CASES:
        path = capture_dir / f"{case}.txt"
        if not path.is_file():
            raise SystemExit(f"go capture left no output for {case}")
        written[case] = path
    return written


def first_diff(reference: str, target: str) -> tuple[str, int, int]:
    """Return (unified diff text, reference line count, target line count)."""
    reference_lines = reference.splitlines(keepends=True)
    target_lines = target.splitlines(keepends=True)
    diff = list(
        difflib.unified_diff(
            reference_lines,
            target_lines,
            fromfile="python",
            tofile="go",
            n=3,
        )
    )
    return "".join(diff), len(reference_lines), len(target_lines)


def write_case(reference_path: Path, target_path: Path, out: Path, case: str) -> dict:
    reference = reference_path.read_text(encoding="utf-8")
    target = target_path.read_text(encoding="utf-8")
    (out / "python").mkdir(parents=True, exist_ok=True)
    (out / "go").mkdir(parents=True, exist_ok=True)
    (out / "python" / f"{case}.txt").write_text(reference, encoding="utf-8")
    (out / "go" / f"{case}.txt").write_text(target, encoding="utf-8")
    ok = reference == target
    entry = {
        "identical": ok,
        "python_chars": len(reference),
        "go_chars": len(target),
        "python_lines": len(reference.splitlines()),
        "go_lines": len(target.splitlines()),
    }
    if not ok:
        diff, _, _ = first_diff(reference, target)
        entry["diff"] = diff
    return entry


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", default=str(DEFAULT_OUT))
    parser.add_argument("--workdir", default="", help="scratch dir (default: a temp dir)")
    parser.add_argument("--keep", action="store_true", help="keep the scratch dir")
    args = parser.parse_args()

    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    scratch = Path(args.workdir).resolve() if args.workdir else Path(
        os.environ.get("TMPDIR", "/tmp")
    ) / f"hive-catalog-parity-{os.getpid()}"
    if scratch.exists() and args.workdir:
        shutil.rmtree(scratch)
    scratch.mkdir(parents=True, exist_ok=True)
    go_capture_dir = scratch / "go-capture"
    go_capture_dir.mkdir(parents=True, exist_ok=True)

    fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))
    python_outputs = run_python_side(fixture, scratch / "python", out)
    go_outputs = run_go_side(go_capture_dir)

    results: dict[str, dict] = {}
    for case in CASES:
        results[case] = write_case(python_outputs[case], go_outputs[case], out, case)

    identical = sum(1 for entry in results.values() if entry["identical"])
    summary_lines = [
        "skill-catalog / reminder rendering parity (canonicalized, verbatim)",
        f"cases: {identical}/{len(CASES)} identical",
        "",
    ]
    for case in CASES:
        entry = results[case]
        verdict = "IDENTICAL" if entry["identical"] else "DIFF"
        summary_lines.append(
            f"  {case:20s} {verdict:9s} python={entry['python_chars']} chars go={entry['go_chars']} chars"
        )
    summary = "\n".join(summary_lines) + "\n"
    (out / "summary.txt").write_text(summary, encoding="utf-8")
    (out / "diff.json").write_text(
        json.dumps({"cases": results, "identical": identical, "total": len(CASES)}, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(summary)
    if not args.keep:
        shutil.rmtree(scratch, ignore_errors=True)
    return 0 if identical == len(CASES) else 1


if __name__ == "__main__":
    sys.exit(main())
