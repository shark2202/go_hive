#!/usr/bin/env python3
"""Measure how close the two CLIs' `--help` *layout* is, line by line.

The CLI surface contract compares parsed facts (commands / flags / help text),
not the rendered page. This script compares the rendered page: for every
command path it runs `<cli> <path> --help` on both sides at a fixed terminal
width and reports

  * line counts and the number of identical lines (multiset intersection),
  * a difflib similarity ratio per command,
  * the longest line and how many lines overflow the width,
  * how many lines the usage block occupies (argparse wraps it),
  * the column at which help text starts for each option row,
  * the section headings (`positional arguments:`, `options:`, ...).

Aggregates are printed per width; `--json-out` keeps the per-command detail.

Usage (from the repo root):
    uv run --no-project python go_hive/scripts/cli_help_layout_compare.py \
        --widths 80,100

Exit code is 0 unless a CLI could not be run; drift is reported, not enforced.
"""

from __future__ import annotations

import argparse
import collections
import json
import os
import re
import subprocess
import sys
import tempfile
from difflib import SequenceMatcher
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
GO_MODULE = REPO / "go_hive" / "go-hive"
USAGE_RE = re.compile(r"^usage:\s*(.*)$")
SECTION_RE = re.compile(r"^([a-z][a-z \-]+):\s*$")
CHOICE_RE = re.compile(r"\{([^{}]*)\}\s+\.\.\.")
RUN_TIMEOUT = 60
OPTION_GAP_RE = re.compile(r"\S {2,}\S")


def collapse(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip()


def find_subcommands(text: str) -> list[str]:
    """Subcommand names: usage `{a,b,c} ...` or a commands/positionals section."""
    names: list[str] = []
    lines = text.splitlines()
    usage = ""
    for index, line in enumerate(lines):
        usage_match = USAGE_RE.match(line.strip())
        if not usage_match:
            continue
        parts = [usage_match.group(1)]
        # argparse (and now the Go renderer) wraps the usage over indented
        # lines; glue the block before looking for the choice set.
        for continuation in lines[index + 1 :]:
            if not continuation.strip():
                break
            parts.append(continuation.strip())
        usage = collapse(" ".join(parts))
        break
    for match in re.finditer(r"\{([^{}]*)\}\s+\.\.\.", usage):
        names.extend(item.strip() for item in match.group(1).split(",") if item.strip())
    if names:
        return names
    in_commands = False
    for line in text.splitlines():
        if line.strip() == "commands:":
            in_commands = True
            continue
        if in_commands and not line.strip():
            in_commands = False
        if in_commands:
            match = re.match(r"^\s{2,}(\S+)(?:\s{2,}.*)?$", line)
            if match and not match.group(1).startswith(("{", "-")):
                names.append(match.group(1))
    return names


def discover_paths(batch, run, cli: str) -> list[str]:
    """Breadth-first command paths from `--help` output."""
    paths = [""]
    seen = {""}
    frontier = [""]
    for _ in range(4):
        argvs = [(path.split() + ["--help"] if path else ["--help"]) for path in frontier]
        batch(argvs)
        next_frontier: list[str] = []
        for path, argv in zip(frontier, argvs):
            for name in find_subcommands(run(argv)[1]):
                child = (path + " " + name).strip()
                if child not in seen:
                    seen.add(child)
                    paths.append(child)
                    next_frontier.append(child)
        if not next_frontier:
            break
        frontier = next_frontier
    return paths


def help_columns(lines: list[str]) -> list[int]:
    """Columns where option/positional help text starts (first 2+ gap)."""
    columns: list[int] = []
    in_section = False
    for line in lines:
        stripped = line.strip()
        if stripped in ("options:", "optional arguments:", "positional arguments:"):
            in_section = True
            continue
        if in_section and SECTION_RE.match(stripped) and ":" in stripped:
            in_section = False
            continue
        if not in_section or not line.startswith("  ") or stripped.startswith("{"):
            continue
        match = OPTION_GAP_RE.search(line)
        if match:
            columns.append(match.start() + 1)
    return columns


def sections(lines: list[str]) -> list[str]:
    return [line.strip() for line in lines if SECTION_RE.match(line.strip())]


def usage_lines(lines: list[str]) -> int:
    count = 0
    started = False
    for line in lines:
        if USAGE_RE.match(line.strip()):
            started = True
            count = 1
            continue
        if started:
            if not line.strip():
                break
            count += 1
    return count


def metrics(py_text: str, go_text: str, width: int) -> dict:
    py_lines = py_text.splitlines()
    go_lines = go_text.splitlines()
    same = sum((collections.Counter(go_lines) & collections.Counter(py_lines)).values())
    return {
        "python_lines": len(py_lines),
        "go_lines": len(go_lines),
        "same_lines": same,
        "similarity": round(SequenceMatcher(None, py_lines, go_lines).ratio(), 4),
        "python_longest": max((len(line) for line in py_lines), default=0),
        "go_longest": max((len(line) for line in go_lines), default=0),
        "python_overflow": sum(1 for line in py_lines if len(line) > width),
        "go_overflow": sum(1 for line in go_lines if len(line) > width),
        "usage_lines_python": usage_lines(py_lines),
        "usage_lines_go": usage_lines(go_lines),
        "help_columns_python": sorted(set(help_columns(py_lines))),
        "help_columns_go": sorted(set(help_columns(go_lines))),
        "sections_python": sections(py_lines),
        "sections_go": sections(go_lines),
    }


def build_go(bin_dir: Path) -> dict[str, str]:
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    built: dict[str, str] = {}
    for name in ("hive", "hive-browser"):
        out = bin_dir / name
        subprocess.run(
            ["go", "build", "-o", str(out), f"./cmd/{name}"],
            cwd=str(GO_MODULE),
            env=env,
            check=True,
        )
        built[name] = str(out)
    return built


def find_python(explicit: str | None) -> str | None:
    if explicit:
        return explicit
    for candidate in (REPO / ".venv" / "bin" / "python", Path(sys.executable)):
        if candidate.exists():
            return str(candidate)
    return None


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repo", default=str(REPO))
    parser.add_argument("--python-bin", default=os.environ.get("CLI_SURFACE_PYTHON"))
    parser.add_argument("--go-bin-dir", default=os.environ.get("CLI_SURFACE_GO_BIN_DIR"))
    parser.add_argument("--widths", default="80,100")
    parser.add_argument("--json-out", default=None)
    parser.add_argument("--scratch", default=None)
    args = parser.parse_args()

    repo = Path(args.repo).resolve()
    scratch = Path(args.scratch) if args.scratch else Path(tempfile.mkdtemp(prefix="cli-help-layout-"))
    bin_dir = Path(args.go_bin_dir) if args.go_bin_dir else scratch / "bin"
    if args.go_bin_dir:
        go_bins = {"hive": str(bin_dir / "hive"), "hive-browser": str(bin_dir / "hive-browser")}
    else:
        go_bins = build_go(bin_dir)
    python_bin = find_python(args.python_bin)
    if not python_bin:
        print("no python interpreter found", file=sys.stderr)
        return 1

    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    base_env = dict(os.environ)
    base_env.update(
        {
            "HIVE_HOME": str(home),
            "HIVE_BROWSER_SESSION": "cli-help-layout",
            "HIVE_BROWSERBRIDGE_HTTP": "http://127.0.0.1:9",
            "NO_COLOR": "1",
            "TERM": "dumb",
            "PYTHONIOENCODING": "utf-8",
            "PYTHONDONTWRITEBYTECODE": "1",
            "PYTHONPATH": os.pathsep.join([str(repo / "tools" / "src")]),
        }
    )
    commands = {
        "hive": {"go": [go_bins["hive"]], "python": [python_bin, str(repo / "core" / "framework" / "cli.py")]},
        "hive-browser": {"go": [go_bins["hive-browser"]], "python": [python_bin, "-m", "gcu.cli"]},
    }
    widths = [int(item) for item in args.widths.split(",") if item.strip()]
    report: dict = {"widths": {}, "go_bin_dir": str(bin_dir), "python_bin": python_bin}

    from concurrent.futures import ThreadPoolExecutor

    for width in widths:
        env = dict(base_env)
        env["COLUMNS"] = str(width)
        cache: dict[tuple[str, tuple[str, ...]], tuple[int, str]] = {}

        def run(prefix: list[str], argv: list[str]) -> tuple[int, str]:
            key = (" ".join(prefix), tuple(argv))
            if key not in cache:
                proc = subprocess.run(prefix + argv, cwd=str(repo), env=env, capture_output=True, text=True, timeout=RUN_TIMEOUT)
                cache[key] = (proc.returncode, proc.stdout or "")
            return cache[key]

        def batch(prefix: list[str], argvs: list[list[str]]) -> None:
            pending = [argv for argv in argvs if (" ".join(prefix), tuple(argv)) not in cache]
            if not pending:
                return
            with ThreadPoolExecutor(max_workers=8) as pool:
                list(pool.map(lambda argv: run(prefix, argv), pending))

        per_width: dict = {"commands": {}, "totals": {}}
        for cli, sides in commands.items():
            py_paths = discover_paths(lambda argvs, p=sides["python"]: batch(p, argvs), lambda argv, p=sides["python"]: run(p, argv), cli)
            go_paths = discover_paths(lambda argvs, p=sides["go"]: batch(p, argvs), lambda argv, p=sides["go"]: run(p, argv), cli)
            shared = [path for path in py_paths if path in set(go_paths)]
            argvs = [(path.split() + ["--help"] if path else ["--help"]) for path in shared]
            batch(sides["python"], argvs)
            batch(sides["go"], argvs)
            for path, argv in zip(shared, argvs):
                _, py_text = run(sides["python"], argv)
                _, go_text = run(sides["go"], argv)
                per_width["commands"][f"{cli} {path}".strip()] = metrics(py_text, go_text, width)

        rows = per_width["commands"].values()
        totals = {
            "commands": len(per_width["commands"]),
            "python_lines": sum(row["python_lines"] for row in rows),
            "go_lines": sum(row["go_lines"] for row in rows),
            "same_lines": sum(row["same_lines"] for row in rows),
            "identical_commands": sum(1 for row in rows if row["similarity"] == 1.0),
            "mean_similarity": round(sum(row["similarity"] for row in rows) / max(len(per_width["commands"]), 1), 4),
            "python_overflow": sum(row["python_overflow"] for row in rows),
            "go_overflow": sum(row["go_overflow"] for row in rows),
            "python_usage_multiline": sum(1 for row in rows if row["usage_lines_python"] > 1),
            "go_usage_multiline": sum(1 for row in rows if row["usage_lines_go"] > 1),
            "python_help_columns": sorted({col for row in rows for col in row["help_columns_python"]}),
            "go_help_columns": sorted({col for row in rows for col in row["help_columns_go"]}),
        }
        per_width["totals"] = totals
        report["widths"][str(width)] = per_width
        print(
            f"COLUMNS={width}: commands={totals['commands']} identical={totals['identical_commands']} "
            f"mean_similarity={totals['mean_similarity']} same_lines={totals['same_lines']}/"
            f"{totals['python_lines']}(py) {totals['go_lines']}(go) "
            f"overflow py/go={totals['python_overflow']}/{totals['go_overflow']} "
            f"usage_multiline py/go={totals['python_usage_multiline']}/{totals['go_usage_multiline']} "
            f"help_columns py={totals['python_help_columns']} go={totals['go_help_columns']}"
        )
        worst = sorted(per_width["commands"].items(), key=lambda item: item[1]["similarity"])[:10]
        for name, row in worst:
            print(
                f"  {row['similarity']:.3f} `{name}` lines {row['python_lines']}/{row['go_lines']} "
                f"same={row['same_lines']} max {row['python_longest']}/{row['go_longest']}"
            )

    if args.json_out:
        Path(args.json_out).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"json: {args.json_out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
