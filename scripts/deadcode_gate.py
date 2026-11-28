#!/usr/bin/env python3
"""Deadcode baseline gate for the go-hive module.

`deadcode` (golang.org/x/tools/cmd/deadcode) reports functions that no binary
or test can reach. The tree carries legacy leftovers from the 1:1 Python port,
so the current set is frozen in a baseline: the gate fails on NEW findings, and
also fails when the baseline is stale so retiring dead code ratchets the
ceiling down instead of leaving slack.

Findings are keyed by module-relative file plus symbol name, never by
line:column, so moving code does not invalidate the baseline.

Usage (from go_hive/go-hive):
    python3 ../scripts/deadcode_gate.py --module .
    python3 ../scripts/deadcode_gate.py --module . --update

Exit codes:
    0  no new findings (baseline unchanged or reduced)
    1  new findings, stale baseline, or malformed analyzer output
    2  deadcode is unavailable / could not run
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
from collections import Counter
from pathlib import Path

BASELINE_SCHEMA = 1
DEFAULT_BASELINE = "internal/quality/deadcode_baseline.json"
TOOL_SOURCE = "golang.org/x/tools@v0.53.0"

# deadcode prints one line per unreachable symbol:
#   internal/foo/bar.go:42:6: unreachable func: NewClient
LINE = re.compile(
    r"^(?P<file>.+?):(?P<line>\d+):(?P<col>\d+): "
    r"unreachable (?P<kind>func|func/method): (?P<name>.+)$"
)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--module", default=".", help="module root containing go.mod")
    parser.add_argument("--baseline", default=DEFAULT_BASELINE, help="baseline path relative to the module root")
    parser.add_argument("--binary", default="deadcode", help="deadcode executable (default: deadcode on PATH)")
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


def run_deadcode(module: Path, binary: str) -> list[dict]:
    resolved = shutil.which(binary) if not Path(binary).is_absolute() else binary
    if not resolved or not Path(resolved).exists():
        print(
            "deadcode-gate: BLOCKED — deadcode is not available.\n"
            "Build the pinned copy with: make tools",
            file=sys.stderr,
        )
        raise SystemExit(2)

    try:
        completed = subprocess.run(
            [resolved, "-test", "./..."],
            cwd=module,
            text=True,
            capture_output=True,
            timeout=900,
            check=False,
            env=offline_env(),
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        print(f"deadcode-gate: BLOCKED — analyzer failed to run: {exc}", file=sys.stderr)
        raise SystemExit(2)

    if completed.returncode != 0:
        print(
            f"deadcode-gate: BLOCKED — deadcode exited {completed.returncode}\n"
            f"{completed.stderr.strip()}",
            file=sys.stderr,
        )
        raise SystemExit(2)

    findings: list[dict] = []
    for line in completed.stdout.splitlines():
        match = LINE.match(line.strip())
        if not match:
            continue
        findings.append(match.groupdict())
    return findings


def finding_key(finding: dict, module: Path) -> str:
    return f"{relative_path(finding['file'], module)}|{finding['kind']} {finding['name']}"


def load_baseline(path: Path) -> dict:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        print(f"deadcode-gate: BLOCKED — baseline missing: {path}", file=sys.stderr)
        raise SystemExit(2)
    except json.JSONDecodeError as exc:
        print(f"deadcode-gate: BLOCKED — invalid baseline {path}: {exc}", file=sys.stderr)
        raise SystemExit(2)
    if data.get("schema") != BASELINE_SCHEMA or not isinstance(data.get("issues"), dict):
        print(f"deadcode-gate: BLOCKED — unsupported baseline schema in {path}", file=sys.stderr)
        raise SystemExit(2)
    return data


def write_baseline(path: Path, module: Path, findings: list[dict]) -> None:
    counts = Counter(finding_key(finding, module) for finding in findings)
    data = {
        "schema": BASELINE_SCHEMA,
        "tool": "deadcode",
        "tool_source": TOOL_SOURCE,
        "issue_count": sum(counts.values()),
        "unique_findings": len(counts),
        "policy": (
            "frozen legacy findings; new findings fail the gate. "
            "Keyed by module-relative file|symbol so line movement is ignored. "
            "Use --update only when intentionally accepting the current state."
        ),
        "issues": dict(sorted(counts.items())),
    }
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(
        f"deadcode-gate: wrote {path} "
        f"({data['issue_count']} findings, {data['unique_findings']} unique)"
    )


def main() -> int:
    args = parse_args()
    module = Path(args.module).resolve()
    if not (module / "go.mod").is_file():
        print(f"deadcode-gate: BLOCKED — no go.mod under {module}", file=sys.stderr)
        return 2

    baseline_path = (module / args.baseline).resolve()
    findings = run_deadcode(module, args.binary)

    if args.update:
        write_baseline(baseline_path, module, findings)
        return 0

    baseline = load_baseline(baseline_path)
    expected = Counter(baseline["issues"])
    current = Counter(finding_key(finding, module) for finding in findings)
    new = current - expected
    fixed = expected - current

    print(
        f"deadcode: {sum(current.values())} findings "
        f"(baseline {sum(expected.values())}, new {sum(new.values())}, retired {sum(fixed.values())})"
    )
    if new:
        print("deadcode: NEW unreachable symbols must be removed (or explicitly accepted with --update):")
        for key, count in sorted(new.items()):
            print(f"  [{count}x] {key}")
        return 1
    if fixed:
        print("deadcode: baseline is stale (symbols were retired); run --update to lower the ceiling")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
