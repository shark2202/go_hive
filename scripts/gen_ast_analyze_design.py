#!/usr/bin/env python3
"""生成 ast_analyze 的需求 → 设计 → 验收链。

唯一真源是下方 DATA。生成文件：
  - go_hive/docs/ast-analyze-design.html
  - go_hive/docs/ast-analyze-design.json

ast_analyze 是 Go 侧、post-parity 的只读仓库 AST 分析工具：复用
internal/astgrep 的语言注册表/解析器/路径安全，把仓库拆成 unit、构建
import 图、计算 wave/环、风险热点、readiness/priority 与 start_here，
输出有界、确定性的 JSON/Markdown 报告。本文件先冻结需求、契约、风险
分类、评分公式与验收链，设计状态不冒充实现状态。

用法：
  uv run --no-project python go_hive/scripts/gen_ast_analyze_design.py
  uv run --no-project python go_hive/scripts/gen_ast_analyze_design.py --check
"""

from __future__ import annotations

import argparse
import html
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT_HTML = REPO / "go_hive" / "docs" / "ast-analyze-design.html"
OUT_JSON = REPO / "go_hive" / "docs" / "ast-analyze-design.json"
ESC = lambda value: html.escape(str(value), quote=True)


def list_items(items):
    return "".join(f"<li>{ESC(item)}</li>" for item in items)


_INPUT_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "path": {
            "type": "string",
            "description": "Absolute path to a file, directory or glob inside the session workspace root. Omitted means the workspace root; relative paths are rejected.",
        },
        "lang": {
            "type": "string",
            "description": "Optional canonical language override (go, python, typescript, javascript, tsx).",
        },
        "include_tests": {"type": "boolean", "default": False},
        "include_ui": {"type": "boolean", "default": True},
        "ignore": {
            "type": "array",
            "items": {"type": "string"},
            "description": "Extra directory names or globs to skip.",
        },
        "focus": {
            "type": "array",
            "items": {"type": "string"},
            "description": "Optional unit id prefixes to report (e.g. internal/app/); not filesystem paths. Dependencies are still analyzed.",
        },
        "detail": {
            "type": "string",
            "enum": ["summary", "units", "full"],
            "default": "summary",
            "description": "summary: counts + start_here + top risks; units: + unit table; full: + packages/dependencies/risk guide.",
        },
        "format": {
            "type": "string",
            "enum": ["json", "markdown"],
            "default": "json",
        },
        "write_to": {
            "type": "string",
            "description": "Absolute output path inside the session workspace root. Parent directories are created and an existing file is replaced atomically; relative paths are rejected. Omitted means auto-spill under <sessionRoot>/data/tool-output when the report exceeds max_inline_bytes.",
        },
        "max_files": {"type": "integer", "minimum": 1, "maximum": 50000, "default": 5000},
        "max_file_bytes": {"type": "integer", "minimum": 1024, "default": 1048576},
        "max_bytes": {"type": "integer", "minimum": 1024, "default": 8388608},
    },
    "required": [],
}

_RESULT_ENVELOPE = {
    "schema": "hive-go/ast-analyze@1",
    "language": "go",
    "summary": {
        "packages": 0,
        "files": 0,
        "test_files": 0,
        "ui_files": 0,
        "loc": 0,
        "units": 0,
        "ready": 0,
        "caution": 0,
        "blocked": 0,
        "keep": 0,
        "parse_errors": 0,
    },
    "packages": [
        {
            "name": "example.com/app",
            "dir": ".",
            "order": 1,
            "runtime": "portable",
            "workspace_deps": [],
            "test_files": 0,
            "units": ["internal/app/"],
        }
    ],
    "units": [
        {
            "id": "internal/app/",
            "package": "example.com/app",
            "files": ["internal/app/app.go"],
            "loc": 0,
            "wave": 0,
            "readiness": 0,
            "status": "ready",
            "bridge": "direct",
            "platforms": [],
            "risks": [
                {
                    "risk": "dynamic-code",
                    "severity": "high",
                    "count": 0,
                    "samples": [],
                }
            ],
            "internal_deps": [],
            "workspace_deps": [],
            "external_deps": [],
            "direct_tests": 0,
            "fan_in": 0,
            "density": 0.0,
            "priority": 0,
            "exports": 0,
            "bridgeable": 0,
            "notes": [],
        }
    ],
    "start_here": [],
    "dependencies": [
        {
            "name": "example.com/lib",
            "used_by": 0,
            "fit": "unknown",
            "crates": None,
            "note": "",
        }
    ],
    "warnings": [],
    "saved_to": None,
    "bytes": 0,
    "truncated": False,
    "error": None,
    "error_kind": None,
}

_ERROR_KINDS = [
    "invalid_input",
    "path_not_absolute",
    "unsupported_language",
    "ambiguous_language",
    "path_escape",
    "no_source_files",
    "too_many_files",
    "file_too_large",
    "parse_failed",
    "write_failed",
    "cancelled",
    "config_disabled",
]

_CONFIG_SCHEMA = {
    "$schema": "ast_analyze.json",
    "enabled": True,
    "languages": ["go", "python", "typescript", "tsx", "javascript", "rust", "json", "yaml", "markdown", "c", "cpp", "java", "csharp"],
    "include_tests": False,
    "include_ui": True,
    "ignore": [
        ".git",
        "node_modules",
        "vendor",
        "dist",
        "build",
        ".venv",
        "__pycache__",
        ".tox",
        ".mypy_cache",
        ".ruff_cache",
        ".pytest_cache",
        ".cache",
    ],
    "max_files": 5000,
    "max_file_bytes": 1048576,
    "max_total_bytes": 67108864,
    "max_units": 2000,
    "max_inline_bytes": 65536,
    "max_report_bytes": 8388608,
    "parse_timeout_ms": 2000,
    "risk_sample_lines": 3,
    "start_here_limit": 10,
    "workers": 4,
}

_RISK_TAXONOMY = [
    {
        "risk": "dynamic-code",
        "severity": "high",
        "go": "plugin.Open、运行期拼接的 import、reflect 调用",
        "python": "eval、exec、compile、importlib",
        "ts": "eval、new Function、动态 import()",
        "guidance": "无法静态确认目标；保留在源语言或改成显式注册表。",
    },
    {
        "risk": "reflection",
        "severity": "high",
        "go": "reflect、unsafe 反射路径",
        "python": "getattr/setattr/__dict__、metaclass",
        "ts": "Proxy、Reflect、原型修改",
        "guidance": "反射目标在运行期才确定；补显式枚举并测试真实分支。",
    },
    {
        "risk": "string-offsets",
        "severity": "high",
        "go": "string 索引/切片、[]byte 转换、utf8 手写",
        "python": "s[i]、s[a:b]、str.find",
        "ts": "slice/substring/substr/indexOf/charCodeAt",
        "guidance": "偏移单位不同（字节/码点/UTF-16）；用非 ASCII 测试。",
    },
    {
        "risk": "regex-syntax",
        "severity": "high",
        "go": "RE2 不支持的 lookaround/backreference",
        "python": "re 的 lookaround/backreference",
        "ts": "RegExp 的 lookbehind/backreference",
        "guidance": "目标语言正则能力不同；用 fancy-regex 或重写并测试。",
    },
    {
        "risk": "dynamic-regex",
        "severity": "medium",
        "go": "regexp.Compile(运行期字符串)",
        "python": "re.compile(运行期字符串)",
        "ts": "new RegExp(运行期字符串)",
        "guidance": "模式来自数据/配置；用真实模式做 golden。",
    },
    {
        "risk": "concurrency",
        "severity": "medium",
        "go": "go、chan、sync、context 共享状态",
        "python": "threading、asyncio、multiprocessing",
        "ts": "Promise、worker_threads、SharedArrayBuffer",
        "guidance": "并发语义与取消/关闭时序需要逐条记录。",
    },
    {
        "risk": "unsafe",
        "severity": "high",
        "go": "unsafe、cgo、//go:linkname",
        "python": "C 扩展、ctypes",
        "ts": "native addon、WASM 边界",
        "guidance": "跨 FFI 的内存所有权与生命周期无法自动证明；隔离成小适配层。",
    },
    {
        "risk": "platform-specific",
        "severity": "high",
        "go": "build tags、syscall、OS 专属路径",
        "python": "os.name/sys.platform 分支",
        "ts": "process.platform 分支",
        "guidance": "目标平台行为无法从当前机器推断；按平台矩阵测试。",
    },
    {
        "risk": "dynamic-typing",
        "severity": "medium",
        "go": "interface{}、type switch、map[string]any",
        "python": "Any、鸭子类型、**kwargs",
        "ts": "any、unknown 断言、结构化对象",
        "guidance": "运行期形状需要显式枚举/serde 模型。",
    },
    {
        "risk": "integer-semantics",
        "severity": "medium",
        "go": "int 宽度、溢出、uint 转换",
        "python": "任意精度 int、// 与 %",
        "ts": "number 双精度、位运算 32 位",
        "guidance": "位宽/溢出/负数除法不同；补边界测试。",
    },
    {
        "risk": "ordered-collections",
        "severity": "medium",
        "go": "map 迭代顺序",
        "python": "dict 插入序",
        "ts": "Map/Set/对象键顺序",
        "guidance": "输出顺序进入结果时使用显式排序或有序容器。",
    },
    {
        "risk": "null-references",
        "severity": "medium",
        "go": "nil 指针/接口",
        "python": "None 属性访问",
        "ts": "undefined/null 可选链",
        "guidance": "空值路径需要显式建模并测试。",
    },
    {
        "risk": "exceptions",
        "severity": "low",
        "go": "panic/recover",
        "python": "raise/except",
        "ts": "throw/try/catch",
        "guidance": "错误传播与清理语义不同；逐条对齐。",
    },
    {
        "risk": "async",
        "severity": "medium",
        "go": "goroutine + channel 的异步",
        "python": "async/await、asyncio",
        "ts": "Promise、async/await",
        "guidance": "取消、超时、背压需要显式契约。",
    },
    {
        "risk": "generators",
        "severity": "medium",
        "go": "channel 迭代器、iter.Seq",
        "python": "yield、生成器表达式",
        "ts": "function*、Iterator",
        "guidance": "惰性求值与资源释放需要逐条记录。",
    },
    {
        "risk": "monkeypatching",
        "severity": "high",
        "go": "运行期替换函数变量",
        "python": "setattr/module __dict__ 修改",
        "ts": "原型/模块对象修改",
        "guidance": "运行期改代码无法静态移植；改成显式接口。",
    },
    {
        "risk": "already-compiled",
        "severity": "medium",
        "go": "预编译 .a/.so、代码生成产物",
        "python": "Cython/numba/JAX 扩展",
        "ts": "native addon",
        "guidance": "已编译部分不提供源码语义；只分析源码边界。",
    },
    {
        "risk": "clock-random",
        "severity": "low",
        "go": "time.Now、math/rand、crypto/rand",
        "python": "datetime.now、random、secrets",
        "ts": "Date.now、Math.random、crypto",
        "guidance": "时钟/随机源需要注入或 pin 住，测试才能复现。",
    },
    {
        "risk": "web-apis",
        "severity": "medium",
        "go": "net/url、encoding/base64",
        "python": "urllib.parse、base64",
        "ts": "URL/URLSearchParams、TextEncoder、atob",
        "guidance": "URL/编码的边界行为不同；用真实输入做 golden。",
    },
    {
        "risk": "intl",
        "severity": "high",
        "go": "golang.org/x/text",
        "python": "locale、unicodedata",
        "ts": "Intl.*、localeCompare",
        "guidance": "分词/排序/规范化依赖 locale；需要固定 locale 测试。",
    },
    {
        "risk": "date-parsing",
        "severity": "high",
        "go": "time.Parse 多布局",
        "python": "datetime.fromisoformat",
        "ts": "Date.parse/new Date(string)",
        "guidance": "实现定义格式与本地时区不同；逐格式对齐。",
    },
    {
        "risk": "ui-code",
        "severity": "low",
        "go": "—",
        "python": "—",
        "ts": "React/Vue/Svelte/编辑器扩展",
        "guidance": "UI/编辑器代码保持原语言；不进入 start_here。",
    },
]

_DATA = {
    "schema_version": 1,
    "feature": "ast_analyze",
    "status": "partial",
    "decision": (
        "在 Go 侧新增只读的 ast_analyze 工具：复用 internal/astgrep 的 gotreesitter 语言注册表、"
        "解析器与路径安全，把仓库拆成 unit、构建 import 图、计算 wave/环、风险热点、readiness/"
        "priority/start_here，输出有界、确定性的 JSON/Markdown 报告；不执行代码、不联网、不新增第三方依赖。"
        "分析根固定为会话沙箱根（c.root），不是进程 cwd、不是 HIVE_HOME、不是 queen/colony 根；输入 path 必须是绝对路径。"
    ),
    "package_layout": {
        "core": "internal/astanalyze",
        "reuse": "internal/astgrep",
        "adapter": "internal/tools/builtin/ast_analyze.go",
        "wiring": "internal/cli/session_tools_ast_analyze.go",
        "rule": (
            "internal/astanalyze 拥有语言规格、逐文件事实、unit 划分、import 解析、依赖图、风险分类、"
            "评分与报告；只依赖标准库与 internal/astgrep，不得 import internal/server、internal/cli "
            "或 internal/tools/builtin。internal/astgrep 保持只读搜索职责，不反向依赖 astanalyze；"
            "适配器负责 schema/输入强制转换，catalogue 负责门控与路由。WorkspaceRoot 必须是会话沙箱根 c.root；"
            "astgrep 的空 root 回退到进程 cwd 只允许测试使用，生产 adapter 不得传空。"
        ),
        "maintenance": (
            "独立包、独立 doc.go、包测试、gofmt/size/lane-prefix 门禁；核心必须能脱离会话与工具注册表，"
            "用临时 fixture 仓库直接测试。"
        ),
    },
    "source_refs": [
        {
            "label": "rust-anything analyze.rs",
            "url": "https://github.com/erkamyaman/rust-anything/blob/main/src/analyze.rs",
            "use": "unit/依赖图/wave/readiness/priority/start_here 的参考模型",
        },
        {
            "label": "rust-anything risks.rs",
            "url": "https://github.com/erkamyaman/rust-anything/blob/main/src/risks.rs",
            "use": "风险分类、严重级权重与迁移建议",
        },
        {
            "label": "rust-anything ts/scan.rs",
            "url": "https://github.com/erkamyaman/rust-anything/blob/main/src/ts/scan.rs",
            "use": "逐文件事实（imports/risks/platforms/loops）的 AST 采集方式",
        },
        {
            "label": "rust-anything report.rs",
            "url": "https://github.com/erkamyaman/rust-anything/blob/main/src/report.rs",
            "use": "Markdown 报告结构（start here / package order / dependency / risk guide）",
        },
        {
            "label": "go_hive internal/astgrep",
            "url": "go_hive/go-hive/internal/astgrep",
            "use": "语言注册表、gotreesitter 解析、路径安全、文件过滤",
        },
        {
            "label": "go_hive ast_grep 设计",
            "url": "go_hive/docs/ast-grep-design.json",
            "use": "独立适配器 + lazy tier + 配置门控先例",
        },
        {
            "label": "go_hive http_tools 设计",
            "url": "go_hive/docs/http-tools-design.json",
            "use": "有界输出、结果信封、配置 fail-closed、验收链写法",
        },
        {
            "label": "gotreesitter",
            "url": "https://github.com/odvcencio/gotreesitter",
            "use": "纯 Go tree-sitter 运行时、语法树遍历与节点类型",
        },
    ],
    "problem": {
        "statement": (
            "ast_grep 回答“这个结构在哪里”，但不回答“这个仓库整体长什么样”：agent 需要自己 grep/"
            "go list/脚本拼出 unit、import 图、环、fan-in、风险热点和先做哪一块；结果不可复现、"
            "无风险分类、无路径安全、上下文易爆。"
        ),
        "user_value": [
            "一次调用拿到 unit、依赖 wave、环、fan-in/out、风险热点与 start_here。",
            "同一仓库的 JSON/Markdown 报告确定、可复现、可 golden。",
            "复用 ast_grep 的语法/路径安全，不引入 curl/tree-sitter CLI 等外部依赖。",
            "按语言识别风险（Go/Python/TS），而不是正则猜。",
            "只读、不执行代码、不联网，报告有界并落盘。",
        ],
        "why_not_now": (
            "上游 Hive 没有 ast_analyze，它是 Go-only、post-parity 的只读代码智能能力；"
            "不改变 builtin 14 / flat 39 / 首轮 24 的冻结面，只进 lazy/searchable 层。"
        ),
    },
    "requirements": [
        {
            "id": "R1",
            "priority": "P0",
            "statement": "新增一个扁平工具 ast_analyze，只读仓库 AST，不做代码生成、不执行代码、不联网。",
            "acceptance": "工具 schema 冻结；空/非法输入返回 typed error；没有任何写文件/执行/网络路径。",
            "traces": ["D1", "D11", "A1", "A10"],
        },
        {
            "id": "R2",
            "priority": "P0",
            "statement": "v1 支持 astgrep 链接的全部 13 种语法：go、python、typescript、tsx、javascript、rust、json、yaml、markdown、c、cpp、java、csharp；自动检测 + lang 覆盖；不支持/歧义扩展名是 typed error。",
            "acceptance": "13 语言矩阵（grep + analyze）通过；.h 等歧义扩展名要求显式 lang；未知语言不会静默当 TS。",
            "traces": ["D3", "A2"],
        },
        {
            "id": "R3",
            "priority": "P0",
            "statement": "逐文件提取 LOC、imports、声明/导出、循环、风险（含行号）、平台与 parse errors。",
            "acceptance": "至少 20 条 fixture 覆盖 imports/risks/platforms/loops/parse error；行号 1-based 且稳定。",
            "traces": ["D4", "A3"],
        },
        {
            "id": "R4",
            "priority": "P0",
            "statement": "unit 模型：包内文件夹为一个 unit，source root 下的单文件为一个 unit；测试/UI 分类与 unit id 稳定。",
            "acceptance": "unit id golden；测试文件不成为 unit；UI 文件标记 keep；同仓库重复运行 id 不变。",
            "traces": ["D5", "A4"],
        },
        {
            "id": "R5",
            "priority": "P0",
            "statement": "import 解析：相对路径、workspace 包、外部依赖、标准库、tsconfig 别名、Go module path、Python 包；无法解析显式标记，不猜。",
            "acceptance": "每种 import 形态的 fixture 通过；unresolved 计数与 warning 可见。",
            "traces": ["D6", "A5"],
        },
        {
            "id": "R6",
            "priority": "P0",
            "statement": "构建 unit 依赖图：fan-in/out、Tarjan SCC、wave 与环；包级拓扑顺序确定。",
            "acceptance": "环、并列依赖、深链 golden 通过；同一输入输出逐字节一致。",
            "traces": ["D7", "A6"],
        },
        {
            "id": "R7",
            "priority": "P0",
            "statement": "风险分类是闭集：每条风险有 severity、计数、最多 N 个 path:line 样本与语言检测器。",
            "acceptance": "风险表与代码枚举一致；样本去重；未知风险不能进入 JSON。",
            "traces": ["D8", "A7"],
        },
        {
            "id": "R8",
            "priority": "P0",
            "statement": "评分冻结：risk points、size、fan-in、dependency、tests 合成 readiness；density/priority/status/start_here 公式固定。",
            "acceptance": "公式单测 + golden；边界 0/100、parse error、blocked、keep、空仓库全部覆盖。",
            "traces": ["D9", "A8"],
        },
        {
            "id": "R9",
            "priority": "P0",
            "statement": "统一结果信封：summary/packages/units/start_here/dependencies/warnings；支持 JSON/Markdown、summary/units/full、inline 与 saved_to。",
            "acceptance": "detail/format 矩阵通过；write_to 绝对路径落点正确；未给 write_to 时超预算自动落盘且 saved_to 可读；未超预算不写文件；截断标记正确；相对/越界 write_to 被拒绝。",
            "traces": ["D10", "A9"],
        },
        {
            "id": "R10",
            "priority": "P0",
            "statement": "安全边界：workspace root、symlink 逃逸拒绝、ignore 目录、二进制/NUL 跳过、文件/总量/unit 上限、parse 超时、context 取消。",
            "acceptance": "路径穿越、symlink、二进制、超大文件、取消、超时全部 fail closed。",
            "traces": ["D11", "A10", "A11"],
        },
        {
            "id": "R11",
            "priority": "P0",
            "statement": "配置只读 $HIVE_HOME/ast_analyze.json；HIVE_AST_ANALYZE=0 硬开关；损坏 fail closed；不读会话目录。",
            "acceptance": "优先级、enabled=false、损坏/不可读、硬开关、会话目录配置被忽略全部通过。",
            "traces": ["D12", "A12"],
        },
        {
            "id": "R12",
            "priority": "P0",
            "statement": "接入不改变冻结面：builtin.Defs()=14、flat 39/24 不变；独立适配器、lazy tier、WithRoot、Close。",
            "acceptance": "catalogue 测试断言 14/39/24 不变、ast_analyze 只进 lazy 层；WithRoot 隔离；Close 释放。",
            "traces": ["D13", "A13"],
        },
        {
            "id": "R13",
            "priority": "P1",
            "statement": "确定性、跨平台、可测试：CGO_ENABLED=0、gofmt/go vet、-race、golden 报告与 design --check。",
            "acceptance": "六目标构建、race、golden、docs --check 全绿；无 map 随机序进入输出。",
            "traces": ["D14", "A14", "A15"],
        },
        {
            "id": "R14",
            "priority": "P0",
            "statement": "分析根固定为会话沙箱根（c.root）：不是进程 cwd、不是 HIVE_HOME、不是 queen/colony 根；path/write_to 必须是绝对路径且落在 root 内（相对路径 invalid_input），focus 是 unit id 前缀；go.mod/tsconfig/pyproject 等 manifest 发现不向父目录越界。",
            "acceptance": "从不同进程 cwd 运行结果一致；空 root 的 process-cwd 回退只允许测试；相对 path 返回 path_not_absolute；绝对路径/../symlink/write_to 逃逸拒绝；WithRoot worker 隔离；父目录 tsconfig 不被读取。",
            "traces": ["D15", "A16"],
        },
        {
            "id": "R15",
            "priority": "P1",
            "statement": "大仓库有界且可优化：索引化 fact/unit 查询、有界 worker 池、manifest 只读一次、MaxUnits 生效、context 取消；提供基准。",
            "acceptance": "图构建不再 O(N²)；Workers=1 与 4 输出逐字节一致；MaxUnits/MaxFiles 截断有 warning；BenchmarkAnalyzeSynthetic 可跑；10k 文件的目标 SLA 记录在开放问题。",
            "traces": ["D17", "A18"],
        },
    ],
    "non_goals": [
        "不做代码生成、重构、自动修改；只读分析。",
        "不执行被分析代码、不启动语言服务器、不联网、不装依赖。",
        "不做完整类型检查/语义等价证明；风险是启发式，不是证明。",
        "不分析二进制/反编译产物；只分析源码。",
        "不承诺 Python/rust-anything 的逐字段 parity；ast_analyze 是 Go-only 能力。",
        "v1 不覆盖 astgrep 的全部语法；先 Go/Python/TS/JS，C/C++/Java/C# 后续。",
    ],
    "options": [
        {
            "id": "O1",
            "name": "调用 rust-anything 二进制",
            "verdict": "rejected",
            "reason": "新增外部二进制与跨平台分发；无法复用 go_hive 的路径安全/工具门控；无法在无 Rust 环境运行。",
        },
        {
            "id": "O2",
            "name": "只用 go/ast + go/types",
            "verdict": "rejected",
            "reason": "只能分析 Go；Python/TS 需要另一套；完整类型检查成本高且不解决跨语言风险分类。",
        },
        {
            "id": "O3",
            "name": "复用 internal/astgrep + gotreesitter",
            "verdict": "chosen",
            "reason": "纯 Go、CGO_ENABLED=0、已有语言注册表/解析/路径安全；ast_grep 已验证 lazy tier 与适配器模式。",
        },
        {
            "id": "O4",
            "name": "tree-sitter CLI / cgo",
            "verdict": "rejected",
            "reason": "引入外部进程或 C 工具链，违反六平台纯 Go 构建。",
        },
        {
            "id": "O5",
            "name": "纯正则/文本启发式",
            "verdict": "rejected",
            "reason": "误报高、无法识别嵌套/作用域；ast_analyze 的价值就是 AST 级事实。",
        },
        {
            "id": "O6",
            "name": "LSP 驱动",
            "verdict": "rejected",
            "reason": "依赖语言服务器安装与启动；延迟/可用性不可控；只读分析不需要 LSP。",
        },
    ],
    "design": {
        "D1": {
            "title": "工具家族与命名",
            "text": (
                "一个扁平工具 ast_analyze，没有 action 开关。输入是仓库路径 + 可选的 lang/focus/detail/"
                "format；输出是一份有界报告。与 ast_grep 的分工：ast_grep 找结构，ast_analyze 看整体。"
            ),
        },
        "D2": {
            "title": "包边界",
            "text": (
                "internal/astanalyze 是核心：语言规格、逐文件事实、unit、import 解析、依赖图、风险、"
                "评分、报告。它 import internal/astgrep 复用 Lang/Grammar/解析/路径安全，不反向依赖；"
                "adapter 在 internal/tools/builtin/ast_analyze.go；catalogue 在 "
                "internal/cli/session_tools_ast_analyze.go 做门控/路由。"
            ),
        },
        "D3": {
            "title": "语言注册表与解析",
            "text": (
                "v1：go、python、typescript、tsx、javascript、rust、json、yaml、markdown、c、cpp、java、csharp"
                "（即 astgrep 链接的全部语法）。用 astgrep.ResolveLanguage/InferLanguage；.h 等歧义扩展名要求"
                "显式 lang；不支持语言是 ErrUnsupportedLanguage。TSX/JSX 用对应 grammar；Vue/Svelte/Astro 的 "
                "<script> 提取留到 v1.1，v1 先分析 .ts/.tsx/.js/.jsx/.mjs/.cjs。非 TS 语言的 import/risk 检测器"
                "按语法逐步补齐；解析、LOC、unit 与报告必须对 13 种语言全部可用。"
            ),
        },
        "D4": {
            "title": "逐文件事实",
            "text": (
                "每个文件产 FileFacts：loc、imports（specifier/type_only/dynamic）、声明/导出、"
                "loops、risks（risk→lines）、platforms、parse_errors。检测器按语言注册；未知节点类型"
                "只跳过，不 panic。解析失败/超时记 parse issue，不产出半棵树的结果。"
            ),
        },
        "D5": {
            "title": "unit 模型",
            "text": (
                "包内文件夹是一个 unit（id 带结尾 /）；source root 下的单文件是一个 unit（id=文件路径）。"
                "测试文件不成为 unit，但给直接 import 的 unit 记 direct_tests；UI 文件（.tsx/.jsx 等）"
                "标记 keep。unit id 是报告标识（workspace root 相对、/ 分隔），不是输入路径；输入 path 必须是绝对路径。"
            ),
        },
        "D6": {
            "title": "import 解析",
            "text": (
                "Go：go.mod module + 相对/绝对包路径 + 标准库；Python：绝对/相对 import + 标准库；"
                "TS/JS：相对路径、tsconfig paths、workspace 包名、node/UI 内建、外部包。无法解析的"
                "specifier 记 unresolved + warning，不猜。type-only import 不进入 external_deps。"
            ),
        },
        "D7": {
            "title": "依赖图与 wave",
            "text": (
                "unit → 它 import 的 unit 建边；fan_in 反向计数；Tarjan 求 SCC，依赖先出，"
                "wave=所有外部依赖 wave 最大值+1；同一 SCC 的 unit 标为一起移植。包级用 Kahn 拓扑排序；"
                "环按稳定顺序追加并给 warning。"
            ),
        },
        "D8": {
            "title": "风险分类",
            "text": (
                "闭集 Risk 枚举 + Severity（high 6 / medium 3 / low 1）+ 每语言检测器 + 最多 N 个"
                "path:line 样本。风险表见本文第 06 节；新增风险必须同时改枚举、表、测试与 golden。"
            ),
        },
        "D9": {
            "title": "评分与状态",
            "text": (
                "readiness = 100 - Σseverity×ln(1+count) - loc/40 - min(fan_in×1.5,15) - 12×no-fit - "
                "4×unknown + tests(10/4)，clamp 0..100；status 按 keep → blocked → caution → ready 的"
                "优先级；density=(loops+string_ops)×100/loc；priority=0.5×readiness+0.5×value；"
                "start_here 取 ready 且 loc≥20 且 bridgeable>0 的前 10，必要时放宽到 caution。"
            ),
        },
        "D10": {
            "title": "报告信封",
            "text": (
                "统一 JSON 信封：schema、language、summary、packages、units、start_here、dependencies、"
                "warnings、saved_to、bytes、truncated、error/error_kind。detail=summary/units/full 控制"
                "section；format=json/markdown。落点见 D16：write_to 给定时写该绝对路径；未给且报告超过 "
                "max_inline_bytes 时写 <sessionRoot>/data/tool-output/ast_analyze_<hash>.<json|md>；"
                "未给且未超预算则不写文件（saved_to=null）。saved_to 永远是绝对路径。"
            ),
        },
        "D11": {
            "title": "安全与边界",
            "text": (
                "只读；workspace root 为唯一安全边界；.. 与 symlink 逃逸拒绝；ignore 目录/二进制/NUL/"
                "超大文件跳过；max_files/max_total_bytes/max_units 上限；单文件 parse 超时；context 取消；"
                "报告只含 path:line 样本与有界文本，不含源码全文/秘密。"
            ),
        },
        "D12": {
            "title": "配置",
            "text": (
                "只读 $HIVE_HOME/ast_analyze.json；缺失即默认启用；格式错误/不可读即关闭（fail closed）；"
                "HIVE_AST_ANALYZE=0 硬开关；=1 不覆盖 enabled=false。字段：enabled、languages、"
                "include_tests、include_ui、ignore、max_files、max_file_bytes、max_total_bytes、max_units、"
                "max_inline_bytes、max_report_bytes、parse_timeout_ms、risk_sample_lines、start_here_limit、"
                "workers。不读会话目录。"
            ),
        },
        "D13": {
            "title": "注册与工具面",
            "text": (
                "独立适配器（ast_grep 先例），不进入 builtin.Defs()（保持 14），不进入冻结 39 面；"
                "默认启用，lazy/searchable 层；WithRoot 为 colony worker 重挂根；Close 释放；"
                "ConcurrencySafe=false（CPU 重分析，避免并行风暴）。"
            ),
        },
        "D14": {
            "title": "测试策略",
            "text": (
                "逐语言 fixture 仓库；unit id/import/图/环/wave/风险/评分 golden；路径穿越/symlink/二进制/"
                "超大/取消/超时；inline 与 saved_to；确定性（同输入逐字节一致）；-race；六目标 "
                "CGO_ENABLED=0 构建。"
            ),
        },
        "D15": {
            "title": "Workspace root 与 cwd",
            "text": (
                "分析根是会话沙箱根 c.root（普通 queen 会话为 <HIVE_HOME>/queens/<queen>/sessions/<id>，"
                "colony worker 为 <colony>/workers/<id>），不是进程 cwd、不是 HIVE_HOME、不是 queen/colony "
                "根。adapter 必须把 c.root 传给 astgrep.Config.WorkspaceRoot；空 root 的 process-cwd "
                "回退只允许测试。path/write_to 必须是绝对路径且落在 root 内；相对 path 返回 "
                "path_not_absolute；focus 是 unit id 前缀，不是文件系统路径。go.mod/tsconfig/pyproject "
                "的发现从 root 开始、不向父目录越界（否则一个 session 目录可能"
                "读到父仓库的 tsconfig 别名）。WithRoot 为 colony worker 重挂根，分析结果互不可见。"
                "详见 go_hive/docs/config-roots.html。"
            ),
        },
        "D16": {
            "title": "输出落点（write_to / auto-spill）",
            "text": (
                "1) write_to 给定时，报告写入该绝对路径：必须在 c.root 内，父目录自动创建（0700），"
                "临时文件 + fsync + 原子 rename，覆盖已有普通文件；目标是目录或写失败返回 write_failed。"
                "2) write_to 省略且渲染后的报告 <= max_inline_bytes 时，不写文件，saved_to=null，"
                "报告直接内联。3) write_to 省略且报告 > max_inline_bytes 时，写入 "
                "<c.root>/data/tool-output/ast_analyze_<hash>.<json|md>，扩展名由 format 决定，"
                "<hash> 取规范化请求（path/lang/detail/format/focus）加 UTC 纳秒的 sha256 前 16 位，"
                "避免并发覆盖；saved_to 为该绝对路径。4) 相对 write_to 返回 path_not_absolute；"
                "越界/../symlink 逃逸返回 path_escape；任何情况下都不写到 c.root 之外。"
            ),
        },
        "D17": {
            "title": "性能边界（大仓库）",
            "text": (
                "逐文件解析用 cfg.Workers 有界 worker 池，结果写回按文件索引的数组，输出顺序与 worker 数无关。"
                "unit 图、test 信用、package test 计数都用 factByPath/unitOfFile 索引，避免 O(N²) 嵌套扫描；"
                "go.mod 每次分析只读一次，TS/Python import 解析直接查 fileIndex，不逐 import stat。"
                "MaxFiles/MaxFileBytes/MaxTotalBytes/MaxUnits 是硬边界，超限写 warning 并截断；"
                "解析仍受单文件 parse_timeout 与 context 取消约束。已知残余：所有源码仍常驻内存（受 64 MiB "
                "总上限），风险规则是逐节点启发式，极深依赖图仍用递归 Tarjan；这些在 P2 基准里量化。"
            ),
        },
    },
    "pipeline": [
        "校验 path 为绝对路径（否则 path_not_absolute）并解析 workspace root；归一化 lang/focus/detail/format",
        "按 ignore/扩展名收集候选文件，应用 max_files/max_file_bytes",
        "逐文件解析（bounded timeout）→ FileFacts（imports/risks/platforms/loops/parse errors）",
        "按包/目录划分 unit；测试/UI 分类；稳定 unit id",
        "解析 import → workspace/external/stdlib/unresolved；建 unit 图",
        "fan-in/out、Tarjan SCC → wave/环；包级拓扑排序",
        "风险分类 + 样本去重；readiness/priority/status/start_here",
        "生成 summary/packages/units/dependencies/warnings",
        "渲染 JSON/Markdown；超预算落盘并回 saved_to/bytes/truncated",
        "取消/超时/错误 → error_kind 闭集；释放解析树",
    ],
    "tool_contract": {
        "name": "ast_analyze",
        "input_schema": _INPUT_SCHEMA,
        "result_envelope": _RESULT_ENVELOPE,
        "error_kinds": _ERROR_KINDS,
        "result_fields": {
            "text": "JSON/Markdown 报告：summary、start_here、units、packages、dependencies、warnings；write_to 给定时写该绝对路径，否则超 max_inline_bytes 时自动落盘，saved_to 为绝对路径。",
            "details": [
                "schema",
                "language",
                "summary",
                "packages",
                "units",
                "start_here",
                "dependencies",
                "warnings",
                "saved_to",
                "bytes",
                "truncated",
                "error",
                "error_kind",
            ],
        },
        "schema_note": (
            "以下 JSON Schema 是工具契约原文：字段名、类型与英文描述保持原样，便于与实现逐字对齐；"
            "其余章节为中文。"
        ),
    },
    "risk_taxonomy": _RISK_TAXONOMY,
    "scoring": {
        "readiness": (
            "readiness = 100 - Σ(severity.weight × ln(1 + count)) - loc/40 - "
            "min(fan_in × 1.5, 15) - 12 × no_fit_deps - 4 × unknown_deps "
            "+ (direct_tests > 0 ? 10 : package_tests > 0 ? 4 : 0)，clamp 0..100。"
        ),
        "status": (
            "platform-specific / UI / editor → keep；存在 no-fit 依赖或 dynamic-code → blocked；"
            "readiness < 50 或 parse_errors > 0 → caution；否则 ready。"
        ),
        "density": "density = (loops + string_ops) × 100 / loc，四舍五入到 1 位小数；loc=0 时为 0。",
        "priority": (
            "value = min(density × 4, 50) + (direct_tests > 0 ? 30 : package_tests > 0 ? 15 : 0) + "
            "size；size = loc/4 (loc<80)，20 (80≤loc<600)，10 (loc≥600)；"
            "priority = round(0.5 × readiness + 0.5 × value)，clamp 0..100。"
        ),
        "start_here": (
            "取 status=ready 且 loc≥20 且 bridgeable>0 的 unit，按 priority→readiness→wave→density→id "
            "排序取前 10；不足 3 个时放宽到 ready+caution；仍为空则去掉 20 行下限。"
        ),
        "bridge": (
            "Go 源码 → direct；C → cgo；Python/TS → subprocess 或 http sidecar；UI/editor → keep。"
            "bridge 是建议，不是自动生成；v1 不产出绑定代码。"
        ),
    },
    "config": {
        "paths": ["<HIVE_HOME>/ast_analyze.json（运维/用户级；在会话沙箱之外）"],
        "precedence": "只读 <HIVE_HOME>/ast_analyze.json；缺失即默认值；格式错误或不可读即关闭（fail closed）。",
        "kill_switch": "HIVE_AST_ANALYZE=0 对进程硬关闭；=1 仅在配置未关闭时生效。",
        "not_session_scoped": (
            "刻意不读取 <sessionDir>/.hive/ast_analyze.json：会话目录对 agent 的文件工具可写，"
            "自动发现会让 agent 改写自己的分析边界（提权）。"
        ),
        "schema": _CONFIG_SCHEMA,
        "field_notes": [
            {"param": "enabled", "zh": "是否启用整组工具；false 时 ast_analyze 不注册。"},
            {"param": "languages", "zh": "允许分析的语言；默认 astgrep 全部 13 种语法（go/python/typescript/tsx/javascript/rust/json/yaml/markdown/c/cpp/java/csharp）。"},
            {"param": "include_tests / include_ui", "zh": "是否把测试/UI 文件计入统计；测试不成为 unit。"},
            {"param": "ignore", "zh": "额外跳过的目录名/glob；默认含 .git/node_modules/vendor/dist/build 等。"},
            {"param": "max_files / max_file_bytes / max_total_bytes / max_units", "zh": "文件数/单文件/总读取/unit 上限。"},
            {"param": "max_inline_bytes / max_report_bytes", "zh": "内联预算与报告硬上限；超限落盘。"},
            {"param": "parse_timeout_ms", "zh": "单文件解析超时；超时记 parse issue。"},
            {"param": "risk_sample_lines / start_here_limit", "zh": "每风险最多样本数 / start_here 条数。"},
            {"param": "workers", "zh": "有界解析 worker 数；输出顺序仍按路径/unit id 确定。"},
        ],
    },
    "safety": {
        "controls": [
            "只读；不执行被分析代码、不启动语言服务器、不联网",
            "workspace root 为唯一安全边界；.. 与 symlink 逃逸拒绝",
            "ignore 目录、二进制/NUL、超大文件、超量文件跳过",
            "单文件 parse 超时 + context 取消 + 有界 worker",
            "报告只含 path:line 样本与有界文本，不含源码全文/秘密",
            "输出上限 + 落盘 + truncated 标记",
            "确定性：BTree/显式排序，map 不进入输出顺序",
        ],
        "residual_risks": [
            "风险是启发式，可能漏报/误报；不能替代真实测试。",
            "gotreesitter 的语法错误恢复不等于 C tree-sitter 逐字节 parity。",
            "超大仓库的解析仍可能受 CPU/内存限制；需要 max_* 与超时兜底。",
            "依赖 fit 表是人工维护的，未知依赖只能标 unknown。",
        ],
    },
    "artifacts": [
        "internal/astanalyze/{astanalyze,paths,facts,graph,analyze}.go + analyze_test.go/language_matrix_test.go/bench_test.go（新增；含 cwd/逃逸/symlink/worker/13 语言矩阵/基准）",
        "internal/tools/builtin/ast_analyze.go + ast_analyze_test.go（新增适配器）",
        "internal/cli/session_tools_ast_analyze.go + session_tools_ast_analyze_test.go（新增接线）",
        "internal/cli/session_tools_code_intel.go（注册/路由/Close 接入，或独立文件）",
        "contracts/env-vars.json + docs/env-config-contract-diff.md + docs/environment-variables.html（HIVE_AST_ANALYZE；重新生成）",
        "contracts/tool-definitions.json（go-only；用抓包 build 重新生成）",
        "docs/tool-contract-diff.md（重新生成 go-only 清单）",
        "docs/ast-analyze-design.json + docs/ast-analyze-design.html + scripts/gen_ast_analyze_design.py（本文档）",
        "docs/ast-grep-design.* / docs/config-roots.html（交叉链接）",
        "docs/alignment-differences.md（新增 A 条目：go-only AST 分析能力）",
        "docs/05-tools-and-integrations.md（built-in 分组表新增一行）",
        "docs/e2e-assets/ast-analyze/（逐语言 fixture 与 golden 报告）",
    ],
    "acceptance": [
        {"id": "A1", "test": "工具契约", "pass": "ast_analyze 的 name/description/input_schema 逐字钉住；无 action 开关；空/非法输入 typed error。"},
        {"id": "A2", "test": "语言矩阵", "pass": "Go/Python/TS/JS/TSX 通过；.h 等歧义扩展名要求显式 lang；未知语言不静默当 TS。"},
        {"id": "A3", "test": "逐文件事实", "pass": "imports/risks/platforms/loops/parse errors 的行号与计数 golden 通过。"},
        {"id": "A4", "test": "unit 模型", "pass": "文件夹/根文件 unit id 稳定；测试不入 unit；UI 标记 keep；重复运行 id 不变。"},
        {"id": "A5", "test": "import 解析", "pass": "相对/workspace/外部/标准库/tsconfig 别名/Go module/Python 包全部通过；unresolved 可见。"},
        {"id": "A6", "test": "图与 wave", "pass": "fan-in、Tarjan SCC、环、深链、包拓扑 golden 通过；输出确定。"},
        {"id": "A7", "test": "风险分类", "pass": "闭集与严重级表一致；样本去重；未知风险不能进入 JSON。"},
        {"id": "A8", "test": "评分", "pass": "readiness/priority/density/status/start_here 公式单测 + 边界 golden。"},
        {"id": "A9", "test": "报告", "pass": "detail/format 矩阵；write_to 绝对路径落点；未给 write_to 时超预算自动落盘、未超不写；saved_to 绝对路径；bytes/truncated 正确。"},
        {"id": "A10", "test": "路径安全", "pass": "..、symlink 逃逸、目录/二进制/超大/取消/超时全部 fail closed。"},
        {"id": "A11", "test": "上限", "pass": "max_files/max_file_bytes/max_total_bytes/max_units/max_report_bytes 生效。"},
        {"id": "A12", "test": "配置门控", "pass": "默认值、enabled=false、损坏 fail closed、HIVE_AST_ANALYZE=0、会话目录配置被忽略。"},
        {"id": "A13", "test": "工具面不变量", "pass": "builtin.Defs()=14、flat 39/24 不变；ast_analyze 只进 lazy 层；WithRoot/Close 正确。"},
        {"id": "A14", "test": "确定性与并发", "pass": "同输入逐字节一致；-race 通过；worker 顺序不影响输出。"},
        {"id": "A15", "test": "跨平台", "pass": "linux/darwin/windows × amd64/arm64 在 CGO_ENABLED=0 下构建；gofmt/go vet 干净。"},
        {"id": "A16", "test": "cwd / 分析根", "pass": "不同进程 cwd 结果一致；空 root 的 process-cwd 回退只允许测试；相对 path 返回 path_not_absolute；绝对路径/../symlink/write_to 逃逸拒绝；WithRoot worker 隔离；父目录 tsconfig 不被读取。"},
        {"id": "A17", "test": "输出落点", "pass": "write_to 给定/省略/超预算/未超预算四种落点正确；saved_to 绝对；父目录创建；原子覆盖；目录/相对/越界/写失败 typed error。"},
        {"id": "A18", "test": "大仓库性能", "pass": "图构建无 O(N²)；Workers=1/4 输出一致；MaxUnits/MaxFiles 截断 warning；BenchmarkAnalyzeSynthetic 有记录；10k 文件 SLA 在开放问题中定稿。"},
    ],
    "risks": [
        {
            "risk": "启发式风险误报/漏报",
            "impact": "agent 误判移植难度",
            "mitigation": "风险是建议而非证明；用真实 fixture/golden 收敛；报告显式 warnings。",
        },
        {
            "risk": "大仓库 CPU/内存压力",
            "impact": "分析超时/OOM",
            "mitigation": "max_files/max_file_bytes/max_total_bytes/max_units、parse timeout、worker 上限、context 取消。",
        },
        {
            "risk": "路径逃逸/读取越界",
            "impact": "读到 workspace 外文件",
            "mitigation": "workspace root、.. 拒绝、symlink 求值后仍在 root 内、二进制/超大跳过。",
        },
        {
            "risk": "配置被 agent 放宽",
            "impact": "分析边界提权",
            "mitigation": "只读 $HIVE_HOME/ast_analyze.json；不读会话目录；损坏 fail closed。",
        },
        {
            "risk": "输出漂移/不确定",
            "impact": "golden/回归失效",
            "mitigation": "BTree/显式排序；JSON schema 版本；同输入逐字节 golden。",
        },
        {
            "risk": "冻结工具面漂移",
            "impact": "parity/回归失败",
            "mitigation": "独立适配器、只进 lazy 层、catalogue 不变量测试、契约清单同变更重生成。",
        },
        {
            "risk": "依赖 fit 表偏差",
            "impact": "blocked/unknown 判断偏",
            "mitigation": "表是人工维护的；未知一律 unknown；报告注明 fit 来源。",
        },
    ],
    "open_questions": [
        "超大仓库目标 SLA：10k 文件的分析时间/内存上限是多少（当前 200 文件基准约 33ms/50MB alloc）？",
        "bridge 是 v1 必选，还是只作为 advisory 字段？",
        "默认启用还是像 ast_grep 一样 opt-in？",
        "detail 默认 summary 还是 units？",
        "风险样本是否只给 path:line，还是允许有界源码片段？",
        "依赖 fit 表先覆盖哪些常见库？",
        "worker 并行与确定性如何取舍（默认 4 个有界 worker）？",
        "UI/keep 语义在非前端仓库是否保留？",
        "ast_grep 的 path 是否也改为绝对路径（当前实现仍接受相对路径）？",
        "是否允许 operator 显式配置 allowed roots，以分析会话根之外的仓库？（v1 不做，避免提权）",
    ],
    "implementation_plan": [
        {"phase": "P0", "work": "冻结工具契约/风险枚举/评分公式；建逐语言 fixture 仓库与 golden。"},
        {"phase": "P1", "work": "实现 internal/astanalyze 的语言规格、逐文件事实、import 解析与 unit 划分。"},
        {"phase": "P2", "work": "实现依赖图/Tarjan wave/环、风险分类、readiness/priority/start_here。"},
        {"phase": "P3", "work": "实现 JSON/Markdown 报告、有界输出/落盘、配置门控、适配器与 catalogue 接线。"},
        {"phase": "P4", "work": "跑六目标 CGO_ENABLED=0、-race、golden、docs/contracts；A1–A18 全过才转实现。"},
    ],
}

CSS = """
:root{--bg:#0b0f14;--panel:#121923;--panel2:#0f1620;--line:#233041;--text:#e6edf3;--muted:#8b9bb0;--accent:#f0b429;--accent2:#58a6ff;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--code:#0a0e13}
*{box-sizing:border-box} body{margin:0;background:linear-gradient(180deg,#0b0f14,#0d131b 55%,#0b0f14);color:var(--text);font:14.5px/1.65 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--accent2);text-decoration:none} a:hover{text-decoration:underline}
.wrap{max-width:1320px;margin:0 auto;padding:44px 26px 80px}
header{border-bottom:1px solid var(--line);padding-bottom:24px;margin-bottom:28px}
.eyebrow{color:var(--accent);font-size:12px;letter-spacing:.18em;text-transform:uppercase;font-weight:700}
h1{font-size:30px;line-height:1.25;margin:10px 0;letter-spacing:-.02em} h1 span{color:var(--accent)}
.lede{color:var(--muted);max-width:1060px;font-size:15px} .meta{display:flex;gap:18px;flex-wrap:wrap;margin-top:15px;color:var(--muted);font-size:12.5px} .meta b{color:var(--text)}
section{margin:38px 0} h2{font-size:22px;margin:0 0 8px} h2 .num{color:var(--accent);font-size:14px;margin-right:8px} h3{font-size:15.5px;margin:16px 0 8px;color:#cfdae6}
p{color:#c6d2df;margin:8px 0} .sub{color:var(--muted);font-size:12.5px}
.grid{display:grid;gap:12px} .g2{grid-template-columns:repeat(2,minmax(0,1fr))} .g3{grid-template-columns:repeat(3,minmax(0,1fr))} .grid>*{min-width:0}
@media(max-width:1000px){.g3{grid-template-columns:repeat(2,minmax(0,1fr))}} @media(max-width:560px){.g3,.g2{grid-template-columns:1fr}.wrap{padding:30px 16px 60px}h1{font-size:24px}}
.card{background:var(--panel);border:1px solid var(--line);border-radius:13px;padding:15px;min-width:0} .card .k{font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted);font-weight:700} .card .v{font-size:20px;font-weight:800;margin-top:5px} .card .d{color:var(--muted);font-size:12px;margin-top:5px}
.tablewrap{overflow-x:auto;border:1px solid var(--line);border-radius:12px;margin:14px 0;background:var(--panel2);max-width:100%}
table{width:100%;border-collapse:collapse;font-size:13px;table-layout:fixed} th,td{border-bottom:1px solid var(--line);padding:9px 10px;vertical-align:top;text-align:left;overflow-wrap:anywhere;word-break:break-word} th{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.07em;font-weight:700;background:#0d141d;position:sticky;top:0;z-index:1} tr:hover td{background:#121a25} tr:last-child td{border-bottom:0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;background:var(--code);border:1px solid #1e2937;border-radius:5px;padding:1px 4px;color:#d7e3f0;overflow-wrap:anywhere}
pre{background:var(--code);border:1px solid var(--line);border-radius:10px;padding:14px;overflow-x:auto;font:12px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:#cfe0f2;white-space:pre-wrap;word-break:break-word}
.pill{display:inline-flex;align-items:center;font-size:11px;border-radius:999px;padding:2px 8px;border:1px solid var(--line);color:var(--muted);white-space:normal;margin:1px 2px 1px 0} .pill.ok{color:var(--ok);border-color:#1d3b26;background:#0f1a13} .pill.warn{color:var(--warn);border-color:#3d3013;background:#1a150a} .pill.muted{color:var(--muted);border-color:var(--line);background:#0f1620}
.callout{border-left:3px solid var(--accent);background:#161207;padding:11px 14px;border-radius:0 9px 9px 0;margin:14px 0;color:#e8d9b4} .callout.info{border-color:var(--accent2);background:#0d1622;color:#cfe3ff} .callout.bad{border-color:var(--bad);background:#1a0f0f;color:#f2c7c7}
ul,ol{margin:7px 0 7px 20px;padding:0} li{margin:4px 0;color:#c6d2df} footer{margin-top:44px;border-top:1px solid var(--line);padding-top:16px;color:var(--muted);font-size:12px}
@media(max-width:560px){table{font-size:12px}th,td{padding:7px 6px}}
"""


def param_rows(notes):
    return "".join(
        f"<tr><td><code>{ESC(n['param'])}</code></td><td>{ESC(n['zh'])}</td></tr>"
        for n in notes
    )


def render_html(data: dict) -> str:
    req_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(r['id'])}</code></td>"
        f"<td>{ESC(r['priority'])}</td>"
        f"<td>{ESC(r['statement'])}</td>"
        f"<td>{ESC(r['acceptance'])}</td>"
        f"<td>{ESC(', '.join(r['traces']))}</td>"
        "</tr>"
        for r in data["requirements"]
    )
    option_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(o['id'])}</code></td>"
        f"<td>{ESC(o['name'])}</td>"
        f"<td><span class=\"pill {'ok' if o['verdict'] == 'chosen' else 'warn' if o['verdict'] == 'deferred' else 'muted'}\">{ESC(o['verdict'])}</span></td>"
        f"<td>{ESC(o['reason'])}</td>"
        "</tr>"
        for o in data["options"]
    )
    design_cards = "".join(
        f"<div class=\"card\"><div class=\"k\">{ESC(key)}</div><h3>{ESC(value['title'])}</h3><p>{ESC(value['text'])}</p></div>"
        for key, value in data["design"].items()
    )
    risk_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(r['risk'])}</code></td>"
        f"<td>{ESC(r['severity'])}</td>"
        f"<td>{ESC(r['go'])}</td>"
        f"<td>{ESC(r['python'])}</td>"
        f"<td>{ESC(r['ts'])}</td>"
        f"<td>{ESC(r['guidance'])}</td>"
        "</tr>"
        for r in data["risk_taxonomy"]
    )
    acc_rows = "".join(
        "<tr>"
        f"<td><code>{ESC(a['id'])}</code></td>"
        f"<td>{ESC(a['test'])}</td>"
        f"<td>{ESC(a['pass'])}</td>"
        "</tr>"
        for a in data["acceptance"]
    )
    artifact_items = "".join(f"<li><code>{ESC(a)}</code></li>" for a in data["artifacts"])
    plan_items = "".join(
        f"<li><b>{ESC(x['phase'])}</b> — {ESC(x['work'])}</li>" for x in data["implementation_plan"]
    )
    source_items = "".join(
        f"<li><a href=\"{ESC(s['url'])}\">{ESC(s['label'])}</a> — {ESC(s['use'])}</li>"
        for s in data["source_refs"]
    )
    safety_items = "".join(f"<li>{ESC(c)}</li>" for c in data["safety"]["controls"])
    residual_items = "".join(f"<li>{ESC(c)}</li>" for c in data["safety"]["residual_risks"])
    error_pills = " ".join(f"<span class=\"pill muted\">{ESC(k)}</span>" for k in data["tool_contract"]["error_kinds"])
    input_schema = json.dumps(data["tool_contract"]["input_schema"], ensure_ascii=False, indent=2)
    result_schema = json.dumps(data["tool_contract"]["result_envelope"], ensure_ascii=False, indent=2)
    config_schema = json.dumps(data["config"]["schema"], ensure_ascii=False, indent=2)
    scoring = data["scoring"]

    return f"""<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>ast_analyze · AST 仓库分析需求—设计链</title>
<style>{CSS}</style></head><body><div class="wrap">
<header><div class="eyebrow">GO_HIVE · AST_ANALYZE · 需求 → 设计</div>
<h1>ast_analyze <span>AST 仓库分析</span></h1>
<p class="lede">在 go_hive 中新增只读的 <code>ast_analyze</code> 工具：复用 <code>internal/astgrep</code> 的 gotreesitter 语言注册表/解析器/路径安全，把仓库拆成 unit、构建 import 图、计算 wave/环、风险热点、readiness/priority/start_here，输出有界、确定性的 JSON/Markdown 报告。参考 rust-anything 的 analyze 模型；本页冻结需求、契约、风险分类、评分公式与验收链，设计状态不冒充实现状态。</p>
<div class="meta"><span>状态：<b>{ESC(data['status'])}</b></span><span>决策：<b>{ESC(data['decision'])}</b></span><span>目标：<b>CGO_ENABLED=0 · 六平台</b></span></div>
</header>

<section id="problem"><h2><span class="num">00</span>问题与价值</h2>
<div class="grid g2"><div class="card"><div class="k">问题</div><div class="d">{ESC(data['problem']['statement'])}</div></div><div class="card"><div class="k">为什么不是当前 parity 项</div><div class="d">{ESC(data['problem']['why_not_now'])}</div></div></div>
<h3>用户价值</h3><ul>{list_items(data['problem']['user_value'])}</ul>
<div class="grid g3">
<div class="card"><div class="k">工具</div><div class="v">1</div><div class="d">ast_analyze</div></div>
<div class="card"><div class="k">新依赖</div><div class="v">0</div><div class="d">复用 internal/astgrep + gotreesitter</div></div>
<div class="card"><div class="k">冻结面</div><div class="v">14 / 39 / 24</div><div class="d">builtin / flat / 首轮 不变</div></div>
</div></section>

<section id="requirements"><h2><span class="num">01</span>需求链</h2>
<p class="sub">R → D → A 是同一张链：每个需求必须落到一个设计项和至少一个验收项。</p>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:7%">优先级</th><th style="width:38%">需求</th><th style="width:38%">验收</th><th style="width:10%">链路</th></tr></thead><tbody>{req_rows}</tbody></table></div>
<h3>非目标</h3><ul>{list_items(data['non_goals'])}</ul></section>

<section id="options"><h2><span class="num">02</span>决策链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:7%">ID</th><th style="width:26%">方案</th><th style="width:10%">结论</th><th style="width:57%">理由</th></tr></thead><tbody>{option_rows}</tbody></table></div>
<div class="callout info"><b>选择：</b>{ESC(data['decision'])} <b>备选：</b>如果无法在纯 Go 下达到可接受的解析/性能，退回“窄语言子集 + 外部 rust-anything 只读结果导入”，而不是改 cgo。</div></section>

<section id="design"><h2><span class="num">03</span>设计链</h2><div class="grid g2">{design_cards}</div>
<div class="callout info"><b>Package 边界：</b><code>{ESC(data['package_layout']['core'])}</code>（核心）· <code>{ESC(data['package_layout']['reuse'])}</code>（复用）· <code>{ESC(data['package_layout']['adapter'])}</code>（schema/适配）· <code>{ESC(data['package_layout']['wiring'])}</code>（门控/路由）。{ESC(data['package_layout']['rule'])} <b>维护：</b>{ESC(data['package_layout']['maintenance'])}</div>
<h3>处理流水线</h3><ol>{list_items(data['pipeline'])}</ol></section>

<section id="contract"><h2><span class="num">04</span>工具契约</h2>
<p class="sub">{ESC(data['tool_contract']['schema_note'])}</p>
<h3>输入 schema</h3><pre>{ESC(input_schema)}</pre>
<h3>结果信封</h3><pre>{ESC(result_schema)}</pre>
<h3>error_kind 闭集</h3><p>{error_pills}</p></section>

<section id="risks"><h2><span class="num">05</span>风险分类</h2>
<p class="sub">闭集 Risk + Severity（high 6 / medium 3 / low 1）；新增风险必须同时改枚举、表、测试与 golden。</p>
<div class="tablewrap"><table><thead><tr><th style="width:16%">Risk</th><th style="width:8%">严重级</th><th style="width:18%">Go</th><th style="width:18%">Python</th><th style="width:18%">TS/JS</th><th style="width:22%">建议</th></tr></thead><tbody>{risk_rows}</tbody></table></div></section>

<section id="scoring"><h2><span class="num">06</span>评分与状态</h2>
<div class="tablewrap"><table><thead><tr><th style="width:18%">维度</th><th style="width:82%">公式/规则</th></tr></thead><tbody>
<tr><td>Readiness</td><td>{ESC(scoring['readiness'])}</td></tr>
<tr><td>Status</td><td>{ESC(scoring['status'])}</td></tr>
<tr><td>Density</td><td>{ESC(scoring['density'])}</td></tr>
<tr><td>Priority</td><td>{ESC(scoring['priority'])}</td></tr>
<tr><td>Start here</td><td>{ESC(scoring['start_here'])}</td></tr>
<tr><td>Bridge</td><td>{ESC(scoring['bridge'])}</td></tr>
</tbody></table></div></section>

<section id="config"><h2><span class="num">07</span>配置与安全</h2>
<h3>配置</h3>
<ul>{list_items(data['config']['paths'])}</ul>
<p class="sub">{ESC(data['config']['precedence'])} {ESC(data['config']['kill_switch'])}</p>
<div class="callout bad"><b>不做会话级自动发现：</b>{ESC(data['config']['not_session_scoped'])}</div>
<pre>{ESC(config_schema)}</pre>
<div class="tablewrap"><table><thead><tr><th style="width:34%">字段</th><th style="width:66%">中文说明</th></tr></thead><tbody>{param_rows(data['config']['field_notes'])}</tbody></table></div>
<h3>安全控制</h3><ul>{safety_items}</ul>
<h3>残余风险</h3><ul>{residual_items}</ul></section>

<section id="artifacts"><h2><span class="num">08</span>落地清单</h2><p class="sub">实现 lane 必须同时改完这些文件，否则冻结面或契约会漂移。</p><ul>{artifact_items}</ul></section>

<section id="acceptance"><h2><span class="num">09</span>验收链</h2>
<div class="tablewrap"><table><thead><tr><th style="width:8%">ID</th><th style="width:26%">测试</th><th style="width:66%">通过条件</th></tr></thead><tbody>{acc_rows}</tbody></table></div></section>

<section id="plan"><h2><span class="num">10</span>实施阶段与开放问题</h2><ol>{plan_items}</ol>
<h3>开放问题</h3><ul>{list_items(data['open_questions'])}</ul></section>

<section id="sources"><h2><span class="num">11</span>来源与风险</h2>
<div class="tablewrap"><table><thead><tr><th style="width:30%">风险</th><th style="width:26%">影响</th><th style="width:44%">缓解</th></tr></thead><tbody>{''.join(f'<tr><td>{ESC(r["risk"])}</td><td>{ESC(r["impact"])}</td><td>{ESC(r["mitigation"])}</td></tr>' for r in data['risks'])}</tbody></table></div>
<h3>来源</h3><ul>{source_items}</ul></section>

<footer>ast_analyze 设计 · 需求—设计—验收链 · 由 <code>go_hive/scripts/gen_ast_analyze_design.py</code> 生成；<code>--check</code> 防漂移 · 设计状态不冒充实现状态</footer>
</div></body></html>
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    json_text = json.dumps(_DATA, ensure_ascii=False, indent=2) + "\n"
    html_text = render_html(_DATA)
    if args.check:
        failures = []
        for path, expected in ((OUT_JSON, json_text), (OUT_HTML, html_text)):
            if not path.exists() or path.read_text(encoding="utf-8") != expected:
                failures.append(str(path))
        if failures:
            print("ast-analyze-design: DRIFT: " + ", ".join(failures))
            return 1
        print("ast-analyze-design: OK")
        return 0
    OUT_JSON.write_text(json_text, encoding="utf-8")
    OUT_HTML.write_text(html_text, encoding="utf-8")
    print(f"ast-analyze-design: wrote {OUT_HTML} and {OUT_JSON}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
