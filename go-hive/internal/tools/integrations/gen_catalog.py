#!/usr/bin/env python3
"""Generate catalog_gen.go from the upstream Python integration tree.

The Go port must expose one tool per upstream integration directory
(``tools/src/aden_tools/tools/<name>/``).  Each integration's MCP tool
functions become *actions* on that tool.  This script reads the Python
sources with ``ast`` (no imports, no third-party deps) and emits a Go
literal so the schema surface stays traceable to upstream instead of
being hand-copied.

Usage (from go_hive/go-hive):
    uv run --no-project python internal/tools/integrations/gen_catalog.py

Sources (relative to the repository root):
    tools/src/aden_tools/tools/*/**/*.py
    tools/src/aden_tools/credentials/*.py
"""

from __future__ import annotations

import ast
import pathlib
import re
import sys
from typing import Any

HERE = pathlib.Path(__file__).resolve()
MODULE_ROOT = HERE.parents[3]          # go_hive/go-hive
REPO_ROOT = MODULE_ROOT.parents[1]     # repository root
TOOLS_ROOT = REPO_ROOT / "tools/src/aden_tools/tools"
CREDS_ROOT = REPO_ROOT / "tools/src/aden_tools/credentials"
OUT = HERE.with_name("catalog_gen.go")

# Modules ported in this lane. Everything else is reported as blocked in the
# delivery notes instead of shipping schema-only stubs.
MODULES = (
    "account_info_tool",
    "airtable_tool",
    "csv_tool",
    "github_tool",
    "risk_scorer",
    "slack_tool",
    "stripe_tool",
    "telegram_tool",
    "time_tool",
    "trello_tool",
)

# Upstream actions that cannot be ported without a native dependency
# (csv_sql is backed by DuckDB). Excluded from the catalog rather than
# shipped as a stub.
EXCLUDE_ACTIONS = {
    "csv_tool": {"csv_sql"},
}

TYPE_MAP = {
    "str": "string",
    "int": "integer",
    "float": "number",
    "bool": "boolean",
    "list": "array",
    "dict": "object",
    "List": "array",
    "Dict": "object",
}


def json_type(annotation: str) -> str:
    """Map a Python annotation to a JSON-schema type (first union member)."""
    text = annotation.strip()
    if not text:
        return "string"
    # Optional[...] / X | None -> inner type
    if text.startswith("Optional[") and text.endswith("]"):
        text = text[len("Optional[") : -1]
    if "|" in text:
        parts = [p.strip() for p in text.split("|") if p.strip() not in ("None",)]
        text = parts[0] if parts else "str"
    if text.startswith(("list[", "List[", "Sequence[")):
        return "array"
    if text.startswith(("dict[", "Dict[", "Mapping[")):
        return "object"
    base = text.split("[", 1)[0]
    return TYPE_MAP.get(base, "string")


def clean_doc(text: str) -> str:
    lines = [ln.strip() for ln in (text or "").strip().splitlines()]
    para: list[str] = []
    for ln in lines:
        if not ln and para:
            break
        if ln:
            para.append(ln)
    joined = re.sub(r"\s+", " ", " ".join(para)).strip()
    if len(joined) > 400:
        joined = joined[:397].rstrip() + "..."
    return joined


def collect_actions() -> dict[str, list[dict[str, Any]]]:
    actions: dict[str, list[dict[str, Any]]] = {}
    for directory in sorted(p for p in TOOLS_ROOT.iterdir() if p.is_dir()):
        if directory.name not in MODULES:
            continue
        found: list[dict[str, Any]] = []
        seen: set[str] = set()
        for path in sorted(directory.rglob("*.py")):
            if "__pycache__" in path.parts or "tests" in path.parts:
                continue
            try:
                tree = ast.parse(path.read_text(encoding="utf-8"))
            except (SyntaxError, UnicodeDecodeError):
                continue
            for node in ast.walk(tree):
                if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    continue
                decorators = [ast.unparse(d) for d in node.decorator_list]
                if not any("mcp.tool" in d for d in decorators):
                    continue
                if node.name in seen:
                    continue
                if node.name in EXCLUDE_ACTIONS.get(directory.name, set()):
                    continue
                seen.add(node.name)
                positional = node.args.args
                defaults: list[str | None] = [None] * (
                    len(positional) - len(node.args.defaults)
                ) + [ast.unparse(d) for d in node.args.defaults]
                params: list[tuple[str, str, str | None]] = []
                required: list[str] = []
                for arg, default in zip(positional, defaults):
                    if arg.arg in ("self", "cls"):
                        continue
                    annotation = ast.unparse(arg.annotation) if arg.annotation else ""
                    params.append((arg.arg, json_type(annotation), default))
                    if default is None:
                        required.append(arg.arg)
                found.append(
                    {
                        "name": node.name,
                        "summary": clean_doc(ast.get_docstring(node) or ""),
                        "params": params,
                        "required": required,
                        "source": str(path.relative_to(directory)),
                    }
                )
        if found:
            found.sort(key=lambda a: a["name"])
            actions[directory.name] = found
    return actions


def collect_credentials() -> dict[str, list[str]]:
    env_by_tool: dict[str, list[str]] = {}
    for path in sorted(CREDS_ROOT.glob("*.py")):
        try:
            tree = ast.parse(path.read_text(encoding="utf-8"))
        except (SyntaxError, UnicodeDecodeError):
            continue
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            if getattr(node.func, "id", "") != "CredentialSpec":
                continue
            kwargs = {kw.arg: kw.value for kw in node.keywords if kw.arg}
            env_var = ""
            if "env_var" in kwargs:
                try:
                    env_var = ast.literal_eval(kwargs["env_var"])
                except (ValueError, SyntaxError):
                    env_var = ""
            tools: list[str] = []
            if "tools" in kwargs:
                try:
                    tools = list(ast.literal_eval(kwargs["tools"]))
                except (ValueError, SyntaxError):
                    tools = []
            for tool_name in tools:
                if env_var and env_var not in env_by_tool.setdefault(tool_name, []):
                    env_by_tool[tool_name].append(env_var)
    return env_by_tool


def tool_name(directory: str, actions: list[dict[str, Any]]) -> str:
    if len(actions) == 1:
        return actions[0]["name"]
    name = re.sub(r"_tool$", "", directory)
    name = re.sub(r"_scanner$", "_scan", name)
    name = re.sub(r"_enumerator$", "_enumerate", name)
    name = re.sub(r"_detector$", "_detect", name)
    name = re.sub(r"_scorer$", "_score", name)
    return name


def display_name(directory: str) -> str:
    base = re.sub(r"_tool$|_scanner$|_enumerator$|_detector$|_scorer$", "", directory)
    return " ".join(part.capitalize() for part in base.split("_"))


def go_quote(value: str) -> str:
    escaped = (
        value.replace("\\", "\\\\")
        .replace('"', '\\"')
        .replace("\n", "\\n")
        .replace("\t", "\\t")
    )
    return '"' + escaped + '"'


def go_default(raw: str) -> str | None:
    """Render a Python default literal as a Go literal, or None to omit it."""
    try:
        value = ast.literal_eval(raw)
    except (ValueError, SyntaxError):
        return None
    if value is None or isinstance(value, (list, dict, tuple, set)):
        return None
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return repr(value)
    if isinstance(value, str):
        return go_quote(value)
    return None


def emit(actions_by_dir: dict[str, list[dict[str, Any]]], env_by_tool: dict[str, list[str]]) -> str:
    out: list[str] = []
    out.append("// Code generated by gen_catalog.py; DO NOT EDIT.")
    out.append("//")
    out.append("// Source: tools/src/aden_tools/tools/<integration>/*.py (@mcp.tool functions)")
    out.append("//         tools/src/aden_tools/credentials/*.py (CredentialSpec.tools -> env_var)")
    out.append("")
    out.append("package integrations")
    out.append("")
    out.append("// builtinCatalog is the upstream integration surface: one Tool per")
    out.append("// aden_tools integration directory, one Action per MCP tool function.")
    out.append("var builtinCatalog = []*Tool{")
    for directory, actions in actions_by_dir.items():
        name = tool_name(directory, actions)
        if any(a["name"] == name for a in actions):
            summary = next(a["summary"] for a in actions if a["name"] == name)
        else:
            summary = f"{display_name(directory)} integration ({len(actions)} actions)."
        out.append("\t{")
        out.append(f"\t\tID:          {go_quote(directory)},")
        out.append(f"\t\tName:        {go_quote(name)},")
        out.append(f"\t\tDisplayName: {go_quote(display_name(directory))},")
        out.append(f"\t\tSummary:     {go_quote(summary)},")
        out.append("\t\tActions: []Action{")
        for action in actions:
            out.append("\t\t\t{")
            out.append(f"\t\t\t\tName:    {go_quote(action['name'])},")
            out.append(f"\t\t\t\tSummary: {go_quote(action['summary'])},")
            out.append(f"\t\t\t\tSource:  {go_quote(directory + '/' + action['source'])},")
            if action["required"]:
                req = ", ".join(go_quote(r) for r in action["required"])
                out.append(f"\t\t\t\tRequired: []string{{{req}}},")
            if action["params"]:
                out.append("\t\t\t\tParams: map[string]any{")
                for pname, ptype, default in action["params"]:
                    fields = [f'"type": {go_quote(ptype)}']
                    rendered = go_default(default) if default is not None else None
                    if rendered is not None:
                        fields.append(f'"default": {rendered}')
                    out.append(
                        f"\t\t\t\t\t{go_quote(pname)}: map[string]any{{{', '.join(fields)}}},"
                    )
                out.append("\t\t\t\t},")
            requires = env_by_tool.get(action["name"], [])
            if requires:
                req = ", ".join(go_quote(r) for r in requires)
                out.append(f"\t\t\t\tRequires: []string{{{req}}},")
            out.append("\t\t\t},")
        out.append("\t\t},")
        out.append("\t},")
    out.append("}")
    out.append("")
    return "\n".join(out)


def main() -> int:
    actions = collect_actions()
    env = collect_credentials()
    source = emit(actions, env)
    OUT.write_text(source, encoding="utf-8")
    count = sum(len(v) for v in actions.values())
    print(f"wrote {OUT} — {len(actions)} integrations, {count} actions")
    return 0


if __name__ == "__main__":
    sys.exit(main())
