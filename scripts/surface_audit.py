#!/usr/bin/env python3
"""API / 仓库表面审计：把「看起来有」变成「可执行地验证有」。

三类表面：

1. 前端 -> Go：`frontend/src/api/*.ts` 里每一处 `api.get/post/...` 的路径，
   必须在 `internal/server/*.go` 里有同方法、同形状的注册；否则运行时就是 404。
2. Python -> Go：上游 `core/framework/server/routes_*.py` 的每条路由，同样要在
   Go 侧有对应注册。按前缀分组报告，已知不适用项进 allowlist。
3. 仓库卫生：空目录（空壳占位）、`batchN`/`laneN` 命名文件、以及 Go 规模门禁
   `size_test.go` 里指向不存在文件的 waiver key。

用法：
    surface_audit.py [--json] [--strict] [--allowlist PATH]
                     [--root PATH] [--write-report] [--report PATH]

    --json           机器可读输出（qc_check 用）
    --strict         有未豁免缺失 / 卫生错误时 exit 1
    --allowlist      豁免清单，默认 go_hive/scripts/surface_audit_allowlist.json
    --write-report   生成 go_hive/docs/surface-audit.md
    --report         覆盖报告路径

只依赖 Python 标准库。
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

DEFAULT_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_ALLOWLIST = "go_hive/scripts/surface_audit_allowlist.json"
DEFAULT_REPORT = "go_hive/docs/surface-audit.md"

# 空目录 / lane 文件扫描时跳过的目录名。`.wt` 是并行 worktree，扫进来既慢又吵。
# `.hive_go` 是 HIVE_HOME 运行时状态（会话/凭据/记忆等），不是交付源码；
# 运行期间出现空目录是常态，不能当作落地残渣阻断门禁。
SKIP_DIR_NAMES = {
    ".git", ".wt", ".hive_go", ".venv", "venv", "node_modules", "__pycache__",
    ".pytest_cache", ".mypy_cache", ".ruff_cache", ".cache", "build",
    "dist", "coverage", "target", ".next", ".idea",
}
# 仓库根目录下的 agent/运行时状态目录：不是交付物，空目录是常态。
SKIP_ROOT_DIRS = {
    ".claude", ".hermes", ".hive", ".omp", ".opcteam", ".pi", ".standalone",
    "pi_hive",
}

FRONTEND_METHODS = {
    "get": "GET",
    "post": "POST",
    "put": "PUT",
    "patch": "PATCH",
    "delete": "DELETE",
    "upload": "POST",
}
PARAM = "{param}"
LANE_FILE_RE = re.compile(r"(?:^|[/_.-])(?:batch|lane)\d", re.IGNORECASE)
WAIVER_SOURCE = "go_hive/go-hive/internal/quality/size_test.go"

# 前端调用起点：api.get<Type>( / api.upload(...)。
# 泛型可能跨行且含对象字面量；用「不跨越下一条 api 调用」限制搜索范围。
FRONTEND_CALL_RE = re.compile(
    r"\bapi\.(get|post|put|patch|delete|upload)\s*"
    r"(?:<(?:(?!\bapi\.)[\s\S])*?>\s*)?"
    r"\(\s*"
)
FRONTEND_CONST_RE = re.compile(
    r"\bconst\s+(?P<name>[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*"
    r"(?:(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>\s*)?"
    r"(?P<lit>`|[\"'])"
)
GO_ROUTE_RE = re.compile(
    r"\bmux\.(?:HandleFunc|Handle)\(\s*\"(?P<method>[A-Z]+)\s+(?P<path>[^\"]+)\""
)
PY_ROUTE_RE = re.compile(
    r"\b(?:r|app\.router)\.add_(?P<method>get|post|put|patch|delete)\("
    r"\s*(?P<arg>\"[^\"]*\"|'[^']*'|[A-Z][A-Z0-9_]*)"
)
PY_CONST_RE = re.compile(
    r"^([A-Z][A-Z0-9_]*)\s*=\s*[\"'](?P<path>/api/[^\"']*)[\"']", re.MULTILINE
)
TS_TEMPLATE_EXPR_RE = re.compile(r"^[A-Za-z_$][\w$]*\((.*)\)$", re.DOTALL)
CALL_ARG_NAME_RE = re.compile(r"([A-Za-z_$][\w$]*)\s*\(")
QUERY_SUFFIX_VARS = {"qs", "q", "query", "params", "search", "queryString"}
# internal/tools/integrations 的文件名就是批次号（batch2_arxiv.go 等），
# 这是既有目录约定，不算泄漏；其他目录出现 batchN/laneN 才是落地残渣。
LANE_CONVENTION_DIRS = ("go_hive/go-hive/internal/tools/integrations",)


@dataclass(frozen=True)
class Route:
    method: str
    path: str
    source: str

    def key(self):
        return (self.method, self.path)

    def to_dict(self):
        return {"method": self.method, "path": self.path, "source": self.source}


@dataclass
class MissingRoute:
    route: Route
    status: str = "pending"  # pending | allowlisted
    owner: str = ""
    reason: str = ""

    def to_dict(self):
        data = self.route.to_dict()
        data.update({"status": self.status, "owner": self.owner, "reason": self.reason})
        return data


# ---------------------------------------------------------------------------
# 路径归一化
# ---------------------------------------------------------------------------


def normalize_path(raw: str, add_api_prefix: bool = False) -> str:
    """把前端/Python/Go 的路径收敛成可比较的形状。

    - 模板串 `${encodeURIComponent(x)}` -> `{param}`
    - aiohttp 的 `{id:\\d+}` / `:id` -> `{param}`
    - 去掉 query / fragment（前端 skills 调用里就有拼 query 的）
    - 前端路径补 `/api` 前缀（client.ts 统一拼 API_BASE）
    """
    path = raw.strip()
    path = re.sub(r"\$\{[^}]*\}", PARAM, path)
    path = re.sub(r"\{[^}]*\}", PARAM, path)
    path = re.sub(r":[A-Za-z_][A-Za-z0-9_]*", PARAM, path)
    path = path.split("?", 1)[0].split("#", 1)[0]
    if not path.startswith("/"):
        path = "/" + path
    if add_api_prefix and not (path == "/api" or path.startswith("/api/")):
        path = "/api" + path
    if len(path) > 1:
        path = path.rstrip("/")
    return path


def _read_template(text: str, start: int) -> tuple[str, int]:
    """读取从 text[start]（反引号）开始的模板串，处理嵌套 `${ ... }` 里的反引号。"""
    if text[start] != "`":
        raise ValueError("template must start with a backtick")
    i = start + 1
    depth = 0
    out: list[str] = []
    while i < len(text):
        ch = text[i]
        if ch == "\\":
            out.append(text[i : i + 2])
            i += 2
            continue
        if depth == 0 and ch == "`":
            return "".join(out), i + 1
        if ch == "$" and i + 1 < len(text) and text[i + 1] == "{":
            depth += 1
            out.append("${")
            i += 2
            continue
        if depth > 0 and ch == "}":
            depth -= 1
            out.append("}")
            i += 1
            continue
        if depth > 0 and ch == "`":
            _, end = _read_template(text, i)
            out.append(text[i:end])
            i = end
            continue
        out.append(ch)
        i += 1
    raise ValueError("unterminated template literal")


def _resolve_frontend_consts(text: str) -> dict[str, str]:
    """收集 `const queenPath = (id) => \"/queen/${id}/skills\"` 这类小助手。

    前端把 queen skills 的公共前缀抽成了 queenPath()，不解析它就会把
    `/queen/{id}/skills/{name}/body` 误判成 `/api/{param}/{param}/body`。
    """
    consts: dict[str, str] = {}
    for match in FRONTEND_CONST_RE.finditer(text):
        quote = match.group("lit")
        start = match.end() - 1
        try:
            if quote == "`":
                value, _ = _read_template(text, start)
            else:
                end = text.find(quote, start + 1)
                if end == -1:
                    continue
                value = text[start + 1 : end]
        except ValueError:
            continue
        consts[match.group("name")] = value
    return consts


def _render_frontend_template(template: str, consts: dict[str, str], depth: int = 0) -> str:
    """展开模板串：已知助手内联，路径参数归一化，尾部 query 片段丢弃。"""
    if depth > 4:
        return normalize_path(template)

    tokens: list[str | tuple[str, str]] = []
    i = 0
    while i < len(template):
        if template[i] == "$" and i + 1 < len(template) and template[i + 1] == "{":
            level = 1
            j = i + 2
            while j < len(template) and level:
                if template[j] == "{":
                    level += 1
                elif template[j] == "}":
                    level -= 1
                j += 1
            tokens.append(("expr", template[i + 2 : j - 1].strip()))
            i = j
            continue
        tokens.append(template[i])
        i += 1

    out: list[str] = []
    for index, token in enumerate(tokens):
        if isinstance(token, str):
            out.append(token)
            continue
        expr = token[1]
        helper = TS_TEMPLATE_EXPR_RE.match(expr)
        if helper:
            name = expr.split("(", 1)[0].strip()
            if name in consts:
                out.append(_render_frontend_template(consts[name], consts, depth + 1))
                continue
        preceding = "".join(out)[-1:] if out else ""
        is_tail = index == len(tokens) - 1
        bare_name = re.split(r"[^A-Za-z0-9_$]", expr, maxsplit=1)[0]
        if is_tail and preceding not in ("", "/", "?", "&"):
            # `${qs ? "?..." : ""}` / `${fresh ? "?fresh=1" : ""}`：query 尾巴。
            if "?" in expr or bare_name in QUERY_SUFFIX_VARS:
                continue
        out.append(PARAM)

    rendered = "".join(out)
    # 展开后的模板里可能还有内联进来的 `${...}`，统一压平。
    rendered = re.sub(r"\$\{[^}]*\}", PARAM, rendered)
    return rendered.split("?", 1)[0].split("#", 1)[0]


def _read_call_argument(text: str, start: int) -> tuple[str, bool] | None:
    """读取 `api.get(` 之后的第一个参数。

    返回 (内容, 是否表达式)。字面量/模板串返回内容本身；`queenPath(x)`
    这类助手调用返回完整表达式，交给模板展开器内联。
    """
    ch = text[start : start + 1]
    if ch == "`":
        try:
            template, _ = _read_template(text, start)
        except ValueError:
            return None
        return template, False
    if ch in ("'", '"'):
        end = text.find(ch, start + 1)
        if end == -1:
            return None
        return text[start + 1 : end], False
    helper = CALL_ARG_NAME_RE.match(text, start)
    if helper:
        depth = 1
        i = helper.end()
        while i < len(text) and depth:
            if text[i] == "(":
                depth += 1
            elif text[i] == ")":
                depth -= 1
            i += 1
        if depth == 0:
            return text[start:i], True
    return None


def extract_frontend_routes(api_dirs: list[Path]) -> tuple[list[Route], int]:
    """返回去重后的前端路由与无法静态解析的调用数。"""
    routes: dict[tuple[str, str], Route] = {}
    unresolved = 0
    for api_dir in api_dirs:
        if not api_dir.is_dir():
            continue
        for path in sorted(api_dir.glob("*.ts")):
            text = path.read_text(encoding="utf-8", errors="replace")
            consts = _resolve_frontend_consts(text)
            for match in FRONTEND_CALL_RE.finditer(text):
                argument = _read_call_argument(text, match.end())
                if argument is None:
                    unresolved += 1
                    continue
                raw, is_expression = argument
                template = f"${{{raw}}}" if is_expression else raw
                rendered = _render_frontend_template(template, consts)
                normalized = normalize_path(rendered, add_api_prefix=True)
                if normalized in ("", "/", "/api"):
                    unresolved += 1
                    continue
                method = FRONTEND_METHODS[match.group(1)]
                route = Route(method, normalized, path.name)
                routes[route.key()] = route
    return sorted(routes.values(), key=lambda r: (r.path, r.method)), unresolved


def extract_go_routes(server_dir: Path) -> list[Route]:
    routes: dict[tuple[str, str], Route] = {}
    if not server_dir.is_dir():
        return []
    for path in sorted(server_dir.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8", errors="replace")
        for match in GO_ROUTE_RE.finditer(text):
            route = Route(match.group("method"), normalize_path(match.group("path")), path.name)
            routes[route.key()] = route
    return sorted(routes.values(), key=lambda r: (r.path, r.method))


def extract_python_routes(server_dir: Path) -> tuple[list[Route], int]:
    routes: dict[tuple[str, str], Route] = {}
    unresolved = 0
    if not server_dir.is_dir():
        return [], 0
    for path in sorted(server_dir.glob("routes_*.py")):
        text = path.read_text(encoding="utf-8", errors="replace")
        consts = {name: value for name, value in PY_CONST_RE.findall(text)}
        for match in PY_ROUTE_RE.finditer(text):
            arg = match.group("arg")
            if arg[0] in ("'", '"'):
                raw = arg[1:-1]
            else:
                raw = consts.get(arg, "")
            if not raw:
                unresolved += 1
                continue
            route = Route(match.group("method").upper(), normalize_path(raw), path.name)
            routes[route.key()] = route
    return sorted(routes.values(), key=lambda r: (r.path, r.method)), unresolved


# ---------------------------------------------------------------------------
# 匹配与缺失
# ---------------------------------------------------------------------------


def route_matches(candidate: str, registered: str) -> bool:
    """逐段比较，任一侧的参数段视为通配；段数必须一致。"""
    left = candidate.split("/")
    right = registered.split("/")
    if len(left) != len(right):
        return False

    def is_param(segment: str) -> bool:
        return segment == PARAM or (segment.startswith("{") and segment.endswith("}"))

    return all(a == b or is_param(a) or is_param(b) for a, b in zip(left, right))


def _covered(route: Route, registry: list[Route]) -> bool:
    return any(
        other.method == route.method and route_matches(route.path, other.path)
        for other in registry
    )


def _allowlist_match(route: Route, entries: list[dict], require_reason: bool = False) -> dict | None:
    for entry in entries:
        if require_reason and not str(entry.get("reason", "")).strip():
            continue
        methods = entry.get("methods")
        if methods and route.method not in methods:
            continue
        if entry.get("path") == route.path:
            return entry
        prefix = entry.get("prefix")
        if prefix and route.path.startswith(prefix):
            return entry
    return None


def find_missing(
    source: list[Route],
    registry: list[Route],
    allowlist: list[dict],
    pending: list[dict],
) -> list[MissingRoute]:
    missing: list[MissingRoute] = []
    for route in source:
        if _covered(route, registry):
            continue
        allowed = _allowlist_match(route, allowlist, require_reason=True)
        if allowed:
            missing.append(MissingRoute(route, "allowlisted", allowed.get("owner", ""), allowed.get("reason", "")))
            continue
        pending_entry = _allowlist_match(route, pending)
        missing.append(
            MissingRoute(
                route,
                "pending",
                (pending_entry or {}).get("owner", ""),
                (pending_entry or {}).get("reason", ""),
            )
        )
    return missing


def find_extra(go_routes: list[Route], other: list[Route]) -> list[Route]:
    # `/` 是 SPA 兜底，不是 API 表面；只提示 /api 下的多余注册。
    return [
        route
        for route in go_routes
        if route.path.startswith("/api/") and not _covered(route, other)
    ]


def group_by_prefix(missing: list[MissingRoute]) -> list[dict]:
    groups: dict[str, list[MissingRoute]] = {}
    for item in missing:
        parts = item.route.path.split("/")
        take = 5 if len(parts) > 4 and parts[3] == PARAM else 3
        prefix = "/".join(parts[:take]) if len(parts) >= take else item.route.path
        groups.setdefault(prefix, []).append(item)
    return [
        {
            "prefix": prefix,
            "count": len(items),
            "routes": [item.to_dict() for item in sorted(items, key=lambda i: (i.route.path, i.route.method))],
        }
        for prefix, items in sorted(groups.items())
    ]


# ---------------------------------------------------------------------------
# 仓库卫生
# ---------------------------------------------------------------------------


def _prune(dirnames: list[str], current: str, root: Path) -> None:
    dirnames[:] = [d for d in dirnames if d not in SKIP_DIR_NAMES]
    if Path(current) == root:
        dirnames[:] = [d for d in dirnames if d not in SKIP_ROOT_DIRS]


def find_empty_dirs(root: Path) -> list[str]:
    empty: list[str] = []
    for current, dirnames, filenames in os.walk(root):
        _prune(dirnames, current, root)
        if not dirnames and not filenames and Path(current) != root:
            empty.append(Path(current).relative_to(root).as_posix())
    return sorted(empty)


def find_lane_named_files(root: Path) -> list[str]:
    hits: list[str] = []
    for current, dirnames, filenames in os.walk(root):
        _prune(dirnames, current, root)
        for name in filenames:
            if LANE_FILE_RE.search(name):
                hits.append((Path(current) / name).relative_to(root).as_posix())
    return sorted(hits)


def _go_block_keys(text: str, var_name: str) -> list[str]:
    """从 size_test.go 里取 `var <name> = map[string]sizeWaiver{ ... }` 的 key。"""
    match = re.search(rf"var\s+{re.escape(var_name)}\s*=\s*map\[string\]sizeWaiver\s*\{{", text)
    if not match:
        return []
    start = match.end()
    depth = 1
    i = start
    while i < len(text) and depth:
        if text[i] == "{":
            depth += 1
        elif text[i] == "}":
            depth -= 1
        i += 1
    block = text[start : i - 1]
    return re.findall(r'"([^"]+)"\s*:', block)


def check_size_waivers(root: Path) -> list[dict]:
    """waiver key 必须指向真实文件；否则冻结上限是在守一个幻影。"""
    source = root / WAIVER_SOURCE
    if not source.exists():
        return [{"key": WAIVER_SOURCE, "kind": "missing_source", "detail": "找不到 size_test.go"}]
    text = source.read_text(encoding="utf-8", errors="replace")
    module_root = root / "go_hive/go-hive"
    problems: list[dict] = []
    for key in _go_block_keys(text, "fileWaivers"):
        if not (module_root / key).exists():
            problems.append({"key": key, "kind": "file", "detail": "fileWaivers 条目找不到文件"})
    for key in _go_block_keys(text, "funcWaivers"):
        rel = key.split("::", 1)[0]
        if not (module_root / rel).exists():
            problems.append({"key": key, "kind": "func", "detail": "funcWaivers 条目找不到文件"})
    return problems


def hygiene_report(root: Path, allowlist: dict) -> dict:
    allowed_empty = {entry.get("path") for entry in allowlist.get("empty_dirs", [])}
    lane_allowed = {entry.get("path") for entry in allowlist.get("lane_named_files", [])}
    waiver_keys = set(_go_block_keys(
        (root / WAIVER_SOURCE).read_text(encoding="utf-8", errors="replace")
        if (root / WAIVER_SOURCE).exists() else "", "fileWaivers"
    ))

    empty_dirs = []
    for rel in find_empty_dirs(root):
        allowed = rel in allowed_empty
        if allowed:
            entry = next(e for e in allowlist.get("empty_dirs", []) if e.get("path") == rel)
            empty_dirs.append({"path": rel, "status": "allowlisted", "reason": entry.get("reason", "")})
        else:
            empty_dirs.append({"path": rel, "status": "pending", "reason": ""})

    lane_files = []
    for rel in find_lane_named_files(root):
        module_rel = rel[len("go_hive/go-hive/") :] if rel.startswith("go_hive/go-hive/") else ""
        waived = module_rel in waiver_keys
        allowed = rel in lane_allowed
        convention = any(rel.startswith(prefix + "/") for prefix in LANE_CONVENTION_DIRS)
        if allowed:
            entry = next(e for e in allowlist.get("lane_named_files", []) if e.get("path") == rel)
            lane_files.append({"path": rel, "status": "allowlisted", "reason": entry.get("reason", "")})
        elif waived:
            lane_files.append({"path": rel, "status": "waived", "reason": "已被 size_test.go fileWaivers 冻结并说明理由"})
        elif convention:
            lane_files.append({"path": rel, "status": "convention", "reason": "批量工具移植目录：文件名即批次，属既有约定"})
        else:
            lane_files.append({"path": rel, "status": "pending", "reason": ""})

    dangling = check_size_waivers(root)
    return {"empty_dirs": empty_dirs, "lane_named_files": lane_files, "dangling_waivers": dangling}


# ---------------------------------------------------------------------------
# 汇总
# ---------------------------------------------------------------------------


def load_allowlist(path: Path) -> dict:
    data: dict = {}
    if path.exists():
        data = json.loads(path.read_text(encoding="utf-8"))
    return {
        "frontend_to_go": data.get("frontend_to_go", []),
        "python_to_go": data.get("python_to_go", []),
        "pending": data.get("pending", []),
        "empty_dirs": data.get("empty_dirs", []),
        "lane_named_files": data.get("lane_named_files", []),
    }


def audit(root: Path, allowlist_path: Path | None = None) -> dict:
    root = root.resolve()
    module = root / "go_hive/go-hive"
    allowlist_file = allowlist_path or (root / DEFAULT_ALLOWLIST)
    allowlist = load_allowlist(allowlist_file)

    frontend_dirs = []
    for candidate in (module / "frontend/src/api", root / "core/frontend/src/api"):
        if candidate.is_dir() and candidate.resolve() not in [d.resolve() for d in frontend_dirs]:
            frontend_dirs.append(candidate)

    frontend, unresolved_frontend = extract_frontend_routes(frontend_dirs)
    go_routes = extract_go_routes(module / "internal/server")
    python, unresolved_python = extract_python_routes(root / "core/framework/server")

    frontend_missing = find_missing(
        frontend, go_routes, allowlist["frontend_to_go"], allowlist["pending"]
    )
    python_missing = find_missing(
        python, go_routes, allowlist["python_to_go"], allowlist["pending"]
    )
    go_extra = find_extra(go_routes, frontend + python)
    hygiene = hygiene_report(root, allowlist)

    blocking_frontend = [m for m in frontend_missing if m.status == "pending"]
    blocking_python = [m for m in python_missing if m.status == "pending"]
    blocking_hygiene = (
        [d for d in hygiene["empty_dirs"] if d["status"] == "pending"]
        + [f for f in hygiene["lane_named_files"] if f["status"] == "pending"]
        + hygiene["dangling_waivers"]
    )

    result = {
        "generated_at": datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds"),
        "root": str(root),
        "allowlist": str(allowlist_file),
        "counts": {
            "frontend_routes": len(frontend),
            "go_routes": len(go_routes),
            "python_routes": len(python),
            "frontend_missing": len(frontend_missing),
            "frontend_pending": len(blocking_frontend),
            "python_missing": len(python_missing),
            "python_pending": len(blocking_python),
            "go_extra": len(go_extra),
            "empty_dirs": sum(1 for d in hygiene["empty_dirs"] if d["status"] == "pending"),
            "lane_named_files": sum(1 for f in hygiene["lane_named_files"] if f["status"] == "pending"),
            "lane_named_convention": sum(1 for f in hygiene["lane_named_files"] if f["status"] == "convention"),
            "dangling_waivers": len(hygiene["dangling_waivers"]),
        },
        "unresolved": {"frontend": unresolved_frontend, "python": unresolved_python},
        "frontend_missing": [m.to_dict() for m in frontend_missing],
        "python_missing": [m.to_dict() for m in python_missing],
        "python_missing_groups": group_by_prefix(python_missing),
        "go_extra": [r.to_dict() for r in go_extra],
        "hygiene": hygiene,
    }
    result["ok"] = not (blocking_frontend or blocking_python or blocking_hygiene)
    return result


# ---------------------------------------------------------------------------
# 报告
# ---------------------------------------------------------------------------


def _note(item: dict) -> str:
    parts = [item.get("owner") or "", item.get("reason") or ""]
    parts = [part for part in parts if part]
    return "：".join(parts) if parts else "-"


def _status_label(status: str) -> str:
    return {
        "pending": "待修",
        "allowlisted": "已豁免",
        "waived": "已冻结",
        "convention": "惯例允许",
    }.get(status, status)


def render_markdown(result: dict) -> str:
    counts = result["counts"]
    lines = [
        "# API / 仓库表面审计",
        "",
        f"- 生成时间：{result['generated_at']}",
        f"- 根目录：`{result['root']}`",
        f"- 豁免清单：`{result['allowlist']}`",
        "- 重新生成：`uv run python go_hive/scripts/surface_audit.py --write-report`",
        "",
        "本报告只回答一个问题：**文档/前端/上游声明的入口，在 Go 运行时里是否真的可达。**",
        "空目录、lane 命名的落地文件、以及指向幻影文件的规模豁免同样计入。",
        "归档是否携带 `hive`/`hive-cred`/`hive-browser`/`browserbridge` 由 `qc_check.py` 的 C4c 守护，本审计不重复。",
        "",
        "## 摘要",
        "",
        "| 项 | 数量 |",
        "| --- | ---: |",
        f"| 前端路由（去重） | {counts['frontend_routes']} |",
        f"| Go 已注册路由 | {counts['go_routes']} |",
        f"| Python 上游路由 | {counts['python_routes']} |",
        f"| 前端 → Go 缺失 | {counts['frontend_missing']}（待修 {counts['frontend_pending']}） |",
        f"| Python → Go 缺失 | {counts['python_missing']}（待修 {counts['python_pending']}） |",
        f"| Go 多余（仅提示） | {counts['go_extra']} |",
        f"| 空目录 | {counts['empty_dirs']} |",
        f"| batch/lane 命名文件（未豁免） | {counts['lane_named_files']} |",
        f"| 悬空 size waiver | {counts['dangling_waivers']} |",
        "",
        f"**结论：{'通过' if result['ok'] else '存在未豁免缺口'}**",
        "",
    ]
    if result["unresolved"]["frontend"] or result["unresolved"]["python"]:
        lines += [
            f"> 静态不可解析调用：前端 {result['unresolved']['frontend']} 处，"
            f"Python {result['unresolved']['python']} 处（首个参数不是字面量/常量）。",
            "",
        ]

    lines += ["## 前端 → Go 缺失", ""]
    if not result["frontend_missing"]:
        lines += ["无。", ""]
    else:
        lines += ["| 方法 | 路径 | 来源 | 状态 | 归属 / 理由 |", "| --- | --- | --- | --- | --- |"]
        for item in result["frontend_missing"]:
            note = _note(item)
            lines.append(
                f"| {item['method']} | `{item['path']}` | {item['source']} | "
                f"{_status_label(item['status'])} | {note} |"
            )
        lines.append("")

    lines += ["## Python → Go 缺失（按前缀分组）", ""]
    skills_python = [item for item in result["python_missing"] if item.get("owner") == "skills-api lane"]
    skills_frontend = [item for item in result["frontend_missing"] if item.get("owner") == "skills-api lane"]
    if skills_python or skills_frontend:
        lines += [
            f"当前 skills 相关缺口：Python 上游 {len(skills_python)} 条、前端 {len(skills_frontend)} 条，"
            "整组归 **skills-api lane** 待修。",
            "",
        ]
    if not result["python_missing"]:
        lines += ["无。", ""]
    for group in result["python_missing_groups"]:
        pending = sum(1 for item in group["routes"] if item["status"] == "pending")
        lines += [f"### `{group['prefix']}` — {group['count']} 条（待修 {pending}）", ""]
        lines += ["| 方法 | 路径 | 状态 | 归属 / 理由 |", "| --- | --- | --- | --- |"]
        for item in group["routes"]:
            note = _note(item)
            lines.append(
                f"| {item['method']} | `{item['path']}` | {_status_label(item['status'])} | {note} |"
            )
        lines.append("")

    lines += ["## Go 多余（仅提示，不阻断）", ""]
    if not result["go_extra"]:
        lines += ["无。", ""]
    else:
        lines += ["| 方法 | 路径 | 来源 |", "| --- | --- | --- |"]
        for item in result["go_extra"]:
            lines.append(f"| {item['method']} | `{item['path']}` | {item['source']} |")
        lines.append("")

    lines += ["## 仓库卫生", "", "### 空目录", ""]
    if not result["hygiene"]["empty_dirs"]:
        lines += ["无。", ""]
    else:
        lines += ["| 路径 | 状态 | 理由 |", "| --- | --- | --- |"]
        for item in result["hygiene"]["empty_dirs"]:
            lines.append(f"| `{item['path']}` | {_status_label(item['status'])} | {item['reason'] or '-'} |")
        lines.append("")

    lines += ["### batch/lane 命名文件", ""]
    lane_rows = [f for f in result["hygiene"]["lane_named_files"] if f["status"] != "convention"]
    convention_count = counts.get("lane_named_convention", 0)
    if convention_count:
        lines += [
            f"`internal/tools/integrations/` 下按批次命名的文件 {convention_count} 个，"
            "属既有目录约定，不逐条列出。",
            "",
        ]
    if not lane_rows:
        lines += ["无待修 / 无豁免项。", ""]
    else:
        lines += ["| 路径 | 状态 | 理由 |", "| --- | --- | --- |"]
        for item in lane_rows:
            lines.append(f"| `{item['path']}` | {_status_label(item['status'])} | {item['reason'] or '-'} |")
        lines.append("")

    lines += ["### 悬空 size waiver", ""]
    if not result["hygiene"]["dangling_waivers"]:
        lines += ["无。", ""]
    else:
        lines += ["| key | 类型 | 问题 |", "| --- | --- | --- |"]
        for item in result["hygiene"]["dangling_waivers"]:
            lines.append(f"| `{item['key']}` | {item['kind']} | {item['detail']} |")
        lines.append("")

    lines += [
        "## 已知待修与豁免的口径",
        "",
        "- **待修**：缺失属实，`--strict` 会阻断；目前 skills 路由整组归 `skills-api` lane。",
        "- **已豁免**：确认不属于 Go 主服务表面（独立进程/上游 Python 专有），理由写入",
        "  `go_hive/scripts/surface_audit_allowlist.json`，并在上表逐条可见。",
        "- 新增豁免必须带 `reason`；空理由视为静默豁免，评审应拒绝。",
        "",
    ]
    return "\n".join(lines)


def print_human(result: dict) -> None:
    counts = result["counts"]
    print("API / 仓库表面审计")
    print(f"  根目录: {result['root']}")
    print(
        f"  前端 {counts['frontend_routes']} 条 | Go {counts['go_routes']} 条 | "
        f"Python {counts['python_routes']} 条"
    )
    print(
        f"  前端→Go 缺失 {counts['frontend_missing']}（待修 {counts['frontend_pending']}）| "
        f"Python→Go 缺失 {counts['python_missing']}（待修 {counts['python_pending']}）| "
        f"Go 多余 {counts['go_extra']}"
    )
    print(
        f"  卫生: 空目录 {counts['empty_dirs']} | lane 文件 {counts['lane_named_files']} | "
        f"悬空 waiver {counts['dangling_waivers']}"
    )
    if result["frontend_missing"]:
        print("\n前端 → Go 缺失:")
        for item in result["frontend_missing"]:
            print(f"  {item['status']:11} {item['method']:6} {item['path']}  ({item['source']})")
    if result["python_missing"]:
        print("\nPython → Go 缺失:")
        for item in result["python_missing"]:
            print(f"  {item['status']:11} {item['method']:6} {item['path']}  ({item['source']})")
    if result["hygiene"]["empty_dirs"]:
        print("\n空目录:")
        for item in result["hygiene"]["empty_dirs"]:
            print(f"  {item['status']:11} {item['path']}")
    print(f"\n结论: {'PASS' if result['ok'] else 'FAIL（存在未豁免缺口）'}")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="API / 仓库表面审计")
    parser.add_argument("--json", action="store_true", help="输出机器可读 JSON")
    parser.add_argument("--strict", action="store_true", help="有未豁免缺口时 exit 1")
    parser.add_argument("--allowlist", type=Path, help=f"豁免清单（默认 {DEFAULT_ALLOWLIST}）")
    parser.add_argument("--root", type=Path, default=DEFAULT_ROOT, help="仓库根目录")
    parser.add_argument("--write-report", action="store_true", help="生成 Markdown 报告")
    parser.add_argument("--report", type=Path, help=f"报告路径（默认 {DEFAULT_REPORT}）")
    args = parser.parse_args(argv)

    result = audit(args.root, args.allowlist)

    if args.write_report:
        report_path = args.report or (args.root.resolve() / DEFAULT_REPORT)
        report_path.parent.mkdir(parents=True, exist_ok=True)
        report_path.write_text(render_markdown(result) + "\n", encoding="utf-8")
        result["report"] = str(report_path)

    if args.json:
        print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=False))
    else:
        print_human(result)
        if args.write_report:
            print(f"\n报告: {result['report']}")

    if args.strict and not result["ok"]:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
