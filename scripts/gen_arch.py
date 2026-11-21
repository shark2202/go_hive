#!/usr/bin/env python3
"""go_hive 架构可视化生成器。

输入：
  architecture/model.yaml        人工维护的层、包、业务能力模型
  go list -json ./...            真实包与 import 边（模块 go-hive/）

输出（相对 go_hive/）：
  architecture/graph.json                    机器可读图（业务 + 技术）
  docs/architecture/dependency-map.mmd       Mermaid 分层依赖图
  docs/architecture/dependency-map.dot       Graphviz 分层依赖图
  docs/architecture/MODULES.md               模块表
  docs/architecture/capability-map.mmd       业务能力关系图
  docs/architecture/capability-tech-crosswalk.md  能力 ↔ 技术对照
  docs/architecture/contracts.md             接口契约 + HTTP 路由表 + 事件类型表 + 磁盘格式表

用法：
  python3 scripts/gen_arch.py            生成/覆盖产物
  python3 scripts/gen_arch.py --check    重新生成并逐字节比较，有差异 exit 1

只用标准库；输出确定性：所有列表排序，唯一的时间性字段是 generated_at_commit
（来自 git HEAD）。--check 会沿用仓库内 graph.json 已记录的 commit 再比较，
因此提交生成物本身不会造成假漂移。
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
MODEL_REL = "architecture/model.yaml"
GRAPH_REL = "architecture/graph.json"

CONTRACT_KINDS = (
    "go-interface",
    "http",
    "sse",
    "event",
    "disk",
    "tool-schema",
    "cli",
)
CONTRACT_KIND_LABELS = {
    "go-interface": "Go 接口 / 导出 API",
    "http": "HTTP 路由族群",
    "sse": "SSE 帧",
    "event": "事件总线",
    "disk": "磁盘格式",
    "tool-schema": "工具 schema",
    "cli": "CLI 命令",
}
COMPATIBILITY_VALUES = {"additive", "breaking", "versioned"}
COMPATIBILITY_LABELS = {
    "additive": "加法兼容",
    "breaking": "破坏性变更",
    "versioned": "版本化",
}
ENFORCEMENT_VALUES = {"compile", "test", "gate", "checker"}
ENFORCEMENT_LABELS = {
    "compile": "编译器 / 接口实现",
    "test": "单元、集成或 e2e 测试",
    "gate": "九门禁 / wiring-check / parity（含 compat-check 与 arch-check）",
    "checker": "gen_arch --check 防漂移",
}

LAYER_STYLES = {
    "entry": ("#eef2ff", "#4338ca"),
    "assembly": ("#e0f2fe", "#0369a1"),
    "transport": ("#dcfce7", "#15803d"),
    "orchestration": ("#fef3c7", "#b45309"),
    "runtime": ("#fee2e2", "#b91c1c"),
    "capability": ("#f3e8ff", "#7e22ce"),
    "foundation": ("#f1f5f9", "#475569"),
    "unmapped": ("#ffe4e6", "#be123c"),
}

STABILITY_VALUES = {"stable", "evolving"}
APPROVAL_VALUES = {"yes", "no", "partial"}
MATURITY_VALUES = {"live", "tested", "unverified"}

EDGE_ORDER = {"depends": 0, "realizes": 1, "operates": 2}


class ModelError(Exception):
    """model.yaml 不符合本生成器支持的子集。"""


# ---------------------------------------------------------------------------
# 极简 YAML 读取器（只覆盖 model.yaml 用到的子集）
# ---------------------------------------------------------------------------

def _tokenize(text: str, path: Path) -> list[list]:
    rows = []
    for lineno, raw in enumerate(text.splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        leading = raw[: len(raw) - len(raw.lstrip())]
        if "\t" in leading:
            raise ModelError(f"{path}:{lineno}: 缩进里不允许制表符")
        rows.append([lineno, len(raw) - len(raw.lstrip(" ")), raw.strip()])
    return rows


def _split_flow(text: str) -> list[str]:
    parts, buf, depth, quote = [], [], 0, ""
    for ch in text:
        if quote:
            buf.append(ch)
            if ch == quote:
                quote = ""
            continue
        if ch in "\"'":
            quote = ch
            buf.append(ch)
            continue
        if ch == "[":
            depth += 1
        elif ch == "]":
            depth -= 1
        if ch == "," and depth == 0:
            parts.append("".join(buf))
            buf = []
            continue
        buf.append(ch)
    parts.append("".join(buf))
    return [p.strip() for p in parts if p.strip()]


class _YamlSubset:
    def __init__(self, rows: list[list], path: Path):
        self.rows = rows
        self.path = path
        self.pos = 0

    def error(self, lineno: int, message: str) -> ModelError:
        return ModelError(f"{self.path}:{lineno}: {message}")

    def parse(self):
        if not self.rows:
            return {}
        if self.rows[0][2].startswith("- "):
            value, pos = self._sequence(0, self.rows[0][1])
        else:
            value, pos = self._mapping(0, self.rows[0][1])
        if pos != len(self.rows):
            raise self.error(self.rows[pos][0], "多余的内容（缩进不一致？）")
        return value

    def _mapping(self, pos: int, indent: int, initial: dict | None = None):
        result = dict(initial or {})
        while pos < len(self.rows):
            lineno, row_indent, content = self.rows[pos]
            if row_indent != indent or content.startswith("- "):
                break
            key, rest = self._split_key(content, lineno)
            if key in result:
                raise self.error(lineno, f"重复的键 {key!r}")
            pos += 1
            rest = rest.strip()
            if rest:
                if pos < len(self.rows) and self.rows[pos][1] > indent:
                    raise self.error(self.rows[pos][0], f"键 {key!r} 的标量值后面不应再有缩进块")
                result[key] = self._scalar(rest, lineno)
                continue
            if pos < len(self.rows) and self.rows[pos][1] > indent:
                value, pos = self._block(pos, self.rows[pos][1])
            else:
                value = None
            result[key] = value
        return result, pos

    def _sequence(self, pos: int, indent: int):
        items = []
        while pos < len(self.rows):
            lineno, row_indent, content = self.rows[pos]
            if row_indent != indent or not content.startswith("- "):
                break
            item_text = content[2:].strip()
            pos += 1
            if not item_text:
                if pos < len(self.rows) and self.rows[pos][1] > indent:
                    value, pos = self._block(pos, self.rows[pos][1])
                else:
                    value = None
                items.append(value)
                continue
            if self._looks_like_mapping(item_text):
                key, rest = self._split_key(item_text, lineno)
                if rest.strip():
                    initial = {key: self._scalar(rest.strip(), lineno)}
                elif pos < len(self.rows) and self.rows[pos][1] > indent:
                    value, pos = self._block(pos, self.rows[pos][1])
                    initial = {key: value}
                else:
                    initial = {key: None}
                if pos < len(self.rows) and self.rows[pos][1] > indent:
                    item, pos = self._mapping(pos, self.rows[pos][1], initial)
                else:
                    item = initial
                items.append(item)
                continue
            items.append(self._scalar(item_text, lineno))
            if pos < len(self.rows) and self.rows[pos][1] > indent:
                raise self.error(self.rows[pos][0], "列表标量项后面不应再有缩进内容")
        return items, pos

    def _block(self, pos: int, indent: int):
        if self.rows[pos][2].startswith("- "):
            return self._sequence(pos, indent)
        return self._mapping(pos, indent)

    @staticmethod
    def _looks_like_mapping(text: str) -> bool:
        if text[:1] in "\"'[":
            return False
        return re.match(r"^[A-Za-z_][A-Za-z0-9_-]*\s*:(\s|$)", text) is not None

    def _split_key(self, text: str, lineno: int):
        match = re.match(r"^([A-Za-z_][A-Za-z0-9_-]*)\s*:(.*)$", text, re.S)
        if not match:
            raise self.error(lineno, f"应为 key: value，得到 {text!r}")
        return match.group(1), match.group(2)

    def _scalar(self, text: str, lineno: int):
        if text.startswith("|") or text.startswith(">"):
            raise self.error(lineno, "不支持多行块标量（| 或 >），请用双引号单行字符串")
        if text.startswith('"'):
            if not text.endswith('"') or len(text) < 2:
                raise self.error(lineno, "双引号字符串未闭合")
            try:
                return json.loads(text)
            except json.JSONDecodeError as exc:
                raise self.error(lineno, f"双引号字符串无法解析: {exc}") from exc
        if text.startswith("'"):
            if not text.endswith("'") or len(text) < 2:
                raise self.error(lineno, "单引号字符串未闭合")
            return text[1:-1].replace("''", "'")
        if text.startswith("["):
            if not text.endswith("]"):
                raise self.error(lineno, "流式列表未闭合")
            inner = text[1:-1].strip()
            if not inner:
                return []
            return [self._scalar(part, lineno) for part in _split_flow(inner)]
        if text in ("true", "True"):
            return True
        if text in ("false", "False"):
            return False
        if text in ("null", "Null", "~"):
            return None
        if re.fullmatch(r"-?\d+", text):
            return int(text)
        if re.fullmatch(r"-?\d+\.\d+", text):
            return float(text)
        if ": " in text:
            raise self.error(lineno, "未加引号的标量里出现 ': '，请用双引号包住整段文本")
        return text


def load_model(path: Path) -> dict:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ModelError(f"读取 {path} 失败: {exc}") from exc
    model = _YamlSubset(_tokenize(text, path), path).parse()
    if not isinstance(model, dict):
        raise ModelError(f"{path}: 顶层必须是映射")
    return model


# ---------------------------------------------------------------------------
# 模型校验
# ---------------------------------------------------------------------------

def _require(condition: bool, message: str, errors: list[str]) -> None:
    if not condition:
        errors.append(message)


def validate_model(model: dict) -> None:
    errors: list[str] = []
    _require(isinstance(model.get("meta"), dict), "缺少 meta 段", errors)
    _require(isinstance(model.get("layers"), list) and model["layers"], "缺少 layers 列表", errors)
    _require(isinstance(model.get("packages"), list) and model["packages"], "缺少 packages 列表", errors)
    _require(isinstance(model.get("capabilities"), list) and model["capabilities"], "缺少 capabilities 列表", errors)
    if errors:
        raise ModelError("\n".join(errors))

    layers = {}
    for index, layer in enumerate(model["layers"]):
        if not isinstance(layer, dict):
            raise ModelError(f"layers[{index}] 应为映射")
        for key in ("id", "name", "depth", "description"):
            if key not in layer:
                raise ModelError(f"layers[{index}] 缺少 {key}")
        if layer["id"] in layers:
            raise ModelError(f"层 id 重复: {layer['id']}")
        layers[layer["id"]] = layer

    seen = set()
    for index, pkg in enumerate(model["packages"]):
        if not isinstance(pkg, dict):
            raise ModelError(f"packages[{index}] 应为映射")
        for key in ("package", "layer", "responsibility", "stability"):
            if key not in pkg or not str(pkg[key]).strip():
                raise ModelError(f"packages[{index}] 缺少 {key}")
        if pkg["package"] in seen:
            raise ModelError(f"包重复映射: {pkg['package']}")
        seen.add(pkg["package"])
        if pkg["layer"] not in layers:
            raise ModelError(f"{pkg['package']}: 未知层 {pkg['layer']}")
        if pkg["stability"] not in STABILITY_VALUES:
            raise ModelError(f"{pkg['package']}: stability 必须是 {sorted(STABILITY_VALUES)}")

    caps = set()
    for index, cap in enumerate(model["capabilities"]):
        if not isinstance(cap, dict):
            raise ModelError(f"capabilities[{index}] 应为映射")
        for key in (
            "id", "name", "business_outcome", "actors", "modules", "operates",
            "entrypoints", "state_artifacts", "human_approval", "maturity", "limits",
        ):
            if key not in cap:
                raise ModelError(f"capabilities[{index}] 缺少 {key}")
        if cap["id"] in caps:
            raise ModelError(f"能力 id 重复: {cap['id']}")
        caps.add(cap["id"])
        if cap["human_approval"] not in APPROVAL_VALUES:
            raise ModelError(f"{cap['id']}: human_approval 必须是 {sorted(APPROVAL_VALUES)}")
        if cap["maturity"] not in MATURITY_VALUES:
            raise ModelError(f"{cap['id']}: maturity 必须是 {sorted(MATURITY_VALUES)}")
        for key in ("actors", "modules", "operates", "entrypoints", "state_artifacts"):
            value = cap[key]
            if not isinstance(value, list) or not all(isinstance(item, str) and item.strip() for item in value):
                raise ModelError(f"{cap['id']}: {key} 必须是非空字符串列表")
        if not cap["modules"]:
            raise ModelError(f"{cap['id']}: modules 不能为空")

    contract_ids = set()
    contracts = model.get("contracts")
    if not isinstance(contracts, list) or not contracts:
        raise ModelError("缺少 contracts 列表")
    if len(contracts) < 15:
        raise ModelError(f"contracts 至少需要 15 条，当前 {len(contracts)} 条")
    covered_kinds = set()
    for index, contract in enumerate(contracts):
        if not isinstance(contract, dict):
            raise ModelError(f"contracts[{index}] 应为映射")
        required = (
            "id", "kind", "providers", "consumers", "definition", "guarantee",
            "failure_behavior", "compatibility", "enforcement", "owner_layer",
        )
        for key in required:
            if key not in contract or contract[key] in (None, "", []):
                raise ModelError(f"contracts[{index}] 缺少 {key}")
        contract_id = str(contract["id"])
        if contract_id in contract_ids:
            raise ModelError(f"契约 id 重复: {contract_id}")
        contract_ids.add(contract_id)
        if contract["kind"] not in CONTRACT_KINDS:
            raise ModelError(f"{contract_id}: kind 必须是 {list(CONTRACT_KINDS)}")
        covered_kinds.add(contract["kind"])
        for key in ("providers", "consumers"):
            value = contract[key]
            if not isinstance(value, list) or not value:
                raise ModelError(f"{contract_id}: {key} 必须是非空列表")
            for module in value:
                if not isinstance(module, str) or not module.strip():
                    raise ModelError(f"{contract_id}: {key} 含空值")
                if not module.startswith("ext:") and module not in seen:
                    raise ModelError(f"{contract_id}: {key} 引用了未登记的包 {module}")
        if contract["compatibility"] not in COMPATIBILITY_VALUES:
            raise ModelError(
                f"{contract_id}: compatibility 必须是 {sorted(COMPATIBILITY_VALUES)}"
            )
        enforcement = contract["enforcement"]
        if not isinstance(enforcement, list) or not enforcement:
            raise ModelError(f"{contract_id}: enforcement 必须是非空列表")
        for item in enforcement:
            if item not in ENFORCEMENT_VALUES:
                raise ModelError(
                    f"{contract_id}: enforcement 含未知值 {item!r}，"
                    f"允许 {sorted(ENFORCEMENT_VALUES)}"
                )
        if contract["owner_layer"] not in layers:
            raise ModelError(f"{contract_id}: owner_layer 未知 {contract['owner_layer']}")
    missing = [kind for kind in CONTRACT_KINDS if kind not in covered_kinds]
    if missing:
        raise ModelError(f"contracts 未覆盖契约类别: {missing}")

    disk_formats = model.get("disk_formats")
    if not isinstance(disk_formats, list) or not disk_formats:
        raise ModelError("缺少 disk_formats 列表")
    for index, row in enumerate(disk_formats):
        if not isinstance(row, dict):
            raise ModelError(f"disk_formats[{index}] 应为映射")
        for key in ("path", "producer", "consumer", "key_fields", "versioning"):
            if not row.get(key):
                raise ModelError(f"disk_formats[{index}] 缺少 {key}")

    flow = model.get("business_flow")
    if not isinstance(flow, dict):
        raise ModelError("缺少 business_flow 段")
    staged = []
    for stage in flow.get("stages") or []:
        for cap_id in stage.get("capabilities") or []:
            if cap_id not in caps:
                raise ModelError(f"business_flow 引用了不存在的 capacity: {cap_id}")
            staged.append(cap_id)
    if len(set(staged)) != len(caps):
        missing = sorted(caps - set(staged))
        raise ModelError(f"business_flow 未覆盖能力: {missing}")
    for edge in flow.get("edges") or []:
        for side in ("from", "to"):
            if edge.get(side) not in caps:
                raise ModelError(f"business_flow 边引用了不存在的 capacity: {edge.get(side)}")


# ---------------------------------------------------------------------------
# Go 包图
# ---------------------------------------------------------------------------

def module_path(module_dir: Path) -> str:
    gomod = module_dir / "go.mod"
    if not gomod.exists():
        raise SystemExit(f"找不到 {gomod}")
    for line in gomod.read_text(encoding="utf-8").splitlines():
        if line.startswith("module "):
            return line.split(None, 1)[1].strip()
    raise SystemExit(f"{gomod} 没有 module 行")


def load_go_packages(module_dir: Path) -> dict[str, dict]:
    proc = subprocess.run(
        ["go", "list", "-json", "./..."],
        cwd=str(module_dir),
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise SystemExit(f"go list 失败:\n{proc.stderr.strip()}")
    decoder = json.JSONDecoder()
    raw, index, packages = proc.stdout, 0, {}
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index >= len(raw):
            break
        record, index = decoder.raw_decode(raw, index)
        packages[record["ImportPath"]] = {
            "dir": record.get("Dir", ""),
            "imports": sorted(record.get("Imports") or []),
        }
    return packages


def head_commit(root: Path) -> str:
    try:
        out = subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--short=12", "HEAD"],
            capture_output=True, text=True, check=True,
        )
        return out.stdout.strip() or "unknown"
    except (subprocess.CalledProcessError, FileNotFoundError):
        return "unknown"


# ---------------------------------------------------------------------------
# 代码提取：HTTP 路由与 EventType
# ---------------------------------------------------------------------------

HTTP_ROUTE_RE = re.compile(r'mux\.HandleFunc\(\s*"([A-Z]+) ([^"]+)"\s*,\s*')
EVENT_TYPE_RE = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)\s+EventType\s*=\s*"([^"]+)"')
HANDLER_CALL_RE = re.compile(r'([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?)\s*\(')
HANDLER_SKIP = {
    "if", "for", "switch", "return", "func", "go", "defer", "range",
    "writeJSON", "writeNamedSSEEvent", "http.Error", "fmt.Fprint", "fmt.Fprintf",
}


def _line_of(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def _route_handler_name(text: str, start: int) -> str:
    """best-effort：取注册闭包里第一个具名调用，找不到就给 (inline)。

    只用于生成可读的路由表，不参与门禁判断；因此比完整解析 Go AST 更宽松，
    遇到纯内联逻辑就诚实写 (inline)。
    """
    head = text[start:start + 2000]
    stripped = head.lstrip()
    if stripped.startswith("func"):
        brace = stripped.find("{")
        body = stripped[brace + 1:brace + 1500] if brace >= 0 else stripped
        calls = [match.group(1) for match in HANDLER_CALL_RE.finditer(body)]
        for name in calls:
            base = name.rsplit(".", 1)[-1]
            if base.startswith("handle") and name not in HANDLER_SKIP:
                return name
        for name in calls:
            if name not in HANDLER_SKIP:
                return name
        return "(inline)"
    match = re.match(r'([A-Za-z_][A-Za-z0-9_.]*)', stripped)
    return match.group(1) if match else "(inline)"


def scan_http_routes(module_dir: Path) -> tuple:
    """从 internal/server 的 mux.HandleFunc("METHOD /path", ...) 提取路由。

    只覆盖标准库 ServeMux 的「方法 + 路径」式注册；webhook_server.go 里
    独立的 mux.HandleFunc("/", ...)（无方法）不在统计内，notes 会说明。
    """
    server_dir = module_dir / "internal" / "server"
    rows = []
    notes = []
    if not server_dir.is_dir():
        return rows, [f"未找到 {server_dir}，HTTP 路由表未覆盖"]
    for path in sorted(server_dir.rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        for match in HTTP_ROUTE_RE.finditer(text):
            rows.append({
                "method": match.group(1),
                "path": match.group(2),
                "handler": _route_handler_name(text, match.end()),
                "source": f"{path.relative_to(module_dir).as_posix()}:{_line_of(text, match.start())}",
            })
    rows.sort(key=lambda row: (row["path"], row["method"], row["source"]))
    unmatched = [
        line.strip()
        for path in sorted(server_dir.rglob("*.go"))
        if not path.name.endswith("_test.go")
        for line in path.read_text(encoding="utf-8").splitlines()
        if ".HandleFunc(" in line and not HTTP_ROUTE_RE.search(line)
    ]
    if unmatched:
        notes.append(
            "未被路由表覆盖的 HandleFunc 注册（无方法前缀或跨行写法）："
            + "；".join(sorted(set(unmatched)))
        )
    return rows, notes


def scan_event_types(module_dir: Path) -> tuple:
    """从 internal/eventbus/event_type.go 提取 EventType 常量。"""
    path = module_dir / "internal" / "eventbus" / "event_type.go"
    if not path.is_file():
        return [], [f"未找到 {path}，EventType 表未覆盖"]
    rows = []
    for lineno, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        code = raw.split("//", 1)[0]
        match = EVENT_TYPE_RE.search(code)
        if not match:
            continue
        rows.append({
            "name": match.group(1),
            "value": match.group(2),
            "source": f"internal/eventbus/event_type.go:{lineno}",
        })
    return rows, []


# ---------------------------------------------------------------------------
# 构图
# ---------------------------------------------------------------------------

def build_graph(
    model: dict,
    go_packages: dict,
    mod: str,
    commit: str,
    http_routes: list,
    event_types: list,
) -> dict:
    layers = {layer["id"]: dict(layer) for layer in model["layers"]}
    warnings: list[dict] = []

    mapped = {pkg["package"]: dict(pkg) for pkg in model["packages"]}
    module_prefix = mod + "/"

    def rel(import_path: str) -> str:
        return import_path[len(module_prefix):]

    known = {rel(path) for path in go_packages}
    for name in sorted(set(mapped) - known):
        warnings.append({
            "code": "stale_mapping",
            "severity": "warning",
            "message": f"model.yaml 映射了不存在的包 {name}（go list 未找到）",
        })
        mapped.pop(name, None)

    for name in sorted(known - set(mapped)):
        mapped[name] = {
            "package": name,
            "layer": "unmapped",
            "responsibility": "未在 architecture/model.yaml 中登记（生成器占位）。",
            "stability": "evolving",
            "entrypoint": False,
            "note": "",
        }
        warnings.append({
            "code": "unmapped_package",
            "severity": "warning",
            "message": f"包 {name} 未在 model.yaml 中映射；已用占位层 unmapped 画出，请补充分层与职责",
        })

    if "unmapped" in {pkg["layer"] for pkg in mapped.values()}:
        layers["unmapped"] = {
            "id": "unmapped",
            "name": "未映射层",
            "depth": 99,
            "description": "生成器为 model.yaml 尚未登记的包临时创建的层；补齐后应消失。",
        }

    edges: list[dict] = []
    for name in sorted(mapped):
        record = go_packages.get(module_prefix + name)
        if not record:
            continue
        for imp in record["imports"]:
            if not imp.startswith(module_prefix):
                continue
            target = rel(imp)
            if target in mapped and target != name:
                edges.append({"from": f"pkg:{name}", "to": f"pkg:{target}", "kind": "depends"})

    edges.sort(key=lambda e: (EDGE_ORDER[e["kind"]], e["from"], e["to"]))

    depth = {layer_id: layer["depth"] for layer_id, layer in layers.items()}
    for edge in edges:
        src = edge["from"][len("pkg:"):]
        dst = edge["to"][len("pkg:"):]
        if depth[mapped[dst]["layer"]] < depth[mapped[src]["layer"]]:
            warnings.append({
                "code": "reverse_dependency",
                "severity": "warning",
                "message": (
                    f"反向依赖：{src}（{mapped[src]['layer']}）导入了更高层 "
                    f"{dst}（{mapped[dst]['layer']}）"
                ),
            })

    out_degree = {name: 0 for name in mapped}
    in_degree = {name: 0 for name in mapped}
    for edge in edges:
        src = edge["from"][len("pkg:"):]
        dst = edge["to"][len("pkg:"):]
        out_degree[src] += 1
        in_degree[dst] += 1

    for name in sorted(mapped):
        pkg = mapped[name]
        if pkg.get("entrypoint") or pkg.get("expected_orphan"):
            continue
        if in_degree[name] == 0:
            warnings.append({
                "code": "no_importer",
                "severity": "info",
                "message": f"包 {name} 没有任何内部导入方（可能是未接线代码或纯入口约定）",
            })

    nodes = []
    ordered = sorted(mapped.values(), key=lambda p: (depth[p["layer"]], p["package"]))
    for pkg in ordered:
        name = pkg["package"]
        nodes.append({
            "id": f"pkg:{name}",
            "kind": "technical",
            "package": name,
            "layer": pkg["layer"],
            "responsibility": pkg["responsibility"],
            "stability": pkg["stability"],
            "entrypoint": bool(pkg.get("entrypoint")),
            "note": pkg.get("note", ""),
            "direct_dependencies": out_degree[name],
            "dependents": in_degree[name],
        })

    package_names = set(mapped)
    stage_order = [
        cap_id
        for stage in model["business_flow"]["stages"]
        for cap_id in stage["capabilities"]
    ]
    order_index = {cap_id: index for index, cap_id in enumerate(stage_order)}
    capabilities = []
    for cap in sorted(model["capabilities"], key=lambda c: (order_index.get(c["id"], 10_000), c["id"])):
        for module in cap["modules"]:
            if module not in package_names:
                warnings.append({
                    "code": "unknown_capability_module",
                    "severity": "warning",
                    "message": f"能力 {cap['id']} 的 modules 引用了不存在的包 {module}",
                })
        for module in cap["operates"]:
            if module not in package_names:
                warnings.append({
                    "code": "unknown_capability_module",
                    "severity": "warning",
                    "message": f"能力 {cap['id']} 的 operates 引用了不存在的包 {module}",
                })
        capabilities.append({
            "id": f"cap:{cap['id']}",
            "capability": cap["id"],
            "kind": "business",
            "name": cap["name"],
            "business_outcome": cap["business_outcome"],
            "actors": list(cap["actors"]),
            "modules": list(cap["modules"]),
            "operates": list(cap["operates"]),
            "entrypoints": list(cap["entrypoints"]),
            "state_artifacts": list(cap["state_artifacts"]),
            "human_approval": cap["human_approval"],
            "maturity": cap["maturity"],
            "limits": cap["limits"],
        })

    staged = {cap_id for stage in model["business_flow"]["stages"] for cap_id in stage["capabilities"]}
    for cap_id in sorted({c["capability"] for c in capabilities} - staged):
        warnings.append({
            "code": "unmapped_capability",
            "severity": "info",
            "message": f"能力 {cap_id} 未出现在 business_flow.stages 中",
        })

    business_edges = []
    for cap in capabilities:
        for module in cap["modules"]:
            if module in package_names:
                business_edges.append({
                    "from": f"pkg:{module}", "to": cap["id"], "kind": "realizes",
                })
        for module in cap["operates"]:
            if module in package_names:
                business_edges.append({
                    "from": cap["id"], "to": f"pkg:{module}", "kind": "operates",
                })
    all_edges = edges + sorted(business_edges, key=lambda e: (e["from"], e["to"]))
    all_edges.sort(key=lambda e: (EDGE_ORDER[e["kind"]], e["from"], e["to"]))

    severity_order = {"warning": 0, "info": 1}
    warnings.sort(key=lambda w: (severity_order.get(w["severity"], 9), w["code"], w["message"]))

    layer_payload = []
    for layer in sorted(layers.values(), key=lambda item: (item["depth"], item["id"])):
        members = sorted(name for name, pkg in mapped.items() if pkg["layer"] == layer["id"])
        layer_payload.append({
            "id": layer["id"],
            "name": layer["name"],
            "depth": layer["depth"],
            "description": layer["description"],
            "packages": members,
            "package_count": len(members),
        })

    contracts = []
    for contract in model["contracts"]:
        contracts.append({
            "id": contract["id"],
            "kind": contract["kind"],
            "providers": list(contract["providers"]),
            "consumers": list(contract["consumers"]),
            "definition": contract["definition"],
            "guarantee": contract["guarantee"],
            "failure_behavior": contract["failure_behavior"],
            "compatibility": contract["compatibility"],
            "enforcement": list(contract["enforcement"]),
            "owner_layer": contract["owner_layer"],
        })
    contracts.sort(key=lambda item: (CONTRACT_KINDS.index(item["kind"]), item["id"]))

    return {
        "schema_version": model["meta"].get("graph_schema_version", 1),
        "module": mod,
        "generated_at_commit": commit,
        "generated_from": (
            "architecture/model.yaml + go list -json ./... + "
            "internal/server mux.HandleFunc + internal/eventbus event_type.go"
        ),
        "stats": {
            "packages": len(nodes),
            "package_edges": sum(1 for edge in all_edges if edge["kind"] == "depends"),
            "capabilities": len(capabilities),
            "capability_edges": sum(1 for edge in all_edges if edge["kind"] in ("realizes", "operates")),
            "layers": len(layer_payload),
            "contracts": len(contracts),
            "http_routes": len(http_routes),
            "event_types": len(event_types),
            "warnings": len(warnings),
        },
        "layers": layer_payload,
        "nodes": nodes + capabilities,
        "edges": all_edges,
        "capabilities": capabilities,
        "contracts": contracts,
        "business_flow": model["business_flow"],
        "warnings": warnings,
    }


# ---------------------------------------------------------------------------
# 渲染：Mermaid 依赖图
# ---------------------------------------------------------------------------

def _mermaid_id(node_id: str) -> str:
    return "n_" + re.sub(r"[^A-Za-z0-9]+", "_", node_id.split(":", 1)[1]).strip("_")


def _cap_id(capability_id: str) -> str:
    return "cap_" + re.sub(r"[^A-Za-z0-9]+", "_", capability_id)


def render_dependency_mmd(graph: dict) -> str:
    lines = [
        "%% go_hive 分层依赖图（生成物，勿手改）",
        f"%% 源：architecture/model.yaml + go list -json ./... · commit {graph['generated_at_commit']}",
        "%% 只画包级 import 边；箭头 A --> B 读作 A imports B（A 依赖 B）。",
        "flowchart TB",
    ]
    node_by_id = {node["id"]: node for node in graph["nodes"] if node["kind"] == "technical"}
    for layer in graph["layers"]:
        if not layer["packages"]:
            continue
        lines.append(f'  subgraph layer_{layer["id"]}["{layer["name"]}（{layer["id"]}）"]')
        lines.append("    direction TB")
        for package in layer["packages"]:
            node_id = _mermaid_id("pkg:" + package)
            lines.append(f'    {node_id}["{package}"]')
        lines.append("  end")
    edges = [edge for edge in graph["edges"] if edge["kind"] == "depends"]
    for edge in edges:
        lines.append(f"  {_mermaid_id(edge['from'])} --> {_mermaid_id(edge['to'])}")
    lines.append("")
    for layer in graph["layers"]:
        fill, stroke = LAYER_STYLES.get(layer["id"], LAYER_STYLES["unmapped"])
        lines.append(f"  classDef style_{layer['id']} fill:{fill},stroke:{stroke},color:#0f172a;")
        members = [
            _mermaid_id(node["id"])
            for node in node_by_id.values()
            if node["layer"] == layer["id"]
        ]
        if members:
            lines.append("  class " + ",".join(sorted(members)) + f" style_{layer['id']};")
    return "\n".join(lines) + "\n"


def render_dependency_dot(graph: dict) -> str:
    lines = [
        "// go_hive 分层依赖图（生成物，勿手改）",
        f"// 源：architecture/model.yaml + go list -json ./... · commit {graph['generated_at_commit']}",
        "// 箭头 A -> B 读作 A imports B。",
        "digraph go_hive {",
        "  graph [rankdir=LR, fontname=\"Helvetica\", nodesep=0.28, ranksep=0.9, splines=true];",
        "  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\", fontsize=10, margin=\"0.10,0.05\"];",
        "  edge [color=\"#94a3b8\", arrowsize=0.6];",
    ]
    node_by_id = {node["id"]: node for node in graph["nodes"] if node["kind"] == "technical"}
    for layer in graph["layers"]:
        if not layer["packages"]:
            continue
        fill, stroke = LAYER_STYLES.get(layer["id"], LAYER_STYLES["unmapped"])
        lines.append(f"  subgraph cluster_{layer['id']} {{")
        lines.append(f"    label=\"{layer['name']}（{layer['id']}）\";")
        lines.append(f"    style=\"rounded,filled\"; fillcolor=\"{fill}\"; color=\"{stroke}\"; fontsize=11;")
        for package in layer["packages"]:
            lines.append(f"    \"{package}\" [fillcolor=\"white\", color=\"{stroke}\"];")
        lines.append("  }")
    for edge in graph["edges"]:
        if edge["kind"] != "depends":
            continue
        src = node_by_id[edge["from"]]["package"]
        dst = node_by_id[edge["to"]]["package"]
        lines.append(f"  \"{src}\" -> \"{dst}\";")
    lines.append("}")
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# 渲染：MODULES.md
# ---------------------------------------------------------------------------

def _md_cell(text: str) -> str:
    return str(text).replace("|", "\\|").replace("\n", " ").strip()


def render_modules_md(graph: dict) -> str:
    nodes = [node for node in graph["nodes"] if node["kind"] == "technical"]
    by_package = {node["package"]: node for node in nodes}
    lines = [
        "# 模块清单（生成物，勿手改）",
        "",
        f"> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` + `go list` 生成。commit: `{graph['generated_at_commit']}`",
        "> 修改分层或职责请改 `architecture/model.yaml`，不要编辑本文件。",
        "",
        "## 分层统计",
        "",
        "| 层 | 包数 | 说明 |",
        "| --- | ---: | --- |",
    ]
    for layer in graph["layers"]:
        lines.append(f"| {_md_cell(layer['name'])}（`{layer['id']}`） | {layer['package_count']} | {_md_cell(layer['description'])} |")
    lines += [
        "",
        f"共 {len(nodes)} 个包、{graph['stats']['package_edges']} 条包间 import 边。",
        "",
        "## 模块表",
        "",
        "| 包 | 层 | 职责 | 直接依赖 | 被依赖 | 入口 |",
        "| --- | --- | --- | ---: | ---: | :---: |",
    ]
    for node in nodes:
        entry = "是" if node["entrypoint"] else ""
        lines.append(
            f"| `{node['package']}` | {node['layer']} | {_md_cell(node['responsibility'])} | "
            f"{node['direct_dependencies']} | {node['dependents']} | {entry} |"
        )

    reverse = [w for w in graph["warnings"] if w["code"] == "reverse_dependency"]
    lines += ["", "## 分层检查", ""]
    if reverse:
        lines.append("发现反向依赖（低层 import 高层）：")
        lines.append("")
        for warning in reverse:
            lines.append(f"- {warning['message']}")
    else:
        lines.append("未发现反向依赖：所有 import 边都从浅层指向深层。")

    orphans = [w for w in graph["warnings"] if w["code"] == "no_importer"]
    lines += ["", "## 无导入方的包", ""]
    if orphans:
        lines.append("以下包没有任何内部导入方。属于未接线代码或纯门禁/入口约定，列在这里供人工判断：")
        lines.append("")
        for warning in orphans:
            lines.append(f"- {warning['message']}")
    else:
        lines.append("无。")

    notes = [node for node in nodes if node.get("note")]
    if notes:
        lines += ["", "## 备注", ""]
        for node in notes:
            lines.append(f"- `{node['package']}`：{node['note']}")

    lines += [
        "",
        "---",
        "",
        "共 %d 个包；层的定义与业务能力对照见 `docs/architecture/README.md` 与 "
        "`docs/architecture/capability-tech-crosswalk.md`。" % len(nodes),
        "",
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# 渲染：业务能力图
# ---------------------------------------------------------------------------

def render_capability_mmd(graph: dict) -> str:
    flow = graph["business_flow"]
    caps = {cap["capability"]: cap for cap in graph["capabilities"]}
    lines = [
        "%% go_hive 业务能力地图（生成物，勿手改）",
        f"%% 源：architecture/model.yaml · commit {graph['generated_at_commit']}",
        "%% 实线 = 业务主链路；虚线 = 反馈/审批关系；六边形 = 人工审批点。",
        "flowchart LR",
    ]
    for stage in flow["stages"]:
        lines.append(f'  subgraph stage_{stage["id"]}["{stage["name"]}"]')
        lines.append("    direction TB")
        for cap_id in stage["capabilities"]:
            cap = caps[cap_id]
            label = f'{cap["name"]}<br/><small>{cap["maturity"]}</small>'
            lines.append(f'    {_cap_id(cap_id)}["{label}"]')
        lines.append("  end")

    approval_caps = [cap for cap in graph["capabilities"] if cap["human_approval"] != "no"]
    if approval_caps:
        lines.append('  subgraph stage_approval["人工审批点"]')
        lines.append("    direction TB")
        for cap in approval_caps:
            node = (
                "appr_" + _cap_id(cap["capability"]) + '{{"人工审批<br/>'
                + cap["name"] + "<br/><small>" + cap["human_approval"] + "</small>" + '"}}'
            )
            lines.append("    " + node)
        lines.append("  end")

    for edge in flow["edges"]:
        arrow = "-.->" if edge.get("dashed") else "-->"
        lines.append(f'  {_cap_id(edge["from"])} {arrow} {_cap_id(edge["to"])}')

    for cap in approval_caps:
        lines.append(f'  {_cap_id(cap["capability"])} -.-> appr_{_cap_id(cap["capability"])}')

    lines.append("")
    lines.append("  classDef cap fill:#eef2ff,stroke:#4338ca,color:#1e1b4b;")
    lines.append("  classDef appr fill:#fef3c7,stroke:#b45309,color:#7c2d12;")
    if caps:
        lines.append("  class " + ",".join(_cap_id(cap_id) for cap_id in sorted(caps)) + " cap;")
    if approval_caps:
        lines.append(
            "  class "
            + ",".join(f"appr_{_cap_id(cap['capability'])}" for cap in approval_caps)
            + " appr;"
        )
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# 渲染：能力 ↔ 技术对照
# ---------------------------------------------------------------------------

MATURITY_LEGEND = """\
- **live**：主路径有端到端测试（`go-hive/e2e/`）或真实二进制冒烟覆盖，能对着代码指出来回。
- **tested**：有单元/集成测试与真实入口，但没有端到端证据；接线正确性靠测试与代码阅读，不靠跑通。
- **unverified**：代码存在但未接线、无入口，或本次审计无法确认其可用性。

人工审批列：**yes** 表示该能力的关键动作必须由人完成；**partial** 表示默认路径可自动，
但缺凭据、ask_user、soul 提案 review、Sentinel escalate 等分支会停下来等人；**no** 表示全自动。
"""


def render_crosswalk_md(graph: dict) -> str:
    caps = graph["capabilities"]
    nodes = {node["package"]: node for node in graph["nodes"] if node["kind"] == "technical"}
    lines = [
        "# 业务能力 ↔ 技术模块对照（生成物，勿手改）",
        "",
        f"> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` 生成。commit: `{graph['generated_at_commit']}`",
        "> 这是业务视图与技术视图的接缝：左边是人关心的结果，右边是代码里真实存在的包、入口与落盘文件。",
        "",
        "## 成熟度与审批口径",
        "",
        MATURITY_LEGEND,
        "## 能力对照表",
        "",
        "| 能力 | 业务结果 | 技术模块 | 入口（HTTP / CLI / 工具） | 状态与证据（落盘） | 人工审批 | 成熟度 | 已知限制 |",
        "| --- | --- | --- | --- | --- | :---: | :---: | --- |",
    ]
    for cap in caps:
        modules = "<br>".join(f"`{m}`" for m in cap["modules"])
        entries = "<br>".join(_md_cell(item) for item in cap["entrypoints"])
        artifacts = "<br>".join(_md_cell(item) for item in cap["state_artifacts"])
        lines.append(
            f"| **{_md_cell(cap['name'])}**<br/>`{cap['capability']}` | {_md_cell(cap['business_outcome'])} | "
            f"{modules} | {entries} | {artifacts} | {cap['human_approval']} | {cap['maturity']} | "
            f"{_md_cell(cap['limits'])} |"
        )

    lines += ["", "## 模块 → 能力 反向索引", "", "| 模块 | 层 | 承载能力 |", "| --- | --- | --- |"]
    reverse: dict[str, list[str]] = {}
    for cap in caps:
        for module in cap["modules"]:
            reverse.setdefault(module, []).append(cap["name"])
    for module in sorted(reverse):
        layer = nodes[module]["layer"] if module in nodes else "?"
        lines.append(f"| `{module}` | {layer} | {'、'.join(sorted(reverse[module]))} |")

    covered = set(reverse)
    uncovered = [
        node for node in graph["nodes"]
        if node["kind"] == "technical" and node["package"] not in covered
    ]
    lines += [
        "",
        "## 未被业务能力直接引用的模块",
        "",
        f"共 {len(uncovered)} 个。基础层与入口层通常不需要挂到业务能力上；如果能力层出现这里的包，"
        "说明画图时漏了映射，应该补 `model.yaml` 的 `capabilities[].modules`。",
        "",
        "| 模块 | 层 | 说明 |",
        "| --- | --- | --- |",
    ]
    for node in sorted(uncovered, key=lambda n: (n["layer"], n["package"])):
        if node["layer"] in ("foundation", "entry", "harness"):
            reason = "基础/入口/测试装置层，预期不挂业务能力"
        elif node["layer"] in ("assembly", "transport"):
            reason = "装配/传输面，通常只体现在能力的入口列"
        elif node["layer"] == "capability":
            reason = "能力层未映射，建议补充 modules"
        else:
            reason = "编排/运行时组件，可按需挂到能力"
        lines.append(f"| `{node['package']}` | {node['layer']} | {reason} |")

    lines += [
        "",
        "## 业务链路的读法",
        "",
        "1. 从 `goal-outcome` 开始：目标与加权成功标准写进 colony 的 `worker.json`（`goal`）。",
        "2. `queen-triage` 决定是直接回答还是派工；派工经 `worker-dispatch` 进入 colony。",
        "3. worker 的证据落 `tracker/tracker.db`，计划落 `tasks.json`，报告以 `[WORKER_REPORT]` 回合回到 Queen。",
        "4. 交付经 `channels-dashboard` 暴露；失败经 `improvement/signals.jsonl` 进入改进；",
        "   下一轮由 `triggers-sentinel` 唤醒，人工审批点见上图六边形。",
        "",
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# 渲染：接口与契约
# ---------------------------------------------------------------------------

def render_contracts_md(
    graph: dict,
    routes: list,
    route_notes: list,
    event_types: list,
    event_notes: list,
    disk_formats: list,
) -> str:
    contracts = graph["contracts"]
    by_kind = {}
    for contract in contracts:
        by_kind.setdefault(contract["kind"], []).append(contract)

    method_counts = {}
    for route in routes:
        method_counts[route["method"]] = method_counts.get(route["method"], 0) + 1

    lines = [
        "# 接口与契约（生成物，勿手改）",
        "",
        f"> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` 的 `contracts:` / `disk_formats:`，"
        f"加上 `internal/server` 路由注册与 `internal/eventbus` 常量生成。commit: `{graph['generated_at_commit']}`",
        "> 契约是模块之间「靠什么约束交互」的清单：谁提供、谁消费、定义在哪、保证什么、失败怎么表现、"
        "变更属于加法还是破坏、由什么机制强制。",
        "> `providers` / `consumers` 里的 `ext:` 前缀表示外部消费面（前端、运维、MCP server、外部调用方），"
        "不是仓库内的包。",
        "",
        "## 统计",
        "",
        "| 维度 | 数量 |",
        "| --- | ---: |",
        f"| 契约 | {len(contracts)} |",
    ]
    for kind in CONTRACT_KINDS:
        lines.append(f"| 其中 {CONTRACT_KIND_LABELS[kind]}（`{kind}`） | {len(by_kind.get(kind, []))} |")
    lines += [
        f"| HTTP 路由（`internal/server`，method+path） | {len(routes)} |",
        f"| EventType 常量 | {len(event_types)} |",
        f"| 磁盘格式（人工维护） | {len(disk_formats)} |",
        "",
        "## 兼容规则口径",
        "",
        "| compatibility | 含义 | 允许的变更 |",
        "| --- | --- | --- |",
        "| `additive` | 加法兼容 | 新增端点、字段、事件类型、工具或可选 JSON key；消费方必须容忍未知字段。 |",
        "| `breaking` | 破坏性 | 任何签名/路径/字段/字符串值的删除或改名都要同步改所有消费方；需要新接口或新版本，不能原地改。 |",
        "| `versioned` | 版本化 | 文件或库自带版本号（soul.json `version`、tracker.db `PRAGMA user_version`），按版本迁移并保留回滚路径。 |",
        "",
        "## 按 kind 汇总",
        "",
        "| kind | 条数 | 提供方模块 | 消费方模块 | 强制手段 |",
        "| --- | ---: | --- | --- | --- |",
    ]
    for kind in CONTRACT_KINDS:
        members = by_kind.get(kind, [])
        if not members:
            continue
        providers = sorted({module for item in members for module in item["providers"]})
        consumers = sorted({module for item in members for module in item["consumers"]})
        enforcement = sorted({token for item in members for token in item["enforcement"]})
        lines.append(
            f"| {CONTRACT_KIND_LABELS[kind]}（`{kind}`） | {len(members)} | "
            f"{'、'.join(f'`{name}`' for name in providers)} | "
            f"{'、'.join(f'`{name}`' for name in consumers)} | "
            f"{'、'.join(ENFORCEMENT_LABELS[token] for token in enforcement if token in ENFORCEMENT_LABELS)} |"
        )

    lines += ["", "## 契约明细", ""]
    for kind in CONTRACT_KINDS:
        members = by_kind.get(kind, [])
        if not members:
            continue
        lines += [f"### {CONTRACT_KIND_LABELS[kind]}（`{kind}`）", ""]
        for contract in members:
            lines += [
                f"#### `{contract['id']}`",
                "",
                f"- **定义**：`{contract['definition']}`",
                f"- **提供方**：{'、'.join(f'`{name}`' for name in contract['providers'])}",
                f"- **消费方**：{'、'.join(f'`{name}`' for name in contract['consumers'])}",
                f"- **保证**：{contract['guarantee']}",
                f"- **失败行为**：{contract['failure_behavior']}",
                f"- **兼容规则**：`{contract['compatibility']}`",
                f"- **强制手段**：{'、'.join(ENFORCEMENT_LABELS[t] for t in contract['enforcement'] if t in ENFORCEMENT_LABELS)}",
                f"- **所属层**：`{contract['owner_layer']}`",
                "",
            ]

    lines += [
        "## HTTP 路由表",
        "",
        "从 `internal/server` 的非测试文件里正则提取 "
        "`mux.HandleFunc(\"METHOD /path\", handler)`：**只覆盖标准库 ServeMux 的「方法 + 路径」式注册**。"
        "handler 列取注册闭包中第一个具名调用（优先 `handle*`），纯内联逻辑写 `(inline)`；"
        "它只用于阅读，不作为门禁判断。`http.*` 契约是按治理职责划分的族群，不是互斥路由集合；"
        "同一条路由可以同时属于一个族群与另一族群的语义边界，全量以本表为准。",
        "",
        f"共 **{len(routes)}** 条："
        + "、".join(f"{method} {count}" for method, count in sorted(method_counts.items())),
        "",
        "| 方法 | 路径 | handler | 来源 |",
        "| --- | --- | --- | --- |",
    ]
    for route in routes:
        lines.append(
            f"| {route['method']} | `{_md_cell(route['path'])}` | "
            f"`{_md_cell(route['handler'])}` | `{route['source']}` |"
        )
    for note in route_notes:
        lines += ["", f"> 覆盖说明：{note}"]

    lines += [
        "",
        "## 事件类型表",
        "",
        "从 `internal/eventbus/event_type.go` 提取 `EventType = \"...\"` 常量。"
        "事件是字符串类型，新增类型必须同时补常量与 `AllEventTypes`；改已有字符串值等于破坏订阅方。",
        "",
        f"共 **{len(event_types)}** 个 EventType。",
        "",
        "| 常量 | 值 | 来源 |",
        "| --- | --- | --- |",
    ]
    for event in event_types:
        lines.append(f"| `{event['name']}` | `{event['value']}` | `{event['source']}` |")
    for note in event_notes:
        lines += ["", f"> 覆盖说明：{note}"]

    lines += [
        "",
        "## 磁盘格式表",
        "",
        "人工维护在 `architecture/model.yaml` 的 `disk_formats:`；契约层 `disk.*` 是它的逐条展开。"
        "JSON 文件没有本地 schema 校验，读取方普遍把「缺失/坏 JSON」降级为空对象或空数组，"
        "因此这里的版本/迁移策略是读代码得出的实际行为，不是生成器验证过的保证。",
        "",
        "| 路径 | 生产者 | 消费者 | 关键字段 | 版本 / 迁移策略 |",
        "| --- | --- | --- | --- | --- |",
    ]
    for row in disk_formats:
        lines.append(
            f"| `{_md_cell(row['path'])}` | {_md_cell(row['producer'])} | {_md_cell(row['consumer'])} | "
            f"{_md_cell(row['key_fields'])} | {_md_cell(row['versioning'])} |"
        )

    lines += [
        "",
        "## 覆盖边界",
        "",
        f"- HTTP 路由表只统计带方法前缀的 `mux.HandleFunc`，共 {len(routes)} 条（含 `GET /` SPA 兜底）；"
        "`internal/server/webhook_server.go` 里独立的 `mux.HandleFunc(\"/\", ...)` webhook 入口不在表内。",
        "- SSE 端点没有单独一张表；它们的帧形状写在 `sse.*` 契约明细里，路由本身仍在 HTTP 路由表中。",
        "- `contracts:` 是人工维护的治理视图，不是从代码自动推导的依赖图；"
        "生成器只校验字段完整性、包名与类别，不能证明每条保证在运行期成立。",
        "",
    ]
    return "\n".join(lines)



# ---------------------------------------------------------------------------
# 产物清单与主流程
# ---------------------------------------------------------------------------

def render_all(root: Path, commit: str) -> dict:
    model = load_model(root / MODEL_REL)
    validate_model(model)
    module_dir = root / model["meta"].get("module_dir", "go-hive")
    go_packages = load_go_packages(module_dir)
    mod = module_path(module_dir)
    routes, route_notes = scan_http_routes(module_dir)
    event_types, event_notes = scan_event_types(module_dir)
    graph = build_graph(model, go_packages, mod, commit, routes, event_types)
    return {
        GRAPH_REL: json.dumps(graph, ensure_ascii=False, indent=2) + "\n",
        "docs/architecture/dependency-map.mmd": render_dependency_mmd(graph),
        "docs/architecture/dependency-map.dot": render_dependency_dot(graph),
        "docs/architecture/MODULES.md": render_modules_md(graph),
        "docs/architecture/capability-map.mmd": render_capability_mmd(graph),
        "docs/architecture/capability-tech-crosswalk.md": render_crosswalk_md(graph),
        "docs/architecture/contracts.md": render_contracts_md(
            graph, routes, route_notes, event_types, event_notes, model["disk_formats"]
        ),
    }


def first_difference(current: str, expected: str) -> str:
    current_lines, expected_lines = current.splitlines(), expected.splitlines()
    for index in range(max(len(current_lines), len(expected_lines))):
        left = current_lines[index] if index < len(current_lines) else "<文件结束>"
        right = expected_lines[index] if index < len(expected_lines) else "<文件结束>"
        if left != right:
            return f"第 {index + 1} 行\n  现存: {left}\n  应为: {right}"
    return "内容相同但换行/结尾不同"


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="生成 go_hive 架构图与模块表")
    parser.add_argument("--check", action="store_true", help="只比较，不写文件；有差异 exit 1")
    parser.add_argument("--commit", default="", help="覆盖 commit 字段（调试用；--check 默认沿用现存 graph.json）")
    args = parser.parse_args(argv)

    root = REPO_ROOT
    graph_path = root / GRAPH_REL
    pinned = args.commit
    if not pinned and args.check and graph_path.exists():
        try:
            pinned = json.loads(graph_path.read_text(encoding="utf-8")).get("generated_at_commit", "")
        except (json.JSONDecodeError, OSError):
            pinned = ""
    commit = pinned or head_commit(root)

    try:
        outputs = render_all(root, commit)
    except ModelError as exc:
        print(f"model.yaml 错误：\n{exc}", file=sys.stderr)
        return 2

    if not args.check:
        for rel, text in outputs.items():
            target = root / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text, encoding="utf-8")
            print(f"写入 {rel}")
        stats = json.loads(outputs[GRAPH_REL])["stats"]
        print(
            f"完成：{stats['packages']} 包 / {stats['package_edges']} 边 / "
            f"{stats['capabilities']} 能力 / {stats['layers']} 层 / {stats['warnings']} 条 warnings"
        )
        return 0

    differences = []
    for rel, expected in outputs.items():
        target = root / rel
        if not target.exists():
            differences.append((rel, "文件不存在"))
            continue
        current = target.read_text(encoding="utf-8")
        if current != expected:
            differences.append((rel, first_difference(current, expected)))

    if differences:
        print("架构图与代码/模型不一致：", file=sys.stderr)
        for rel, detail in differences:
            print(f"  - {rel}: {detail}", file=sys.stderr)
        print("请运行 `python3 scripts/gen_arch.py` 重新生成。", file=sys.stderr)
        return 1

    print(f"一致：{len(outputs)} 个生成物与 model.yaml + go list 完全一致（commit {commit}）")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
