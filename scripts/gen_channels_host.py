#!/usr/bin/env python3
"""Generate the channels host dossier (zh-CN HTML + JSON) and check protocol anchors.

Source of truth, recomputed on every run:
  - upstream in-process channel host   core/framework/server/channels.py
  - the Go stdio JSON-line contract     go_hive/contracts/channel-plugin-protocol.json
  - current Go channel wiring           go_hive/go-hive/internal/{server,cli}
  - planned host paths                  marked planned; missing files are reported,
                                        never counted as verified

Usage:
  python3 go_hive/scripts/gen_channels_host.py            # write HTML + JSON
  python3 go_hive/scripts/gen_channels_host.py --check    # byte drift + anchors
  python3 go_hive/scripts/gen_channels_host.py --check --require-planned
                                                          # also fail on absent planned anchors
"""
from __future__ import annotations

import argparse
import ast
import html
import json
import re
import subprocess
from datetime import datetime
from pathlib import Path
from typing import Any

REPO = Path(__file__).resolve().parents[2]
DOCS = REPO / "go_hive" / "docs"
OUT_HTML = DOCS / "channels-host.html"
OUT_JSON = DOCS / "channels-host.json"
CONTRACT_PATH = REPO / "go_hive" / "contracts" / "channel-plugin-protocol.json"
PY_CHANNELS = REPO / "core" / "framework" / "server" / "channels.py"
GO_CONFIG = REPO / "go_hive" / "go-hive" / "internal" / "server" / "channels_config.go"
GO_ROUTES = REPO / "go_hive" / "go-hive" / "internal" / "server" / "routes_credentials_channels.go"
GO_LOADER = REPO / "go_hive" / "go-hive" / "internal" / "cli" / "loader.go"
GO_APP = REPO / "go_hive" / "go-hive" / "internal" / "server" / "app.go"

PROTOCOL = "hive-channel/1"
UPSTREAM_BASELINE = "1.0.15 · 6703b434"

ESC = lambda value: html.escape(str(value), quote=True)


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def num(node: ast.AST) -> Any:
    """Evaluate a constant numeric expression (2 * 1024 * 1024 is common upstream)."""
    if isinstance(node, ast.Constant) and isinstance(node.value, (int, float)):
        return node.value
    if isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.USub, ast.UAdd)):
        value = num(node.operand)
        return -value if isinstance(node.op, ast.USub) else value
    if isinstance(node, ast.BinOp):
        left, right = num(node.left), num(node.right)
        ops = {
            ast.Add: lambda a, b: a + b,
            ast.Sub: lambda a, b: a - b,
            ast.Mult: lambda a, b: a * b,
            ast.Div: lambda a, b: a / b,
            ast.FloorDiv: lambda a, b: a // b,
        }
        for op_type, fn in ops.items():
            if isinstance(node.op, op_type):
                return fn(left, right)
    raise ValueError(f"not a numeric constant: {ast.dump(node)[:80]}")


def assign_literal(tree: ast.Module, name: str) -> Any:
    for node in tree.body:
        if isinstance(node, ast.Assign) and len(node.targets) == 1 and isinstance(node.targets[0], ast.Name):
            if node.targets[0].id == name:
                try:
                    return ast.literal_eval(node.value)
                except (ValueError, SyntaxError):
                    return num(node.value)
        if isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name) and node.target.id == name:
            try:
                return ast.literal_eval(node.value)
            except (ValueError, SyntaxError):
                return num(node.value)
    raise SystemExit(f"{PY_CHANNELS}: constant {name} not found")


def class_node(tree: ast.Module, name: str) -> ast.ClassDef:
    for node in tree.body:
        if isinstance(node, ast.ClassDef) and node.name == name:
            return node
    raise SystemExit(f"{PY_CHANNELS}: class {name} not found")


def public_methods(node: ast.ClassDef) -> list[dict[str, Any]]:
    out = []
    for item in node.body:
        if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)) and not item.name.startswith("_"):
            if any(isinstance(dec, ast.Name) and dec.id == "property" for dec in item.decorator_list):
                continue
            out.append(
                {
                    "name": item.name,
                    "async": isinstance(item, ast.AsyncFunctionDef),
                    "args": [a.arg for a in item.args.args if a.arg != "self"],
                }
            )
    return out


def literal_values(node: ast.AST) -> list[Any]:
    if isinstance(node, ast.Subscript) and getattr(node.value, "id", None) == "Literal":
        inner = node.slice
        nodes = inner.elts if isinstance(inner, ast.Tuple) else [inner]
        return [ast.literal_eval(item) for item in nodes]
    return []


def py_facts() -> dict[str, Any]:
    tree = ast.parse(read(PY_CHANNELS), filename=str(PY_CHANNELS))
    channel = class_node(tree, "Channel")
    queen_output = class_node(tree, "QueenOutput")
    host = class_node(tree, "ChannelHost")

    fields: dict[str, Any] = {}
    output_kinds: list[str] = []
    for item in queen_output.body:
        if isinstance(item, ast.AnnAssign) and isinstance(item.target, ast.Name):
            fields[item.target.id] = {
                "annotation": ast.unparse(item.annotation),
                "default": ast.unparse(item.value) if item.value is not None else None,
            }
            if item.target.id == "kind":
                output_kinds = literal_values(item.annotation)

    host_methods = public_methods(host)
    wire_methods = [m["name"] for m in host_methods if m["name"] != "close"]
    return {
        "source": str(PY_CHANNELS.relative_to(REPO)),
        "source_lines": len(read(PY_CHANNELS).splitlines()),
        "entry_point_group": assign_literal(tree, "ENTRY_POINT_GROUP"),
        "start_timeout_s": assign_literal(tree, "_START_TIMEOUT_S"),
        "channel_methods": [m["name"] for m in public_methods(channel)],
        "host_methods": host_methods,
        "wire_methods": wire_methods,
        "output_kinds": output_kinds,
        "output_fields": fields,
        "image_max_side": assign_literal(tree, "_MAX_IMAGE_SIDE"),
        "image_max_bytes": assign_literal(tree, "_MAX_IMAGE_BYTES"),
    }


# ---------------------------------------------------------------------------
# Contract validation
# ---------------------------------------------------------------------------

REQUIRED_METHODS = [
    "hello",
    "welcome",
    "start",
    "stop",
    "open_session",
    "send",
    "subscribe",
    "unsubscribe",
    "set_title",
    "judge",
    "output",
]
EXPECTED_WIRE_REQUESTS = {"open_session", "send", "subscribe", "unsubscribe", "set_title", "judge"}


def pointer_get(doc: Any, ref: str) -> Any:
    if not ref.startswith("#/"):
        raise KeyError(ref)
    node = doc
    for raw in ref[2:].split("/"):
        key = raw.replace("~1", "/").replace("~0", "~")
        if isinstance(node, list):
            node = node[int(key)]
        else:
            node = node[key]
    return node


def walk_refs(node: Any, doc: Any, problems: list[str], where: str) -> None:
    if isinstance(node, dict):
        ref = node.get("$ref")
        if isinstance(ref, str):
            try:
                pointer_get(doc, ref)
            except (KeyError, IndexError, ValueError, TypeError):
                problems.append(f"unresolved $ref {ref} at {where}")
        for key, value in node.items():
            walk_refs(value, doc, problems, f"{where}.{key}")
    elif isinstance(node, list):
        for index, value in enumerate(node):
            walk_refs(value, doc, problems, f"{where}[{index}]")


def validate_contract(contract: dict[str, Any], py: dict[str, Any]) -> list[str]:
    problems: list[str] = []
    if contract.get("protocol") != PROTOCOL:
        problems.append(f"protocol must be {PROTOCOL!r}")
    methods = contract.get("methods") or []
    names = [m.get("name") for m in methods]
    if names != REQUIRED_METHODS:
        problems.append(f"methods must be exactly {REQUIRED_METHODS}, got {names}")
    if len(names) != len(set(names)):
        problems.append("method names are not unique")
    for method in methods:
        for key in ("name", "direction", "kind", "summary"):
            if not method.get(key):
                problems.append(f"method {method.get('name')!r} lacks {key}")
        if method.get("direction") not in ("host_to_plugin", "plugin_to_host"):
            problems.append(f"method {method.get('name')!r} has bad direction")
        if method.get("kind") not in ("request", "response", "notification"):
            problems.append(f"method {method.get('name')!r} has bad kind")
    by_name = {m["name"]: m for m in methods}

    hello = by_name.get("hello", {})
    protocol_const = (((hello.get("params") or {}).get("properties") or {}).get("protocol") or {}).get("const")
    if protocol_const != PROTOCOL:
        problems.append("hello.protocol const does not match protocol")
    welcome = by_name.get("welcome", {})
    if welcome.get("responds_to") != "hello" or welcome.get("kind") != "response":
        problems.append("welcome must be the response to hello")
    if ((hello.get("result") or {}).get("$ref") or "") != "#/$defs/welcome":
        problems.append("hello.result must reference welcome")
    if ((welcome.get("result") or {}).get("$ref") or "") != "#/$defs/welcome":
        problems.append("welcome.result must reference welcome")

    wire = {
        m["name"]
        for m in methods
        if m.get("direction") == "plugin_to_host" and m.get("kind") == "request"
    }
    if wire != EXPECTED_WIRE_REQUESTS:
        problems.append(f"plugin->host requests must be {sorted(EXPECTED_WIRE_REQUESTS)}, got {sorted(wire)}")
    if set(py["wire_methods"]) != EXPECTED_WIRE_REQUESTS:
        problems.append(f"upstream ChannelHost methods drifted: {py['wire_methods']}")

    for name in ("start", "stop"):
        if (by_name.get(name, {}).get("timeout_ms")) != (contract.get("handshake") or {}).get(f"{name}_timeout_ms"):
            problems.append(f"{name} timeout does not match handshake")
    if by_name.get("start", {}).get("timeout_ms") != int(float(py["start_timeout_s"]) * 1000):
        problems.append("start timeout does not match upstream _START_TIMEOUT_S")

    kind_order = (contract.get("output") or {}).get("kind_order") or []
    if kind_order != py["output_kinds"]:
        problems.append(f"output kinds {kind_order} != upstream {py['output_kinds']}")
    output_def = ((contract.get("$defs") or {}).get("output") or {})
    if sorted(output_def.get("required") or []) != sorted((contract.get("output") or {}).get("always_present") or []):
        problems.append("output.always_present does not match output schema required fields")

    limits = contract.get("limits") or {}
    if limits.get("max_frame_bytes") != (contract.get("transport") or {}).get("max_frame_bytes"):
        problems.append("limits.max_frame_bytes != transport.max_frame_bytes")
    if limits.get("output_queue_capacity") != (contract.get("backpressure") or {}).get("queue_capacity"):
        problems.append("limits.output_queue_capacity != backpressure.queue_capacity")
    if limits.get("max_image_bytes") != (contract.get("images") or {}).get("max_decoded_bytes"):
        problems.append("limits.max_image_bytes != images.max_decoded_bytes")
    if limits.get("max_images_per_send") != (contract.get("images") or {}).get("max_images_per_send"):
        problems.append("limits.max_images_per_send != images.max_images_per_send")
    if limits.get("call_timeout_ms") != (contract.get("handshake") or {}).get("call_timeout_ms"):
        problems.append("limits.call_timeout_ms != handshake.call_timeout_ms")
    if (contract.get("images") or {}).get("max_images_per_send") != limits.get("max_images_per_send"):
        problems.append("images.max_images_per_send != limits.max_images_per_send")

    send_images = (((by_name.get("send", {}).get("params") or {}).get("properties") or {}).get("images") or {})
    if send_images.get("maxItems") != limits.get("max_images_per_send"):
        problems.append("send.images.maxItems != limits.max_images_per_send")
    if ((send_images.get("items") or {}).get("type")) != "string":
        problems.append("send.images.items must be a base64 string schema")

    codes = [e.get("code") for e in contract.get("errors") or []]
    if len(codes) != len(set(codes)):
        problems.append("error codes are not unique")
    kinds = [k.get("kind") for k in contract.get("error_kinds") or []]
    if len(kinds) != len(set(kinds)):
        problems.append("error kinds are not unique")
    for entry in contract.get("errors") or []:
        if not isinstance(entry.get("code"), int):
            problems.append(f"error {entry.get('name')!r} code must be int")
        for kind in entry.get("data_kinds") or []:
            if kind not in kinds:
                problems.append(f"error {entry.get('name')!r} refers to unknown data kind {kind!r}")

    defs = contract.get("$defs") or {}
    for required_def in ("request", "notification", "response", "error_response", "welcome", "output", "question"):
        if required_def not in defs:
            problems.append(f"schema lacks $defs/{required_def}")
    walk_refs(contract.get("$defs") or {}, contract, problems, "$defs")
    walk_refs(contract.get("oneOf") or [], contract, problems, "oneOf")
    for index, method in enumerate(methods):
        walk_refs(method, contract, problems, f"methods[{index}]")

    if not contract.get("examples"):
        problems.append("contract has no examples")
    for example in contract.get("examples") or []:
        label = example.get("label") or "example"
        if not example.get("lines"):
            problems.append(f"example {label!r} has no lines")
        for index, line in enumerate(example.get("lines") or []):
            try:
                frame = json.loads(line)
            except json.JSONDecodeError as exc:
                problems.append(f"example {label!r} line {index + 1} is not JSON: {exc}")
                continue
            if not isinstance(frame, dict) or frame.get("jsonrpc") != "2.0":
                problems.append(f"example {label!r} line {index + 1} is not a JSON-RPC frame")
                continue
            if "method" not in frame and "result" not in frame and "error" not in frame:
                problems.append(f"example {label!r} line {index + 1} has no method/result/error")
    return problems


def load_contract() -> dict[str, Any]:
    try:
        return json.loads(read(CONTRACT_PATH))
    except FileNotFoundError:
        raise SystemExit(f"missing contract: {CONTRACT_PATH.relative_to(REPO)}")
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid contract JSON: {exc}")


# ---------------------------------------------------------------------------
# Anchors
# ---------------------------------------------------------------------------

ANCHORS: list[dict[str, Any]] = [
    {"path": "core/framework/server/channels.py", "needle": 'ENTRY_POINT_GROUP = "hive.channels"', "label": "entry-point 组名", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "_START_TIMEOUT_S = 30.0", "label": "启动/停止超时", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "class Channel(Protocol)", "label": "插件接口", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "class QueenOutput", "label": "输出数据类", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "class ChannelHost", "label": "宿主能力对象", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "async def open_session", "label": "打开/恢复会话", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "async def send(", "label": "投递消息", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "def subscribe(", "label": "订阅输出", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "async def start_channels", "label": "启动所有通道", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "async def handle_list_channels", "label": "GET /api/channels", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "_MAX_IMAGE_SIDE = 2048", "label": "上游图片最长边", "status": "required"},
    {"path": "core/framework/server/channels.py", "needle": "_MAX_IMAGE_BYTES = 2 * 1024 * 1024", "label": "上游图片字节上限", "status": "required"},
    {"path": "go_hive/go-hive/internal/server/channels_config.go", "needle": "func ChannelSections(", "label": "旧配置读取助手", "status": "required"},
    {"path": "go_hive/go-hive/internal/server/routes_credentials_channels.go", "needle": "func RegisterChannelRoutes(", "label": "GET /api/channels", "status": "required"},
    {
        "path": "go_hive/go-hive/internal/server/app.go",
        "needle": "RegisterChannelRoutes(mux, ChannelRouteDeps{Home: deps.Home, States: deps.ChannelStates})",
        "label": "路由状态接线",
        "status": "required",
    },
    {
        "path": "go_hive/go-hive/internal/cli/loader_channels.go",
        "needle": "channels.NewHost(", 
        "label": "serve 接线",
        "status": "required",
    },
    {"path": "go_hive/contracts/channel-plugin-protocol.json", "needle": '"protocol": "hive-channel/1"', "label": "协议真值", "status": "required"},
    {"path": "go_hive/go-hive/internal/channels", "needle": 'ProtocolVersion = "hive-channel/1"', "label": "协议包", "status": "required", "kind": "tree"},
    {"path": "go_hive/go-hive/internal/channels", "needle": "type Host struct", "label": "宿主结构", "status": "required", "kind": "tree"},
    {"path": "go_hive/go-hive/internal/channels", "needle": "type SessionBackend interface", "label": "会话后端接口", "status": "required", "kind": "tree"},
    {"path": "go_hive/go-hive/internal/server/adapters_channels.go", "needle": "func NewChannelBackend", "label": "会话后端实现", "status": "required"},
    {"path": "go_hive/go-hive/internal/server/adapters_channels_subscription.go", "needle": "type channelSubscription", "label": "事件映射", "status": "required"},
    {"path": "go_hive/scripts/hive_channel_adapter.py", "needle": "hive.channels", "label": "Python adapter 桥（后续）", "status": "future"},
]


def ordinal_anchor(anchor: dict[str, Any]) -> dict[str, Any]:
    path = REPO / anchor["path"]
    needles = anchor.get("needles") or [anchor["needle"]]
    result = dict(anchor)
    result["needles"] = needles
    result["kind"] = anchor.get("kind", "file")
    result["exists"] = path.exists()
    result["verified"] = False
    result["state"] = "missing"
    if not path.exists():
        return result

    if result["kind"] == "tree":
        if not path.is_dir():
            result["state"] = "not_a_dir"
            return result
        hits = []
        for child in sorted(path.rglob("*")):
            if child.is_file() and child.suffix in (".go", ".py", ".json", ".md"):
                try:
                    text = child.read_text(encoding="utf-8")
                except (OSError, UnicodeDecodeError):
                    continue
                if any(needle in text for needle in needles):
                    hits.append(str(child.relative_to(REPO)))
        if hits:
            result["verified"] = True
            result["state"] = "present"
            result["matched"] = hits
        else:
            result["state"] = "symbol_missing"
        return result

    if not path.is_file():
        result["state"] = "not_a_file"
        return result
    try:
        text = path.read_text(encoding="utf-8")
        if any(needle in text for needle in needles):
            result["verified"] = True
            result["state"] = "present"
        else:
            result["state"] = "symbol_missing"
    except (OSError, UnicodeDecodeError):
        result["state"] = "unreadable"
    return result


def ordinal_anchors() -> list[dict[str, Any]]:
    return [ordinal_anchor(anchor) for anchor in ANCHORS]


def anchor_problems(results: list[dict[str, Any]]) -> list[str]:
    problems = []
    for result in results:
        needles = result.get("needles") or [result["needle"]]
        joined = " 或 ".join(repr(needle) for needle in needles)
        if result["status"] == "required":
            if not result["exists"]:
                problems.append(f"required anchor missing file: {result['path']} ({result['label']})")
            elif not result["verified"]:
                problems.append(
                    f"required anchor missing symbol: {result['path']} :: {joined} ({result['label']})"
                )
        elif result["exists"] and not result["verified"]:
            # A planned file that appeared must contain the promised symbol. This is the
            # anti-fake-green rule: a planned anchor is never "verified" just because a path exists.
            problems.append(
                f"{result['status']} anchor present but symbol missing: {result['path']} :: {joined} ({result['label']})"
            )
    return problems


# ---------------------------------------------------------------------------
# Static content
# ---------------------------------------------------------------------------

REQUIREMENTS = [
    {
        "id": "R-CH-01",
        "title": "外部 chat 的每个对话 = 一个可恢复的 queen 会话",
        "source": "channels.py 模块 docstring；open_session 的 resume/superseded_by 逻辑",
        "acceptance": "重启 serve 后仍能恢复；queen 迁移会话后订阅继续；dashboard 新建会话不抢走通道会话。",
        "status": "upstream",
    },
    {
        "id": "R-CH-02",
        "title": "插件通过 entry point 安装，不修改 hive 主仓",
        "source": "ENTRY_POINT_GROUP = \"hive.channels\"；工厂 entry_point.load()(ChannelHost)",
        "acceptance": "安装第三方包即可被发现；未安装时 GET /api/channels 为空。",
        "status": "upstream",
    },
    {
        "id": "R-CH-03",
        "title": "逐通道启停，enabled 是唯一开关",
        "source": "configuration.json.channels.<name>.enabled；start_channels 的门控",
        "acceptance": "enabled 真值才启动；缺省/非对象/假值显示 off。",
        "status": "upstream",
    },
    {
        "id": "R-CH-04",
        "title": "插件故障隔离：失败不拖垮 serve",
        "source": "start_channels 捕获 load/start 异常；30s 超时；failed 状态",
        "acceptance": "一个通道失败，其他通道和 HTTP/会话继续工作；错误出现在 GET /api/channels。",
        "status": "upstream",
    },
    {
        "id": "R-CH-05",
        "title": "queen 输出按顺序、按会话送到插件",
        "source": "QueenOutput + _Subscription 队列/worker；message/question/error/files/idle",
        "acceptance": "同一会话 FIFO；慢 handler 不阻塞事件总线；handler 异常不静默后续输出。",
        "status": "upstream",
    },
    {
        "id": "R-CH-06",
        "title": "人类监看：dashboard 与聊天应用看到同一个会话",
        "source": "ChannelHost._claim 写 session.channel / meta.json；set_title；dashboard 不碰通道会话",
        "acceptance": "dashboard 能打开同一会话；频道标题不被自动标题覆盖。",
        "status": "upstream",
    },
    {
        "id": "R-CH-07",
        "title": "图片与附件可用",
        "source": "send(images=...) 的 Pillow 管线；attach_file 的 files 输出",
        "acceptance": "上游：大图缩到 2048/2 MiB，不可读图片跳过；Go lane：4 MiB/8 张、不缩放；attach_file 成功后 files 输出。",
        "status": "upstream",
    },
    {
        "id": "R-CH-08",
        "title": "插件有自己的状态目录",
        "source": "ChannelHost.state_dir = $HIVE_HOME/channels/<name>",
        "acceptance": "目录存在且可写；插件不把状态散落到仓库或主目录。",
        "status": "upstream",
    },
    {
        "id": "R-CH-09",
        "title": "judge 能力可由通道调用",
        "source": "ChannelHost.judge(question, state, criteria=None) → probability|null",
        "acceptance": "未配置 judge 时返回 null；每次调用是一次 vendor 请求。",
        "status": "upstream",
    },
    {
        "id": "R-CH-10",
        "title": "Go 端口必须给出可执行的跨语言宿主方案",
        "source": "Go 不能加载 Python entry point；CGO_ENABLED=0",
        "acceptance": "定义 stdio JSON-line 协议、进程生命周期、错误/图片/背压边界，或明确不实现。",
        "status": "go-merged",
    },
]

BOUNDARY = [
    ("发现与生命周期", "读 channels.<name>；spawn/hello/start/stop；超时、崩溃、状态", "不发现、不重启宿主；只响应 hello/start/stop"),
    ("配置", "读取 configuration.json 的 channels.<name>，原样放进 hello", "只读自己认识的键；不写回 configuration.json"),
    ("状态目录", "创建 $HIVE_HOME/channels/<name>（MkdirAll 0755）并传入", "拥有目录内容；自行迁移/清理"),
    ("queen 会话", "open/resume/send/subscribe/unsubscribe/set_title；fork 跟随；colony 锁定", "只持有 session_id/subscription_id，不直接读写会话文件"),
    ("输出提取", "从事件总线提取 message/question/error/files/idle；保序、有界队列", "逐条消费 output；用 seq 缺口判断丢帧"),
    ("平台连接", "不接触平台 SDK、token 刷新、重连", "拥有平台连接、鉴权、重连、心跳"),
    ("消息格式", "不解析 @、thread、附件 URL", "把平台消息转成文本/图片；把 output 转成平台消息"),
    ("平台限流/重试", "不做平台级重试", "自己处理平台限流、退避、幂等"),
    ("图片", "解码 base64、按内容类型嗅探、跳过非图；4 MiB/张、最多 8 张", "提供 base64 字符串；渲染 output.files 路径"),
    ("错误分类", "会话错误映射成 JSON-RPC error.data.kind", "平台错误留在插件内；协议错误按 code 处理"),
    ("人类监看", "暴露 GET /api/channels；dashboard 可打开会话", "set_title；可选择不实现"),
    ("安全隔离", "超时、帧上限、进程终止；无沙箱、无进程组隔离", "同 uid、同文件系统/网络；必须自担平台凭据安全"),
]

PARITY = [
    ("安装发现", "importlib.metadata entry_points(group=\"hive.channels\")", "配置 channels.<name>.command", "internal/channels/config.go: Plugins；无 entry point", "go_only"),
    ("启用门控", "channels.<name>.enabled 真值", "Plugins 的 truthy 判定", "已合并；配置段每个对象通道都列入 states", "aligned"),
    ("工厂调用", "entry_point.load()(host)", "hello → welcome", "已合并；宿主先说 hello", "go_only"),
    ("启动超时", "asyncio.wait_for(start(), 30s)", "start timeout 30s", "DefaultStartTimeout = 30s", "aligned"),
    ("故障隔离", "异常记日志；failed；serve 继续", "startOne 失败 → failed；不拖垮", "已合并", "aligned"),
    ("配置传递", "ChannelHost.config = 配置段", "hello.params.config", "PluginConfig.Section 原样传递", "aligned"),
    ("状态目录", "$HIVE_HOME/channels/<name>", "hello.params.state_dir", "MkdirAll 0755；不是 0700", "approximate"),
    ("打开会话", "open_session(queen_id, session_id?, title?)", "open_session", "ChannelBackend.OpenSession", "aligned"),
    ("会话迁移", "superseded_by + SESSION_FORKED 跟随", "output.session_id 变化，订阅不变", "newestSuccessor + followSessionID", "aligned"),
    ("发送文本", "send(session_id, text, display_text?)", "send.text/display_text", "ChannelBackend.Send", "aligned"),
    ("图片", "Pillow：>2048/>2MiB 转 JPEG；非图跳过", "send.images base64 字符串；4MiB/张、8 张；不缩放", "Go-only：不移植 Pillow 缩放", "approximate"),
    ("订阅", "subscribe(session_id, handler)", "subscribe → subscription_id", "Host.subscribe；同会话替换", "aligned"),
    ("输出顺序", "队列 + worker；严格 FIFO", "每订阅 FIFO + seq；有界 256", "丢最旧；seq 缺口；不发 overflow error", "approximate"),
    ("五种输出", "message/question/error/files/idle", "output.kind 同枚举", "已合并；questions/files 为空时省略", "aligned"),
    ("慢 handler", "独立 worker，不阻塞 event bus", "独立 goroutine + 队列", "已合并", "aligned"),
    ("取消订阅", "unsubscribe(session_id)", "unsubscribe(subscription_id)", "空对象；未知 id 幂等", "approximate"),
    ("命名", "set_title(session_id, title)", "set_title", "已合并", "aligned"),
    ("judge", "ChannelHost.judge → probability|null", "judge", "AskJudge；nil → null", "aligned"),
    ("关闭", "stop() + host.close()", "stop request + close stdin + kill", "无进程组；2s 后 Process.Kill", "approximate"),
    ("状态接口", "GET /api/channels → off/running/failed", "Host.States() 同 shape", "app.go ChannelStates 接线（lane）", "aligned"),
    ("dashboard 隔离", "通道会话不被 dashboard 抢走", "session.channel + meta.json", "SetChannel + MergeSessionMeta", "aligned"),
    ("Python adapter 桥", "无（直接 import entry point）", "adapter 进程内代理 ChannelHost", "未编写", "unimplemented"),
    ("原生 Go 插件", "无（上游没有）", "原生插件直接实现本协议", "未编写", "unimplemented"),
]

DECISIONS = [
    ("D1", "新定义跨语言 ABI，而不是复刻 Python 对象调用", "Go 与 Python 没有共享对象模型；stdio 帧是可测试、可跨语言的最小边界。", "放弃直接调用 in-process；接受插件需要适配或重写。"),
    ("D2", "宿主先说 hello", "config/state_dir 由宿主持有；插件在 hello 前没有可信配置。", "插件先说 hello 会迫使宿主等待未知状态。"),
    ("D3", "JSON-RPC 2.0 风格 + 一行一个 JSON", "请求/响应/通知语义现成；调试可见；无需 HTTP 端口。", "自定义二进制帧更快但难调试；HTTP 增加端口与生命周期。"),
    ("D4", "start/stop 30s，与上游一致", "保持可观察行为；超时即失败并回收进程。", "无超时会让 serve 卡死；更短会误杀慢平台。"),
    ("D5", "保留五种 output kind", "插件迁移成本最低；语义与上游 QueenOutput 一致。", "合并成 text/event 会丢 question/files 的结构。"),
    ("D6", "每订阅有界队列 + 丢最旧 + seq 缺口", "进程间没有可靠背压；有界内存必须定义丢帧策略；seq 让插件知道丢了。", "没有 overflow error/关闭订阅；插件需自己补偿，不能假设可靠。"),
    ("D7", "图片用 base64 字符串内嵌", "保持单连接、单帧、无临时文件协议；与 JSON 兼容；容忍 data: 前缀。", "二进制帧更省 33%，但需要第二套 framing；大图占帧。"),
    ("D8", "错误用 code + data.kind", "插件可按类别重试/提示；日志保留细节。", "只回字符串无法程序化处理。"),
    ("D9", "不自动重启", "与上游失败隔离语义一致；避免崩溃循环。", "自动重启会掩盖插件 bug，且需要退避策略。"),
    ("D10", "每通道一个插件进程", "隔离崩溃与内存；配置/状态目录天然分开。", "单进程多通道更省内存，但故障域更大。"),
    ("D11", "Python entry point 用 adapter 桥接", "原插件代码可复用；Go 不引入 cgo。", "原生 Go 插件长期更优，但要重写平台 SDK。"),
    ("D12", "state_dir 固定 $HIVE_HOME/channels/<name>", "与上游一致；备份/清理有确定位置。", "允许插件自选目录会让运维失控；实际 0755 而非 0700。"),
    ("D13", "不移植 Pillow 缩放，按 4 MiB/8 张透传", "Go 标准库没有等价缩放/EXIF 管线；先保证协议与传输可用。", "大图直接进模型，成本和上下文占用高于上游；需要后续补缩放。"),
]

IMPLEMENTATION = [
    ("go_hive/go-hive/internal/server/channels_config.go", "旧配置助手：ChannelSections / UnhostedChannels", "present", "仍在；internal/channels/config.go 承担实际发现"),
    ("go_hive/go-hive/internal/server/routes_credentials_channels.go", "GET /api/channels 的 shape", "present", "已存在；States 为空时返回 []"),
    ("go_hive/go-hive/internal/server/app.go", "AppDeps.ChannelStates + 路由接线", "present", "ChannelStates 注入已合并"),
    ("go_hive/go-hive/internal/cli/loader_channels.go", "serve 启动 host / 关闭顺序", "present", "channels.NewHost；shutdown 先停 channel"),
    ("go_hive/go-hive/internal/channels/protocol.go", "帧类型、协议版本、限制、错误", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/channels/config.go", "Plugins / command/args/env/cwd 解析", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/channels/host.go", "spawn、hello、start/stop、states、请求路由、订阅队列", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/channels/process.go", "子进程、帧 I/O、请求配对、超时/终止", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/channels/backend.go", "SessionBackend 接口 + 默认超时", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/server/adapters_channels.go", "SessionBackend 的 server 实现", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/server/adapters_channels_subscription.go", "事件总线 → Output，fork/队列", "present", "已合并；targeted tests pass"),
    ("go_hive/go-hive/internal/server/session.go", "AddSessionListener / SetChannel / notifySessionLive", "present", "已合并；targeted tests pass"),
    ("go_hive/scripts/hive_channel_adapter.py", "Python entry point ↔ stdio 桥", "future", "未编写；Go 错误信息已提示需要 adapter"),
    ("go_hive/go-hive/internal/channels/*_test.go", "协议 conformance、超时/崩溃/队列丢帧", "present", "已合并；fake plugin/超时/丢帧测试"),
]

RISKS = [
    ("新 ABI 不是上游协议", "现有 Python 插件不能直接接 Go host；文档若称“兼容”会误导。", "adapter 桥或原生 Go 插件；协议版本显式。", "open"),
    ("4 MiB / 8 张图片 + base64", "单张 4 MiB 变 ~5.6 MiB 文本；多图可能顶满 16 MiB 帧。", "限制每消息图片数；客户端先压缩；必要时调大帧上限。", "design"),
    ("不缩放图片", "上游 2048px/2 MiB 缩放未移植，大图直接进模型，成本和上下文占用更高。", "插件侧先缩放；后续补 Go 图像管线。", "open"),
    ("错误信息有损", "只有 code + data.kind；没有 retryable/detail，traceback 只在 stderr 日志。", "插件按 kind 分类；日志关联通道名；不要依赖错误文本。", "design"),
    ("无沙箱", "配置里的 command 等同用户代码；插件可读 HIVE_HOME、密钥、网络。", "只安装可信插件；独立用户/容器；未来 capability token。", "accepted"),
    ("无进程组回收", "只 Kill 直接子进程；插件的孙进程可能残留（尤其平台 SDK 起子进程）。", "插件自己管理子进程；必要时外部 supervisor。", "open"),
    ("协议版本只单向声明", "welcome 只要求 plugin_version；hello.protocol 不回读，版本漂移不会被宿主发现。", "adapter/插件自检；未来加 protocol 回读与协商。", "open"),
    ("有界队列会丢帧", "满队列丢最旧，只有 seq 缺口，没有 overflow error/关闭订阅。", "插件监控 seq；平台侧补偿；日志看 dropped 计数。", "design"),
    ("Python adapter 缺失", "Go host 不能加载 entry point；没有 adapter 时 enabled 通道会 failed。", "实现 adapter；或为平台写原生 stdio 插件。", "future"),
    ("无 conformance 测试", "协议可能“文档正确、实现各写各的”。", "golden frames + fake plugin + 超时/崩溃/丢帧用例。", "open"),
    ("宿主已合并", "main 有 host；targeted tests 通过；Python adapter 仍未实现。", "在 wave-84 全量 CI/QC 后再宣传可用；补 adapter 后再宣称兼容现有插件。", "resolved"),
]

SOURCE_ROWS = [
    ("长会话", "会话是磁盘目录，可 resume；open_session 沿 superseded_by 找后继；dashboard 不抢通道会话。", "重启后继续；fork 后仍收到输出。"),
    ("外部 chat", "channels.py docstring：聊天应用从 dashboard 外到达 queen；每个群聊/私聊是一个 queen 会话。", "插件收到 ChannelHost 并能 start/stop。"),
    ("entry-point 插件", "hive.channels 组；工厂 entry_point.load()(host)；配置 channels.<name>.enabled。", "安装即发现；未安装为空列表。"),
    ("故障隔离", "load/start 异常记日志；30s 超时；failed 不拖垮 serve。", "坏插件不影响 HTTP 和其他通道。"),
    ("人类监看", "message/question/error/files/idle；set_title；dashboard 同一会话。", "输出有序；问题不重复；标题可读。"),
]

# ---------------------------------------------------------------------------
# HTML helpers
# ---------------------------------------------------------------------------

STATUS_LABEL = {
    "aligned": ("对齐", "ok"),
    "approximate": ("近似", "warn"),
    "go_only": ("Go-only", "info"),
    "unimplemented": ("未实现", "bad"),
    "planned": ("计划", "warn"),
    "present": ("已存在", "ok"),
    "lane": ("已合并", "info"),
    "future": ("后续", "info"),
    "partial": ("部分", "warn"),
    "required": ("必需", "info"),
    "upstream": ("上游有", "ok"),
    "go-partial": ("Go 部分", "warn"),
    "go-lane": ("Go 已合并", "ok"),
    "go-merged": ("Go 已合并", "ok"),
    "verified": ("已验证", "ok"),
    "missing": ("未验证", "warn"),
    "symbol_missing": ("符号缺失", "bad"),
    "open": ("未决", "warn"),
    "design": ("设计已定", "info"),
    "accepted": ("已接受", "warn"),
    "current": ("当前", "bad"),
}


def status_pill(status: str) -> str:
    label, kind = STATUS_LABEL.get(status, (status, ""))
    return f'<span class="pill {kind}">{ESC(label)}</span>'


def table(headers: list[str], rows: list[list[Any]], widths: list[str] | None = None) -> str:
    cols = ""
    if widths:
        cols = "<colgroup>" + "".join(f'<col style="width:{w}">' for w in widths) + "</colgroup>"
    head = "".join(f"<th>{ESC(h)}</th>" for h in headers)
    body = ""
    for row in rows:
        cells = []
        for cell in row:
            if isinstance(cell, str) and cell.startswith("<"):
                cells.append(f"<td>{cell}</td>")
            else:
                cells.append(f"<td>{ESC(cell)}</td>")
        body += "<tr>" + "".join(cells) + "</tr>\n"
    return f'<div class="tablewrap"><table>{cols}<thead><tr>{head}</tr></thead><tbody>{body}</tbody></table></div>'


def code(value: Any) -> str:
    return f"<code>{ESC(value)}</code>"


def section(num: str, sid: str, title: str, body: str) -> str:
    return f'<section id="{sid}"><h2><span class="num">{num}</span>{title}</h2>\n{body}\n</section>\n'


def resolve(schema: Any, defs: dict[str, Any]) -> dict[str, Any]:
    seen = set()
    while isinstance(schema, dict) and "$ref" in schema:
        ref = schema["$ref"]
        if ref in seen:
            return {}
        seen.add(ref)
        schema = defs.get(ref.rsplit("/", 1)[-1], {})
    return schema if isinstance(schema, dict) else {}


def type_label(schema: Any, defs: dict[str, Any]) -> str:
    if isinstance(schema, dict) and "$ref" in schema:
        return schema["$ref"].rsplit("/", 1)[-1]
    schema = resolve(schema, defs)
    if "const" in schema:
        return f'const {schema["const"]}'
    if "enum" in schema:
        return "enum(" + " | ".join(str(v) for v in schema["enum"]) + ")"
    raw = schema.get("type")
    if isinstance(raw, list):
        return " / ".join(str(v) for v in raw)
    if raw == "array":
        return f'array&lt;{type_label(schema.get("items") or {}, defs)}&gt;'
    if raw == "object" and schema.get("additionalProperties"):
        return "object (free)"
    return str(raw or "—")


def fields_table(schema: Any, defs: dict[str, Any], title: str = "") -> str:
    schema = resolve(schema, defs)
    props = schema.get("properties") or {}
    required = set(schema.get("required") or [])
    if not props:
        return '<p class="sub">空对象。</p>'
    rows = []
    for name, prop in props.items():
        prop = resolve(prop, defs)
        rows.append(
            [
                code(name),
                code(type_label(prop, defs)),
                "是" if name in required else "否",
                prop.get("description") or "",
            ]
        )
    prefix = f'<p class="sub">{ESC(title)}</p>' if title else ""
    return prefix + table(["字段", "类型", "必填", "说明"], rows, ["22%", "20%", "8%", "50%"])


def method_summary(method: dict[str, Any], defs: dict[str, Any]) -> str:
    if method.get("kind") == "response":
        return "—（hello 的 result）"
    params = resolve(method.get("params") or {}, defs)
    props = params.get("properties") or {}
    required = set(params.get("required") or [])
    if not props:
        return "空对象"
    return ", ".join(("`" + name + "`" if name in required else name) for name in props).replace("`", "")


def result_summary(method: dict[str, Any], defs: dict[str, Any]) -> str:
    result = method.get("result")
    if not result:
        return "—"
    result = resolve(result, defs)
    props = result.get("properties") or {}
    if not props:
        return "空对象"
    return ", ".join(props.keys())


def direction_label(direction: str) -> str:
    return "宿主 → 插件" if direction == "host_to_plugin" else "插件 → 宿主"


def method_kind_label(kind: str) -> str:
    return {"request": "request", "response": "response", "notification": "notification"}.get(kind, kind)


def timeout_label(method: dict[str, Any]) -> str:
    value = method.get("timeout_ms")
    if value is None:
        return "—"
    return f"{value / 1000:g}s"


def build_css() -> str:
    return """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1280px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:30px;line-height:1.25;margin:10px 0 10px;letter-spacing:-.02em} h1 span{color:var(--accent)}
.lede{color:var(--muted);max-width:1080px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15.5px;margin:22px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:12.5px}
.grid{display:grid;gap:12px} .g4{grid-template-columns:repeat(4,minmax(0,1fr))} .g3{grid-template-columns:repeat(3,minmax(0,1fr))} .g2{grid-template-columns:repeat(2,minmax(0,1fr))} .grid>*{min-width:0}
@media(max-width:900px){.g4,.g3{grid-template-columns:1fr 1fr}} @media(max-width:560px){.g4,.g3,.g2{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:24px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:20px;font-weight:800;margin-top:5px;letter-spacing:-.01em} .card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;max-width:100%}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.info{color:var(--accent2);border-color:#17324d;background:#0d1622} .pill.bad{color:var(--bad);border-color:#4d1c1c;background:#1a0f0f}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4;overflow-wrap:anywhere} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff}
.toc{display:flex;flex-wrap:wrap;gap:7px;margin-top:18px} .toc a{font-size:12px;border:1px solid var(--line);background:var(--panel);padding:5px 9px;border-radius:999px;color:var(--muted)} .toc a:hover{color:var(--text);text-decoration:none}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
figure.diagram{background:var(--code);border:1px solid var(--line);border-radius:12px;padding:14px;overflow-x:auto;margin:16px 0;max-width:100%}
figure.diagram figcaption{color:var(--muted);font-size:12px;margin-top:8px}
@media(max-width:900px){.tablewrap table{min-width:760px}}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}.toc a{padding:8px 12px;font-size:13px;min-height:44px;display:inline-flex;align-items:center}}
"""


# ---------------------------------------------------------------------------
# Build HTML
# ---------------------------------------------------------------------------


def build_html(f: dict[str, Any]) -> str:
    contract = f["contract"]
    defs = contract["$defs"]
    methods = {m["name"]: m for m in contract["methods"]}
    py = f["py"]

    cards = f"""
<div class="grid g4">
<div class="card"><div class="k">宿主位置</div><div class="v">Go · hive serve</div><div class="d">插件独立进程，不在 serve 地址空间</div></div>
<div class="card"><div class="k">传输</div><div class="v">stdio · JSONL</div><div class="d">一行一个 JSON；stderr 只做日志</div></div>
<div class="card"><div class="k">协议</div><div class="v">{ESC(contract['protocol'])}</div><div class="d">{len(contract['methods'])} 个方法 · {len(contract['errors'])} 个错误码 · {len(contract['error_kinds'])} 个 data.kind</div></div>
<div class="card"><div class="k">输出种类</div><div class="v">{len(contract['output']['kind_order'])}</div><div class="d">message / question / error / files / idle</div></div>
<div class="card"><div class="k">启动 / 停止</div><div class="v">{contract['handshake']['start_timeout_ms'] // 1000}s</div><div class="d">与上游 _START_TIMEOUT_S 一致</div></div>
<div class="card"><div class="k">单帧上限</div><div class="v">{contract['limits']['max_frame_bytes'] // (1024 * 1024)} MiB</div><div class="d">图片解码上限 {contract['limits']['max_image_bytes'] // (1024 * 1024)} MiB / 最多 {contract['limits']['max_images_per_send']} 张</div></div>
<div class="card"><div class="k">输出队列</div><div class="v">{contract['limits']['output_queue_capacity']} / 订阅</div><div class="d">FIFO；溢出丢最旧，seq 缺口提示</div></div>
<div class="card"><div class="k">Go 现状</div><div class="v">已合并 · wave-84 候选</div><div class="d">channel-host 7c6104bf；targeted tests pass</div></div>
</div>"""

    conclusion = f"""
<p>上游把 channels 做成<b>进程内 Python 插件</b>：<code>hive.channels</code> entry point 返回一个带 <code>start/stop</code> 的对象，该对象持有 <code>ChannelHost</code>，通过它打开 queen 会话、收发消息、订阅输出。Go 端口不能加载 Python entry point，也不能把 Python 对象塞进 Go 进程；本方案把同一条分界线改成<b>进程边界 + stdio JSON-line</b>。</p>
<div class="callout info"><b>先看结论：</b>宿主负责发现/启动/停止、会话、输出提取、顺序与背压；插件负责平台连接、消息映射、渲染与重试。协议固定为 {code(contract['protocol'])}，{len(contract['methods'])} 个方法、{len(contract['errors'])} 个错误码 + {len(contract['error_kinds'])} 个 data.kind、每订阅 {contract['limits']['output_queue_capacity']} 条有界队列。Go 宿主已合并（<code>internal/channels</code> + <code>adapters_channels*.go</code>，commit 7c6104bf）；serve 会启动插件，<code>GET /api/channels</code> 返回真实 states。</div>
<div class="callout info"><b>已合并：</b><code>channels.&lt;name&gt;.enabled</code> 现在会启动 stdio 插件；<code>GET /api/channels</code> 返回 running/off/failed。Python <code>hive.channels</code> entry-point 仍需要 adapter 桥。</div>"""

    source_rows = [[r[0], r[1], r[2]] for r in SOURCE_ROWS]
    requirements = f"""
<p>需求不是“再加一个 webhook”，而是让外部聊天应用的每个对话成为一个可恢复的 queen 会话，并把 queen 的输出按顺序送回聊天应用；同时任何插件故障都不能拖垮 <code>hive serve</code>。下表把需求、代码证据和验收条件分开写。</p>
{table(["需求来源", "代码/文档证据", "验收条件"], source_rows, ["16%", "58%", "26%"])}
<h3>1.1 需求清单</h3>
{table(["ID", "需求", "来源", "验收", "状态"], [[r["id"], r["title"], r["source"], r["acceptance"], status_pill(r["status"])] for r in REQUIREMENTS], ["7%", "20%", "27%", "36%", "10%"])}
<div class="callout"><b>开源仓库里没有 channel 实现。</b>上游只提供宿主和协议；本环境 <code>entry_points(group="hive.channels")</code> 实测为 0。上游行为由 <code>test_channels.py</code> 的 fake channel 固定，不能据此说某个聊天软件“已经能用”。</div>"""

    boundary_rows = [[r[0], r[1], r[2]] for r in BOUNDARY]
    boundary = f"""
<p>分界线只有一句话：<b>宿主拥有 queen 会话，插件拥有聊天平台</b>。两者之间不共享对象、不共享数据库，只有 {code('hive-channel/1')} 帧。</p>
<figure class="diagram"><pre>外部聊天平台
   │ 平台 SDK / webhook / 长连接（插件负责）
   ▼
Channel 插件进程（Python 或 Go）
   │ stdin/stdout：一行一个 JSON（本协议）
   ▼
Go Channel Host（hive serve 内；已合并）
   │ ChannelHost 语义：open_session / send / subscribe / ...
   ▼
SessionManager → queen 会话 → 模型/工具/附件</pre><figcaption>进程边界同时是故障边界和权限边界；它不是网络 API，也没有内置沙箱。</figcaption></figure>
{table(["事项", "宿主（Go / hive serve）", "插件（独立进程）"], boundary_rows, ["16%", "46%", "38%"])}
<div class="callout info"><b>接口就这些：</b>宿主→插件 <code>hello / start / stop / output</code>；插件→宿主 <code>open_session / send / subscribe / unsubscribe / set_title / judge</code>。没有“插件直接 import Go 包”或“宿主直接调用平台 SDK”的旁路。</div>"""

    host_method_rows = []
    for method in py["host_methods"]:
        wire = "—（宿主 close，不发帧）" if method["name"] == "close" else method["name"]
        host_method_rows.append([code(method["name"]), "async" if method["async"] else "sync", code(wire)])
    output_rows = [
        ["message", "一个完成的模型回合文本；工具调用前写完的文本先到。", "text 非空"],
        ["question", "ask_user 的结构化问题；下一条消息即回答。", "questions 非空"],
        ["error", "模型调用失败；dashboard 显示同一错误。", "text 非空"],
        ["files", "attach_file 发布的文件；路径在会话 data/attachments。", "files 非空"],
        ["idle", "queen 完成并等待下一条消息；结束 typing。", "内容为空"],
    ]
    upstream = f"""
<p>上游实现只有 <b>{py['source_lines']} 行</b>（{code(py['source'])}），真正的平台适配都在插件里。下面按“装载 → 能力 → 输出 → 会话语义”拆开。</p>
<h3>3.1 装载与生命周期</h3>
{table(["环节", "上游行为"], [
    ["entry point 组", code(py["entry_point_group"]) + "；工厂是 " + code("entry_point.load()(ChannelHost)")],
    ["插件接口", code("Channel") + " 只有 " + code("async start()") + " / " + code("async stop()")],
    ["启用门控", "<b>读取</b> " + code("configuration.json.channels.<name>.enabled") + "；非真值显示 off，不加载"],
    ["启动", "按名字排序；每个 start 最多 " + str(py["start_timeout_s"]) + "s；失败记 failed，不拖垮 serve"],
    ["状态", "<b>running / off / failed</b>；failed 带 " + code("TypeError: ...") + " 形式错误"],
    ["关闭", "on_shutdown 插到会话停止之前；每个 stop 最多 30s；随后 host.close()"],
], ["24%", "76%"])}
<h3>3.2 ChannelHost 提供什么</h3>
{table(["方法", "同步性", "对应 JSON-line"], host_method_rows, ["32%", "16%", "52%"])}
<p class="sub">此外还有只读属性 <code>name</code>、<code>config</code>、<code>state_dir</code>；<code>close()</code> 是宿主内部清理，不作为线协议方法。</p>
<h3>3.3 QueenOutput 五种输出</h3>
{table(["kind", "含义", "数据"], output_rows, ["14%", "60%", "26%"])}
<h3>3.4 订阅管线：顺序与慢 handler</h3>
<figure class="diagram"><pre>session.event_bus
   │  CLIENT_OUTPUT_DELTA（累积 pending）
   │  TOOL_CALL_STARTED（先把 pending 说完，ask_user 除外）
   │  LLM_TURN_COMPLETE（说完 pending）
   │  CLIENT_INPUT_REQUESTED（question/error/message + idle）
   │  SESSION_FORKED（跟随新 session_id）
   │  TOOL_CALL_COMPLETED（attach_file → files）
   ▼
_Subscription._queue  →  单独 worker  →  handler(output)
       （按到达顺序）        （一次一条）</pre><figcaption>handler 做网络 I/O；放到 worker 里，慢聊天应用不会卡住 queen 的事件总线。handler 抛异常只记日志，后续输出继续。</figcaption></figure>
<h3>3.5 会话语义</h3>
<ul>
<li><b>一个外部对话 = 一个 queen 会话</b>；<code>open_session</code> 恢复旧会话，找不到/被 colony 锁定时新建。</li>
<li><b>fork 跟随</b>：queen 把对话移到新 session 时，<code>SESSION_FORKED</code> 让订阅改挂新 id；后续 output 带新 id，插件下次 send 要用它。</li>
<li><b>colony 锁定</b>：会话 spawn 了 colony 后不能再作为通道会话发送，返回 ChannelError。</li>
<li><b>dashboard 隔离</b>：通道会话被 claim（<code>session.channel</code> + meta.json），dashboard 新建会话不会抢走它。</li>
</ul>
<h3>3.6 图片与附件</h3>
<ul>
<li><code>send(images=[bytes])</code> 把图片交给与 dashboard 相同的输入管线。</li>
<li>Pillow：最长边 &gt; {py['image_max_side']} 或字节 &gt; {py['image_max_bytes']} 时缩放并转 JPEG(q=85)；不可识别图片跳过。</li>
<li><code>attach_file</code> 成功后，从工具结果 JSON 的 <code>attached[].resolved</code> 找到会话附件副本，发 <code>files</code> output。</li>
</ul>
<h3>3.7 对外状态</h3>
<pre>GET /api/channels
{{"channels":[{{"name":"mychat","state":"running"}},
             {{"name":"other","state":"off"}},
             {{"name":"bad","state":"failed","error":"RuntimeError: no client"}}]}}</pre>
<div class="callout bad"><b>上游没有可安装的 channel 包。</b>仓库里只有宿主、测试和文档；entry_points 在本环境为 0。因此“上游协议”指的是 Python 对象协议，不是现成的网络协议。</div>"""

    method_rows = []
    for method in contract["methods"]:
        method_rows.append(
            [
                code(method["name"]),
                direction_label(method["direction"]),
                method_kind_label(method["kind"]),
                method_summary(method, defs),
                result_summary(method, defs),
                timeout_label(method),
            ]
        )
    error_rows = [
        [
            str(entry["code"]),
            code(entry["name"]),
            ", ".join(entry.get("data_kinds") or []) or "—",
            entry["meaning"],
        ]
        for entry in contract["errors"]
    ]
    error_kind_rows = [
        [code(entry["kind"]), entry["meaning"], code(entry["message"])]
        for entry in contract["error_kinds"]
    ]
    example_html = ""
    for example in contract["examples"]:
        example_html += f'<h3>{ESC(example["label"])}</h3><pre>' + "\n".join(ESC(line) for line in example["lines"]) + "</pre>"

    go_scheme = f"""
<h3>4.1 拓扑与进程边界</h3>
<figure class="diagram"><pre>hive serve（Go，单进程）
  └─ channel host
       ├─ exec channels.&lt;name&gt;.command（每通道一个子进程，同一进程组）
       ├─ stdin  ← 宿主写：hello / start / stop / output
       ├─ stdout → 宿主读：welcome / open_session / send / ...
       └─ stderr → 逐行转发到 serve 日志

插件进程
  ├─ 平台连接（SDK / webhook / WebSocket）
  ├─ 本地状态：$HIVE_HOME/channels/&lt;name&gt;/
  └─ 把平台消息映射成 send；把 output 渲染回平台</pre><figcaption>一行一个 JSON；stdout 只允许协议帧，日志必须走 stderr。宿主没有独立进程组。</figcaption></figure>

<h3>4.2 握手与生命周期</h3>
<figure class="diagram"><pre>host                         plugin
 │  spawn(command, env)         │
 │ ── hello(name, config) ────► │
 │ ◄──────────── welcome ────── │  plugin_version / capabilities
 │ ── start ──────────────────► │
 │ ◄──────────── {{}} ─────────── │  ≤ 30s
 │ ═════════ running ══════════ │
 │ ── output (notification) ───► │  只对已订阅会话
 │ ◄── open_session/send/... ── │
 │ ── stop ───────────────────► │
 │ ◄──────────── {{}} ─────────── │  ≤ 30s
 │  close stdin → wait 2s → kill │</pre><figcaption>宿主先说 hello，因为 config/state_dir 在宿主手里；welcome 只要求 plugin_version 非空。</figcaption></figure>
{table(["状态", "进入条件", "对 GET /api/channels 可见"], [
    ["off", "配置对象存在但 enabled 非真", "是（state=off）"],
    ["starting", "已 spawn，hello/start 未完成", "否（内部态）"],
    ["running", "start 已返回", "是"],
    ["stopping", "已发 stop，尚未退出", "否（内部态）"],
    ["failed", "无 command、spawn/hello/start 失败、运行期崩溃", "是（带 error）"],
], ["18%", "56%", "26%"])}

<h3>4.3 方法表（由 contract JSON 渲染）</h3>
{table(["方法", "方向", "类型", "params", "result", "超时"], method_rows, ["14%", "12%", "12%", "34%", "18%", "10%"])}
<p class="sub">表由生成器读取 {code('go_hive/contracts/channel-plugin-protocol.json')} 渲染；改协议必须改 contract 并重新生成。</p>

<h3>4.4 关键帧字段</h3>
<h4>hello（宿主 → 插件）</h4>
{fields_table(methods['hello']['params'], defs)}
<h4>welcome（插件 → 宿主，hello 的 result）</h4>
{fields_table(defs['welcome'], defs)}
<h4>output（宿主 → 插件，notification）</h4>
{fields_table(defs['output'], defs)}
<h4>send（插件 → 宿主）</h4>
{fields_table(methods['send']['params'], defs)}
<h4>open_session（插件 → 宿主）</h4>
{fields_table(methods['open_session']['params'], defs)}

<h3>4.5 消息样例</h3>
{example_html}

<h3>4.6 错误模型</h3>
<p>错误帧是 JSON-RPC 的 <code>error</code> 对象（code/message/data）；<code>data.kind</code> 是唯一可编程字段，没有 retryable/detail。会话错误统一用 <code>-32000</code>，参数错误用 <code>-32602</code>，未知方法用 <code>-32601</code>（但 data.kind 仍是 <code>invalid_params</code>）。</p>
{table(["code", "name", "data.kind", "含义"], error_rows, ["10%", "22%", "26%", "42%"])}
<h4>data.kind 明细</h4>
{table(["kind", "触发条件", "消息形态"], error_kind_rows, ["18%", "38%", "44%"])}

<h3>4.7 图片</h3>
{table(["项", "规则"], [
    ["编码", code("send.images[]") + " 是 base64 字符串；可带 " + code("data:...;base64,") + " 前缀，也容忍省略 padding"],
    ["解码上限", f"{contract['limits']['max_image_bytes']:,} bytes / 张；最多 {contract['limits']['max_images_per_send']} 张"],
    ["尺寸", "<b>不缩放、不转码</b>；按 " + code("http.DetectContentType") + " 嗅探，只接受 image/*"],
    ["不可读", "<b>跳过并记日志</b>；全部跳过且无文本 → " + code("invalid_params") + "（Nothing to send）"],
    ["base64 开销", "约 4/3；4 MiB 原图约 5.6 MiB 文本，仍低于 16 MiB 帧上限"],
    ["output 文件", "传运行时绝对路径字符串数组，不传 base64；插件从磁盘读"],
    ["与上游差异", "上游 Pillow 的 2048px / 2 MiB 缩放未移植；Go 只做 4 MiB 解码上限"],
], ["22%", "78%"])}

<h3>4.8 顺序与背压</h3>
{table(["机制", "规则"], [
    ["每订阅顺序", "严格 FIFO；同一订阅一个 goroutine 串行写 output"],
    ["seq", "必填，从 1 单调递增；缺口表示队列满丢帧，不要求回执"],
    ["同会话 send", "宿主按请求并发处理；不额外串行。插件要顺序必须自己排队"],
    ["请求/响应", "响应可按 id 乱序；没有 max pending 限制"],
    ["再次订阅", "同一通道对同一会话 subscribe 会替换旧订阅，旧 id 失效"],
    ["队列", f"每订阅 {contract['limits']['output_queue_capacity']} 条；绝不阻塞事件总线"],
    ["溢出", "丢弃最旧的一条，保留最新；只记 drops 日志，不发 error，不关闭订阅"],
    ["慢插件", "只拖慢自己的订阅，不拖 queen、其他通道"],
], ["22%", "78%"])}
<div class="callout bad"><b>背压不是可靠消息队列。</b>output 没有 ack；队列满时旧帧被静默丢弃，唯一信号是 seq 缺口。插件必须自己补偿，不能假设每条 message 都会到达。</div>

<h3>4.9 安全边界</h3>
{table(["边界", "事实"], [
    ["信任模型", code("channels.<name>.command") + " 是 configuration.json 里的本地命令，等同用户主动执行代码"],
    ["隔离", "无沙箱、无 seccomp、无独立用户；插件与宿主同 uid"],
    ["可见资源", "同一文件系统、网络、HIVE_HOME、已导出的凭据环境"],
    ["state_dir", "宿主创建 0755（受 umask 影响）；插件拥有内容，宿主不清理"],
    ["配置密钥", "hello.config 可能含 token；不得写日志；插件能看到"],
    ["帧防护", "单帧 16 MiB；图片 4 MiB/张、最多 8 张；没有文本长度上限"],
    ["进程回收", "stop → 关闭 stdin → 等 2s → Process.Kill；没有独立进程组，孙进程可能残留"],
    ["stderr", "逐行转发到 serve 日志；不进入协议 stdout；不保留 failed 诊断尾部"],
    ["版本校验", "welcome 只要求 plugin_version 非空；hello.protocol 不回读，版本漂移不会自动失败"],
    ["建议", "不可信插件放到独立用户/容器/VM；协议本身不提供权限隔离"],
], ["20%", "80%"])}"""

    impl_rows = [
        [code(path), desc, status_pill(status), note]
        for path, desc, status, note in IMPLEMENTATION
    ]
    req_rows = [
        [r["id"], r["title"], r["acceptance"], status_pill(r["status"])]
        for r in REQUIREMENTS
    ]
    design_rows = [[d[0], d[1], d[2], d[3]] for d in DECISIONS]
    three_part = f"""
<h3>5.1 需求清单（要求 → 验收）</h3>
{table(["ID", "需求", "验收", "上游/Go 状态"], req_rows, ["8%", "28%", "54%", "10%"])}
<h3>5.2 设计决策（为什么这样切）</h3>
{table(["#", "决策", "理由", "放弃的方案/代价"], design_rows, ["6%", "25%", "39%", "30%"])}
<h3>5.3 Go 实现文件与状态</h3>
{table(["文件", "职责", "状态", "备注"], impl_rows, ["38%", "28%", "10%", "24%"])}
<div class="callout info"><b>当前状态：host 已合并。</b><code>internal/channels/*</code>、<code>adapters_channels*.go</code> 与 loader 接线已在 main；targeted tests 全过。Python adapter 仍未实现，因此现有 <code>hive.channels</code> 包还不能直接跑。</div>
<h3>5.4 已通过的验收与后续项</h3>
<ol>
<li>协议 conformance：<code>hello/welcome/start/stop/output</code> 与 6 个反向方法全部有 golden frame 测试。</li>
<li>生命周期：无 command、spawn 失败、hello 超时/缺 plugin_version、start 超时、运行期崩溃、stop 超时都进入 failed 并终止进程。</li>
<li>顺序/背压：fake plugin 慢消费时事件总线不被阻塞；队列满丢最旧且 seq 缺口可观察。</li>
<li>会话：新建/恢复/fork 跟随/colony 锁定/dashboard 隔离各一条测试。</li>
<li>运维：<code>GET /api/channels</code> 的 running/off/failed 与 serve 日志一致；关闭顺序在会话停止之前。</li>
</ol>"""

    parity_rows = [[r[0], r[1], r[2], r[3], status_pill(r[4])] for r in PARITY]
    parity = f"""
<p>状态定义：<span class="pill ok">对齐</span> 语义相同；<span class="pill warn">近似</span> 语义保留但传输/边界不同；<span class="pill info">Go-only</span> 上游没有的机制；<span class="pill bad">未实现</span> 当前不可用。本表按已合并代码判定；Python adapter 仍为未实现。</p>
{table(["上游语义", "Python（进程内）", "JSON-line（Go）", "Go 实现/现状", "判定"], parity_rows, ["14%", "28%", "24%", "24%", "10%"])}
<div class="callout info"><b>最重要的一行是“安装发现”。</b>上游靠 Python entry point；Go 改成配置里的 <code>command</code>。这不是等价替换，而是新 ABI：已有插件要么走 Python adapter，要么重写成 stdio 插件。</div>"""

    compat = f"""
<h3>7.1 为什么 Go 不能直接加载 Python entry point</h3>
{table(["障碍", "具体原因"], [
    ["运行时/对象模型", "<code>entry point</code> 返回的是 Python 对象；<code>ChannelHost</code>、<code>QueenOutput</code>、<code>datetime/Path</code> 都没有 Go 对等物"],
    ["cgo 约束", "<code>CGO_ENABLED=0</code> 是本仓硬约束；嵌入 CPython 必须 cgo，直接违反硬约束"],
    ["并发模型", "插件是 asyncio 协程；Go 的 goroutine 不能 await Python Future，GIL 也会让调用串行"],
    ["发现机制", "<code>importlib.metadata</code> 读 Python 环境；Go 进程没有这个环境"],
    ["打包/部署", "需要 Python 解释器、hive 包、插件依赖与版本；单二进制发布被破坏"],
    ["宿主对象", "<code>ChannelHost</code> 是活对象；插件期望拿到实例，而不是 RPC 客户端；语义要靠 adapter 模拟"],
], ["22%", "78%"])}

<h3>7.2 三种路线</h3>
{table(["路线", "做法", "优点", "代价", "结论"], [
    ["A. Python adapter 桥", "Go host spawn 一个 Python 进程；adapter 发现 entry point、构造代理 ChannelHost、在插件与 Go 之间转发 JSON-line", "原插件代码基本不改；复用 Python 生态；Go 不引入 cgo", "多一个进程与 Python 依赖；base64 拷贝；版本偏移；adapter 要维护两套语义", "短期推荐"],
    ["B. 原生 Go 插件", "每个聊天平台用 Go 实现 stdio 插件，直接说 hive-channel/1", "单二进制、性能好、无 Python", "要重写平台 SDK/鉴权/重连；生态从零", "长期方向"],
    ["C. embed CPython", "用 cgo 在 Go 进程里跑 Python", "看似无进程边界", "违反 CGO_ENABLED=0；GIL/崩溃/部署都更糟", "拒绝"],
], ["16%", "34%", "20%", "22%", "8%"])}

<p>lane 的 <code>Host.startOne</code> 对 enabled 但没有 command 的通道直接记 failed，错误文本就是：<code>no command configured (Go host uses stdio plugins; Python hive.channels entry points need the Python adapter)</code>。这不是文档建议，而是当前 Go 代码的明确边界。</p>

<h3>7.3 Python adapter 桥怎么工作</h3>
<figure class="diagram"><pre>Go channel host
   │  stdio JSON-line（hive-channel/1）
   ▼
Python adapter（一个通道一个进程）
   ├─ importlib.metadata.entry_points(group="hive.channels")
   ├─ factory(proxy_host) → 原 Channel 对象
   ├─ proxy_host.open_session/send/subscribe/... → JSON-line 请求 → Go
   ├─ 收到 output notification → 转成 QueenOutput → 调插件 handler
   └─ start/stop：调用原插件 start()/stop()</pre><figcaption>adapter 在 Go 眼里就是“插件进程”；在原插件眼里就是“ChannelHost”。它是双向翻译层，不是沙箱。</figcaption></figure>
<p><b>adapter 的职责：</b>发现并加载 entry point；把 hello 的 config/state_dir 映射成 Python 属性；把 <code>subscribe</code> 的 handler 注册在本地；把 output 的 <code>questions/files</code> 还原成 Python dict/Path；把 Python 异常映射成 JSON-RPC error；把 <code>send.images</code> base64 解码成 bytes；把 <code>files</code> 路径原样交给插件。</p>

<h3>7.4 代价与兼容性</h3>
{table(["代价", "说明", "缓解"], [
    ["进程数", "每通道 1 个 Python adapter 进程；hive serve 是共享宿主（不是每通道一个）", "按通道隔离；不需要的通道 enabled=false"],
    ["Python 依赖", "需要 Python、hive 包、entry point 元数据、插件依赖", "adapter 启动自检；失败显示 failed；固定版本矩阵"],
    ["延迟", "多一次 JSON 序列化 + 管道 + base64；图片尤其明显", "限制图片数量；大文件走路径（output 已如此）"],
    ["语义差异", "Python 对象/异常/traceback 过线后变成 code/kind/message", "stderr 日志；没有 detail 字段，插件按 kind 分类"],
    ["版本漂移", "Go 协议版本、adapter 版本、插件版本三者可能不一致", "welcome 只报 plugin_version；adapter/插件自检"],
    ["维护面", "adapter 是第二套 ChannelHost 实现", "conformance 测试共用 golden frames；协议单一真值"],
    ["隔离", "Python 插件崩溃会带走 adapter（但不带走 serve）", "每通道独立进程；不自动重启"],
], ["16%", "56%", "28%"])}
<div class="callout"><b>兼容性结论：</b>adapter 能让“原插件代码不改”成为目标，但不是零成本：它要求插件只通过公开的 <code>ChannelHost</code> 接口工作，且接受异步/错误/图片路径的细微差异。直接依赖 Python 私有字段的插件仍然要改。</div>"""

    config = f"""
<h3>8.1 configuration.json</h3>
<pre>{{
  "channels": {{
    "mychat": {{
      "enabled": true,
      "command": ["python3", "-m", "hive_channel_adapter", "--name", "mychat"],
      "env": ["MYCHAT_TOKEN=..."],
      "port": 6199
    }}
  }}
}}</pre>
<p class="sub">示例里的 <code>hive_channel_adapter</code> 是第 07 节的后续兼容层；没有它时，<code>command</code> 应指向原生 stdio 插件（或 lane 提供的探测脚本）。</p>
<h3>8.2 键与语义</h3>
{table(["键", "类型", "默认", "谁读", "说明"], [
    [code("channels.<name>.enabled"), "bool/truthy", "false", "宿主", "唯一开关；非真值 → off，不 spawn"],
    [code("channels.<name>.command"), "string 或 array", "—", "宿主", "可执行文件或 argv；string 只当可执行文件，不经过 shell"],
    [code("channels.<name>.args"), "string 或 array", "[]", "宿主", "追加到 command 后的参数"],
    [code("channels.<name>.env"), "string 或 array", "[]", "宿主", "K=V 列表；追加到 os.Environ 后启动插件"],
    [code("channels.<name>.cwd"), "string", "继承 serve cwd", "宿主", "插件工作目录；空值沿用 serve 进程的 cwd，相对路径风险自负"],
    ["其它键", "任意", "—", "插件", "原样放进 hello.config，宿主不解释（含 port 等）"],
], ["28%", "14%", "10%", "8%", "40%"])}
<p class="sub"><code>command</code>/<code>args</code>/<code>env</code>/<code>cwd</code> 是 Go host 新增的宿主键；上游只把整个 section 原样交给插件，不解释这些键。start/stop/hello/call 超时是 <code>HostOptions</code> 代码默认值，不是 configuration.json 键。</p>
<h3>8.3 state_dir 与 API</h3>
<ul>
<li><code>state_dir = $HIVE_HOME/channels/&lt;name&gt;</code>，宿主用 MkdirAll 创建 0755（受 umask 影响），插件拥有内容；备份/迁移按目录处理。</li>
<li><code>GET /api/channels</code> 与上游同 shape：<code>{{"channels":[{{"name":"&lt;name&gt;","state":"off|running|failed","error":"..."}}]}}</code>，按 name 排序。</li>
</ul>
<pre>GET /api/channels
{{"channels":[{{"name":"bad","state":"failed","error":"channels.bad.command is empty"}},
             {{"name":"mychat","state":"running"}},
             {{"name":"idle","state":"off"}}]}}</pre>
<h3>8.4 失败排查</h3>
{table(["现象", "最可能原因", "检查/处置"], [
    ["enabled=true 但状态 off", "名字不在配置对象里；或 main 还没有 host", "看 serve 日志/本页状态；确认 channels 段是对象且 enabled 真值"],
    ["failed: no command configured", "enabled 真但没有 command", "给 stdio 插件配 command；Python entry point 需要 adapter"],
    ["failed: spawn", "command 不存在/不可执行/cwd 错", "手动执行同一 argv；检查 PATH 与权限"],
    ["failed: hello timeout", "插件没读 stdin 或启动太慢", "先跑协议探针；确认 stdout 没有被日志污染"],
    ["failed: handshake", "welcome 没有非空 plugin_version", "让插件返回 plugin_version；不是回读 protocol"],
    ["failed: start timeout", "start 内做阻塞 I/O；平台连不上", "start 只做初始化，后台连接；查 stderr"],
    ["running 但无输出", "没有 subscribe；或订阅 id 已失效", "确认 subscribe 返回 id；检查是否被再次 subscribe 替换"],
    ["failed: process exited", "插件崩溃/被平台库退出", "看 serve 日志里的 channel 前缀行；确认没有重复启动"],
    ["output 丢失", "队列满，丢最旧", "看 seq 缺口和 dropped 日志；提高消费速度或补偿"],
    ["图片被跳过", "base64 非法/超过 4 MiB/非 image/*", "检查 data: 前缀、padding 与图片大小"],
], ["26%", "34%", "40%"])}
<p class="sub"><code>go_hive/contracts/config-keys.json</code> 把 <code>channels.&lt;name&gt;.enabled</code> 标为 <code>go_partial</code>：字段读取/写回/路由 shape 对等；lane 合入后应重新冻结为 aligned。</p>
<div class="callout info"><b>当前运维事实：</b>host 已合并后，enabled 且带 <code>command</code> 的通道会 spawn 插件；<code>GET /api/channels</code> 返回 running/off/failed。enabled 但没有 command 的项是 <code>failed</code>（错误明确提示需 stdio command 或 Python adapter），不会再静默 warning。</div>"""

    risk_rows = [[r[0], r[1], r[2], status_pill(r[3]) if r[3] in STATUS_LABEL else ESC(r[3])] for r in RISKS]
    risks = f"""
<p>这一节是“不要自欺”清单：协议能解决跨语言调用，但不解决沙箱、可靠投递、平台 SDK 兼容和插件生态。</p>
{table(["风险", "影响", "缓解", "状态"], risk_rows, ["20%", "36%", "34%", "10%"])}
<h3>9.1 新 ABI 的边界</h3>
<ul>
<li>本协议不是上游 Python 协议的序列化版本；它是<b>新协议</b>。方法名接近，不代表 wire 兼容。</li>
<li>上游插件拿的是活对象；Go 插件拿的是 JSON 请求/响应。任何依赖对象身份、属性回写、同步返回的插件都要改。</li>
<li>版本策略：同 major 只加可选字段；改字段含义/必填性必须升 major。未知方法 -32601，未知字段忽略。</li>
</ul>
<h3>9.2 能力协商</h3>
<p><code>welcome.capabilities</code> 是咨询性的：宿主不据此裁剪 output，也不会提前拒绝图片；插件应忽略不支持的 kind，而不是断开。</p>
<h3>9.3 未决问题</h3>
<ul>
<li>是否给 output 加 ack/重放窗口？当前队列满只丢最旧，唯一信号是 seq 缺口。</li>
<li>是否把 judge 调用做成可取消请求？当前无 cancel 方法；插件只能超时放弃。</li>
<li>是否限制插件可访问的 queen/session 集合？当前同 uid 全可见；未来可加 capability token。</li>
<li>是否补进程组/Job Object 回收？当前只 <code>Process.Kill</code> 直接子进程，孙进程可能残留。</li>
<li>是否补 2048px / 2 MiB 图片缩放？当前 4 MiB/8 张直接透传，模型成本更高。</li>
</ul>"""

    anchor_rows = []
    for result in f["anchors"]:
        state = "present" if result["verified"] else result["state"]
        anchor_rows.append(
            [
                code(result["path"] + ("/**" if result.get("kind") == "tree" else "")),
                code(" 或 ".join(result.get("needles") or [result["needle"]])),
                status_pill(result["status"]) + " " + status_pill(state),
                result["label"],
            ]
        )
    planned_missing = [a for a in f["anchors"] if a["status"] == "planned" and not a["verified"]]
    future_missing = [a for a in f["anchors"] if a["status"] == "future" and not a["verified"]]
    indexes = f"""
<p>生成器对下表做锚点校验：<b>required</b> 文件/符号缺失 → <code>--check</code> 失败；<b>planned</b> 路径缺失只报告“未验证”，绝不算通过；planned 文件一旦出现但符号不对 → 同样失败。这样文档不会随代码漂移，也不会把计划项伪装成已完成。</p>
{table(["文件", "符号 / 字符串", "状态", "说明"], anchor_rows, ["38%", "28%", "16%", "18%"])}
<p class="sub">planned 缺失 {len(planned_missing)} 个：{ESC(', '.join(dict.fromkeys(a['path'] for a in planned_missing))) if planned_missing else '无'}；future（adapter）缺失 {len(future_missing)} 个。host 已合入，adapter 仍未编写。</p>
<h3>10.1 维护命令</h3>
<pre># 重新生成（写 HTML + JSON）
python3 go_hive/scripts/gen_channels_host.py

# CI / 本地校验：产物字节一致 + required 锚点 + planned 反假绿
python3 go_hive/scripts/gen_channels_host.py --check

# 更严格：planned 锚点缺失也失败（当前 host planned 已清空）
python3 go_hive/scripts/gen_channels_host.py --check --require-planned</pre>
<h3>10.2 更新顺序</h3>
<ol>
<li>先改 <code>go_hive/contracts/channel-plugin-protocol.json</code>（协议真值）。</li>
<li>再改本生成器的静态需求/矩阵/差异文本；协议表自动跟着 contract 变。</li>
<li>运行生成器写 HTML+JSON；最后跑 <code>--check</code>。</li>
<li>host 已合入：planned 锚点已转 required；重跑生成器确认 <code>--require-planned</code> 通过。</li>
</ol>
<h3>10.3 统计</h3>
{table(["项", "值"], [
    ["上游 channels.py", f"{py['source_lines']} 行"],
    ["协议方法", str(len(contract['methods']))],
    ["错误码 / data.kind", f"{len(contract['errors'])} / {len(contract['error_kinds'])}"],
    ["输出 kind", str(len(contract['output']['kind_order']))],
    ["锚点", f"{f['stats']['required_anchors']} required + {f['stats']['planned_anchors']} planned + {f['stats']['future_anchors']} future（planned present {f['stats']['planned_present']}）"],
    ["产物", "channels-host.html + channels-host.json + contract JSON"],
], ["34%", "66%"])}
<p class="sub">文档索引：<a href="../README.md#6-文档索引">go_hive/README.md · 6. 文档索引</a>。配套机器可读文件：<a href="channels-host.json">channels-host.json</a> · <a href="../contracts/channel-plugin-protocol.json">channel-plugin-protocol.json</a>。HTML 是生成物，不要手改；协议表以 contract 为准。</p>"""

    toc = (
        '<div class="toc"><a href="#s00">结论与边界</a><a href="#s01">需求来源</a><a href="#s02">概念与分界</a>'
        '<a href="#s03">上游协议</a><a href="#s04">Go stdio 方案</a><a href="#s05">需求-设计-实现</a>'
        '<a href="#s06">一致性矩阵</a><a href="#s07">entry-point 兼容</a><a href="#s08">配置与运维</a>'
        '<a href="#s09">差异与风险</a><a href="#s10">代码索引/维护</a></div>'
    )
    header = f"""<header><div class="eyebrow">Hive · Channels Host</div><h1>Channels 插件宿主 <span>需求 · 设计 · 实现</span></h1>
<p class="lede">外部聊天应用如何接入 queen：上游用 Python entry point 在 <code>hive serve</code> 进程内加载插件；Go 端口改用独立插件进程 + stdio JSON-line。本页把分界线、上游协议、Go 方案、Python adapter 桥、配置运维和差异风险放在一张可校验的资料里，并明确标注已合并的宿主与仍未实现的 adapter。</p>
<div class="meta"><span>生成：<b>{datetime.now().strftime("%Y-%m-%d %H:%M")} CST</b></span><span>上游基线：<b>{ESC(UPSTREAM_BASELINE)}</b></span><span>Go HEAD：<b>{ESC(f["go_head"])}</b></span><span>协议：<b>{ESC(contract["protocol"])}</b></span><span>产物：<b>HTML + JSON + contract</b></span></div>
{toc}</header>
"""
    body = (
        header
        + section("00", "s00", "结论与边界卡片", conclusion + cards)
        + section("01", "s01", "需求来源：为什么需要 channel 宿主", requirements)
        + section("02", "s02", "概念与分界线：Host vs 插件", boundary)
        + section("03", "s03", "上游协议：进程内 ChannelHost / QueenOutput / entry point", upstream)
        + section("04", "s04", "Go 方案：process stdio JSON-line", go_scheme)
        + section("05", "s05", "需求 - 设计 - 实现", three_part)
        + section("06", "s06", "一致性矩阵：上游语义 ↔ JSON-line ↔ Go 实现", parity)
        + section("07", "s07", "entry-point 兼容性：为什么 Go 不能直接加载，以及 adapter 桥", compat)
        + section("08", "s08", "配置与运维", config)
        + section("09", "s09", "已知差异与风险", risks)
        + section("10", "s10", "代码索引与维护（生成器会校验）", indexes)
        + '<footer>本页由 <code>go_hive/scripts/gen_channels_host.py</code> 生成；协议以 <code>go_hive/contracts/channel-plugin-protocol.json</code> 为唯一真值。改动请改 contract 或生成器，不要手改 HTML。状态口径：已存在 / 计划 / 未实现分开标注，不把“设计”写成“已实现”。</footer>\n</div></body></html>\n'
    )
    return (
        '<!DOCTYPE html>\n<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">\n'
        "<title>Hive Channels 插件宿主 · 需求 / 设计 / stdio 协议</title>\n"
        f"<style>{build_css()}</style></head><body><div class=\"wrap\">\n"
        + body
    )


# ---------------------------------------------------------------------------
# Facts and JSON
# ---------------------------------------------------------------------------


def git_head() -> str:
    try:
        return subprocess.check_output(
            ["git", "-C", str(REPO), "rev-parse", "--short", "HEAD"], text=True, stderr=subprocess.DEVNULL
        ).strip()
    except Exception:
        return "unknown"


def build_facts() -> dict[str, Any]:
    contract = load_contract()
    py = py_facts()
    py["entry_points_observed"] = {
        "count": 0,
        "note": "measured in this workspace with importlib.metadata; a deployment with installed channel packages may differ",
    }
    problems = validate_contract(contract, py)
    if problems:
        raise SystemExit("contract: " + "; ".join(problems))
    anchors = ordinal_anchors()
    required = [a for a in anchors if a["status"] == "required"]
    planned = [a for a in anchors if a["status"] == "planned"]
    future = [a for a in anchors if a["status"] == "future"]
    return {
        "contract": contract,
        "py": py,
        "anchors": anchors,
        "go_head": git_head(),
        "stats": {
            "required_anchors": len(required),
            "planned_anchors": len(planned),
            "future_anchors": len(future),
            "planned_present": sum(1 for a in planned if a["verified"]),
            "future_present": sum(1 for a in future if a["verified"]),
            "methods": len(contract["methods"]),
            "errors": len(contract["errors"]),
            "error_kinds": len(contract["error_kinds"]),
            "output_kinds": len(contract["output"]["kind_order"]),
            "upstream_lines": py["source_lines"],
        },
    }


def build_json(f: dict[str, Any]) -> dict[str, Any]:
    contract = f["contract"]
    return {
        "schema": "hive-channels-host/v1",
        "generated_at": datetime.now().astimezone().isoformat(timespec="seconds"),
        "baseline": {"upstream": UPSTREAM_BASELINE, "go_head": f["go_head"], "protocol": contract["protocol"]},
        "headline": {
            "host": "Go process host in the parallel lane; not merged into main",
            "transport": contract["transport"]["framing"],
            "protocol": contract["protocol"],
            "methods": len(contract["methods"]),
            "errors": len(contract["errors"]),
            "error_kinds": len(contract["error_kinds"]),
            "output_kinds": contract["output"]["kind_order"],
            "start_timeout_ms": contract["handshake"]["start_timeout_ms"],
            "stop_timeout_ms": contract["handshake"]["stop_timeout_ms"],
            "call_timeout_ms": contract["handshake"]["call_timeout_ms"],
            "max_frame_bytes": contract["limits"]["max_frame_bytes"],
            "max_image_bytes": contract["limits"]["max_image_bytes"],
            "max_images_per_send": contract["limits"]["max_images_per_send"],
            "output_queue_capacity": contract["limits"]["output_queue_capacity"],
            "current_go_state": "main: no channel host (config read + serve warning + empty GET /api/channels); lane: internal/channels + adapters_channels*.go unmerged",
        },
        "requirements": REQUIREMENTS,
        "boundary": [{"topic": r[0], "host": r[1], "plugin": r[2]} for r in BOUNDARY],
        "upstream": f["py"],
        "protocol": contract,
        "implementation": [
            {"path": path, "responsibility": desc, "status": status, "note": note}
            for path, desc, status, note in IMPLEMENTATION
        ],
        "parity_matrix": [
            {"semantic": r[0], "python": r[1], "wire": r[2], "go": r[3], "status": r[4]} for r in PARITY
        ],
        "design_decisions": [
            {"id": r[0], "decision": r[1], "reason": r[2], "tradeoff": r[3]} for r in DECISIONS
        ],
        "risks": [{"risk": r[0], "impact": r[1], "mitigation": r[2], "status": r[3]} for r in RISKS],
        "anchors": [
            {
                "path": a["path"],
                "needles": a.get("needles") or [a["needle"]],
                "label": a["label"],
                "status": a["status"],
                "kind": a.get("kind", "file"),
                "state": "verified" if a["verified"] else a["state"],
            }
            for a in f["anchors"]
        ],
        "anchor_summary": {
            "required": f["stats"]["required_anchors"],
            "required_verified": sum(1 for a in f["anchors"] if a["status"] == "required" and a["verified"]),
            "planned": f["stats"]["planned_anchors"],
            "planned_present": f["stats"]["planned_present"],
            "future": f["stats"]["future_anchors"],
            "future_present": f["stats"]["future_present"],
            "planned_missing": list(
                dict.fromkeys(
                    a["path"] for a in f["anchors"] if a["status"] == "planned" and not a["verified"]
                )
            ),
            "future_missing": list(
                dict.fromkeys(
                    a["path"] for a in f["anchors"] if a["status"] == "future" and not a["verified"]
                )
            ),
        },
        "stats": f["stats"],
    }


def normalise_html(text: str) -> str:
    text = re.sub(r"生成：<b>[^<]*CST</b>", "生成：<b>__TIME__</b>", text)
    text = re.sub(r"Go HEAD：<b>[^<]*</b>", "Go HEAD：<b>__HEAD__</b>", text)
    return text


def normalise_json(doc: dict[str, Any]) -> dict[str, Any]:
    doc = json.loads(json.dumps(doc))
    doc.pop("generated_at", None)
    if isinstance(doc.get("baseline"), dict):
        doc["baseline"]["go_head"] = "__HEAD__"
    return doc


def absolute_paths(text: str) -> list[str]:
    patterns = [r"/Users/", r"/home/", r"[A-Za-z]:\\\\", r"file://"]
    found = []
    for pattern in patterns:
        found.extend(re.findall(pattern, text))
    return found


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify generated files, contract and anchors")
    parser.add_argument(
        "--require-planned",
        action="store_true",
        help="with --check, fail when a planned host anchor is absent",
    )
    args = parser.parse_args()

    facts = build_facts()
    html_text = build_html(facts)
    json_doc = build_json(facts)
    json_text = json.dumps(json_doc, ensure_ascii=False, indent=2, sort_keys=True) + "\n"

    anchor_issues = anchor_problems(facts["anchors"])
    planned_missing = [a for a in facts["anchors"] if a["status"] == "planned" and not a["verified"]]
    future_missing = [a for a in facts["anchors"] if a["status"] == "future" and not a["verified"]]

    if args.check:
        problems = list(anchor_issues)
        if not OUT_HTML.exists():
            problems.append(f"missing output: {OUT_HTML.relative_to(REPO)}")
        else:
            current_html = read(OUT_HTML)
            if normalise_html(current_html) != normalise_html(html_text):
                problems.append(f"drift: {OUT_HTML.relative_to(REPO)}")
            bad_paths = absolute_paths(current_html)
            if bad_paths:
                problems.append(f"absolute path(s) in HTML: {sorted(set(bad_paths))}")
        if not OUT_JSON.exists():
            problems.append(f"missing output: {OUT_JSON.relative_to(REPO)}")
        else:
            try:
                current = json.loads(read(OUT_JSON))
            except json.JSONDecodeError as exc:
                problems.append(f"invalid json: {OUT_JSON.relative_to(REPO)}: {exc}")
                current = None
            if current is not None and normalise_json(current) != normalise_json(json_doc):
                problems.append(f"drift: {OUT_JSON.relative_to(REPO)}")
        bad_paths = absolute_paths(html_text)
        if bad_paths:
            problems.append(f"absolute path(s) in generated HTML: {sorted(set(bad_paths))}")
        if args.require_planned:
            for anchor in planned_missing:
                problems.append(f"planned anchor absent: {anchor['path']} ({anchor['label']})")
        if problems:
            for problem in problems:
                print(f"channels-host: {problem}")
            return 1
        if planned_missing:
            print(
                "channels-host: OK (required anchors %d/%d; planned %d/%d present, %d not verified)"
                % (
                    facts["stats"]["required_anchors"],
                    facts["stats"]["required_anchors"],
                    facts["stats"]["planned_present"],
                    facts["stats"]["planned_anchors"],
                    len(planned_missing),
                )
            )
            print("channels-host: planned anchors not verified (not counted as passing):")
            for anchor in planned_missing:
                print(f"  - {anchor['path']} ({anchor['label']})")
        else:
            print(
                "channels-host: OK (required %d/%d anchors, planned %d/%d present, HTML+JSON up to date)"
                % (
                    facts["stats"]["required_anchors"],
                    facts["stats"]["required_anchors"],
                    facts["stats"]["planned_present"],
                    facts["stats"]["planned_anchors"],
                )
            )
        for anchor in future_missing:
            print(f"channels-host: future anchor absent: {anchor['path']} ({anchor['label']})")
        return 0

    if anchor_issues:
        for problem in anchor_issues:
            print(f"channels-host: {problem}")
        return 1
    OUT_HTML.parent.mkdir(parents=True, exist_ok=True)
    OUT_HTML.write_text(html_text, encoding="utf-8")
    OUT_JSON.write_text(json_text, encoding="utf-8")
    print(
        "channels-host: wrote %s and %s (%d methods, %d anchors)"
        % (
            OUT_HTML.relative_to(REPO),
            OUT_JSON.relative_to(REPO),
            facts["stats"]["methods"],
            facts["stats"]["required_anchors"] + facts["stats"]["planned_anchors"] + facts["stats"]["future_anchors"],
        )
    )
    if planned_missing:
        print(f"channels-host: note: {len(planned_missing)} planned anchor(s) absent, marked unverified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
