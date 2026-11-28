#!/usr/bin/env python3
"""Freeze the CLI surface (commands / flags / defaults / positionals / exit codes).

Dimension 7 of the appearance scope registry: the command line an agent or a
script can observe. This script walks both runtimes' CLIs the same way a caller
would -- recursively invoking `--help` at every command level and running a
fixed no-side-effect exit-code battery -- and writes:

  * go_hive/contracts/cli-surface.json   machine-readable frozen surface + diff
  * go_hive/docs/cli-surface-diff.md     human-readable diff report

Reference side is the Python tree (core/framework/cli.py for `hive`,
tools/src/gcu/cli.py for `hive-browser`); the Go tree (cmd/hive,
cmd/hive-browser) is the target. Nothing here executes an action: probes are
limited to `--help`, a bogus flag, a missing required positional, and a
curated battery of browser commands that fail before any bridge call.

Usage:
    uv run --no-project python go_hive/scripts/cli_surface_contract.py
    uv run --no-project python go_hive/scripts/cli_surface_contract.py --check

The Python side needs an interpreter that can import `framework` (core deps)
and `gcu` (tools deps). Discovery order: --python-bin, then
<root>/.venv/bin/python (or the same bin dir as the script's repo .venv). When
no usable interpreter is found the Python side is reported as SKIP and only the
Go side is checked.

Environment is pinned for determinism: COLUMNS=100 (argparse wraps to it), a
scratch HIVE_HOME, a session profile name, and NO_COLOR.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import socket
import subprocess
import sys
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent  # go_hive/scripts -> go_hive -> repo root
DEFAULT_CONTRACT = REPO / "go_hive" / "contracts" / "cli-surface.json"
DEFAULT_REPORT = REPO / "go_hive" / "docs" / "cli-surface-diff.md"
SCHEMA = "hive-cli-surface-contract/1"

# Script-relative scratch: never the system temp dir.
# Scratch lives outside the repo: empty dirs under go_hive/ trip the
# surface-audit hygiene gate (C8). Override with CLI_SURFACE_SCRATCH.
SCRATCH_ROOT = Path(
    os.environ.get("CLI_SURFACE_SCRATCH", str(Path(tempfile.gettempdir()) / "hive-cli-surface"))
)

MAX_DEPTH = 4
HELP_TIMEOUT = 60
PROBE_TIMEOUT = 30

# ---------------------------------------------------------------------------
# help-text parsing
# ---------------------------------------------------------------------------

USAGE_RE = re.compile(r"^usage:\s*(.*)$")
SECTION_RE = re.compile(r"^([a-z][a-z \-]+):\s*$")
FLAG_TOKEN_RE = re.compile(r"^-{1,2}[A-Za-z][\w\-]*$")
FLAG_COMMA_RE = re.compile(r"^-{1,2}[A-Za-z][\w\-]*,?$")
PLACEHOLDER_RE = re.compile(r"^[A-Z][A-Z_0-9]*$")
CHOICES_RE = re.compile(r"\{([^}]*)\}")
CHOICES_ONLY_RE = re.compile(r"^\{[^{}]*\}$")
METAVAR_RE = re.compile(r"^[a-z][a-z0-9_\-]*(,[a-z0-9_\-]+)+$")
# Symbolic metavars argparse prints verbatim (`--js SRC|@FILE|-`).
METAVAR_SYMBOLS_RE = re.compile(r"^[A-Za-z0-9_@.\-]*[|@][A-Za-z0-9_|@.\-]*$")
DEFAULT_RE = re.compile(r"\(default:\s*([^)]*)\)")


def choice_set_from_usage(usage: str, prog: str) -> list[str]:
    """Return the subcommand choice set of a usage line, or [].

    A `{a,b,c}` in the usage line is only a *subcommand* set when it is not a
    flag's value placeholder: `hive navigate [-h] [--wait-until {commit,load}]`
    declares choices for `--wait-until`, not subcommands. The subcommand set
    sits right after the prog (or after a bracketed `[-h]` / `[options]`), so
    any brace set directly preceded by a flag token is skipped.
    """
    tokens = usage.split()
    for index, token in enumerate(tokens):
        match = CHOICES_RE.search(token)
        if not match:
            continue
        following = tokens[index + 1] if index + 1 < len(tokens) else ""
        previous = tokens[index - 1] if index > 0 else ""
        # argparse renders a subcommand set as a trailing `{a,b,c} ...`; a
        # flag's value placeholder renders as `--flag {a,b}` (often inside
        # `[...]`), so it is neither immediately followed by `...` nor bare.
        if following != "..." or token.startswith("[") or token.endswith("]"):
            continue
        if previous.startswith("-"):
            continue
        names = [item.strip() for item in match.group(1).split(",") if item.strip()]
        if names:
            return names
    return []


def usage_positional_is_required(usage: str, prog: str, name: str) -> bool:
    """A positional is required when the usage renders it without brackets."""
    prog_words = len(prog.split()) if prog else 1
    for token in usage.split()[prog_words:]:
        bare = token.strip("[]")
        if bare.strip("<>") == name:
            return not token.startswith("[")
    return False


def positionals_from_usage(usage: str, prog: str) -> list[str]:
    """Positional names from a usage line: argv words that are neither flags,
    prose (`...`), bracketed optionals, nor uppercase value placeholders."""
    prog_words = len(prog.split()) if prog else 0
    names: list[str] = []
    for token in usage.split()[prog_words:]:
        bare = token.strip("[]<>")
        if not bare or token.startswith("-") or bare.startswith("-"):
            continue
        if bare in ("...", "options") or "{" in bare:
            continue
        if not re.match(r"^[a-z][a-z0-9_\-]*$", bare):
            continue
        names.append(bare)
    return names


def collapse(text: str) -> str:
    return re.sub(r"\s+", " ", text).strip()


def split_label_and_help(body: str) -> tuple[str, str]:
    """Split one option line into (label, help).

    Handles both the argparse layout (`--port PORT, -p PORT  Port to listen`)
    and the Go renderer's `padRight` edge case, where a label at/over the pad
    width is concatenated with its help text without a separator
    (`--limit-files LIMIT_FILESMaximum number ...`). A value placeholder is only
    consumed directly after a flag token, so a help sentence that starts with a
    capitalised word (`LLM model for ...`) is never mistaken for one.
    """
    tokens = body.split()
    label: list[str] = []
    index = 0
    expect_value = False
    while index < len(tokens):
        token = tokens[index]
        bare = token.strip(",")
        if FLAG_COMMA_RE.match(token):
            label.append(token)
            index += 1
            expect_value = True
            continue
        if expect_value and PLACEHOLDER_RE.match(bare):
            label.append(token)
            index += 1
            expect_value = False
            continue
        if expect_value and CHOICES_ONLY_RE.match(bare):
            # argparse renders a restricted choice as the metavar (`--action
            # {left_click,...}`); it is a value placeholder, not help text.
            label.append(token)
            index += 1
            expect_value = False
            continue
        if expect_value and (METAVAR_RE.match(bare) or METAVAR_SYMBOLS_RE.match(bare)):
            # argparse may also render a lowercase or symbolic metavar
            # (`--coordinate x,y`, `--js SRC|@FILE|-`).
            label.append(token)
            index += 1
            expect_value = False
            continue
        # A glued placeholder + help start (`LIMIT_FILESMaximum`): only when the
        # uppercase run is at least two characters, so real prose that merely
        # starts with a capital (`Output as JSON`) is never split.
        glued = re.match(r"^([A-Z_]{2,}[A-Z_0-9]*?)([a-z].*)$", token)
        if glued and expect_value:
            label.append(glued.group(1))
            tokens = [glued.group(2)] + tokens[index + 1 :]
            index = 0
            break
        break
    help_text = collapse(" ".join(tokens[index:]))
    return " ".join(label), help_text


def parse_option_section(lines: list[str]) -> list[dict]:
    """Parse an `options:`-style section, gluing wrapped continuation lines.

    argparse keeps new options at the section's base indent and wraps long
    labels/help onto deeper-indented lines; the Go renderer never wraps. A
    deeper line that starts with a flag (or follows a label ending in a comma)
    continues the label; every other deeper line continues the help text.
    """
    parsed: list[dict] = []
    label = ""
    help_parts: list[str] = []
    base_indent: int | None = None

    def flush() -> None:
        nonlocal label, help_parts
        if label:
            record = to_flag_record(label, " ".join(help_parts))
            if record:
                parsed.append(record)
        label = ""
        help_parts = []

    for line in lines:
        stripped = line.strip()
        if not stripped:
            continue
        indent = len(line) - len(line.lstrip(" "))
        if base_indent is None:
            base_indent = indent
        deeper = indent > base_indent
        continues_label = label.rstrip().endswith(",")
        if not deeper and stripped.startswith("-"):
            flush()
            line_label, inline_help = split_label_and_help(stripped)
            label = line_label
            if inline_help:
                help_parts.append(inline_help)
            continue
        if label and (continues_label or (deeper and stripped.startswith("-"))):
            label = f"{label} {stripped}"
            continue
        if label:
            help_parts.append(stripped)
    flush()
    return parsed


def to_flag_record(label: str, help_text: str) -> dict | None:
    label, inline_help = split_label_and_help(label)
    if not label.startswith("-"):
        return None
    help_text = collapse(" ".join([inline_help, help_text]))
    names = [token.rstrip(",") for token in label.split() if token.startswith("-")]
    long_name = next((name for name in names if name.startswith("--")), None)
    shorts = [name for name in names if name.startswith("-") and not name.startswith("--")]
    placeholder = None
    choices: list[str] | None = None
    for token in label.split():
        bare = token.strip(",")
        if PLACEHOLDER_RE.match(bare) and placeholder is None:
            placeholder = bare
        elif CHOICES_ONLY_RE.match(bare) and choices is None:
            choices = [item.strip() for item in bare.strip("{}").split(",") if item.strip()]
        elif (METAVAR_RE.match(bare) or METAVAR_SYMBOLS_RE.match(bare)) and placeholder is None:
            placeholder = bare
    if choices is None:
        choices = choices_from_help(help_text)
    key = long_name or (shorts[0] if shorts else label)
    key = key.split()[0]
    default_match = DEFAULT_RE.search(help_text)
    return {
        "key": key,
        "names": [name.split()[0] for name in names],
        "takes_value": placeholder is not None or choices is not None,
        "placeholder": placeholder,
        "choices": choices,
        "default_in_help": collapse(default_match.group(1)) if default_match else None,
        "help": help_text,
    }


def choices_from_help(help_text: str) -> list[str] | None:
    """Recover a choice list from help prose (`one of: a, b, c` / `a | b | c`)."""
    match = re.search(r"one of:\s*([a-z0-9_\-]+(?:\s*,\s*[a-z0-9_\-]+)+)", help_text)
    if match:
        return [item.strip() for item in match.group(1).split(",") if item.strip()]
    match = re.search(r"^([a-z0-9_\-]+(?:\s*\|\s*[a-z0-9_\-]+)+)", help_text)
    if match:
        return [item.strip() for item in match.group(1).split("|") if item.strip()]
    return None


def parse_option_line(line: str) -> dict | None:
    stripped = line.strip()
    if not stripped.startswith("-"):
        return None
    label, help_text = split_label_and_help(stripped)
    return to_flag_record(label, help_text)


def parse_help(text: str, prog: str) -> dict:
    lines = text.splitlines()
    usage = ""
    usage_start = None
    for index, line in enumerate(lines):
        match = USAGE_RE.match(line.strip())
        if match:
            usage = match.group(1).strip()
            usage_start = index
            break
    # argparse and the Go renderer both wrap the usage over indented lines and
    # close the block with a blank line; glue those, then start the prose.
    body_start = usage_start + 1 if usage_start is not None else 0
    if usage_start is not None:
        parts = [usage]
        cursor = usage_start + 1
        while cursor < len(lines) and lines[cursor].strip():
            parts.append(lines[cursor].strip())
            cursor += 1
        usage = collapse(" ".join(parts))
        body_start = cursor

    sections: dict[str, list[str]] = {}
    current = None
    for line in lines:
        match = SECTION_RE.match(line.strip())
        if match and not line.strip().startswith("-"):
            current = match.group(1)
            sections.setdefault(current, [])
            continue
        if current is None:
            continue
        if line.strip() == "":
            continue
        sections[current].append(line)

    flags: dict[str, dict] = {}
    for name in ("options", "optional arguments", "flags"):
        for parsed in parse_option_section(sections.get(name, [])):
            flags[parsed["key"]] = parsed

    subcommands: list[str] = []
    usage_choices = choice_set_from_usage(usage, prog)
    if usage_choices:
        subcommands = usage_choices
    if not subcommands:
        # A Go-style group header may list its verbs in a `commands:` section
        # instead of a usage choice set (the mcp group used to; kept as a
        # fallback for any group that renders that way).
        for line in sections.get("commands", []):
            match = re.match(r"^\s{2,}(\S+)", line)
            if match:
                subcommands.append(match.group(1))

    positionals: list[str] = []
    for section_name in ("positional arguments",):
        for line in sections.get(section_name, []):
            match = re.match(r"^\s{2,}(\S+)\s{2,}", line)
            if not match:
                continue
            token = match.group(1)
            if token.startswith("{") or token.startswith("-"):
                continue
            token = token.strip("<>")
            if token in subcommands:
                # The choice set's per-command description lines share the
                # section; only real argv slots belong here.
                continue
            positionals.append(token)
    if not positionals and usage:
        positionals = positionals_from_usage(usage, prog)

    required_positionals = [
        name for name in positionals if usage_positional_is_required(usage, prog, name)
    ]

    paragraphs: list[str] = []
    current_paragraph: list[str] = []
    for line in lines[body_start:]:
        stripped = line.strip()
        if SECTION_RE.match(stripped) or (stripped.startswith("-") and line.startswith("  ")):
            break
        if not stripped:
            if current_paragraph:
                paragraphs.append(collapse(" ".join(current_paragraph)))
                current_paragraph = []
            continue
        current_paragraph.append(stripped)
    if current_paragraph:
        paragraphs.append(collapse(" ".join(current_paragraph)))

    return {
        "usage": usage,
        "description": paragraphs[0] if paragraphs else "",
        "text_block": " ".join(paragraphs),
        "subcommands": subcommands,
        "flags": flags,
        "positionals": positionals,
        "required_positionals": required_positionals,
        "help_sha256": sha256(text),
    }


def sha256(text: str) -> str:
    import hashlib

    return hashlib.sha256(text.encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# runners
# ---------------------------------------------------------------------------


class Runner:
    def __init__(self, name: str, argv: list[str], env: dict[str, str], cwd: Path, jobs: int = 8):
        self.name = name
        self.argv = argv
        self.env = env
        self.cwd = cwd
        self.jobs = max(1, jobs)
        self._cache: dict[tuple[str, ...], tuple[int, str, str]] = {}
        self._lock = threading.Lock()
        self._pool = ThreadPoolExecutor(max_workers=self.jobs)

    def _run(self, args: list[str], timeout: int) -> tuple[int, str, str]:
        try:
            proc = subprocess.run(
                self.argv + args,
                cwd=str(self.cwd),
                env=self.env,
                capture_output=True,
                text=True,
                timeout=timeout,
            )
            return proc.returncode, proc.stdout, proc.stderr
        except subprocess.TimeoutExpired:
            return 124, "", "timeout"
        except OSError as exc:  # interpreter missing etc.
            return 127, "", f"{exc}"

    def run(self, args: list[str], timeout: int = HELP_TIMEOUT) -> tuple[int, str, str]:
        key = tuple(args)
        with self._lock:
            cached = self._cache.get(key)
        if cached is not None:
            return cached
        result = self._run(args, timeout)
        with self._lock:
            self._cache[key] = result
        return result

    def batch(self, argvs: list[list[str]], timeout: int = HELP_TIMEOUT) -> list[tuple[int, str, str]]:
        """Run many invocations concurrently; identical argv runs once."""
        order: list[tuple[str, ...]] = []
        seen: set[tuple[str, ...]] = set()
        for args in argvs:
            key = tuple(args)
            if key not in seen:
                seen.add(key)
                order.append(key)
        with self._lock:
            pending = [key for key in order if key not in self._cache]
        if pending:
            futures = {self._pool.submit(self._run, list(key), timeout): key for key in pending}
            for future, key in futures.items():
                result = future.result()
                with self._lock:
                    self._cache[key] = result
        return [self._cache[tuple(args)] for args in argvs]


def free_port() -> int:
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    port = sock.getsockname()[1]
    sock.close()
    return port


def build_env(home: Path, session: str, bridge_url: str) -> dict[str, str]:
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": str(home),
            "HIVE_BROWSER_SESSION": session,
            "HIVE_BROWSERBRIDGE_HTTP": bridge_url,
            "COLUMNS": "100",
            "NO_COLOR": "1",
            "TERM": "dumb",
            "PYTHONIOENCODING": "utf-8",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
    )
    return env


def find_python(repo: Path, explicit: str | None) -> str | None:
    if explicit:
        return explicit
    candidates = [
        repo / ".venv" / "bin" / "python",
        Path.home() / "hive" / ".venv" / "bin" / "python",
        Path(sys.executable),
    ]
    for candidate in candidates:
        if candidate.exists():
            return str(candidate)
    return None


def build_go(bin_dir: Path, module_dir: Path) -> dict[str, str]:
    """Build the two Go entry points once; every probe reuses the binaries."""
    bin_dir.mkdir(parents=True, exist_ok=True)
    built: dict[str, str] = {}
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    for name in ("hive", "hive-browser"):
        out = bin_dir / name
        subprocess.run(
            ["go", "build", "-o", str(out), f"./cmd/{name}"],
            cwd=str(module_dir),
            env=env,
            check=True,
        )
        built[name] = str(out)
    return built


# ---------------------------------------------------------------------------
# capture
# ---------------------------------------------------------------------------


def capture_cli(runner: Runner, cli_name: str) -> dict:
    """Breadth-first recursive `--help` capture over the whole command tree."""
    node: dict[str, dict] = {}
    frontier: list[list[str]] = []
    root_key = runner.run(["--help"])
    root = parse_help(root_key[1], cli_name)
    root["help_exit_code"] = root_key[0]
    node[""] = root
    frontier = [[sub] for sub in root["subcommands"]]
    depth = 1
    while frontier and depth <= MAX_DEPTH:
        results = runner.batch([path + ["--help"] for path in frontier])
        next_frontier: list[list[str]] = []
        for path, (code, out, _) in zip(frontier, results):
            prog = " ".join([cli_name] + path)
            parsed = parse_help(out, prog)
            parsed["help_exit_code"] = code
            parent = node.get(" ".join(path[:-1]))
            if parent is not None and parsed["help_sha256"] == parent["help_sha256"]:
                # A command that re-prints its parent's help has no surface of
                # its own to recurse into (kept for passthrough groups).
                parsed["recursion_stopped"] = "identical_to_parent_help"
                parsed["subcommands"] = []
            node[" ".join(path)] = parsed
            for sub in parsed["subcommands"]:
                next_frontier.append(path + [sub])
        frontier = next_frontier
        depth += 1
    return node


def probe_exit_codes(runner: Runner, node: dict) -> list[dict]:
    """Per-command, side-effect-free exit-code probes (parse errors only)."""
    plan: list[tuple[list[str], str]] = []
    for path, spec in sorted(node.items()):
        argv = path.split() if path else []
        if spec.get("subcommands"):
            # A group with a required subcommand: bare invocation is a usage
            # error on both sides, no action runs.
            plan.append((argv, "no_args"))
        plan.append((argv + ["--no-such-flag-9f3"], "unknown_flag"))
        if spec.get("required_positionals") and not spec.get("subcommands"):
            plan.append((argv, "missing_required"))
    results = runner.batch([argv for argv, _ in plan])
    return [
        {"argv": argv, "probe": probe, "exit": result[0]}
        for (argv, probe), result in zip(plan, results)
    ]


# Curated browser probes: commands that fail before any bridge round trip, so
# they are safe to run and their exit codes are the observable contract.
BROWSER_PROBE_TABLE = [
    (["help"], "help"),
    (["help", "nosuchcmd"], "help_unknown_topic"),
    (["help", "upload"], "help_topic"),
    ([], "no_args"),
    (["nosuchverb"], "unknown_verb"),
    (["upload", "#f"], "upload_missing_file_flag"),
    (["upload", "#f", "--file", "{missing}"], "upload_missing_file"),
    (["select", "#f"], "select_missing_value"),
    (["page", "resize"], "page_resize_missing_required_flags"),
    (["interact"], "interact_missing_action"),
    (["evaluate"], "evaluate_missing_js"),
    (["screenshot"], "screenshot_missing_intent"),
    (["status"], "status"),
    (["tab", "list"], "tab_list"),
    (["bogus"], "bogus"),
]

CORE_PROBE_TABLE = [
    (["--version"], "version"),
    ([], "no_args"),
    (["nosuchcmd"], "unknown_command"),
    (["queen", "show"], "queen_show_missing_id"),
    (["skill", "validate"], "skill_validate_missing_path"),
    (["mcp", "run"], "mcp_run_missing_name"),
    (["--no-such-flag-9f3"], "root_unknown_flag"),
]


def run_battery(runner: Runner, table: list[tuple[list[str], str]], missing_file: str) -> list[dict]:
    results = []
    for argv, probe in table:
        resolved = [missing_file if token == "{missing}" else token for token in argv]
        code, out, err = runner.run(resolved, timeout=PROBE_TIMEOUT)
        results.append(
            {
                "argv": list(argv),
                "probe": probe,
                "exit": code,
                "stdout_head": collapse(out).replace(missing_file, "<missing-file>")[:200],
                "stderr_head": collapse(err).replace(missing_file, "<missing-file>")[:200],
            }
        )
    return results


# ---------------------------------------------------------------------------
# diffs
# ---------------------------------------------------------------------------


def flag_key_diff(python_flags: dict, go_flags: dict) -> dict:
    py_keys = set(python_flags)
    go_keys = set(go_flags)
    shared = sorted(py_keys & go_keys)
    value_mismatch = []
    default_mismatch = []
    help_mismatch = []
    alias_mismatch = []
    choices_mismatch = []
    for key in shared:
        py = python_flags[key]
        go = go_flags[key]
        if py["takes_value"] != go["takes_value"]:
            value_mismatch.append(
                {"flag": key, "python": py["takes_value"], "go": go["takes_value"]}
            )
        if (py.get("choices") or None) != (go.get("choices") or None):
            choices_mismatch.append(
                {"flag": key, "python": py.get("choices"), "go": go.get("choices")}
            )
        if (py["default_in_help"] or None) != (go["default_in_help"] or None):
            default_mismatch.append(
                {
                    "flag": key,
                    "python": py["default_in_help"],
                    "go": go["default_in_help"],
                }
            )
        if {name.lstrip("-") for name in py["names"]} != {name.lstrip("-") for name in go["names"]}:
            alias_mismatch.append(
                {"flag": key, "python": py["names"], "go": go["names"]}
            )
        if py["help"] != go["help"]:
            help_mismatch.append({"flag": key, "python": py["help"], "go": go["help"]})
    return {
        "shared": shared,
        "python_only": sorted(py_keys - go_keys),
        "go_only": sorted(go_keys - py_keys),
        "value_mismatch": value_mismatch,
        "default_mismatch": default_mismatch,
        "alias_mismatch": alias_mismatch,
        "choices_mismatch": choices_mismatch,
        "help_mismatch": help_mismatch,
    }


def scan_source_defaults(repo: Path) -> dict:
    """Static default scan: flag long name → declared default per side.

    Complements the help-declared defaults: `argSpec{Long: ..., Default: ...}`
    literals in the Go CLI sources and `add_argument("--flag", default=...)`
    calls in the Python CLI modules. Values that are not literals (Go
    constants, Python calls) are recorded as symbolic and excluded from the
    equality check, because the contract compares observable values, not
    symbol names.
    """
    # Recurse: command specs live in the composition root and in the leaf
    # command packages (browsercli, browsersupervisor) under internal/cli.
    go_files = sorted((repo / "go_hive" / "go-hive" / "internal" / "cli").glob("**/*.go"))
    go_consts: dict[str, str] = {}
    for path in go_files:
        if path.name.endswith("_test.go"):
            continue
        for name, value in re.findall(r'(\w+)\s*=\s*"([^"]*)"', path.read_text("utf-8", errors="replace")):
            go_consts.setdefault(name, value)
    python_files = [
        repo / "core" / "framework" / "cli.py",
        repo / "core" / "framework" / "loader" / "cli.py",
        repo / "core" / "framework" / "skills" / "cli.py",
        repo / "core" / "framework" / "debugger" / "cli.py",
        repo / "core" / "framework" / "loader" / "mcp_registry_cli.py",
        repo / "core" / "framework" / "maintenance" / "cli.py",
        repo / "tools" / "src" / "gcu" / "cli.py",
    ]

    result: dict[str, dict] = {}

    def record(flag: str, side: str, value: str, kind: str) -> None:
        entry = result.setdefault(flag, {"python": [], "go": [], "kinds": {}})
        if value not in entry[side]:
            entry[side].append(value)
        entry["kinds"].setdefault(side, kind)

    for path in go_files:
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text("utf-8", errors="replace")
        for spec in re.finditer(r"argSpec\{([^}]*)\}", text, re.S):
            body = spec.group(1)
            long_match = re.search(r'Long:\s*"([^"]+)"', body)
            default_match = re.search(r"Default:\s*([^,}]+)", body)
            if not long_match or not default_match:
                continue
            raw = default_match.group(1).strip()
            flag = "--" + long_match.group(1)
            if raw in go_consts:
                # Resolve a package constant to its literal (`defaultServerURL`).
                raw = f'"{go_consts[raw]}"'
            if re.match(r'^".*"$', raw) or re.match(r"^-?\d+(\.\d+)?$", raw) or raw in ("true", "false"):
                record(flag, "go", raw.strip('"'), "literal")
            else:
                record(flag, "go", raw, "symbolic")

    for path in python_files:
        if not path.exists():
            continue
        text = path.read_text("utf-8", errors="replace")
        for match in re.finditer(r'add_argument\(\s*"(--[\w\-]+)"((?:[^()]|\([^()]*\))*)\)', text, re.S):
            flag = match.group(1)
            body = match.group(2)
            default_match = re.search(r"default=([^,)]+)", body)
            if not default_match:
                continue
            raw = default_match.group(1).strip()
            if re.match(r'^".*"$', raw) or re.match(r"^-?\d+(\.\d+)?$", raw) or raw in ("True", "False"):
                record(flag, "python", raw.strip('"'), "literal")
            else:
                record(flag, "python", raw, "symbolic")

    compared, mismatched, symbolic = [], [], []
    for flag, entry in sorted(result.items()):
        py_values, go_values = entry["python"], entry["go"]
        if not py_values or not go_values:
            continue
        kinds = entry.get("kinds", {})
        if kinds.get("python") == "literal" and kinds.get("go") == "literal":
            compared.append({"flag": flag, "python": py_values, "go": go_values})
            if sorted(py_values) != sorted(go_values):
                mismatched.append({"flag": flag, "python": py_values, "go": go_values})
        else:
            symbolic.append({"flag": flag, "python": py_values, "go": go_values})
    return {
        "method": "regex scan of argSpec{Default:} (Go CLI) vs add_argument(default=) (Python CLI)",
        "flags_seen": {flag: entry for flag, entry in sorted(result.items()) if entry["python"] and entry["go"]},
        "literal_compared": compared,
        "literal_mismatch": mismatched,
        "symbolic_only": symbolic,
    }


def commands_diff(python_node: dict, go_node: dict) -> dict:
    py_paths = set(python_node)
    go_paths = set(go_node)
    shared = sorted(py_paths & go_paths)
    subcommand_mismatch = []
    positional_mismatch = []
    flag_diffs = {}
    help_text_diff = []
    for path in shared:
        py = python_node[path]
        go = go_node[path]
        if py["subcommands"] != go["subcommands"]:
            subcommand_mismatch.append(
                {"command": path, "python": py["subcommands"], "go": go["subcommands"]}
            )
        if py["positionals"] != go["positionals"] or py.get("required_positionals") != go.get(
            "required_positionals"
        ):
            positional_mismatch.append(
                {
                    "command": path,
                    "python": py["positionals"],
                    "go": go["positionals"],
                    "python_required": py.get("required_positionals", []),
                    "go_required": go.get("required_positionals", []),
                }
            )
        diff = flag_key_diff(py["flags"], go["flags"])
        entry = {key: value for key, value in diff.items() if value}
        if entry:
            flag_diffs[path] = entry
        if py.get("text_block") != go.get("text_block"):
            py_text = py.get("text_block", "")
            go_text = go.get("text_block", "")
            # The renderers differ in structure (Go prints a description plus a
            # one-line summary; argparse prints only the description), so only a
            # genuinely different wording is recorded.
            if py_text and go_text and py_text not in go_text and go_text not in py_text:
                help_text_diff.append(
                    {
                        "command": path,
                        "field": "text",
                        "python": py_text,
                        "go": go_text,
                    }
                )
    totals = {
        "paths_python": len(py_paths),
        "paths_go": len(go_paths),
        "paths_shared": len(shared),
        "python_only": sorted(py_paths - go_paths),
        "go_only": sorted(go_paths - py_paths),
        "subcommand_mismatch": subcommand_mismatch,
        "positional_mismatch": positional_mismatch,
        "description_mismatch": help_text_diff,
    }
    return {"totals": totals, "flags": flag_diffs}


def global_flag_universe(node: dict) -> list[str]:
    seen: set[str] = set()
    for path, spec in node.items():
        for key in spec["flags"]:
            seen.add(key)
    return sorted(seen)


def battery_diff(python_probes: list[dict], go_probes: list[dict]) -> list[dict]:
    by_probe_py = {probe["probe"]: probe for probe in python_probes}
    by_probe_go = {probe["probe"]: probe for probe in go_probes}
    drift = []
    for probe in sorted(set(by_probe_py) | set(by_probe_go)):
        py = by_probe_py.get(probe)
        go = by_probe_go.get(probe)
        if py is None or go is None:
            drift.append({"probe": probe, "python": py and py["exit"], "go": go and go["exit"]})
            continue
        if py["exit"] != go["exit"]:
            drift.append(
                {
                    "probe": probe,
                    "argv": go["argv"],
                    "python": py["exit"],
                    "go": go["exit"],
                    "python_stderr": py["stderr_head"],
                    "go_stderr": go["stderr_head"],
                }
            )
    return drift


def probe_diff(python_probes: list[dict], go_probes: list[dict]) -> list[dict]:
    """Diff per-command probes keyed by (probe kind, argv tail)."""
    def key(probe: dict) -> str:
        return f"{probe['probe']} {' '.join(probe['argv'])}"

    py_map = {key(probe): probe for probe in python_probes}
    go_map = {key(probe): probe for probe in go_probes}
    drift = []
    for probe_key in sorted(set(py_map) | set(go_map)):
        py = py_map.get(probe_key)
        go = go_map.get(probe_key)
        py_exit = py["exit"] if py else None
        go_exit = go["exit"] if go else None
        if py_exit != go_exit:
            drift.append({"probe": probe_key, "python": py_exit, "go": go_exit})
    return drift


# ---------------------------------------------------------------------------
# report
# ---------------------------------------------------------------------------


def render_report(contract: dict) -> str:
    lines: list[str] = []
    meta = contract["meta"]
    lines.append("# CLI 对外面契约对照（维度 7：CLI 子命令 / flags / 退出码）")
    lines.append("")
    lines.append(
        f"生成：{meta['generated_at']} · 工作树基线 `{meta['baseline_commit']}` · "
        f"生成器 `go_hive/scripts/cli_surface_contract.py` · 契约 `go_hive/contracts/cli-surface.json`"
    )
    lines.append("")
    lines.append("## 方法与范围")
    lines.append("")
    lines.append(
        "- 参考面（reference）：Python `hive`（`core/framework/cli.py`）与 `hive-browser`（`tools/src/gcu/cli.py`）。"
    )
    lines.append(
        "- 对齐面（target）：Go `hive`（`go_hive/go-hive/cmd/hive`）与 `hive-browser`（`cmd/hive-browser`）。"
    )
    lines.append(
        "- 采集方式：对每个命令路径递归执行 `<cmd> --help`（子命令树、flags、取值占位符、help 文案、"
        "help 中声明的默认值、位置参数），再跑一组无副作用退出码探针（`--help`、伪造 flag、缺必填位置参数，"
        "外加 browser 的 fail-before-bridge 命令表）。"
    )
    lines.append(
        "- 环境固定：`COLUMNS=100`、`HIVE_HOME=<scratch>`、`HIVE_BROWSER_SESSION=cli-surface-contract`、"
        "`HIVE_BROWSERBRIDGE_HTTP=http://127.0.0.1:<dead>`、`NO_COLOR=1`。探针不启动服务、不连桥。"
    )
    lines.append("")
    lines.append("## 门禁保护（C11：CLI 外观逐 slot）")
    lines.append("")
    lines.append(
        "- 本页的 CLI 外观**逐 slot 受门禁保护**：`make compat-check`（`make ci` 已包含）的 C11 用 "
        "`git archive` + 本地 `go build` 把 `meta.baseline_commit` 复现成旧构建、当前树构建成新构建，"
        "核对 `hive --help`、`hive-browser --help`、`hive mcp --help`、`hive-cred --help` 的 usage 行，"
        "以及本契约冻结的命令路径集合与每路径 flag 集合（旧构建 vs 新构建、新构建 vs 契约基线、"
        "新构建 vs Python）；任一漂移即 FAIL（非 0 退出）。"
    )
    lines.append(
        "- 合成漂移自检：`make compat-cli-selftest`（`check_compat_contracts.py --self-test`）注入"
        "「删一个 flag」「改一个 usage 行」，必须判定 FAIL；基线副本不得误报。"
    )
    lines.append(
        "- 改本页任何 slot（usage 行 / 子命令 / flag）都必须重采本契约，否则 C11 直接 FAIL；"
        "全量重采（命令树 + 退出码探针 + help 文案）仍由 `cli_surface_contract.py --check` 在发布时执行。"
    )
    lines.append("")
    lines.append("## 汇总")
    lines.append("")
    summary = contract["summary"]
    lines.append("| 项 | Python | Go | 共有 | Python 独有 | Go 独有 |")
    lines.append("| --- | --- | --- | --- | --- | --- |")
    for cli_name, block in contract["clis"].items():
        totals = block["diff"]["totals"]
        lines.append(
            f"| `{cli_name}` 命令路径 | {totals['paths_python']} | {totals['paths_go']} | "
            f"{totals['paths_shared']} | {len(totals['python_only'])} | {len(totals['go_only'])} |"
        )
        universe = block["flag_universe"]
        lines.append(
            f"| `{cli_name}` flag 名 | {len(universe['python'])} | {len(universe['go'])} | "
            f"{len(universe['shared'])} | {len(universe['python_only'])} | {len(universe['go_only'])} |"
        )
    lines.append(
        f"| 退出码探针漂移（总/两侧可探测） | {summary['probe_drift_total']} / {summary.get('probe_drift_both_sides', 0)} | | | | |"
    )
    lines.append(f"| 命令行为探针漂移 | {summary['battery_drift_total']} | | | | |")
    flag_help_diff = sum(
        len(entry.get("help_mismatch", []))
        for block in contract["clis"].values()
        for entry in block["diff"]["flags"].values()
    )
    lines.append(f"| help 文案差异（命令级） | {summary['text_diff_total']} | | | | |")
    lines.append(f"| flag help 文案差异（非阻断） | {flag_help_diff} | | | | |")
    lines.append("")
    for cli_name, block in contract["clis"].items():
        lines.append(f"## `{cli_name}`")
        lines.append("")
        totals = block["diff"]["totals"]
        lines.append("### 命令树")
        lines.append("")
        lines.append(f"- 共用命令路径 {totals['paths_shared']} 条。")
        if totals["python_only"]:
            lines.append(
                "- 仅 Python：" + ", ".join(f"`{item or '<root>'}`" for item in totals["python_only"])
            )
        else:
            lines.append("- 仅 Python：无。")
        if totals["go_only"]:
            lines.append(
                "- 仅 Go（登记为 Go 扩展或缺口）："
                + ", ".join(f"`{item or '<root>'}`" for item in totals["go_only"])
            )
        else:
            lines.append("- 仅 Go：无。")
        if totals["subcommand_mismatch"]:
            lines.append("")
            lines.append("| 命令 | Python 子命令 | Go 子命令 |")
            lines.append("| --- | --- | --- |")
            for item in totals["subcommand_mismatch"]:
                lines.append(
                    f"| `{item['command'] or '<root>'}` | {_names(item['python'])} | {_names(item['go'])} |"
                )
        lines.append("")
        lines.append("### flags")
        lines.append("")
        universe = block["flag_universe"]
        lines.append(
            f"- 全局 flag 名：共有 {len(universe['shared'])}（{', '.join('`' + item + '`' for item in universe['shared'])}）。"
        )
        lines.append(
            "- Python 独有：" + (", ".join(f"`{item}`" for item in universe["python_only"]) or "无") + "。"
        )
        lines.append(
            "- Go 独有：" + (", ".join(f"`{item}`" for item in universe["go_only"]) or "无") + "。"
        )
        if block["diff"]["flags"]:
            lines.append("")
            lines.append("| 命令 | 项 | 明细 |")
            lines.append("| --- | --- | --- |")
            for path, entry in sorted(block["diff"]["flags"].items()):
                if entry.get("python_only"):
                    lines.append(
                        f"| `{path or '<root>'}` | Python 独有 flag | "
                        + ", ".join(f"`{item}`" for item in entry["python_only"])
                        + " |"
                    )
                if entry.get("go_only"):
                    lines.append(
                        f"| `{path or '<root>'}` | Go 独有 flag | "
                        + ", ".join(f"`{item}`" for item in entry["go_only"])
                        + " |"
                    )
                for item in entry.get("value_mismatch", []):
                    lines.append(
                        f"| `{path or '<root>'}` | 是否取值不一致 | `{item['flag']}`："
                        f"Python 取值为 {item['python']} vs Go 取值为 {item['go']} |"
                    )
                for item in entry.get("choices_mismatch", []):
                    lines.append(
                        f"| `{path or '<root>'}` | 可选值集合不一致 | `{item['flag']}`："
                        f"Python `{item['python']}` vs Go `{item['go']}` |"
                    )
                for item in entry.get("alias_mismatch", []):
                    lines.append(
                        f"| `{path or '<root>'}` | 别名不一致 | `{item['flag']}`："
                        f"{item['python']} vs {item['go']} |"
                    )
                for item in entry.get("default_mismatch", []):
                    lines.append(
                        f"| `{path or '<root>'}` | 默认值（help 声明）不一致 | `{item['flag']}`："
                        f"Python `{item['python']}` vs Go `{item['go']}` |"
                    )
        else:
            lines.append("")
            lines.append("- 无按命令的 flag 集合差异。")
        if totals["positional_mismatch"]:
            lines.append("")
            lines.append("### 位置参数")
            lines.append("")
            lines.append("| 命令 | Python 位置参数 | Python 必填 | Go 位置参数 | Go 必填 |")
            lines.append("| --- | --- | --- | --- | --- |")
            for item in totals["positional_mismatch"]:
                lines.append(
                    f"| `{item['command'] or '<root>'}` | {_names(item['python'])} | "
                    f"必填：{_names(item.get('python_required', []))} | {_names(item['go'])} | "
                    f"必填：{_names(item.get('go_required', []))} |"
                )
        lines.append("")
        lines.append("### 退出码")
        lines.append("")
        lines.append("逐命令探针（`--help`、伪造 flag、缺必填位置参数）：")
        lines.append("")
        go_only_commands = set(block["diff"]["totals"]["go_only"])
        if block["diff"]["probes"]:
            lines.append("| 探针 | Python | Go | 备注 |")
            lines.append("| --- | --- | --- | --- |")
            real, one_sided = 0, 0
            for item in block["diff"]["probes"]:
                note = probe_note(item, go_only_commands, cli_name)
                if item["python"] is None or item["go"] is None:
                    one_sided += 1
                else:
                    real += 1
                lines.append(
                    f"| `{item['probe']}` | {item['python']} | {item['go']} | {note} |"
                )
            lines.append("")
            lines.append(
                f"其中单侧可探测（另一侧 help 未声明该命令的该参数/命令，属探测面差异）{one_sided} 条；"
                f"两侧都可探测且退出码不同 {real} 条。"
            )
        else:
            lines.append("- 无差异。")
        lines.append("")
        lines.append("命令行为探针：")
        lines.append("")
        if block["diff"]["battery"]:
            lines.append("| 探针 | Python | Go | 备注 |")
            lines.append("| --- | --- | --- | --- |")
            for item in block["diff"]["battery"]:
                lines.append(
                    f"| `{item['probe']}` | {item['python']} | {item['go']} | {item.get('note', '')} |"
                )
        else:
            lines.append("- 无差异。")
        lines.append("")
        if block.get("text_diff"):
            lines.append("### help 关键行差异（非阻断，逐项记录）")
            lines.append("")
            lines.append("| 命令 | 字段 | Python | Go |")
            lines.append("| --- | --- | --- | --- |")
            for item in block["text_diff"][:80]:
                lines.append(
                    f"| `{item['command'] or '<root>'}` | {item['field']} | "
                    f"{_clip(item['python'])} | {_clip(item['go'])} |"
                )
            if len(block["text_diff"]) > 80:
                lines.append(f"| … | | 其余 {len(block['text_diff']) - 80} 条见契约 JSON | |")
            lines.append("")
    lines.append("## 默认值核对")
    lines.append("")
    defaults = contract.get("defaults", {})
    lines.append(
        f"- 方法：{defaults.get('method', '')}；只比较两侧都声明为字面量的 flag（符号/常量另有标注）。"
    )
    lines.append(
        f"- 两侧都声明且为字面量：{summary['defaults_literal_compared']} 个 flag，"
        f"其中值不同 {summary['defaults_literal_mismatch']} 个；"
        f"一侧为符号常量/表达式（不参与等值比较）：{summary['defaults_symbolic_only']} 个。"
    )
    if defaults.get("literal_mismatch"):
        lines.append("")
        lines.append("| flag | Python 默认值 | Go 默认值 |")
        lines.append("| --- | --- | --- |")
        for item in defaults["literal_mismatch"]:
            lines.append(f"| `{item['flag']}` | `{item['python']}` | `{item['go']}` |")
    if defaults.get("literal_compared"):
        lines.append("")
        lines.append("已核对且一致：")
        lines.append("")
        lines.append("| flag | 默认值（两侧相同） |")
        lines.append("| --- | --- |")
        for item in defaults["literal_compared"]:
            if item["flag"] in {entry["flag"] for entry in defaults.get("literal_mismatch", [])}:
                continue
            lines.append(f"| `{item['flag']}` | `{item['python'][0]}` |")
    lines.append("")
    lines.append("## 已修 / 保留")
    lines.append("")
    for item in contract["alignment"]["fixed"]:
        lines.append(f"- 已修：{item}")
    for item in contract["alignment"].get("verified", []):
        lines.append(f"- 已核对：{item}")
    for item in contract["alignment"]["kept"]:
        lines.append(f"- 保留：{item}")
    lines.append("")
    lines.append("## 高风险待办（未改，附估算）")
    lines.append("")
    for item in contract["alignment"]["deferred"]:
        lines.append(f"- {item}")
    lines.append("")
    return "\n".join(lines)


def probe_note(item: dict, go_only_commands: set[str], cli_name: str) -> str:
    """Explain one-sided probes so a reader does not count them as code drift."""
    tokens = item["probe"].split()
    command = tokens[1] if len(tokens) > 1 else ""
    if command in go_only_commands:
        return "Go 扩展命令，Python 侧无对应"
    if item["go"] is None:
        return "Go 侧未生成该探针（该命令 help 未声明位置参数）"
    if item["python"] is None:
        if cli_name == "hive-browser":
            return "Python 侧未生成该探针（argparse 把 choices 渲染成无名位置参数）"
        return "Python 侧未生成该探针"
    return "退出码不一致"


def _names(names: list[str]) -> str:
    if not names:
        return "（无）"
    return ", ".join(f"`{name}`" for name in names)


def _clip(text: str, width: int = 140) -> str:
    text = text.replace("|", "\\|")
    return text if len(text) <= width else text[: width - 1] + "…"


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def collect(args) -> dict:
    scratch = Path(args.scratch) if args.scratch else SCRATCH_ROOT
    scratch.mkdir(parents=True, exist_ok=True)
    home = scratch / "home"
    home.mkdir(parents=True, exist_ok=True)
    dead_port = free_port()
    env = build_env(home, "cli-surface-contract", f"http://127.0.0.1:{dead_port}")
    missing_file = str(scratch / "cli-surface-missing-upload.txt")
    if Path(missing_file).exists():
        Path(missing_file).unlink()

    repo = Path(args.repo).resolve() if args.repo else REPO
    go_module = repo / "go_hive" / "go-hive"
    if args.go_bin_dir:
        go_bins = {
            "hive": str(Path(args.go_bin_dir) / "hive"),
            "hive-browser": str(Path(args.go_bin_dir) / "hive-browser"),
        }
    else:
        go_bins = build_go(scratch / "bin", go_module)

    python_bin = find_python(repo, args.python_bin)
    jobs = max(1, int(args.jobs))
    runners: dict[str, dict[str, Runner]] = {}
    runners["hive"] = {"go": Runner("go", [go_bins["hive"]], env, repo, jobs)}
    runners["hive-browser"] = {"go": Runner("go", [go_bins["hive-browser"]], env, repo, jobs)}
    python_skip = None
    if python_bin:
        py_env = dict(env)
        py_env["PYTHONPATH"] = str(repo / "tools" / "src")
        hive_py = Runner("python", [python_bin, str(repo / "core" / "framework" / "cli.py")], py_env, repo, jobs)
        browser_py = Runner("python", [python_bin, "-m", "gcu.cli"], py_env, repo, jobs)
        code, _, err = hive_py.run(["--help"], timeout=PROBE_TIMEOUT)
        if code == 0:
            runners["hive"]["python"] = hive_py
            runners["hive-browser"]["python"] = browser_py
        else:
            python_skip = f"`{python_bin}` 无法运行 core CLI（exit {code}）：{collapse(err)[:200]}"
    else:
        python_skip = "未找到可用的 Python 解释器（--python-bin / <repo>/.venv）"

    contract: dict = {
        "schema": SCHEMA,
        "meta": {
            "generated_at": datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds"),
            "generator": "go_hive/scripts/cli_surface_contract.py",
            "baseline_commit": git_head(repo),
            "reference": "python: core/framework/cli.py, tools/src/gcu/cli.py",
            "target": "go: go_hive/go-hive/cmd/hive, cmd/hive-browser",
            "method": "recursive --help capture + no-side-effect exit-code battery",
            "normalization": "whitespace-collapsed; COLUMNS=100; scratch HIVE_HOME",
            "python_bin": python_bin,
            "python_skip": python_skip,
        },
        "clis": {},
    }

    defaults_scan = scan_source_defaults(repo)

    for cli_name in ("hive", "hive-browser"):
        python_node: dict = {}
        go_node: dict = {}
        py_runner = runners[cli_name].get("python")
        go_runner = runners[cli_name]["go"]
        if py_runner is not None:
            print(f"[{cli_name}] python capture…", file=sys.stderr)
            python_node = capture_cli(py_runner, cli_name)
        print(f"[{cli_name}] go capture…", file=sys.stderr)
        go_node = capture_cli(go_runner, cli_name)

        python_probes = probe_exit_codes(py_runner, python_node) if python_node and py_runner else []
        go_probes = probe_exit_codes(go_runner, go_node)

        table = BROWSER_PROBE_TABLE if cli_name == "hive-browser" else CORE_PROBE_TABLE
        # Serial: concurrent browser probes would race for the bridge port.
        python_battery = run_battery(py_runner, table, missing_file) if python_node and py_runner else []
        go_battery = run_battery(go_runner, table, missing_file)
        battery = battery_diff(python_battery, go_battery) if python_battery else []

        diff = commands_diff(python_node, go_node)
        if python_node:
            diff["probes"] = probe_diff(python_probes, go_probes)
        else:
            diff["probes"] = []
        python_universe = global_flag_universe(python_node) if python_node else []
        go_universe = global_flag_universe(go_node)
        contract["clis"][cli_name] = {
            "python": {"available": bool(python_node), "commands": python_node, "probes": python_probes, "battery": python_battery},
            "go": {"commands": go_node, "probes": go_probes, "battery": go_battery},
            "diff": {"totals": diff["totals"], "flags": diff["flags"], "probes": diff["probes"], "battery": battery},
            "flag_universe": {
                "python": python_universe,
                "go": go_universe,
                "shared": sorted(set(python_universe) & set(go_universe)),
                "python_only": sorted(set(python_universe) - set(go_universe)),
                "go_only": sorted(set(go_universe) - set(python_universe)),
            },
            "text_diff": _text_diff(diff["totals"]["description_mismatch"]),
        }

    summary = {
        "probe_drift_total": sum(len(block["diff"]["probes"]) for block in contract["clis"].values()),
        "probe_drift_both_sides": sum(
            1
            for block in contract["clis"].values()
            for item in block["diff"]["probes"]
            if item["python"] is not None and item["go"] is not None
        ),
        "battery_drift_total": sum(len(block["diff"]["battery"]) for block in contract["clis"].values()),
        "text_diff_total": sum(len(block["text_diff"]) for block in contract["clis"].values()),
        "defaults_literal_compared": len(defaults_scan["literal_compared"]),
        "defaults_literal_mismatch": len(defaults_scan["literal_mismatch"]),
        "defaults_symbolic_only": len(defaults_scan["symbolic_only"]),
    }
    contract["summary"] = summary
    contract["defaults"] = defaults_scan
    alignment_path = REPO / "go_hive" / "docs" / "cli-surface-alignment.json"
    if alignment_path.exists():
        contract["alignment"] = json.loads(alignment_path.read_text("utf-8"))
    else:
        contract["alignment"] = {"fixed": [], "verified": [], "kept": [], "deferred": []}
    return contract


def _text_diff(entries: list[dict]) -> list[dict]:
    """Keep command-level description diffs; flag help diffs live under flags."""
    return [entry for entry in entries if entry["python"] or entry["go"]]


def git_head(repo: Path) -> str:
    try:
        out = subprocess.run(
            ["git", "-C", str(repo), "rev-parse", "--short", "HEAD"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        return out.stdout.strip()
    except Exception:
        return "unknown"


def strip_volatile(contract: dict) -> dict:
    """Projection compared by --check: drops provenance and message heads.

    `baseline_commit` is provenance (the HEAD the surface was captured at), not
    contract: comparing it would make --check fail as soon as the regenerated
    contract is committed, because the commit itself moves HEAD. The report
    still prints it for traceability.
    """
    stable = json.loads(json.dumps(contract))
    for key in ("generated_at", "baseline_commit", "python_bin", "python_skip"):
        stable["meta"].pop(key, None)
    for block in stable["clis"].values():
        for side in ("go", "python"):
            for probe in block[side].get("probes", []):
                probe.pop("stderr_head", None)
                probe.pop("stdout_head", None)
            for probe in block[side].get("battery", []):
                probe.pop("stderr_head", None)
                probe.pop("stdout_head", None)
    return stable


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="regenerate and compare against the committed contract")
    parser.add_argument("--render-only", action="store_true", help="re-render the report from the committed contract")
    parser.add_argument("--contract", default=str(DEFAULT_CONTRACT))
    parser.add_argument("--report", default=str(DEFAULT_REPORT))
    parser.add_argument("--repo", default=None, help="repo root (default: derived from this script)")
    parser.add_argument("--python-bin", default=os.environ.get("CLI_SURFACE_PYTHON"), help="interpreter with core+tools deps")
    parser.add_argument("--go-bin-dir", default=os.environ.get("CLI_SURFACE_GO_BIN_DIR"), help="reuse prebuilt hive/hive-browser")
    parser.add_argument("--scratch", default=os.environ.get("CLI_SURFACE_SCRATCH"), help="scratch dir (binaries + HOME)")
    parser.add_argument("--jobs", default=os.environ.get("CLI_SURFACE_JOBS", "8"), help="parallel CLI invocations (default 8)")
    args = parser.parse_args()

    if getattr(args, "render_only", False):
        contract = json.loads(Path(args.contract).read_text("utf-8"))
        report_path = Path(args.report)
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(render_report(contract), "utf-8")
        print(f"report:   {report_path} (from committed contract)")
        return 0

    contract = collect(args)
    stable = strip_volatile(contract)

    if args.check:
        path = Path(args.contract)
        if not path.exists():
            print(f"cli-surface: {path} 缺失（先运行 cli_surface_contract.py 生成）", file=sys.stderr)
            return 1
        committed = strip_volatile(json.loads(path.read_text("utf-8")))
        if committed == stable:
            print("cli-surface: OK — 两侧 CLI 面与冻结契约一致")
            return 0
        print("cli-surface: DRIFT — 当前 CLI 面与冻结契约不一致：", file=sys.stderr)
        for cli_name in stable["clis"]:
            old = committed["clis"].get(cli_name, {})
            new = stable["clis"][cli_name]
            for side in ("go", "python"):
                old_cmd = old.get(side, {}).get("commands")
                new_cmd = new.get(side, {}).get("commands")
                if old_cmd is None and new_cmd is None:
                    continue
                if old_cmd != new_cmd:
                    added = sorted(set(new_cmd or {}) - set(old_cmd or {}))
                    removed = sorted(set(old_cmd or {}) - set(new_cmd or {}))
                    print(f"  {cli_name}/{side}: +{added} -{removed}", file=sys.stderr)
                    for path_key in sorted(set(old_cmd or {}) & set(new_cmd or {})):
                        if old_cmd[path_key] != new_cmd[path_key]:
                            print(f"    changed: `{path_key or '<root>'}`", file=sys.stderr)
        return 1

    path = Path(args.contract)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(contract, ensure_ascii=False, indent=2, sort_keys=False) + "\n", "utf-8")
    report_path = Path(args.report)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(render_report(contract), "utf-8")

    print(f"contract: {path}")
    print(f"report:   {report_path}")
    print(
        "summary: "
        + ", ".join(
            f"{name} paths={block['diff']['totals']['paths_shared']} "
            f"(py={block['diff']['totals']['paths_python']}, go={block['diff']['totals']['paths_go']}) "
            f"flags shared={len(block['flag_universe']['shared'])} "
            f"py_only={len(block['flag_universe']['python_only'])} "
            f"go_only={len(block['flag_universe']['go_only'])}"
            for name, block in stable["clis"].items()
        )
    )
    print(
        f"drift: probes={stable['summary']['probe_drift_total']} "
        f"battery={stable['summary']['battery_drift_total']} "
        f"description={stable['summary']['text_diff_total']}"
    )
    if contract["meta"]["python_skip"]:
        print(f"python side: SKIP — {contract['meta']['python_skip']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
