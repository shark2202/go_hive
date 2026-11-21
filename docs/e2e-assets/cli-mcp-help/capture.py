#!/usr/bin/env python3
"""Capture `hive mcp` help/exit-code parity evidence (Python argparse vs Go).

Runs both CLIs under the same pinned environment the CLI-surface contract uses
(COLUMNS=100, scratch HIVE_HOME, NO_COLOR=1) and records:

  * help-python/<page>.txt, help-go/<page>.txt — `hive mcp [<verb>] --help`
  * help-diff.txt                                — byte diff of every page
  * exit-codes.json                              — exit codes for the help
    pages and a set of parse-error / passthrough invocations
  * summary.json                                 — pass/fail roll-up

Usage (from the repo root):
  uv run --no-project python go_hive/docs/e2e-assets/cli-mcp-help/capture.py \
      --repo . [--python-bin <interpreter>] [--go-bin <hive>] [--scratch <dir>]

Without --go-bin the Go binary is built from <repo>/go_hive/go-hive into the
scratch dir. Without --python-bin the repo's .venv interpreter is used.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
VERBS = [
    "install", "add", "mode", "run", "remove", "enable", "disable",
    "list", "info", "config", "search", "health", "init", "update",
]
PAGES = ["group"] + VERBS

# Documented behaviour differences that this lane does not remove.
KNOWN_EXCEPTIONS = {
    "mcp help": "Go-only alias for `hive mcp --help`; upstream argparse rejects `help` as an unknown verb (2).",
}


def build_env(home: Path) -> dict:
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": str(home),
            "COLUMNS": "100",
            "NO_COLOR": "1",
            "TERM": "dumb",
            "PYTHONIOENCODING": "utf-8",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
    )
    return env


def run(argv: list[str], env: dict, cwd: Path) -> tuple[int, str, str]:
    proc = subprocess.run(argv, cwd=str(cwd), env=env, capture_output=True, text=True, timeout=60)
    return proc.returncode, proc.stdout, proc.stderr


def build_go(repo: Path, scratch: Path) -> str:
    bin_dir = scratch / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    out = bin_dir / "hive"
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    subprocess.run(["go", "build", "-o", str(out), "./cmd/hive"], cwd=str(repo / "go_hive" / "go-hive"), env=env, check=True)
    return str(out)


def find_python(repo: Path, explicit: str | None) -> str:
    if explicit:
        return explicit
    for candidate in (repo / ".venv" / "bin" / "python", Path.home() / "hive" / ".venv" / "bin" / "python"):
        if candidate.exists():
            return str(candidate)
    print("no Python interpreter found (--python-bin)", file=sys.stderr)
    raise SystemExit(2)


def display(path: str, repo: Path, scratch: Path) -> str:
    """Mask local absolute prefixes so the committed evidence stays portable."""
    for prefix, label in (
        (str(scratch), "<scratch>"),
        (str(repo), "<repo>"),
        (str(Path.home()), "<home>"),
    ):
        if path.startswith(prefix):
            return path.replace(prefix, label, 1)
    return path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repo", default=None, help="repo root (default: four levels up from this file)")
    parser.add_argument("--python-bin", default=os.environ.get("CLI_MCP_PYTHON"))
    parser.add_argument("--go-bin", default=os.environ.get("CLI_MCP_GO_BIN"))
    parser.add_argument("--scratch", default=os.environ.get("CLI_MCP_SCRATCH"))
    args = parser.parse_args()

    repo = Path(args.repo).resolve() if args.repo else HERE.parents[3]
    scratch = Path(args.scratch) if args.scratch else Path(os.environ.get("TMPDIR", "/tmp")) / "cli-mcp-help-evidence"
    scratch.mkdir(parents=True, exist_ok=True)
    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    env = build_env(home)

    python_bin = find_python(repo, args.python_bin)
    py_cmd = [python_bin, str(repo / "core" / "framework" / "cli.py")]

    if args.go_bin:
        go_cmd = [str(Path(args.go_bin))]
    else:
        go_cmd = [build_go(repo, scratch)]

    py_dir, go_dir = HERE / "help-python", HERE / "help-go"
    for directory in (py_dir, go_dir):
        directory.mkdir(exist_ok=True)

    help_codes: dict[str, dict[str, int]] = {}
    diff_lines: list[str] = []
    identical = 0
    for page in PAGES:
        argv = ["mcp", page, "--help"] if page != "group" else ["mcp", "--help"]
        py_code, py_out, py_err = run(py_cmd + argv, env, repo)
        go_code, go_out, go_err = run(go_cmd + argv, env, repo)
        help_codes[page] = {"python": py_code, "go": go_code}
        (py_dir / f"{page}.txt").write_text(py_out, encoding="utf-8")
        (go_dir / f"{page}.txt").write_text(go_out, encoding="utf-8")
        if py_out == go_out:
            identical += 1
            diff_lines.append(f"OK  {page}: stdout identical ({len(py_out)} bytes), stderr empty: {py_err == '' and go_err == ''}")
        else:
            diff_lines.append(f"DIFF {page}: python {len(py_out)} bytes vs go {len(go_out)} bytes")
    (HERE / "help-diff.txt").write_text("\n".join(diff_lines) + "\n", encoding="utf-8")

    # Parse-error / passthrough battery. Python is authoritative; Go must match.
    battery: list[list[str]] = [
        ["mcp"],
        ["mcp", "help"],
        ["mcp", "foo"],
        ["mcp", "--no-such-flag-9f3"],
    ]
    for verb in VERBS:
        battery.append(["mcp", verb, "--no-such-flag-9f3"])
    for verb in ("install", "run", "mode", "remove", "enable", "disable", "info", "config", "search", "health"):
        battery.append(["mcp", verb])
    battery += [
        ["mcp", "run", "--help"],
        ["mcp", "run", "-h"],
        ["mcp", "run", "nosuchserver"],
        ["mcp", "run", "nosuchserver", "--list"],
        ["mcp", "run", "nosuchserver", "--help"],
        ["mcp", "run", "--no-such-flag-9f3=junk"],
    ]

    exit_rows = []
    for argv in battery:
        py_code, _, py_err = run(py_cmd + argv, env, repo)
        go_code, _, go_err = run(go_cmd + argv, env, repo)
        key = " ".join(argv)
        exit_rows.append(
            {
                "argv": argv,
                "python_exit": py_code,
                "go_exit": go_code,
                "match": py_code == go_code,
                "known_exception": KNOWN_EXCEPTIONS.get(key),
                "python_stderr": " ".join(py_err.split())[:160],
                "go_stderr": " ".join(go_err.split())[:160],
            }
        )
    (HERE / "exit-codes.json").write_text(json.dumps(exit_rows, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    help_ok = identical == len(PAGES)
    unexpected = [row["argv"] for row in exit_rows if not row["match"] and not row["known_exception"]]
    exit_ok = not unexpected
    expected = [row["argv"] for row in exit_rows if not row["match"] and row["known_exception"]]
    summary = {
        "schema": "hive-cli-mcp-help-evidence/1",
        "python_bin": display(python_bin, repo, scratch),
        "go_bin": display(go_cmd[0], repo, scratch),
        "pages": {
            "count": len(PAGES),
            "byte_identical": identical,
            "help_exit_codes": help_codes,
        },
        "battery": {
            "cases": len(exit_rows),
            "unexpected_exit_mismatches": unexpected,
            "documented_exceptions": expected,
        },
        "pass": help_ok and exit_ok,
    }
    (HERE / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    print(f"help pages identical: {identical}/{len(PAGES)}")
    print(f"exit-code cases matching: {sum(1 for r in exit_rows if r['match'])}/{len(exit_rows)}")
    return 0 if (help_ok and exit_ok) else 1


if __name__ == "__main__":
    raise SystemExit(main())
