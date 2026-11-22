#!/usr/bin/env python3
"""Freeze the env-var / configuration.json contract of the Python reference
(Hive) against the Go target (go_hive), and emit machine-readable manifests.

Deliverables (default paths, relative to the repository root):

  go_hive/contracts/env-vars.json        env-var contract + per-name diff status
  go_hive/contracts/config-keys.json     configuration.json contract + per-key diff status
  go_hive/docs/env-config-contract-diff.md   human-readable diff report

The script is deterministic: it reads only the checked-out sources and the
repository HEAD; running it twice on the same commit produces byte-identical
manifests (no timestamps in the output).

Scan scope (recorded in each manifest's meta):

  python  core/**, tools/**, scripts/**, ./hive, ./hive.ps1,
          ./quickstart.sh, ./quickstart.ps1
  go      go_hive/go-hive/internal/**, go_hive/go-hive/cmd/**,
          go_hive/go-hive/e2e/**, go_hive/scripts/**

The browserbridge module (go_hive/browserbridge) is a separate module with its
own protocol surface; it is reported in `meta.scope_note` and excluded from the
per-name tables.

Usage:
  python3 go_hive/scripts/freeze_env_config_contract.py [--root DIR] [--check]

--check re-runs the scan and exits 1 when a manifest is stale (the report-only
companion gate is go_hive/scripts/check_env_config_contract.py).
"""

from __future__ import annotations

import argparse
import ast
import json
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import env_config_contract_data as data  # noqa: E402

SCHEMA_VERSION = 1
SCANNER_VERSION = "1.0.0"

NAME_RE = re.compile(r"HIVE_[A-Z0-9_]+")
# HIVE_RETENTION_<FIELD> is a name pattern, not a literal name.
DYNAMIC_SUFFIX = "HIVE_RETENTION_"

PY_DIR_SCOPE = {
    "core/framework": "runtime",
    "core/tests": "test",
    "tools/src": "runtime",
    "tools/tests": "test",
    "scripts": "tool_dev",
    "quickstart.sh": "runtime",
    "quickstart.ps1": "runtime",
    "hive": "runtime",
    "hive.ps1": "runtime",
}
GO_DIR_SCOPE = {
    "go_hive/go-hive/internal": "runtime",
    "go_hive/go-hive/cmd/hive-longmemeval": "dev_eval",
    "go_hive/go-hive/cmd": "runtime",
    "go_hive/go-hive/e2e": "test",
    "go_hive/scripts": "tool_dev",
}


def path_scope(rel: str, side: str) -> str:
    if rel.endswith("_test.py") or rel.endswith("_test.go") or "/tests/" in rel or "/e2e/" in rel:
        return "test"
    table = PY_DIR_SCOPE if side == "python" else GO_DIR_SCOPE
    best = ""
    best_scope = "runtime"
    for prefix, scope in table.items():
        if rel == prefix or rel.startswith(prefix + "/"):
            if len(prefix) > len(best):
                best, best_scope = prefix, scope
    return best_scope


# ---------------------------------------------------------------------------
# Python scan
# ---------------------------------------------------------------------------

PY_READ = re.compile(
    r"os\.environ\.get\(\s*(?P<q>[\"'])(?P<name>HIVE_[A-Z0-9_]+)(?P=q)\s*(?:,\s*(?P<default>[^()]*?))?\)"
    r"|os\.getenv\(\s*(?P<q2>[\"'])(?P<name2>HIVE_[A-Z0-9_]+)(?P=q2)\s*(?:,\s*(?P<default2>[^()]*?))?\)"
    r"|(?:os\.)?environ\.get\(\s*(?P<q3>[\"'])(?P<name3>HIVE_[A-Z0-9_]+)(?P=q3)\s*(?:,\s*(?P<default3>[^()]*?))?\)"
)
PY_READ_INDEX = re.compile(r"(?:os\.)?environ\[[^\]]*[\"'](?P<name>HIVE_[A-Z0-9_]+)[\"']\]")
PY_HELPER = re.compile(
    r"(?P<fn>[A-Za-z_]\w*[Ee]nv\w*|_env_int|env_int|_int_env|int_env|_env_flag|_env_num|_env_float)"
    r"\(\s*(?P<q>[\"'])(?P<name>HIVE_[A-Z0-9_]+)(?P=q)\s*(?:,\s*(?P<default>[^,)]+))?"
)
PY_WRITE = re.compile(
    r"(?:os\.)?environ\[\s*[\"'](?P<name>HIVE_[A-Z0-9_]+)[\"']\s*\]\s*=(?!=)"
)
PY_FSTRING = re.compile(r"f[\"'][^\"']*HIV[E]_")
PY_DYNAMIC_GET = re.compile(r"os\.environ\.get\(\s*[a-zA-Z_]")


def classify_py_stmt(stmt: str) -> dict:
    """Best-effort parse rule for one Python read statement."""
    s = stmt
    rule = {"parse": "string"}
    if re.search(r"\bbool\(", s):
        rule = {"parse": "truthy", "note": "any non-empty string is True (incl. '0')"}
    elif re.search(r"\.strip\(\)\.lower\(\)\s+not\s+in\s*\(", s) or re.search(r"\.lower\(\)\s+not\s+in\s*\(", s):
        rule = {"parse": "bool_deny_list", "note": "falsey = strip().lower() in (0,false,no,off); empty string handled separately"}
    elif re.search(r"\.strip\(\)\.lower\(\)\s+in\s*\(", s) or re.search(r"\.lower\(\)\s+in\s*\(", s):
        rule = {"parse": "bool_allow_list"}
    elif re.search(r"\bint\(", s):
        rule = {"parse": "int"}
    elif re.search(r"\bfloat\(", s):
        rule = {"parse": "float"}
    elif re.search(r"json\.loads\(", s):
        rule = {"parse": "json"}
    elif re.search(r"\.split\(", s):
        rule = {"parse": "list"}
    elif re.search(r"\bPath\(", s):
        rule = {"parse": "path"}
    rule["snippet"] = s.strip()[:240]
    return rule


def scan_python(root: Path) -> list[dict]:
    occ: list[dict] = []
    targets: list[Path] = []
    for sub in ("core", "tools", "scripts"):
        targets.extend(sorted((root / sub).rglob("*.py")))
    for f in ("hive", "hive.ps1", "quickstart.sh", "quickstart.ps1"):
        p = root / f
        if p.exists():
            targets.append(p)
    for p in targets:
        rel = str(p.relative_to(root))
        if any(part in rel for part in (".venv/", "node_modules/", ".git/")):
            continue
        try:
            text = p.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        lines = text.splitlines()
        scope = path_scope(rel, "python")
        # enclosing symbol per line (python)
        symbol_at: dict[int, str] = {}
        if p.suffix == ".py":
            try:
                tree = ast.parse(text)
                stack = []
                for node in ast.walk(tree):
                    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                        for sub in ast.walk(node):
                            if sub is node:
                                continue
                            start = getattr(sub, "lineno", None)
                            if start is not None:
                                symbol_at.setdefault(start, node.name)
            except SyntaxError:
                pass
        for i, line in enumerate(lines, 1):
            if not NAME_RE.search(line) and DYNAMIC_SUFFIX not in line:
                continue
            names = set(NAME_RE.findall(line))
            if DYNAMIC_SUFFIX in line and not names:
                names = {DYNAMIC_SUFFIX + "*"}
            for name in sorted(names):
                kind = "mention"
                rule = None
                if p.suffix == ".py":
                    stripped = line.strip()
                    m = PY_WRITE.search(line)
                    if m and m.group("name") == name:
                        kind = "write"
                    else:
                        m = PY_READ.search(line)
                        if m and name in (m.group("name"), m.group("name2"), m.group("name3")):
                            kind = "read"
                            default = next(
                                (g for g in (m.group("default"), m.group("default2"), m.group("default3")) if g is not None),
                                None,
                            )
                            # gather the full statement for parse analysis
                            stmt = line
                            if line.count("(") > line.count(")"):
                                depth = line.count("(") - line.count(")")
                                j = i
                                while depth > 0 and j < len(lines):
                                    nxt = lines[j]
                                    stmt += " " + nxt.strip()
                                    depth += nxt.count("(") - nxt.count(")")
                                    j += 1
                            rule = classify_py_stmt(stmt)
                            rule["default"] = default.strip() if default else None
                        else:
                            m = PY_HELPER.search(line)
                            if m and m.group("name") == name:
                                kind = "read"
                                rule = {
                                    "parse": "helper",
                                    "helper": m.group("fn"),
                                    "default": m.group("default").strip() if m.group("default") else None,
                                    "snippet": line.strip()[:240],
                                }
                            elif name == DYNAMIC_SUFFIX + "*" or stripped.startswith("#"):
                                kind = "mention"
                            elif "environ" in line and DYNAMIC_SUFFIX in line:
                                kind = "mention"
                    if kind == "mention" and stripped.startswith("#"):
                        kind = "comment"
                elif p.name == "hive":
                    kind = "mention"  # wrapper script: no HIVE_ env interface
                else:
                    # shell / ps1: only "${NAME...}", "$NAME", "$env:NAME",
                    # GetEnvironmentVariable / SetEnvironmentVariable count.
                    if re.search(r"\[System\.Environment\]::SetEnvironmentVariable\(\s*[\"']" + name, line):
                        kind = "write"
                    elif re.search(r"\$env:" + name + r"\s*=", line):
                        kind = "write"
                    elif re.search(r"\[System\.Environment\]::GetEnvironmentVariable\(\s*[\"']" + name, line) or re.search(r"\$env:" + name + r"\b", line):
                        kind = "read"
                    elif re.search(r"\$\{?" + name + r"[:\}]", line) or re.search(r"\$\{?" + name + r"\b", line):
                        kind = "read"
                    elif re.search(r"export\s+" + name + r"\b", line):
                        kind = "write"
                    else:
                        kind = "mention"
                occ.append(
                    {
                        "name": name,
                        "file": rel,
                        "line": i,
                        "kind": kind,
                        "scope": scope,
                        "symbol": symbol_at.get(i),
                        "rule": rule,
                        "snippet": line.strip()[:240],
                    }
                )
    return occ


# ---------------------------------------------------------------------------
# Go scan
# ---------------------------------------------------------------------------

GO_READ = re.compile(r"os\.(?:Getenv|LookupEnv)\(\s*\"(?P<name>HIVE_[A-Z0-9_]+)\"")
GO_SET = re.compile(r"os\.Setenv\(\s*\"(?P<name>HIVE_[A-Z0-9_]+)\"")
GO_HELPER = re.compile(
    r"(?P<fn>(?:[A-Za-z_]\w*)?[Ee]nv\w*|lookupEnv|lookupEnvFunc|getenv|[a-zA-Z_]\w*[Gg]etenv\w*)"
    r"\(\s*\"(?P<name>HIVE_[A-Z0-9_]+)\"\s*(?:,\s*(?P<default>[^)]*))?\)"
)


def classify_go_stmt(stmt: str) -> dict:
    rule = {"parse": "string"}
    if re.search(r"strconv\.Atoi\(", stmt):
        rule = {"parse": "int"}
    elif re.search(r"strconv\.ParseFloat\(", stmt):
        rule = {"parse": "float"}
    elif re.search(r"strconv\.ParseBool\(", stmt):
        rule = {"parse": "bool"}
    elif re.search(r"time\.ParseDuration\(", stmt):
        rule = {"parse": "duration"}
    elif re.search(r"json\.Unmarshal\(", stmt):
        rule = {"parse": "json"}
    elif re.search(r"strings\.Split\(", stmt):
        rule = {"parse": "list"}
    elif re.search(r"filepath\.Abs\(|filepath\.Join\(", stmt):
        rule = {"parse": "path"}
    if re.search(r"==\s*\"1\"", stmt) and not re.search(r"ParseBool|Atoi", stmt):
        rule["parse"] = "bool_eq_1"
    if re.search(r"(?i)(envBool|EnvBool|boolEnv)", stmt):
        rule["parse"] = "bool"
    if re.search(r"(?i)(envInt|EnvInt|intEnv)", stmt):
        rule["parse"] = "int"
    if re.search(r"(?i)(envFloat|EnvFloat|floatEnv)", stmt):
        rule["parse"] = "float"
    rule["snippet"] = stmt.strip()[:240]
    return rule


def scan_go(root: Path) -> list[dict]:
    occ: list[dict] = []
    roots = [
        root / "go_hive/go-hive/internal",
        root / "go_hive/go-hive/cmd",
        root / "go_hive/go-hive/e2e",
        root / "go_hive/scripts",
    ]
    for base in roots:
        if not base.exists():
            continue
        for p in sorted(base.rglob("*")):
            if not p.is_file() or not (str(p).endswith(".go") or str(p).endswith(".sh") or str(p).endswith(".py")):
                continue
            rel = str(p.relative_to(root))
            try:
                text = p.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            lines = text.splitlines()
            scope = path_scope(rel, "go")
            func_at: dict[int, str] = {}
            cur = None
            for i, line in enumerate(lines, 1):
                fm = re.match(r"func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)", line)
                if fm:
                    cur = fm.group(1)
                func_at[i] = cur
            for i, line in enumerate(lines, 1):
                names = set(NAME_RE.findall(line))
                if DYNAMIC_SUFFIX in line and not names:
                    names = {DYNAMIC_SUFFIX + "*"}
                if not names:
                    continue
                for name in sorted(names):
                    kind = "mention"
                    rule = None
                    if p.suffix == ".go":
                        if line.strip().startswith("//"):
                            kind = "comment"
                        elif GO_SET.search(line):
                            kind = "write"
                        elif GO_READ.search(line):
                            kind = "read"
                            stmt = line
                            if line.count("(") > line.count(")"):
                                depth = line.count("(") - line.count(")")
                                j = i
                                while depth > 0 and j < len(lines):
                                    nxt = lines[j]
                                    stmt += " " + nxt.strip()
                                    depth += nxt.count("(") - nxt.count(")")
                                    j += 1
                            rule = classify_go_stmt(stmt)
                            rule["default"] = ""
                        else:
                            m = GO_HELPER.search(line)
                            if m and m.group("name") == name:
                                kind = "read"
                                rule = {
                                    "parse": "helper",
                                    "helper": m.group("fn"),
                                    "default": m.group("default").strip() if m.group("default") else None,
                                    "snippet": line.strip()[:240],
                                }
                                lower = (m.group("fn") or "").lower()
                                if "bool" in lower:
                                    rule["parse"] = "bool"
                                elif "int" in lower:
                                    rule["parse"] = "int"
                                elif "float" in lower or "duration" in lower:
                                    rule["parse"] = "float"
                    else:
                        kind = "mention"
                    occ.append(
                        {
                            "name": name,
                            "file": rel,
                            "line": i,
                            "kind": kind,
                            "scope": scope,
                            "symbol": func_at.get(i),
                            "rule": rule,
                            "snippet": line.strip()[:240],
                        }
                    )
    return occ


# ---------------------------------------------------------------------------
# Contract assembly
# ---------------------------------------------------------------------------

# Names whose Python read is a *dynamic* lookup whose env-var name comes from
# configuration.json (`api_key_env_var`) or a fixed suffix pattern. They are
# not literal Python-side env names; the Go side mirrors the same lookup.
DYNAMIC_NAMES = {
    "HIVE_RETENTION_*": "HIVE_RETENTION_<FIELD_UPPERCASE> for every RetentionConfig field",
}

# Names that exist only as local shell variables inside the launcher scripts,
# not as environment variables. Kept out of the contract with a reason.
SHELL_LOCAL_EXEMPT = {
    "HIVE_CONFIG_DIR": "quickstart.sh local shell variable (not exported, not read from env)",
    "HIVE_CONFIG_FILE": "quickstart.sh local shell variable",
    "HIVE_CRED_DETECTED": "quickstart.sh local shell variable",
    "HIVE_CRED_DIR": "quickstart.sh local shell variable",
    "HIVE_KEY_FILE": "quickstart.sh local shell variable",
    "HIVE_LINK": "quickstart.sh local shell variable",
    "HIVE_SCRIPT": "quickstart.sh local shell variable",
    "HIVE_SCRIPT_ESCAPED": "quickstart.sh local shell variable",
    "HIVE_LLM_ENDPOINT": "quickstart.sh local shell variable (not env-overridable)",
    "HIVE_LLM_AVAILABILITY_ENDPOINT": "quickstart.sh local shell variable",
    "HIVE_LLM_AVAILABLE": "quickstart.sh local shell variable",
    "HIVE_LLM_STATUS": "quickstart.sh local shell variable",
    "HIVE_PATH_CONSUMERS": "packaging doc/comment mention only",
    "HIVE_PATH_NAMES": "packaging doc/comment mention only",
    "HIVE_API_BASE": "docstring/comment mention only",
}


def aggregate(occ: list[dict]) -> dict[str, dict]:
    out: dict[str, dict] = {}
    for o in occ:
        rec = out.setdefault(
            o["name"],
            {"prod_reads": [], "dev_reads": [], "test_reads": 0, "writes": [], "mentions": 0, "comments": 0, "scopes": set()},
        )
        rec["scopes"].add(o["scope"])
        if o["kind"] == "read":
            if o["scope"] == "test":
                rec["test_reads"] += 1
            elif o["scope"] in ("dev_eval", "tool_dev"):
                rec["dev_reads"].append(o)
            else:
                rec["prod_reads"].append(o)
        elif o["kind"] == "write":
            rec["writes"].append(o)
        elif o["kind"] == "comment":
            rec["comments"] += 1
        else:
            rec["mentions"] += 1
    return out


def site(brief: bool, o: dict) -> dict:
    out = {
        "file": o["file"],
        "line": o["line"],
        "symbol": o.get("symbol"),
    }
    if o.get("rule"):
        out.update({k: v for k, v in o["rule"].items() if v is not None})
    if not brief:
        out["snippet"] = o["snippet"]
    return out


def norm_default(value):
    if value is None:
        return None
    s = str(value).strip()
    if s in ("\"\"", "''"):
        return ""
    if (s.startswith('"') and s.endswith('"')) or (s.startswith("'") and s.endswith("'")):
        s = s[1:-1]
    return s


def compare(prec: dict, grec: dict) -> tuple[str, str]:
    """Auto status + basis string. Curated overrides are applied by the caller."""
    py_reads = prec["prod_reads"]
    go_reads = grec["prod_reads"]
    if not py_reads and not go_reads:
        if prec.get("dev_reads") or grec.get("dev_reads"):
            return "dev_only", "read only from dev/eval or tooling paths"
        if prec["test_reads"] or grec["test_reads"]:
            return "test_only", "read only from test files"
        return "mention_only", "no read sites on either side"
    if py_reads and not go_reads:
        if grec.get("dev_reads"):
            return "go_dev_only", "go reads only in dev/eval or tooling paths"
        if grec["test_reads"]:
            return "go_test_only", "go reads only in tests"
        return "python_only", "python reads, go has no read site"
    if go_reads and not py_reads:
        if prec.get("dev_reads"):
            return "python_dev_only", "python reads only in dev/eval or tooling paths"
        if prec["test_reads"]:
            return "python_test_only", "python reads only in tests"
        return "go_only", "go reads, python has no read site"
    py_defaults = sorted({norm_default(r["rule"].get("default")) for r in py_reads if r.get("rule")}, key=lambda v: (v is None, v or ""))
    go_defaults = sorted({norm_default(r["rule"].get("default")) for r in go_reads if r.get("rule")}, key=lambda v: (v is None, v or ""))
    py_kinds = sorted({r["rule"].get("parse") or "" for r in py_reads if r.get("rule")})
    go_kinds = sorted({r["rule"].get("parse") or "" for r in go_reads if r.get("rule")})
    pd = [d for d in py_defaults if d is not None]
    gd = [d for d in go_defaults if d is not None]
    default_diff = bool(pd and gd and pd != gd)
    kind_diff = py_kinds != go_kinds and not (set(py_kinds) <= {"helper"} or set(go_kinds) <= {"helper"})
    if default_diff and kind_diff:
        return "default_and_parse_differ", f"python default={pd} parse={py_kinds}; go default={gd} parse={go_kinds}"
    if default_diff:
        return "default_differs", f"python default={pd}; go default={gd}"
    if kind_diff:
        return "parse_differs", f"python parse={py_kinds}; go parse={go_kinds}"
    return "aligned", f"defaults={pd or '(none)'} parse={py_kinds}"


# Curated overrides where the auto comparison cannot see the semantics.
# status basis: "curated"; note explains the evidence.
CURATED: dict[str, dict] = {
    "HIVE_HOME": {
        "status": "aligned",
        "note": "py: os.environ.get → Path(expanduser), fallback ~/.hive; go: hivehome.Resolve → abs path, fallback ~/.hive. go additionally expands to absolute path (upstream keeps relative Path()).",
    },
    "HIVE_DESKTOP_MODE": {
        "status": "parse_differs",
        "note": "py: bool(os.environ.get(...)) — any non-empty value incl. \"0\" is true; go reads the raw string and compares == \"1\" where used (credentials/keys.go). Empty vs \"0\" vs \"1\" semantics differ.",
    },
    "HIVE_API_KEY": {
        "status": "aligned",
        "note": "both treat it as a fallback API-key source; py reads at routes_config/queen defaults, go reads in loader/llm_config. Value semantics: non-empty wins.",
    },
    "HIVE_CREDENTIAL_KEY": {
        "status": "aligned",
        "note": "presence-only gate on both sides (encrypted credential store unavailable without it).",
    },
    "HIVE_ADAPTIVE_TOOL_BUDGET": {
        "status": "aligned",
        "note": "env wins over configuration.json over default(true); falsey = strip().lower() in (0,false,no,off); empty string falls through to config on both sides.",
    },
    "HIVE_EMAIL_SENDERS": {
        "status": "aligned",
        "note": "env > configuration.json > default(false); same deny-list parsing. Config is a bool; non-bool is ignored on both sides.",
    },
    "HIVE_STALL_CHECK": {
        "status": "aligned",
        "note": "env > configuration.json stall_check > default(false).",
    },
    "HIVE_MEMORY_TIMELINE": {
        "status": "aligned",
        "note": "env > configuration.json memory_timeline > default(false).",
    },
    "HIVE_RETENTION_*": {
        "status": "parse_differs",
        "note": "py: bool fields treat (0,false,no,'') as false and raise→warn on bad int (keeps file/default value); go maintenance.intEnv silently ignores bad ints. Empty string: py bool→false, int parse error→keep. go: LookupEnv present → f.env(v) with each field's own parser.",
    },
    "HIVE_BRIDGE_PORT": {
        "status": "parse_differs",
        "note": "py: int(os.environ.get(...,\"14829\")) at 4+ sites (raises on garbage); go: parses with fallback where read — verify each site's default/format before claiming byte parity.",
    },
    "HIVE_MICROCOMPACT_KEEP_RECENT": {
        "status": "parse_differs",
        "note": "py: reads raw and validates per-site; go envPositiveInt → <=0 or garbage falls back to default. Negative-value behavior may differ.",
    },
    "HIVE_MAX_VERBATIM_CLIENT_MSGS": {
        "status": "parse_differs",
        "note": "py: raw env → int with local semantics; go envPositiveIntPtr: garbage→default(40), <=0→nil (feature off), >0→value.",
    },
    "HIVE_TASK_LIST_ID": {
        "status": "aligned",
        "note": "non-empty override wins over session-derived id on both sides.",
    },
    "HIVE_MAX_ITER": {
        "status": "python_only",
        "note": "python queen_orchestrator caps the reply loop via HIVE_MAX_ITER; no go reader found.",
    },
    "HIVE_NO_FILE_LOG": {
        "status": "python_only",
        "note": "python observability.logging reads HIVE_NO_FILE_LOG; no go reader found.",
    },
    "HIVE_COMPACTION_DEBUG": {
        "status": "python_only",
        "note": "python compaction.py reads it; no go reader found.",
    },
    "HIVE_HISTORY_READ_WORKERS": {
        "status": "python_only",
        "note": "python routes_sessions reads it; no go reader found.",
    },
    "HIVE_SPLIT_WORKER_EVENTS": {
        "status": "python_only",
        "note": "python host/colony_runtime reads it; no go reader found.",
    },
    "HIVE_SENTINEL_LOG": {
        "status": "python_only",
        "note": "python sentinel/manager reads it; no go reader found.",
    },
    "HIVE_PLAYBOOK_FAST_GRACE": {
        "status": "python_only",
        "note": "python playbook_tools reads it; no go reader found.",
    },
    "HIVE_APP_VERSION": {
        "status": "aligned",
        "note": "both read a runtime-provided version override; go also uses internal/version + buildinfo.",
    },
    "HIVE_CLOUD_BASE": {
        "status": "aligned",
        "note": "trim+empty→nil/\"\" on both sides (cloud features disabled).",
    },
    "HIVE_CLOUD_JWT": {
        "status": "aligned",
        "note": "trim+empty→nil/\"\" on both sides.",
    },
    "HIVE_RUNTIME_KIND": {
        "status": "aligned",
        "note": "strip().lower(); \"cloud\" vs \"local\" (go: local default; py same).",
    },
    "HIVE_RUNTIME_ID": {
        "status": "aligned",
        "note": "strip(); empty falls back to E2B_SANDBOX_ID / persisted runtime_id on both sides.",
    },
    "HIVE_GCU_MEMTRACE": {
        "status": "python_only",
        "note": "platform difference: python gcu memtrace capture is a python-tools-only debug facility; the Go port's browser integration is the Go browser bridge (go_hive/browserbridge + internal/cli/browser_supervisor.go), which samples no GCU heap. Recorded in contracts/config-support-ledger.json.",
    },
    "HIVE_GCU_MEMTRACE_FILE": {
        "status": "python_only",
        "note": "python gcu memtrace only.",
    },
    "HIVE_GCU_MEMTRACE_FRAMES": {
        "status": "python_only",
        "note": "python gcu memtrace only.",
    },
    "HIVE_GCU_MEMTRACE_INTERVAL_S": {
        "status": "python_only",
        "note": "python gcu memtrace only.",
    },
    "HIVE_GCU_MEMTRACE_TOP": {
        "status": "python_only",
        "note": "python gcu memtrace only.",
    },
    "HIVE_GCU_SCREENSHOT_CONCURRENCY": {
        "status": "python_only",
        "note": "python gcu browser inspection only (bridge-side); not in the hive binary contract.",
    },
    "HIVE_BASH_PATH": {
        "status": "aligned",
        "note": "terminal shell override on both sides; value semantics: non-empty path.",
    },
    "HIVE_RIPGREP_PATH": {
        "status": "aligned",
        "note": "terminal search binary override on both sides.",
    },
    "HIVE_STORAGE_PATH": {
        "status": "aligned",
        "note": "attach/image/web-scrape storage root override on both sides; non-empty wins.",
    },
    "HIVE_LLM_BASE_URL": {
        "status": "aligned",
        "note": "OpenAI-compatible base override on both sides; go trims trailing '/' where used.",
    },
    "HIVE_MCP_CALL_RESULT_TIMEOUT": {
        "status": "aligned",
        "note": "seconds; garbage/<=0 falls back to 240s on both sides (py identical timeout constant).",
    },
    "HIVE_DEBUG_EVENTS": {
        "status": "aligned",
        "note": "on values: (1,true,full) or any non-empty except (0,false); matches on both sides.",
    },
    "HIVE_REQUEST_EXECUTOR_MAX": {
        "status": "parse_differs",
        "note": "py: int with fallback in server/app.py; go: strconv with fallback in server/app.go — verify bounds and negative handling at both sites.",
    },
    "HIVE_TOOL_EXECUTOR_WORKERS": {
        "status": "parse_differs",
        "note": "py and go both parse int workers; confirm garbage/negative semantics (py int() may raise/fallback; go Atoi fallback).",
    },
    "HIVE_DESKTOP_PARENT_PID": {
        "status": "aligned",
        "note": "presence used to detect desktop-spawned runtime on both sides.",
    },
    "HIVE_DESKTOP_TOKEN": {
        "status": "aligned",
        "note": "loopback bind guard / desktop auth token on both sides.",
    },
    "HIVE_DESKTOP_THEME": {
        "status": "aligned",
        "note": "chart theme override on both sides.",
    },
    "HIVE_CRED_LOG": {
        "status": "aligned",
        "note": "credential CLI log file path on both sides.",
    },
    "HIVE_REGISTRY_URL": {
        "status": "aligned",
        "note": "skills registry index URL override on both sides.",
    },
    "HIVE_QUEEN_MAIL_URL": {"status": "aligned", "note": "queen-mail MCP stdio client; same GET envs both sides."},
    "HIVE_QUEEN_MAIL_TOKEN": {"status": "aligned", "note": "queen-mail MCP stdio client; same GET envs both sides."},
    "HIVE_QUEEN_MAIL_FROM": {"status": "aligned", "note": "queen-mail MCP stdio client; same GET envs both sides."},
    "HIVE_LIVE_API_BASE": {"status": "dev_eval", "note": "longmemeval live harness env (both sides, dev/eval scope)."},
    "HIVE_LIVE_API_KEY": {"status": "dev_eval", "note": "longmemeval live harness env."},
    "HIVE_LIVE_MODEL": {"status": "dev_eval", "note": "longmemeval live harness env."},
    "HIVE_LME_RESUME": {"status": "dev_eval", "note": "longmemeval harness (python side lives in core/tests/e2e; go cmd/hive-longmemeval)."},
    "HIVE_LME_RUN_DIR": {"status": "dev_eval", "note": "longmemeval harness."},
    "HIVE_TIMELINE_BATCH": {"status": "aligned", "note": "timeline live batch size; both envInt defaults 3."},
    "HIVE_TIMELINE_COOLDOWN": {"status": "aligned", "note": "seconds; both default 300 (go stores as Duration)."},
    "HIVE_PAST_RECALL_TIMEOUT": {"status": "aligned", "note": "seconds; both default 3."},
    "HIVE_MAX_PLAYBOOK_CONCURRENCY": {"status": "aligned", "note": "both default 32."},
    "HIVE_DEFAULT_PLAYBOOK_CONCURRENCY": {"status": "aligned", "note": "both default 8."},
    "HIVE_ACTIVE_WORKERS_SNAPSHOT_MAX": {"status": "aligned", "note": "both default 8."},
    "HIVE_ACTIVE_WORKERS_TASK_MAX": {"status": "aligned", "note": "both default 80."},
    "HIVE_COLONY_PARALLEL_COOLDOWN": {"status": "aligned", "note": "both default 4."},
    "HIVE_COLONY_PARALLEL_MAX": {"status": "aligned", "note": "both default 4."},
    "HIVE_MAX_CONCURRENT_WORKERS": {"status": "aligned", "note": "both default 4."},
    "HIVE_ADAPTIVE_BUDGET_FLOOR": {"status": "aligned", "note": "both default 30."},
    "HIVE_ADAPTIVE_BUDGET_MIN_SAMPLES": {"status": "aligned", "note": "both default 3."},
}


def _read_versions(root: Path) -> tuple[str, str, str]:
    # The baseline is pinned in env_config_contract_data.py, not taken from the
    # current HEAD: the manifests are committed, and embedding HEAD would make
    # every commit invalidate them (self-reference). Anchors are still resolved
    # against the working tree by resolve_evidence().
    head = data.HEAD
    py_version = ""
    go_version = ""
    try:
        m = re.search(r'version\s*=\s*"([^"]+)"', (root / "core/pyproject.toml").read_text(encoding="utf-8"))
        if m:
            py_version = m.group(1)
    except OSError:
        pass
    try:
        m = re.search(r'Version\s*=\s*"([^"]+)"', (root / "go_hive/go-hive/internal/version/version.go").read_text(encoding="utf-8"))
        if m:
            go_version = m.group(1)
    except OSError:
        pass
    return head, py_version, go_version


# ---------------------------------------------------------------------------
# Env contract: reproduce the published audit's counting rule
# ---------------------------------------------------------------------------

# The published audit (go_hive/docs/env-surface-audit.json @ 622cc236) is the
# inventory baseline. AUDIT_BASELINE freezes its counts so the delta against the
# current tree stays visible, and AUDIT_EXPECTED pins the expected post-lane
# state. History:
#   * config-env-align lane: HIVE_MAX_ITER, HIVE_SPLIT_WORKER_EVENTS and
#     HIVE_PYTHON gained Go readers; HIVE_CRM_PRINCIPAL entered the Go tree
#     between the audit commit and that lane; the network-scanners lane removed
#     the Go-only HIVE_NETWORK_SCANNERS switch (Go 96→100, common 66→69,
#     python_only 23→20, go_only 30→31).
#   * env-verbatim-port lane: thirteen Python-only names gained Go readers
#     (tracker worker cadence, history read pool, server log file, compaction
#     debug, LLM turn log, playbook fast grace, sentinel log, agent-name
#     injection and the six resource-sampling knobs), so Go 100→118,
#     common 69→83 and python_only 20→6; the six remaining Python-only names
#     are the GCU platform difference (no Go reader by design).
#   * upstream 1.0.15 merge (2026-11-22, origin/main 6703b434): the reference
#     tree gained HIVE_MEMORY_RECALL (memory_recall override) and
#     HIVE_PAST_RECALL_TIMEOUT, so the Python surface is 90 names.
#   * memory-notes lanes (same day): notes-push added the Go reader for
#     HIVE_MEMORY_RECALL (Go 118→119, common 83→84) and the Settings→Memory
#     routes read it too, so python_only 7→6.
#   * longmemeval e2e runner + installer-verify (same day): the runner's
#     HIVE_LME_* knobs and the verification scripts' switches are Go-only
#     reads (Go 119→125, go_only 35→41); runner/test-harness surface only.
AUDIT_BASELINE = {"python": 89, "go": 96, "common": 66, "python_only": 23, "go_only": 30}
AUDIT_EXPECTED = {"python": 90, "go": 125, "common": 84, "python_only": 6, "go_only": 41}
AUDIT_EXPECTED = {"python": 90, "go": 125, "common": 84, "python_only": 6, "go_only": 41}
# Names the config-env-align lane gave Go readers to (relative to the baseline).
LANE_ENV_PORTED = ("HIVE_MAX_ITER", "HIVE_SPLIT_WORKER_EVENTS", "HIVE_PYTHON")
# Names the env-verbatim-port lane gave Go readers to (relative to the baseline).
VERBATIM_LANE_ENV_PORTED = (
    "HIVE_TRACKER_WORKER_TOOL_CALLS",
    "HIVE_HISTORY_READ_WORKERS",
    "HIVE_NO_FILE_LOG",
    "HIVE_COMPACTION_DEBUG",
    "HIVE_ENABLE_LLM_LOGS",
    "HIVE_SENTINEL_LOG",
    "HIVE_PLAYBOOK_FAST_GRACE",
    "HIVE_AGENT_NAME",
    "HIVE_RESOURCE_INTERVAL_S",
    "HIVE_RESOURCE_HISTORY",
    "HIVE_RESOURCE_WARN_AVAIL_FRAC",
    "HIVE_RESOURCE_CRIT_AVAIL_FRAC",
    "HIVE_RESOURCE_WARN_RENDERERS",
    "HIVE_RESOURCE_CRIT_RENDERERS",
)
# Python-only names that stay without a Go reader because the mechanism they
# tune does not exist in the Go port (recorded as a platform difference in
# contracts/config-support-ledger.json).
PLATFORM_DIFF_ENV = (
    "HIVE_GCU_MEMTRACE",
    "HIVE_GCU_MEMTRACE_FILE",
    "HIVE_GCU_MEMTRACE_FRAMES",
    "HIVE_GCU_MEMTRACE_INTERVAL_S",
    "HIVE_GCU_MEMTRACE_TOP",
    "HIVE_GCU_SCREENSHOT_CONCURRENCY",
)

QUOTED_LITERAL = re.compile(r"[\"'](HIVE_[A-Z0-9_]+)[\"']")


def _audit_files(root: Path, side: str):
    if side == "python":
        for base in ("core", "tools"):
            yield from sorted((root / base).rglob("*.py"))
        for name in ("hive", "hive.ps1", "quickstart.sh", "quickstart.ps1"):
            p = root / name
            if p.exists():
                yield p
    else:
        for base in ("go_hive/go-hive/internal", "go_hive/go-hive/cmd"):
            yield from sorted((root / base).rglob("*.go"))


def _is_test_path(rel: str) -> bool:
    p = rel.replace("\\", "/")
    return (
        "/tests/" in p or "/test/" in p or p.endswith("_test.py") or p.endswith("_test.go")
        or p.endswith("_test.sh") or "conftest" in p or "/testdata/" in p or "/e2e/" in p
    )


def scan_audit_literals(root: Path) -> dict:
    """Exact reproduction of go_hive/docs/env-surface-audit.json's rule:
    quoted HIVE_* literals in production sources, tests/testdata excluded.
    Python: core/*.py + tools/*.py + root launcher scripts.
    Go:     go_hive/go-hive/{internal,cmd}/**/*.go."""
    out = {}
    for side in ("python", "go"):
        names = set()
        for p in _audit_files(root, side):
            rel = str(p.relative_to(root))
            if _is_test_path(rel):
                continue
            try:
                text = p.read_text(encoding="utf-8", errors="replace")
            except OSError:
                continue
            for line in text.splitlines():
                stripped = line.strip()
                if stripped.startswith("//") or stripped.startswith("#"):
                    continue
                for m in QUOTED_LITERAL.finditer(line):
                    name = m.group(1)
                    if name.endswith("_"):
                        name = "HIVE_RETENTION_*"
                    names.add(name)
        out[side] = names
    out["common"] = out["python"] & out["go"]
    out["python_only"] = out["python"] - out["go"]
    out["go_only"] = out["go"] - out["python"]
    return out


def _env_side(agg: dict, name: str) -> dict:
    rec = agg.get(name)
    if rec is None:
        return {"prod_reads": [], "dev_reads": [], "test_reads": 0, "writes": [], "mentions": 0, "comments": 0}
    return {
        "prod_reads": [site(False, o) for o in rec["prod_reads"]],
        "dev_reads": [site(False, o) for o in rec["dev_reads"]],
        "test_reads": rec["test_reads"],
        "writes": [{"file": o["file"], "line": o["line"], "scope": o["scope"]} for o in rec["writes"]],
        "mentions": rec["mentions"],
        "comments": rec["comments"],
    }


ENV_STATUS_NOTES = {
    "HIVE_HOME": "py: Path(expanduser), fallback ~/.hive; go: abs path, fallback ~/.hive. Go's abs conversion is the only behavioral delta.",
    "HIVE_MAX_ITER": "py: int(raw) only when raw.isdigit(), else 999_999; go: envPositiveInt trims and requires > 0, else the configured loop default. Zero/negative/padded values differ.",
    "HIVE_SPLIT_WORKER_EVENTS": "both default off; only 1/true/yes (case-insensitive) enable the split; go trims surrounding whitespace, py does not.",
    "HIVE_DESKTOP_MODE": "py bool() treats \"0\" as true; go compares the raw string with \"1\" where it gates. Parse parity gap.",
    "HIVE_RETENTION_*": "bool fields: (0,false,no,'') false; int fields: invalid ignored (Go) / warned+ignored (py). Empty bool env = false on both.",
    "HIVE_ADAPTIVE_TOOL_BUDGET": "env > configuration.json > true; empty env falls through to file.",
    "HIVE_EMAIL_SENDERS": "env > configuration.json > false; the resolved value is written back to the process env on both sides.",
    "HIVE_STALL_CHECK": "env > configuration.json > false.",
    "HIVE_MEMORY_TIMELINE": "env > configuration.json > false.",
    "HIVE_MEMORY_RECALL": "mode name, strip+lower; unknown/empty falls through to configuration.json memory_recall.mode, then index. Read at each message.",
    "HIVE_MCP_CALL_RESULT_TIMEOUT": "seconds; <=0/garbage -> 240s on both sides. configuration.json mcp.timeout_seconds (Go-only key) wins over this env var.",
    "HIVE_DEBUG_EVENTS": "true for 1/true/full and any non-empty except 0/false.",
    "HIVE_BRIDGE_PORT": "per-site defaults mirror upstream (14829 main bridge, 9229 legacy status). py int() raises on garbage; go ignores the bad value per site.",
    "HIVE_DESKTOP_PARENT_PID": "presence used to detect a desktop-spawned runtime.",
    "HIVE_TASK_LIST_ID": "non-empty override wins over the session-derived task list id.",
    # ---- env-verbatim-port lane: read points and parse semantics both sides ----
    "HIVE_TRACKER_WORKER_TOOL_CALLS": "py: _env_int(…, 20) — int() semantics with ValueError→default; go: agentloop.envInt → pyenv.Int (same rule). Worker tracker cadence; pinned by TestTrackerWorkerToolCallsDefaultAndParse and the live worker reminder test.",
    "HIVE_HISTORY_READ_WORKERS": "py: max(4, int(os.environ.get(name, \"8\"))) sizing a dedicated UI-read thread pool (no except: garbage raises); go: bounded semaphore in internal/server sized by the same expression, floor 4, garbage → 500 (envIntOrError).",
    "HIVE_NO_FILE_LOG": "py: `if not os.getenv(...)` — any non-empty value (incl. \"0\") turns the $HIVE_HOME/logs/hive-server.log handler off (20MB×3); go: same gate in internal/logging, wired into the serve logger by installRuntimeLogBuffer.",
    "HIVE_COMPACTION_DEBUG": "py: `if os.environ.get(...)` before write_compaction_debug_log; go: same presence gate before the $HIVE_HOME/compaction_log/<ts>_<node>.md report.",
    "HIVE_ENABLE_LLM_LOGS": "py: .strip().lower() in (\"1\",\"true\"); go: pyenv.Is1OrTrue — identical opt-in table for the $HIVE_HOME/llm_logs JSONL writer.",
    "HIVE_SENTINEL_LOG": "py: `if not os.environ.get(...)` diverts framework.sentinel.∗ to $HIVE_HOME/logs/sentinel.log (propagate=False); go: same presence gate diverts the sentinel package logger to the same file.",
    "HIVE_PLAYBOOK_FAST_GRACE": "py: max(0.0, float(env or 1.5)) with ValueError→1.5; go: pyenv.Float + clamp, same 1.5 default. The grace window now bounds the Python-playbook tool call; a run past it returns status=started plus a [PLAYBOOK_COMPLETE] bus notice.",
    "HIVE_AGENT_NAME": "injection, not a read: py sets it into each agent's MCP subprocess env (agent_loader.set_mcp_extra_env); go sets it into the colony worker catalogue's MCP env and the env-declaring tools' subprocess env input. Both sides write-only, so the contract status is literal_only.",
    "HIVE_RESOURCE_*": "py: _env_num(name, default, cast) — empty/garbage falls back to the documented default; go: pyenv.Int/Float with the same fallback. Six knobs drive internal/resources (sampler cadence, history depth, avail/renderer warn+crit).",
    "HIVE_GCU_MEMTRACE*": "platform difference: the GCU memory tracer is a Python tools facility (tools/src/gcu/memtrace.py); the Go port replaces the Python GCU with the Go browser bridge, which samples no GCU heap. No Go reader by design — recorded in contracts/config-support-ledger.json with the substitute mechanism.",
    "HIVE_GCU_SCREENSHOT_CONCURRENCY": "platform difference: python gcu screenshot batching; the Go bridge owns its own CDP concurrency. No Go reader by design.",
}

# Names whose auto status needed a hand check (the Go fallback sits a few lines
# below the os.Getenv call, so the extractor cannot always see the default).
ENV_REVIEWED = {
    "HIVE_ADAPTIVE_TOOL_BUDGET": ("aligned", "helper defaults true on both sides (colonyEnvBool/envFeatureFlag mirror _env_flag)."),
    "HIVE_CLOUD_BASE": ("aligned", "strip+empty -> disabled on both sides."),
    "HIVE_DESKTOP_PARENT_PID": ("aligned", "presence/empty gate; no numeric parsing."),
    "HIVE_HOME": ("aligned", "fallback ~/.hive on both; Go converts to an absolute path."),
    "HIVE_MAX_VERBATIM_CLIENT_MSGS": ("aligned", "default 40 on both (DefaultMaxVerbatimClientMessages). Go maps <=0 to nil (feature off) where Python keeps a positive-int list."),
    "HIVE_MCP_CALL_RESULT_TIMEOUT": ("aligned", "both default 240s; garbage/<=0 -> 240s."),
    "HIVE_MEMORY_TIMELINE": ("aligned", "env > configuration.json > false."),
    "HIVE_MICROCOMPACT_KEEP_RECENT": ("aligned", "default 6 on both (DefaultMicrocompactKeepRecent)."),
    "HIVE_REGISTRY_URL": ("aligned", "same default URL string in both trees."),
    "HIVE_REQUEST_EXECUTOR_MAX": ("aligned", "both default 32; invalid int keeps the default."),
    "HIVE_TOOL_EXECUTOR_WORKERS": ("aligned", "both default 64 and floor at 8."),
    "HIVE_BRIDGE_PORT": ("parse_differs", "py int() raises at import on garbage; go parses per site and keeps the default. Per-site defaults (14829/9229) match upstream."),
    "HIVE_DESKTOP_MODE": ("parse_differs", "py bool() treats \"0\" as true; go compares the raw string with \"1\" where it gates."),
}



def build_env_manifest(root: Path, py_occ: list[dict], go_occ: list[dict]) -> dict:
    audit = scan_audit_literals(root)
    py_agg = aggregate(py_occ)
    go_agg = aggregate(go_occ)

    known = set(audit["python"]) | set(audit["go"])
    variables = []
    for name in sorted(known):
        membership = "common" if name in audit["common"] else ("python_only" if name in audit["python_only"] else "go_only")
        pside = _env_side(py_agg, name)
        gside = _env_side(go_agg, name)
        py_reads = pside["prod_reads"]
        go_reads = gside["prod_reads"]
        if py_reads and go_reads:
            status, basis = compare(
                {"prod_reads": py_agg[name]["prod_reads"], "dev_reads": py_agg[name]["dev_reads"], "test_reads": py_agg[name]["test_reads"]},
                {"prod_reads": go_agg[name]["prod_reads"], "dev_reads": go_agg[name]["dev_reads"], "test_reads": go_agg[name]["test_reads"]},
            )
        elif py_reads:
            status, basis = "python_only", "python has a read site, go has none"
        elif go_reads:
            status, basis = "go_only", "go has a read site, python has none"
        else:
            status, basis = "literal_only", "name appears in production literals but no read site was found on either side"
        entry = {
            "name": name,
            "audit_membership": membership,
            "status": status,
            "status_basis": basis,
            "python": pside,
            "go": gside,
        }
        if name in ENV_REVIEWED:
            status, note = ENV_REVIEWED[name]
            entry["status"] = status
            entry["status_basis"] = "reviewed"
            entry["notes"] = note
        elif name in ENV_STATUS_NOTES:
            entry["notes"] = ENV_STATUS_NOTES[name]
        variables.append(entry)

    counts = {
        "python": len(audit["python"]),
        "go": len(audit["go"]),
        "common": len(audit["common"]),
        "python_only": len(audit["python_only"]),
        "go_only": len(audit["go_only"]),
    }
    reproduced = counts == AUDIT_EXPECTED

    published = root / "go_hive/docs/env-surface-audit.json"
    published_compare = None
    if published.exists():
        def norm(name: str) -> str:
            return "HIVE_RETENTION_*" if name == "HIVE_RETENTION_" else name

        try:
            doc = json.loads(published.read_text(encoding="utf-8"))
            hl = doc.get("hive_literals", {})
            pub_common = {norm(n) for n in hl.get("common", [])}
            pub_python_only = {norm(n) for n in hl.get("python_only", [])}
            pub_go_only = {norm(n) for n in hl.get("go_only", [])}
            published_compare = {
                "file": "go_hive/docs/env-surface-audit.json",
                "commit": "622cc236",
                "baseline": AUDIT_BASELINE,
                "counts": {k: hl.get(k) for k in ("python", "go")},
                "common_equal": pub_common == audit["common"],
                "python_only_equal": pub_python_only == audit["python_only"],
                "go_only_equal": pub_go_only == audit["go_only"],
                "common_gained": sorted(audit["common"] - pub_common),
                "common_lost": sorted(pub_common - audit["common"]),
                "python_only_dropped": sorted(pub_python_only - audit["python_only"]),
                "python_only_added": sorted(audit["python_only"] - pub_python_only),
                "normalization": "published 'HIVE_RETENTION_' (trailing underscore) maps to 'HIVE_RETENTION_*'",
            }
        except (OSError, json.JSONDecodeError):
            published_compare = {"file": "go_hive/docs/env-surface-audit.json", "error": "unreadable"}

    status_counts: dict[str, int] = defaultdict(int)
    for v in variables:
        status_counts[v["status"]] += 1

    recommendations = []
    for name, priority, why in data.ENV_PRODUCTION_ONLY:
        recommendations.append({"name": name, "priority": priority, "why": why, "direction": "port to go"})
    for name, why in data.ENV_GO_PRODUCTION_ONLY:
        recommendations.append({"name": name, "priority": "document", "why": why, "direction": "keep go-only, document as extension"})

    return {
        "schema_version": SCHEMA_VERSION,
        "scanner": "go_hive/scripts/freeze_env_config_contract.py",
        "scanner_version": SCANNER_VERSION,
        "audit_reference": {
            "file": "go_hive/docs/env-surface-audit.json",
            "commit": "622cc236",
            "doc": "go_hive/docs/environment-variables.html",
        },
        "verification": {
            "rule": (
                "quoted HIVE_* literals in production sources; tests/testdata/_test.go excluded; "
                "python = core/*.py + tools/*.py + root launcher scripts; go = go_hive/go-hive/{internal,cmd}/**/*.go"
            ),
            "baseline": AUDIT_BASELINE,
            "expected": AUDIT_EXPECTED,
            "reproduced": counts,
            "counts_match": reproduced,
            "published_lists_compared": published_compare,
        },
        "summary": {
            **counts,
            "status_counts": dict(sorted(status_counts.items())),
            "python_read_sites": sum(len(v["python"]["prod_reads"]) for v in variables),
            "go_read_sites": sum(len(v["go"]["prod_reads"]) for v in variables),
        },
        "inventory": {
            "common": sorted(audit["common"]),
            "python_only": sorted(audit["python_only"]),
            "go_only": sorted(audit["go_only"]),
        },
        "recommendations": recommendations,
        "scope_note": (
            "Adopts the published audit (622cc236) as the inventory baseline instead of re-inventing it. "
            "browserbridge's protocol env surface is out of scope; quickstart.sh local shell variables are not env vars. "
            "Python dev/eval vars (LongMemEval, core/tests) are excluded by the audit's production rule."
        ),
        "variables": variables,
    }


# ---------------------------------------------------------------------------
# configuration.json contract
# ---------------------------------------------------------------------------


def resolve_evidence(root: Path, item: dict) -> dict:
    path = root / item["file"]
    if not path.exists():
        raise SystemExit(f"config contract: missing file {item['file']}")
    needle = item["contains"]
    for n, line in enumerate(path.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
        if needle in line:
            return {"file": item["file"], "line": n, "snippet": line.strip()[:200]}
    raise SystemExit(f"config contract: {item['file']} no longer contains {needle!r} — update env_config_contract_data.py")


def build_config_manifest(root: Path) -> dict:
    sections: dict[str, list[dict]] = defaultdict(list)
    status_counts: dict[str, int] = defaultdict(int)
    for key in data.CONFIG_KEYS:
        section = key["path"].partition(".")[0] if "." in key["path"] else "top-level"
        entry = {
            "path": key["path"],
            "type": key["type"],
            "default": key["default"],
            "env_override": key["env"],
            "python": [resolve_evidence(root, e) for e in key["py"]],
            "go": [resolve_evidence(root, e) for e in key["go"]],
            "status": key["status"],
            "notes": key.get("notes", ""),
        }
        sections[section].append(entry)
        status_counts[key["status"]] += 1

    rules = []
    for rule in data.BOUNDARY_RULES:
        rules.append(
            {
                "rule": rule["rule"],
                "contract": rule["contract"],
                "evidence": [resolve_evidence(root, e) for e in rule["evidence"]],
                "status": rule["status"],
                "notes": rule.get("notes", ""),
            }
        )

    head, py_version, go_version = _read_versions(root)
    return {
        "schema_version": SCHEMA_VERSION,
        "scanner": "go_hive/scripts/freeze_env_config_contract.py",
        "scanner_version": SCANNER_VERSION,
        "reference": {"name": "python", "head": head, "version": py_version},
        "target": {"name": "go_hive", "head": head, "version": go_version},
        "method": (
            "curated per-key table (go_hive/scripts/env_config_contract_data.py) whose reader anchors are "
            "resolved and validated against the checked-out source; line numbers are recomputed, a missing "
            "anchor is a hard failure. Types/defaults/invalid-value rules were read from both implementations."
        ),
        "scope_note": (
            "Covers configuration.json keys read by either runtime. Per-colony notifications.json "
            "(sentinel_enabled/channel/target/allowlist/thread) and agent/worker sidecars are separate files "
            "and out of scope. The Go-only `mcp` block is included and marked go_only."
        ),
        "summary": {
            "keys": len(data.CONFIG_KEYS),
            "sections": len(sections),
            "status_counts": dict(sorted(status_counts.items())),
            "env_overrides": sum(1 for k in data.CONFIG_KEYS if k["env"]),
            "boundary_rules": len(rules),
        },
        "sections": [{"name": name, "keys": sections[name]} for name in sorted(sections)],
        "boundary_rules": rules,
    }


# ---------------------------------------------------------------------------
# Markdown report
# ---------------------------------------------------------------------------


def _table(rows: list[list[str]], header: list[str]) -> str:
    def esc(s: str) -> str:
        return str(s).replace("|", "\\|").replace("\n", " ")
    out = ["| " + " | ".join(header) + " |", "|" + "|".join(["---"] * len(header)) + "|"]
    for r in rows:
        out.append("| " + " | ".join(esc(c) for c in r) + " |")
    return "\n".join(out)


def _anchor(entries: list[dict]) -> str:
    if not entries:
        return "—"
    e = entries[0]
    extra = f" (+{len(entries)-1})" if len(entries) > 1 else ""
    return f"`{e['file']}:{e['line']}`{extra}"


def build_doc(env: dict, cfg: dict) -> str:
    s = env["verification"]
    rep = s["reproduced"]
    base = s.get("baseline") or {}
    pc = s.get("published_lists_compared") or {}
    gained = pc.get("common_gained") or []
    lane = [n for n in gained if n in LANE_ENV_PORTED]
    other = [n for n in gained if n not in LANE_ENV_PORTED]

    def _names(items: list[str]) -> str:
        return "、".join(f"`{n}`" for n in items) if items else "无"

    def _baseline_cell(key: str) -> str:
        cur, old = rep.get(key), base.get(key)
        if cur is None:
            return "—"
        if old is None or old == cur:
            return str(cur)
        return f"{old} → {cur}"

    def _verdict(key: str) -> str:
        return "✅" if rep.get(key) == s["expected"].get(key) else "❌"

    counts_note = "复算与「本车道期望」一致。" if s.get("counts_match") else "**复算与期望不一致，见验证块。**"
    baseline_note = "与发布审计基线逐项一致。"
    if gained:
        baseline_note = (
            f"与发布审计基线（Python {base['python']} / Go {base['go']} / 共有 {base['common']} / "
            f"Python 独有 {base['python_only']} / Go 独有 {base['go_only']}）相比，"
            f"差额来自 {len(gained)} 个名字由 Python 独有转为共有：{_names(lane)} 由本车道新增 Go 读取点"
            + (f"，{_names(other)} 在审计提交之后、本车道之前已进入 Go 树" if other else "")
            + f"；Go 独有清单 {AUDIT_BASELINE['go_only']}→{AUDIT_EXPECTED['go_only']}（`HIVE_PYTHON` 新增、`HIVE_NETWORK_SCANNERS` 移除，另有审计后进入 Go 树的开发/评估变量）。"
        )

    lines = [
        "# 环境变量与 configuration.json 契约差异（Python 基准 → go_hive）",
        "",
        f"- reference: `python@{env['verification'].get('head', '') or cfg['reference']['head']}`（Hive {cfg['reference']['version']}）",
        f"- target: `go_hive@{cfg['target']['head']}`（{cfg['target']['version']}）",
        "- 生成器：`go_hive/scripts/freeze_env_config_contract.py`（确定性；`--check` 可重放）",
        "- 机器可读：`go_hive/contracts/env-vars.json`、`go_hive/contracts/config-keys.json`",
        "",
        "## 0. 结论摘要",
        "",
        f"- 环境变量清单以已发布的审计（`go_hive/docs/env-surface-audit.json` @ 622cc236）为基线，本次按同一口径独立复算："
        f"Python **{rep['python']}** / Go **{rep['go']}** / 共有 **{rep['common']}**；"
        f"Python 独有 **{rep['python_only']}**，Go 独有 **{rep['go_only']}**。{counts_note}{baseline_note}",
        f"- configuration.json：**{cfg['summary']['keys']}** 个键 / **{cfg['summary']['sections']}** 个 section；"
        f"其中 `aligned` {cfg['summary']['status_counts'].get('aligned', 0)}，"
        f"`go_partial` {cfg['summary']['status_counts'].get('go_partial', 0)}，"
        f"`python_only` {cfg['summary']['status_counts'].get('python_only', 0)}，"
        f"`go_only` {cfg['summary']['status_counts'].get('go_only', 0)}，"
        f"`unread` {cfg['summary']['status_counts'].get('unread', 0)}。",
        "- 本车道（config-verbatim）把主槽位 `llm.*` 的消费键接进运行时：会话构建统一走 `MainSlotProvider`（键序 = endpoint → api_keys 池 → literal → claude/codex/kimi/antigravity 订阅 → 凭据库 → `api_key_env_var` → provider env；"
        "`MainSlotAPIBase` 复刻订阅端点与 openrouter 默认；`extra_body`/`num_ctx`/订阅请求头随 provider 构建下发），`llm.max_tokens` 预算 queen 回合、`llm.max_context_tokens` 参与 queen 窗口的主槽位回退，"
        "`llm.api_keys` 以 `llm.KeyPool` 轮转并在 429 时停键；kimi 订阅运行时接通（`~/.kimi/config.toml` token + `https://api.kimi.com/coding`）。",
        "- 模型槽位状态收敛：`queen_llm.*`（8）、`worker_llm.*`（10）、`vision_fallback.*`（7）全部转 `aligned`（逐键读取点与热切换矩阵见 §2 与 `docs/configuration.md` §3.8）；"
        "`worker_llm.max_tokens` 增加读取点 `WorkerMaxTokens`（与上游 `get_worker_max_tokens` 同为无调用点）；"
        "`channels.<name>.enabled` 由 `python_only` 变为 `go_partial`：字段按上游形状读取、写入时保留、GET /api/channels 形状不变，启用时 serve 启动打印「无渠道宿主、不会运行」的告警。",
        "- 仍未对齐的 4 个残余（结构性理由 + 替代验证见 `contracts/config-support-ledger.json`）："
        "`llm.antigravity_client_id` / `llm.antigravity_client_secret`（只读；唯一上游消费者是未移植的 AntigravityProvider 内的 OAuth 刷新）、"
        "`llm.use_antigravity_subscription`（同上：会话构建对该标志显式报错而非静默发无键请求）、"
        "`channels.<name>.enabled`（无渠道插件宿主，读到但不生效）。"
        "`mcp.*` 4 键为逐项确认过的 Go 扩展（上游 MCP 来自 registry/`.mcp.json`，从不读 configuration.json 的 `mcp` 块）；"
        "`created_at` 为安装器写入、两侧运行时都不读（Go 在配置写入时保留，见测试）。",
        "- 环境变量侧，env-verbatim-port 车道已把 `HIVE_TRACKER_WORKER_TOOL_CALLS`、`HIVE_HISTORY_READ_WORKERS`、"
        "`HIVE_NO_FILE_LOG`、`HIVE_COMPACTION_DEBUG`、`HIVE_ENABLE_LLM_LOGS`、`HIVE_SENTINEL_LOG`、"
        "`HIVE_PLAYBOOK_FAST_GRACE`、`HIVE_AGENT_NAME`（注入）与 6 个 `HIVE_RESOURCE_*` 接进 Go 运行时；"
        "剩余 Python 独有仅 GCU memtrace / GCU 截图并发（6 个名字），是显式平台差异，"
        "逐项理由与替代证据见 `contracts/config-support-ledger.json`。",
        "",
        "## 1. 环境变量核对（采用既有审计清单，独立复算）",
        "",
        "审计口径：两侧源码中的带引号 `HIVE_*` 字面量，排除 tests/testdata/_test.go；"
        "Python 范围 `core/` + `tools/` + 根启动脚本，Go 范围 `go_hive/go-hive/{internal,cmd}`。"
        "本次用同一规则复算，并与「本车道期望」（基线 + 本车道已知变更）逐项核对：",
        "",
        _table(
            [
                ["Python", str(rep["python"]), str(s["expected"]["python"]), _baseline_cell("python"), _verdict("python")],
                ["Go", str(rep["go"]), str(s["expected"]["go"]), _baseline_cell("go"), _verdict("go")],
                ["共有", str(rep["common"]), str(s["expected"]["common"]), _baseline_cell("common"), _verdict("common")],
                ["Python 独有", str(rep["python_only"]), str(s["expected"]["python_only"]), _baseline_cell("python_only"), _verdict("python_only")],
                ["Go 独有", str(rep["go_only"]), str(s["expected"]["go_only"]), _baseline_cell("go_only"), _verdict("go_only")],
            ],
            ["项", "本次复算", "本车道期望", "发布审计基线", "判定"],
        ),
        "",
        "> `env-vars.json` 逐个名字给出两侧的读取点/默认值，并标注 `audit_membership`；"
        "只出现在字面量里、没有读取点的名字标为 `literal_only`，避免把 MCP env 字段名或注释当成读取面。",
        "",
    ]
    if isinstance(pc, dict) and "common_equal" in pc:
        lines.append(
            "逐名单比对（相对发布审计）："
            f"common {'一致' if pc['common_equal'] else '不一致'}（新增 {_names(pc.get('common_gained') or [])}，"
            f"移除 {_names(pc.get('common_lost') or [])}）、"
            f"python_only {'一致' if pc['python_only_equal'] else '不一致'}（减少 {_names(pc.get('python_only_dropped') or [])}）、"
            f"go_only {'一致' if pc['go_only_equal'] else '不一致'}"
            f"（{pc.get('normalization', '')}）。"
        )
        lines.append("")
    lines += [
        "### 1.1 Python 独有：对齐建议清单",
        "",
        f"- config-env-align 车道已接入：{_names(list(LANE_ENV_PORTED))}；env-verbatim-port 车道已接入：{_names(list(VERBATIM_LANE_ENV_PORTED))}（Go 读取点与测试见 `contracts/config-support-ledger.json`）。",
        f"- 剩余 Python 独有：{_names(list(PLATFORM_DIFF_ENV))} — 均为 GCU 平台差异（Go 侧以 browserbridge 替代 Python GCU 进程），已在支持矩阵逐项登记理由与替代证据；20 个名字的逐项事实表（Python 读取点/默认/解析 → Go 读取点/默认/解析 → 状态与证据）见 `docs/env-verbatim-port.md`。",
        "",
        _table(
            [[r["name"], r["priority"], r["why"]] for r in env["recommendations"] if r["direction"] == "port to go"],
            ["变量", "优先级", "作用 / 建议"],
        ),
        "",
        "### 1.2 Go 独有：保留并写清为扩展",
        "",
        _table(
            [[r["name"], r["why"]] for r in env["recommendations"] if r["direction"].startswith("keep")],
            ["变量", "说明"],
        ),
        "",
        "## 2. configuration.json 逐键契约",
        "",
        f"共 {cfg['summary']['keys']} 个键。状态定义见生成器数据文件头："
        "`aligned` 两侧读取且类型/默认/语义一致；`go_partial` Go 有该键（REST/CRUD 或部分路径）但运行时读取点缺失或未接线；"
        "`python_only` Go 无读取；`go_only` Python 无对应键；`unread` 两侧都不读。",
        "",
        "### 2.0 读取 / 写入 / 生效速查（逐键明细在各小节）",
        "",
        "| 段 | Go 运行时读取点 | 写入接口 | 生效时机（热切换矩阵） |",
        "|---|---|---|---|",
        "| `llm` | `MainSlotProvider`（会话构建时整体解析） | `PUT /api/config/llm`、`PUT /api/config/llm-sections`(role=llm)、`PUT /api/models/slots/llm` | 写入即重建运行中会话 transport（跳过有自己模型的 queen），**下一回合**生效；进行中回合保持旧 provider（回合级 pin，见 `docs/configuration.md` §3.8） |",
        "| `queen_llm.<id>` | `queenAwareChatProvider` → `QueenLLMSection` | `PUT /api/queen/{id}/llm`、`PUT /api/models/slots/queen:<id>` | 同上（`sessions_switched` 计数）；清空 section 回主模型 |",
        "| `worker_llm` | `workerChatProvider` → `WorkerLLMSection` | `PUT /api/config/llm-sections`(role=worker_llm)、`PUT /api/models/slots/worker_llm` | **下一个 colony/会话**构建时（与上游 `_write_role_section` 一致，运行中会话不换） |",
        "| `vision_fallback` | `visionCaptionerFor`（每次 caption 重新解析） | `PUT /api/config/llm-sections`(role=vision_fallback)、`PUT /api/models/slots/vision_fallback` | **下一张工具图片**即用新值，无需重建会话 |",
        "| `judge` | `server/judge.go`（每次判定新建 client） | `PUT /api/config/llm-sections`(role=judge) | 立即 |",
        "| `channels.<name>.enabled` | `ChannelSections`（serve 启动告警） | 无接口（配置文件/安装器写入） | **不生效**（无渠道宿主）；字段按上游形状读取并在任何配置写入时保留 |",
        "| `mcp.*` | `readMCPFileSettings`（serve 启动装载） | 无接口（配置文件） | serve 启动；`mcp.timeout_seconds` 还受 `HIVE_MCP_CALL_RESULT_TIMEOUT` 覆盖（env > file） |",
        "| 顶层开关 / `retention` / `sentinel` / `soul` / `queen_mail` / `turn_context` / `user_profile` / `rate_limits` 等 | 各自模块的读取点（见各小节表格） | `PUT /api/config/features`、按模块接口或配置文件 | 各自模块在“每次读取/启动/判定”时重新解析，详见各小节 notes 与 `docs/configuration.md` §7 |",
        "",
    ]

    for section in cfg["sections"]:
        lines.append(f"### 2.{cfg['sections'].index(section)+1} `{section['name']}`")
        lines.append("")
        rows = []
        for k in section["keys"]:
            rows.append(
                [
                    f"`{k['path']}`",
                    k["type"],
                    json.dumps(k["default"], ensure_ascii=False) if not isinstance(k["default"], str) else k["default"],
                    k["env_override"] or "—",
                    _anchor(k["python"]),
                    _anchor(k["go"]),
                    k["status"],
                ]
            )
        lines.append(_table(rows, ["键", "类型", "默认", "env 覆盖", "Python 读取点", "Go 读取点", "状态"]))
        notes = [(k["path"], k["notes"]) for k in section["keys"] if k["notes"]]
        if notes:
            lines.append("")
            for path, note in notes:
                lines.append(f"- `{path}`：{note}")
        lines.append("")

    lines += [
        "## 3. 优先级与边界",
        "",
        "总体规则：**env > configuration.json > 内置默认值**。下面把容易写错的边界单独冻结；"
        "逐条证据在 `config-keys.json.boundary_rules`。",
        "",
        _table(
            [[r["rule"], r["contract"], r["status"]] for r in cfg["boundary_rules"]],
            ["规则", "契约", "状态"],
        ),
        "",
        "**空串 / 非 bool / 越界 / 坏 JSON 的处理要点**",
        "",
        "- 功能开关（`adaptive_tool_budget`/`email_senders`/`stall_check`/`memory_timeline`）env 为空串或纯空白时按“未设置”处理，落到 configuration.json；"
        "`retention.enabled` 的空串则按 false 处理。两侧实现一致，这是两个不同的约定，不要合并。",
        "- 非 bool 的 env 值走 deny-list：`0/false/no/off` 为 false，其它任何非空值为 true。"
        "因此 `HIVE_STALL_CHECK=disabled` 在两侧都为 true（刻意与 strconv.ParseBool 不同）。",
        "- JSON 里类型不对的值：布尔开关要求 JSON bool（`1` 不算 true）；整数要求 JSON integer（`7.0`、`true`、字符串都被忽略）；"
        "section 不是对象时按空对象处理。",
        "- 坏 JSON / 文件不可读：两侧都按 `{}` 处理并走默认值（Python 记 warning）；都接受 UTF-8 BOM。",
        "- 越界：soul `hour` 0..23、各 count ≥1、sentinel seconds ≥1、`gcu_viewport_scale` 0.1..1.0；"
        "越界时 soul/gcu 回默认，sentinel 夹到下限。",
        "- **MCP 调用超时**：`HIVE_MCP_CALL_RESULT_TIMEOUT` > `mcp.timeout_seconds` > 240s 默认（env > file，符合总体规则）。"
        "Go 早期版本这里是 file > env 的反向优先级，已在本车道修复（`TestMCPCallResultTimeoutPrecedence` 钉住）；"
        "`mcp.*` 块是 Go 扩展，`config-keys.json` 标 `go_only`。",
        "",
        "## 4. 差异清单",
        "",
    ]

    by_status: dict[str, list[dict]] = defaultdict(list)
    for section in cfg["sections"]:
        for k in section["keys"]:
            by_status[k["status"]].append(k)
    for status in ("go_partial", "python_only", "go_only", "unread", "default_differs", "type_differs", "parse_differs"):
        items = by_status.get(status, [])
        if not items:
            continue
        lines.append(f"### {status}（{len(items)}）")
        lines.append("")
        for k in items:
            note = f" — {k['notes']}" if k["notes"] else ""
            lines.append(f"- `{k['path']}`{note}")
        lines.append("")

    total_non_aligned = sum(len(v) for k, v in by_status.items() if k != "aligned")
    lines += [
        f"非 `aligned` 键合计 **{total_non_aligned}** 个，已全部列出（上表）。",
        "",
        "## 5. 复现与门禁",
        "",
        "```bash",
        "# 重新生成两份 manifest 与本报告（确定性输出）",
        "python3 go_hive/scripts/freeze_env_config_contract.py",
        "# 校验 manifest 与当前代码一致；报告型门禁（默认退出 0）",
        "python3 go_hive/scripts/check_env_config_contract.py",
        "python3 go_hive/scripts/check_env_config_contract.py --strict   # 有漂移/缺失/多余时退出 1",
        "```",
        "",
        "Go 侧另有一个报告型测试 `internal/envcontract`（读取 `../../../contracts/*.json`，只 t.Logf 计数，不阻断）。"
        "它带 `// Parity-exempt: ...` 注解：上游 Python 没有对应测试，这是 Go 特有的治理工件。",
        "",
        "## 6. 覆盖与限制",
        "",
        "- 本报告冻结的是**配置契约**，不是行为实现；`aligned` 表示两侧读取同一键且默认/类型/语义一致，不表示端到端行为已验证。",
        "- `go_partial` 的判定基于生产代码里是否存在读取点；REST/CRUD 端点存在不等于运行时消费。",
        "- 未覆盖：per-colony `notifications.json`、agent/worker 子配置文件、`.mcp.json`、前端 localStorage；这些不是全局 `configuration.json`。",
        "- 环境变量采用既有审计（622cc236）的清单为基线；config-env-align 车道（`HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS`、`HIVE_PYTHON`）与 env-verbatim-port 车道（14 个名字，含 `HIVE_AGENT_NAME` 注入）新增的 Go 读取点已计入，network-scanners 车道移除的 Go 独有 `HIVE_NETWORK_SCANNERS` 也已反映。"
        "`env-vars.json` 里的读取点由扫描器补充，默认值/解析差异对运行时相关变量做了人工核对。",
        "",
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[2]))
    parser.add_argument("--check", action="store_true", help="fail when manifests are stale")
    args = parser.parse_args(argv)

    root = Path(args.root).resolve()
    py_occ = scan_python(root)
    go_occ = scan_go(root)
    env_manifest = build_env_manifest(root, py_occ, go_occ)
    cfg_manifest = build_config_manifest(root)
    doc = build_doc(env_manifest, cfg_manifest)

    out_dir = root / "go_hive/contracts"
    out_dir.mkdir(parents=True, exist_ok=True)
    env_path = out_dir / "env-vars.json"
    cfg_path = out_dir / "config-keys.json"
    doc_path = root / "go_hive/docs/env-config-contract-diff.md"
    env_payload = json.dumps(env_manifest, indent=2, sort_keys=False, ensure_ascii=False) + "\n"
    cfg_payload = json.dumps(cfg_manifest, indent=2, sort_keys=False, ensure_ascii=False) + "\n"

    if args.check:
        stale = []
        for path, payload in ((env_path, env_payload), (cfg_path, cfg_payload), (doc_path, doc)):
            existing = path.read_text(encoding="utf-8") if path.exists() else ""
            if existing != payload:
                stale.append(str(path))
        if stale:
            print("STALE: " + ", ".join(stale), file=sys.stderr)
            return 1
        print("OK: env-vars.json, config-keys.json and env-config-contract-diff.md are up to date")
        return 0

    env_path.write_text(env_payload, encoding="utf-8")
    cfg_path.write_text(cfg_payload, encoding="utf-8")
    doc_path.write_text(doc, encoding="utf-8")

    s = env_manifest["verification"]["reproduced"]
    print(f"wrote {env_path}")
    print(f"wrote {cfg_path}")
    print(f"wrote {doc_path}")
    print(
        "env audit: python={python} go={go} common={common} python_only={python_only} go_only={go_only} "
        "(match={match})".format(**s, match=env_manifest["verification"]["counts_match"])
    )
    print(f"config: {cfg_manifest['summary']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
