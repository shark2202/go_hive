#!/usr/bin/env python3
"""gosec baseline gate for the go-hive module.

gosec reports Go security findings (tainted file paths, subprocess arguments,
weak crypto, integer conversions, ...). The tree carries a large legacy set
inherited from the Python port; the gate freezes the current set and fails on
NEW findings only. Generated files are excluded by the analyzer itself.

Findings are keyed by rule plus module-relative file, never by line, so moving
code does not invalidate the baseline; a second finding of the same rule in the
same file does count, because the baseline stores per-key counts.

Usage (from go_hive/go-hive):
    python3 ../scripts/gosec_gate.py --module .
    python3 ../scripts/gosec_gate.py --module . --update

Exit codes:
    0  no new findings (baseline unchanged or reduced)
    1  new findings, stale baseline, or malformed analyzer output
    2  gosec is unavailable / could not run
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
from collections import Counter
from pathlib import Path

BASELINE_SCHEMA = 1
DEFAULT_BASELINE = "internal/quality/gosec_baseline.json"

# G703 (path traversal) and G706 (log injection) come from gosec's taint
# analysis, which is not deterministic: the same tree alternates between two
# result sets across runs (observed in internal/server/worker_read.go). A gate
# that flakes is worse than no gate, so those rules are reported by
# `make gosec-report` instead of being frozen here.
EXCLUDED_RULES = ("G703", "G706")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--module", default=".", help="module root containing go.mod")
    parser.add_argument("--baseline", default=DEFAULT_BASELINE, help="baseline path relative to the module root")
    parser.add_argument("--binary", default="gosec", help="gosec executable (default: gosec on PATH)")
    parser.add_argument("--update", action="store_true", help="rewrite the baseline from the current analyzer output")
    return parser.parse_args()


def relative_path(raw: str, module: Path) -> str:
    path = Path(raw)
    try:
        return path.resolve().relative_to(module.resolve()).as_posix()
    except ValueError:
        return path.as_posix()


def offline_env() -> dict[str, str]:
    env = dict(os.environ)
    env.update(
        {
            "CGO_ENABLED": "0",
            "GOPROXY": "off",
            "GOSUMDB": "off",
            "GOFLAGS": "-mod=mod",
        }
    )
    return env


def analyzer_version(binary: str) -> str:
    try:
        completed = subprocess.run(
            [binary, "-version"],
            text=True,
            capture_output=True,
            timeout=30,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"
    for line in (completed.stdout or completed.stderr).splitlines():
        if line.strip().startswith("Version:"):
            return line.split(":", 1)[1].strip() or "unknown"
    return "unknown"


def run_gosec(module: Path, binary: str) -> list[dict]:
    resolved = shutil.which(binary) if not Path(binary).is_absolute() else binary
    if not resolved or not Path(resolved).exists():
        print(
            "gosec-gate: BLOCKED — gosec is not on PATH.\n"
            "Install it with: go install github.com/securego/gosec/v2/cmd/gosec@latest",
            file=sys.stderr,
        )
        raise SystemExit(2)

    try:
        completed = subprocess.run(
            [
                resolved,
                "-fmt=json",
                "-quiet",
                "-exclude-generated",
                f"-exclude={','.join(EXCLUDED_RULES)}",
                "./...",
            ],
            cwd=module,
            text=True,
            capture_output=True,
            timeout=1800,
            check=False,
            env=offline_env(),
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        print(f"gosec-gate: BLOCKED — analyzer failed to run: {exc}", file=sys.stderr)
        raise SystemExit(2)

    # gosec exits 1 when it reports findings; that is the normal case for this
    # baseline. Any other non-zero code is an analyzer failure.
    if completed.returncode not in (0, 1):
        print(
            f"gosec-gate: BLOCKED — gosec exited {completed.returncode}\n"
            f"{completed.stderr.strip()}",
            file=sys.stderr,
        )
        raise SystemExit(2)

    try:
        payload = json.loads(completed.stdout or "{}")
    except json.JSONDecodeError as exc:
        print(f"gosec-gate: malformed JSON from gosec: {exc}", file=sys.stderr)
        raise SystemExit(1)

    issues = payload.get("Issues")
    if not isinstance(issues, list):
        print("gosec-gate: analyzer produced no Issues array", file=sys.stderr)
        raise SystemExit(1)
    return [issue for issue in issues if isinstance(issue, dict)]


def issue_key(issue: dict, module: Path) -> str:
    return f"{issue.get('rule_id') or '?'}|{relative_path(str(issue.get('file') or ''), module)}"


def load_baseline(path: Path) -> dict:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"gosec-gate: BLOCKED — baseline missing: {path}", file=sys.stderr)
        raise SystemExit(2)
    except json.JSONDecodeError as exc:
        print(f"gosec-gate: BLOCKED — invalid baseline {path}: {exc}", file=sys.stderr)
        raise SystemExit(2)
    if data.get("schema") != BASELINE_SCHEMA or not isinstance(data.get("issues"), dict):
        print(f"gosec-gate: BLOCKED — unsupported baseline schema in {path}", file=sys.stderr)
        raise SystemExit(2)
    return data


def write_baseline(path: Path, module: Path, issues: list[dict], version: str) -> None:
    counts = Counter(issue_key(issue, module) for issue in issues)
    data = {
        "schema": BASELINE_SCHEMA,
        "tool": "gosec",
        "version": version,
        "issue_count": sum(counts.values()),
        "unique_findings": len(counts),
        "policy": (
            "frozen legacy findings; new findings fail the gate. "
            "Keyed by rule|module-relative file so line movement is ignored. "
            "Taint rules " + ", ".join(EXCLUDED_RULES) + " are excluded because gosec's taint "
            "analysis is nondeterministic; see `make gosec-report`. "
            "Use --update only when intentionally accepting the current state."
        ),
        "issues": dict(sorted(counts.items())),
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(
        f"gosec-gate: wrote {path} "
        f"({data['issue_count']} findings, {data['unique_findings']} unique)"
    )


def main() -> int:
    args = parse_args()
    module = Path(args.module).resolve()
    if not (module / "go.mod").is_file():
        print(f"gosec-gate: BLOCKED — no go.mod under {module}", file=sys.stderr)
        return 2

    baseline_path = (module / args.baseline).resolve()
    issues = run_gosec(module, args.binary)
    version = analyzer_version(args.binary)

    if args.update:
        write_baseline(baseline_path, module, issues, version)
        return 0

    baseline = load_baseline(baseline_path)
    expected = Counter(baseline["issues"])
    current = Counter(issue_key(issue, module) for issue in issues)
    new = current - expected
    fixed = expected - current

    print(
        f"gosec: {sum(current.values())} findings "
        f"(baseline {sum(expected.values())}, new {sum(new.values())}, retired {sum(fixed.values())})"
    )
    if new:
        print("gosec: NEW findings must be fixed (or explicitly accepted with --update):")
        for key, count in sorted(new.items()):
            print(f"  [{count}x] {key}")
        return 1
    if fixed:
        print("gosec: baseline is stale (findings were fixed); run --update to lower the ceiling")
        return 1
    # gosec's own version affects the rule set; surface a mismatch without
    # failing, because several builds report an empty dev version.
    recorded = str(baseline.get("version") or "unknown")
    if version != "unknown" and recorded != "unknown" and version != recorded:
        print(f"gosec: note — analyzer version changed ({recorded} -> {version}); re-baseline when convenient")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
