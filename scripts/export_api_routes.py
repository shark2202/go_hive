#!/usr/bin/env python3
"""Freeze the HTTP route tables of both Hive implementations.

Static extraction only: the Python table is parsed from the ``aiohttp``
registration calls in ``core/framework/server`` and the Go table from the
``mux.HandleFunc("METHOD /path", ...)`` patterns under ``go_hive/go-hive``.
No server is started here; ``freeze_api_responses.py`` owns the live capture.

Output: ``go_hive/contracts/api-routes.json``

The comparison is keyed by path *shape* (``{param}`` names are normalised to
``{}``) because Python and Go routinely give a path parameter a different
name; the original spelling is preserved on each route entry.

Usage:
    uv run python go_hive/scripts/export_api_routes.py
    uv run python go_hive/scripts/export_api_routes.py --check   # exit 1 on drift
"""

from __future__ import annotations

import argparse
import ast
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
PY_SERVER_DIR = REPO_ROOT / "core" / "framework" / "server"
GO_MODULE_DIR = REPO_ROOT / "go_hive" / "go-hive"
OUT_PATH = REPO_ROOT / "go_hive" / "contracts" / "api-routes.json"

HTTP_METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
PY_ROUTE_METHODS = {
    "add_get": "GET",
    "add_post": "POST",
    "add_put": "PUT",
    "add_patch": "PATCH",
    "add_delete": "DELETE",
    "add_head": "HEAD",
    "add_options": "OPTIONS",
}

# Routes that are intentionally outside the JSON contract surface.
SPA_PATHS = {"/", "/{path:.*}"}

# Curated annotations for routes the Python 1.0.14 reference does not
# register.  The annotation is advisory: the manifest is a freeze, not a
# verdict on whether the extension should exist.
GO_EXTENSION_ANNOTATIONS: dict[str, str] = {
    "POST /api/mcp/reload": "Go extension: MCP registry reload; Python reloads per-server only",
    "POST /api/skills/reload": "Go extension: library-wide skill reload; Python exposes per-queen reload only",
    "GET /api/sessions/{}/reminders": "Go extension: reminder-hub drain endpoint (Go-only runtime feature)",
    "DELETE /api/sessions/{}/reminders": "Go extension: reminder-hub dismissal endpoint (Go-only runtime feature)",
    "DELETE /api/sessions/{}/{}": (
        "Go compatibility alias: dispatches DELETE /api/sessions/history/{id} "
        "and DELETE /api/sessions/{id}/colony"
    ),
}

GO_EXTENSION_PREFIXES: list[tuple[str, str]] = [
    ("/api/browser/", "Go extension: browser-bridge control panel (no Python HTTP route)"),
    ("/api/crm/grid/", "Go extension: colony CRM grid surface"),
    ("/api/global/data/", "Go extension: global data table surface"),
    ("/api/global/crm/", "Go extension: global data table surface"),
]

# Python routes served in Go through one combined ServeMux pattern (Go 1.22
# rejects the two shapes as ambiguous with the generic session routes).
ALIAS_COVERAGE: dict[str, str] = {
    "DELETE /api/sessions/history/{}": "DELETE /api/sessions/{first}/{second}",
    "DELETE /api/sessions/{}/colony": "DELETE /api/sessions/{first}/{second}",
}


def read_python_version() -> str:
    pyproject = REPO_ROOT / "core" / "pyproject.toml"
    if not pyproject.is_file():
        return "unknown"
    match = re.search(r'^version\s*=\s*"([^"]+)"', pyproject.read_text(encoding="utf-8"), re.M)
    return match.group(1) if match else "unknown"


def read_go_version() -> str:
    version_file = GO_MODULE_DIR / "internal" / "version" / "version.go"
    if not version_file.is_file():
        return "unknown"
    match = re.search(r'const\s+Version\s*=\s*"([^"]+)"', version_file.read_text(encoding="utf-8"))
    return match.group(1) if match else "unknown"


def git_head(repo: Path) -> str:
    return git_capture(repo, ["rev-parse", "HEAD"])


def git_capture(repo: Path, argv: list[str]) -> str:
    try:
        return subprocess.run(
            ["git", *argv],
            cwd=str(repo),
            capture_output=True,
            text=True,
            timeout=20,
        ).stdout.strip() or "unknown"
    except (OSError, subprocess.TimeoutExpired):
        return "unknown"


def last_commit_for(repo: Path, *paths: str) -> str:
    return git_capture(repo, ["log", "-1", "--format=%H", "--", *paths])


def path_shape(path: str) -> str:
    """Normalise path parameters so ``{session_id}`` == ``{session}``."""
    if path in SPA_PATHS:
        # Python spells its SPA catch-all ``/{path:.*}`` and Go spells it
        # ``/``; both are the same non-API shell.
        return "<spa>"

    def replace(match: re.Match[str]) -> str:
        inner = match.group(1)
        # aiohttp regex parameters keep their regex; collapse them too.
        if ":" in inner:
            return "{}"
        return "{}"

    return re.sub(r"\{([^}]*)\}", replace, path)


def module_string_constants(tree: ast.Module) -> dict[str, str]:
    constants: dict[str, str] = {}
    for node in tree.body:
        if not isinstance(node, ast.Assign):
            continue
        if not isinstance(node.value, ast.Constant) or not isinstance(node.value.value, str):
            continue
        for target in node.targets:
            if isinstance(target, ast.Name):
                constants[target.id] = node.value.value
    return constants


def resolve_string(node: ast.AST, constants: dict[str, str]) -> str | None:
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.Name) and node.id in constants:
        return constants[node.id]
    return None


def extract_python() -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    routes: list[dict[str, Any]] = []
    unresolved: list[dict[str, Any]] = []
    for path in sorted(PY_SERVER_DIR.glob("*.py")):
        try:
            source = path.read_text(encoding="utf-8")
            tree = ast.parse(source)
        except (OSError, SyntaxError) as exc:  # pragma: no cover - repo guard
            unresolved.append({"file": str(path.relative_to(REPO_ROOT)), "reason": str(exc)})
            continue
        constants = module_string_constants(tree)
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            func = node.func
            if not isinstance(func, ast.Attribute):
                continue
            attr = func.attr
            if attr in PY_ROUTE_METHODS:
                method = PY_ROUTE_METHODS[attr]
                path_arg = node.args[0] if node.args else None
                handler_arg = node.args[1] if len(node.args) > 1 else None
            elif attr == "add_route":
                method_str = resolve_string(node.args[0], constants) if node.args else None
                if method_str is None:
                    unresolved.append(
                        {"file": str(path.relative_to(REPO_ROOT)), "line": node.lineno, "reason": "method not resolvable"}
                    )
                    continue
                method = method_str.upper()
                path_arg = node.args[1] if len(node.args) > 1 else None
                handler_arg = node.args[2] if len(node.args) > 2 else None
            else:
                continue
            route_path = resolve_string(path_arg, constants) if path_arg is not None else None
            if route_path is None:
                unresolved.append(
                    {
                        "file": str(path.relative_to(REPO_ROOT)),
                        "line": node.lineno,
                        "reason": f"{attr} path not resolvable",
                    }
                )
                continue
            if method not in HTTP_METHODS:
                continue
            routes.append(
                {
                    "method": method,
                    "path": route_path,
                    "path_shape": path_shape(route_path),
                    "handler": ast.unparse(handler_arg) if handler_arg is not None else "",
                    "source": f"{path.relative_to(REPO_ROOT)}:{node.lineno}",
                    "kind": "spa_fallback" if route_path in SPA_PATHS else "api",
                }
            )
    return routes, unresolved


GO_HANDLE_RE = re.compile(r'HandleFunc\(\s*"([A-Z]+)\s+([^"]+)"\s*,\s*([^)\n]*)', re.MULTILINE)


def extract_go() -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    routes: list[dict[str, Any]] = []
    unresolved: list[dict[str, Any]] = []
    # The API surface is registered by internal/server; cmd/ only wires
    # dependencies and e2e/ contains test gateways that are not Hive routes.
    server_root = GO_MODULE_DIR / "internal" / "server"
    for path in sorted(server_root.rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        try:
            source = path.read_text(encoding="utf-8")
        except OSError as exc:  # pragma: no cover - repo guard
            unresolved.append({"file": str(path.relative_to(REPO_ROOT)), "reason": str(exc)})
            continue
        for match in GO_HANDLE_RE.finditer(source):
            method, route_path, tail = match.group(1), match.group(2), match.group(3).strip()
            line_no = source.count("\n", 0, match.start()) + 1
            handler = tail.split(",")[0].strip() if tail else ""
            if handler in {"func", "func(w", ""}:
                # Multi-line closure: point at the source line instead of
                # inventing a handler name.
                handler = f"<inline closure at {path.name}:{line_no}>"
            routes.append(
                {
                    "method": method,
                    "path": route_path,
                    "path_shape": path_shape(route_path),
                    "handler": handler,
                    "source": f"{path.relative_to(REPO_ROOT)}:{line_no}",
                    "kind": "spa_fallback" if route_path in SPA_PATHS else "api",
                }
            )
    return routes, unresolved


def dedupe(routes: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Collapse exact duplicates; keep a duplicate list for the report."""
    seen: dict[tuple[str, str], dict[str, Any]] = {}
    duplicates: list[dict[str, Any]] = []
    for route in routes:
        key = (route["method"], route["path"])
        if key in seen:
            duplicates.append(route)
            continue
        seen[key] = route
    ordered = sorted(seen.values(), key=lambda r: (r["path"], r["method"]))
    return ordered, duplicates


def annotation_for(route: dict[str, Any]) -> str:
    if route["kind"] == "spa_fallback":
        return "spa-fallback: SPA shell, not an API contract endpoint"
    key = f"{route['method']} {route['path_shape']}"
    if key in GO_EXTENSION_ANNOTATIONS:
        return GO_EXTENSION_ANNOTATIONS[key]
    for prefix, reason in GO_EXTENSION_PREFIXES:
        if route["path"].startswith(prefix):
            return reason
    return "go-extension: route not registered by the Python 1.0.14 reference"


def build_manifest() -> dict[str, Any]:
    py_routes, py_unresolved = extract_python()
    go_routes, go_unresolved = extract_go()
    py_routes, py_dupes = dedupe(py_routes)
    go_routes, go_dupes = dedupe(go_routes)

    py_by_shape: dict[tuple[str, str], dict[str, Any]] = {
        (r["method"], r["path_shape"]): r for r in py_routes
    }
    go_by_shape: dict[tuple[str, str], dict[str, Any]] = {
        (r["method"], r["path_shape"]): r for r in go_routes
    }

    missing = [py_by_shape[key] for key in sorted(set(py_by_shape) - set(go_by_shape), key=lambda k: (k[1], k[0]))]
    extra = [go_by_shape[key] for key in sorted(set(go_by_shape) - set(py_by_shape), key=lambda k: (k[1], k[0]))]
    for route in extra:
        route["extension"] = route["kind"] != "spa_fallback"
        route["annotation"] = annotation_for(route)

    alias_covered = []
    missing_effective = []
    for entry in missing:
        key = f"{entry['method']} {entry['path_shape']}"
        if key in ALIAS_COVERAGE:
            entry["served_by_alias"] = ALIAS_COVERAGE[key]
            alias_covered.append(entry)
        else:
            entry["served_by_alias"] = ""
            missing_effective.append(entry)

    py_paths: dict[str, set[str]] = {}
    go_paths: dict[str, set[str]] = {}
    for route in py_routes:
        py_paths.setdefault(route["path_shape"], set()).add(route["method"])
    for route in go_routes:
        go_paths.setdefault(route["path_shape"], set()).add(route["method"])

    method_mismatch = []
    for shape in sorted(set(py_paths) & set(go_paths)):
        if py_paths[shape] != go_paths[shape]:
            method_mismatch.append(
                {
                    "path_shape": shape,
                    "reference_methods": sorted(py_paths[shape]),
                    "target_methods": sorted(go_paths[shape]),
                    "only_reference": sorted(py_paths[shape] - go_paths[shape]),
                    "only_target": sorted(go_paths[shape] - py_paths[shape]),
                }
            )

    return {
        "schema": "hive-api-routes/1",
        "description": (
            "Frozen HTTP route tables. The contract reference is the Python hive checkout "
            "(reference=python@<commit>); go_hive is the target being aligned (target=go_hive@<commit>). "
            "Static extraction; path parameters are compared by shape, original spelling preserved."
        ),
        "reference_id": f"python@{git_head(REPO_ROOT)}",
        "target_id": f"go_hive@{git_head(REPO_ROOT)}",
        "reference": {
            "implementation": "python",
            "version": read_python_version(),
            "commit": git_head(REPO_ROOT),
            "last_core_commit": last_commit_for(REPO_ROOT, "core"),
            "last_tools_commit": last_commit_for(REPO_ROOT, "tools"),
            "source_root": "core/framework/server",
        },
        "target": {
            "implementation": "go_hive",
            "version": read_go_version(),
            "commit": git_head(REPO_ROOT),
            "source_root": "go_hive/go-hive/internal/server",
        },
        "generated_at": __import__("datetime").datetime.now().astimezone().isoformat(timespec="seconds"),
        "python": {
            "version": read_python_version(),
            "commit": git_head(REPO_ROOT),
            "source_root": "core/framework/server",
            "route_count": len(py_routes),
            "routes": py_routes,
            "duplicates": py_dupes,
            "unresolved": py_unresolved,
        },
        "go": {
            "version": read_go_version(),
            "commit": git_head(REPO_ROOT),
            "source_root": "go_hive/go-hive/internal/server",
            "route_count": len(go_routes),
            "routes": go_routes,
            "duplicates": go_dupes,
            "unresolved": go_unresolved,
        },
        "summary": {
            "reference_routes": len(py_routes),
            "target_routes": len(go_routes),
            "python_routes": len(py_routes),
            "go_routes": len(go_routes),
            "missing_in_target": len(missing),
            "missing_in_target_effective": len(missing_effective),
            "missing_in_target_served_by_alias": len(alias_covered),
            "missing_in_go": len(missing),
            "missing_in_go_effective": len(missing_effective),
            "missing_in_go_served_by_alias": len(alias_covered),
            "extra_in_target": len(extra),
            "extra_in_target_api": sum(1 for r in extra if r["kind"] == "api"),
            "extra_in_target_spa": sum(1 for r in extra if r["kind"] == "spa_fallback"),
            "extra_in_go": len(extra),
            "extra_in_go_api": sum(1 for r in extra if r["kind"] == "api"),
            "extra_in_go_spa": sum(1 for r in extra if r["kind"] == "spa_fallback"),
            "method_mismatch": len(method_mismatch),
        },
        "missing_in_target": missing,
        "missing_in_target_effective": missing_effective,
        "missing_in_target_served_by_alias": alias_covered,
        "extra_in_target": extra,
        "method_mismatch": method_mismatch,
        "missing_in_go": missing,
        "extra_in_go": extra,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Freeze Python and Go HTTP route tables")
    parser.add_argument("--out", default=str(OUT_PATH), help="manifest path")
    parser.add_argument("--check", action="store_true", help="compare with the existing manifest and exit 1 on drift")
    args = parser.parse_args(argv)

    manifest = build_manifest()
    out = Path(args.out).expanduser()
    rendered = json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"

    if args.check:
        if not out.is_file():
            print(f"[routes] missing manifest: {out}", file=sys.stderr)
            return 1
        existing = json.loads(out.read_text(encoding="utf-8"))
        # generated_at is intentionally not part of the drift check.
        for side in ("python", "go"):
            for key in ("routes",):
                if existing.get(side, {}).get(key) != manifest[side][key]:
                    print(f"[routes] DRIFT in {side}.{key}: re-run export_api_routes.py", file=sys.stderr)
                    return 1
        print("[routes] manifest is current", file=sys.stderr)
        return 0

    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(rendered, encoding="utf-8")
    summary = manifest["summary"]
    print(f"[routes] wrote {out}")
    print(
        "[routes] reference(python)={reference_routes} target(go)={target_routes} "
        "missing_in_target={missing_in_target} (effective={missing_in_target_effective}, "
        "alias={missing_in_target_served_by_alias}) extra_in_target={extra_in_target} "
        "(api={extra_in_target_api}, spa={extra_in_target_spa}) "
        "method_mismatch={method_mismatch}".format(**summary)
    )
    for entry in manifest["missing_in_target"][:15]:
        print(f"[routes]   missing: {entry['method']} {entry['path']}  ({entry['source']})")
    if len(manifest["missing_in_target"]) > 15:
        print(f"[routes]   … {len(manifest['missing_in_target']) - 15} more missing")
    for entry in manifest["extra_in_target"][:15]:
        print(f"[routes]   extra:   {entry['method']} {entry['path']}  -> {entry['annotation']}")
    if len(manifest["extra_in_target"]) > 15:
        print(f"[routes]   … {len(manifest['extra_in_target']) - 15} more extra")
    for entry in manifest["method_mismatch"]:
        print(
            f"[routes]   method:  {entry['path_shape']} reference={entry['reference_methods']} "
            f"target={entry['target_methods']}"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
