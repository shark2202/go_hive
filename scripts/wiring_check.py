#!/usr/bin/env python3
"""接线点门禁：关键装配代码是否仍然存在。

为什么需要它：Go 包能编译、单测能过，并不代表子系统真的接进了主流程——
一个 installer 不再被引用、一次 recall 注入被删掉、会话日志少了一半，
都可能是「编译通过 + 测试通过」的静默退化。本脚本只做静态文本检查，
不联网、不依赖第三方包，可在离线环境里跑。

检查方法：先用一个 Go 词法感知的小扫描器去掉注释（注释里出现函数名不算接线），
再对每条要求做正则计数。任何一项命中次数不足即视为缺失，退出码 1。

用法:
    uv run --no-project python go_hive/scripts/wiring_check.py
    uv run --no-project python go_hive/scripts/wiring_check.py --root go_hive/go-hive
    uv run --no-project python go_hive/scripts/wiring_check.py --list
    uv run --no-project python go_hive/scripts/wiring_check.py --self-test

退出码: 0 = 全部接线点存在；1 = 有缺失；2 = 用法或根目录错误。
"""

from __future__ import annotations

import argparse
import re
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Requirement:
    """一条接线要求：正则 + 最少命中次数。"""

    name: str
    pattern: str
    minimum: int = 1


@dataclass(frozen=True)
class WiringPoint:
    """一个接线文件及其必须保留的引用。"""

    path: str  # 相对模块根目录（go.mod 所在目录）
    description: str
    requirements: tuple[Requirement, ...]


WIRING: tuple[WiringPoint, ...] = (
    WiringPoint(
        path="internal/server/chat_runtime.go",
        description="Chat 运行时：每轮 recall 注入 + 会话事件日志",
        requirements=(
            Requirement("injectRecall 定义", r"func \(r \*ChatRuntime\) injectRecall\("),
            Requirement("injectRecall 调用点", r"\.injectRecall\("),
            Requirement("会话日志打开 SetSessionLog", r"SetSessionLog\("),
            Requirement("会话日志关闭 CloseSessionLog", r"CloseSessionLog\("),
        ),
    ),
    WiringPoint(
        path="internal/cli/loader.go",
        description="会话装配：ChatRuntimeDeps 的事件总线与会话身份",
        requirements=(
            Requirement("NewChatRuntime 调用", r"NewChatRuntime\("),
            Requirement("Bus 字段", r"\bBus:\s*\S"),
            Requirement("SessionID 字段", r"\bSessionID:\s*\S"),
            Requirement("QueenName 字段", r"\bQueenName:\s*\S"),
            Requirement("HomeRoot 字段", r"\bHomeRoot:\s*\S"),
        ),
    ),
    WiringPoint(
        path="internal/cli/session_tools.go",
        description="会话工具目录：批量 installer 必须被引用",
        requirements=(
            Requirement("registry 安装入口", r"\.Install\(registry\)"),
            Requirement("批量 installer 表", r"sessionIntegrationInstallers"),
            Requirement("Install* 引用", r"\bInstall[A-Z][A-Za-z0-9]*\b", minimum=2),
        ),
    ),
)


def strip_comments(src: str) -> str:
    """去掉 // 与 /* */ 注释，保留字符串字面量。

    门禁必须能失败：如果注释里的函数名也算数，删掉真实调用后只留一句注释
    就能骗过检查。这里按 Go 字符串/反引号/转义规则逐字符扫描。
    """
    out: list[str] = []
    i, n = 0, len(src)
    in_line = in_block = False
    quote = ""
    while i < n:
        ch = src[i]
        nxt = src[i + 1] if i + 1 < n else ""
        if in_line:
            if ch == "\n":
                in_line = False
                out.append(ch)
            i += 1
            continue
        if in_block:
            if ch == "*" and nxt == "/":
                in_block = False
                i += 2
                continue
            if ch == "\n":
                out.append(ch)
            i += 1
            continue
        if quote:
            out.append(ch)
            if quote != "`" and ch == "\\" and i + 1 < n:
                out.append(src[i + 1])
                i += 2
                continue
            if ch == quote:
                quote = ""
            i += 1
            continue
        if ch in ('"', "'", "`"):
            quote = ch
            out.append(ch)
            i += 1
            continue
        if ch == "/" and nxt == "/":
            in_line = True
            i += 2
            continue
        if ch == "/" and nxt == "*":
            in_block = True
            i += 2
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def resolve_target(root: Path, rel: str) -> tuple[Path | None, str]:
    """定位接线文件；被移动（同样的接线换了个包）时按文件名回退并给出提示。

    返回 (路径, 提示)。路径为 None 表示找不到或存在歧义。
    """
    direct = root / rel
    if direct.is_file():
        return direct, ""
    matches = sorted(p for p in (root / "internal").rglob(Path(rel).name) if p.is_file())
    if len(matches) == 1:
        moved = matches[0].relative_to(root).as_posix()
        return matches[0], f"注意：{rel} 已移动到 {moved}（门禁按文件名跟踪）"
    if len(matches) > 1:
        names = ", ".join(m.relative_to(root).as_posix() for m in matches)
        return None, f"{rel} 不存在，且同名文件有多个（{names}），无法确定接线点"
    return None, f"{rel} 不存在"


def check_point(root: Path, point: WiringPoint) -> list[str]:
    """检查一个接线文件，返回缺失项描述（空列表 = 通过）。"""
    target, note = resolve_target(root, point.path)
    if target is None:
        return [f"{point.path}: {note} —— {point.description}"]
    if note:
        print(f"  {note}", file=sys.stderr)
    try:
        text = strip_comments(target.read_text(encoding="utf-8", errors="replace"))
    except OSError as exc:
        return [f"{point.path}: 读取失败（{exc}）"]

    rel = target.relative_to(root).as_posix()
    problems: list[str] = []
    for req in point.requirements:
        hits = len(re.findall(req.pattern, text, re.MULTILINE))
        if hits < req.minimum:
            problems.append(
                f"{rel}: 缺少「{req.name}」 —— 命中 {hits} 次，需要 ≥{req.minimum}（正则 /{req.pattern}/）"
            )
    return problems


def run(root: Path) -> list[str]:
    problems: list[str] = []
    for point in WIRING:
        problems.extend(check_point(root, point))
    return problems


def requirement_count() -> int:
    return sum(len(p.requirements) for p in WIRING)


def self_test() -> int:
    """证明门禁会失败：空文件全报缺失；只在注释里出现的接线也不算数。"""
    failures: list[str] = []
    with tempfile.TemporaryDirectory(prefix="wiring-check-") as tmp:
        root = Path(tmp)
        (root / "go.mod").write_text("module example.com/tmp\n", encoding="utf-8")
        for point in WIRING:
            target = root / point.path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("package stub\n", encoding="utf-8")
        empty_problems = run(root)
        if len(empty_problems) < requirement_count():
            failures.append(
                f"空文件场景只报出 {len(empty_problems)} 项，期望 {requirement_count()} 项"
            )

        # 注释里写满接线文本，代码里什么都没有：必须仍然全部报缺失。
        for point in WIRING:
            body = ["package stub", ""]
            for req in point.requirements:
                body.append(f"// {req.pattern}")
            (root / point.path).write_text("\n".join(body) + "\n", encoding="utf-8")
        comment_problems = run(root)
        if len(comment_problems) < requirement_count():
            failures.append(
                f"注释场景只报出 {len(comment_problems)} 项，期望 {requirement_count()} 项（注释被当成接线）"
            )

        # 正常场景：把真实仓库片段写回临时树，必须 0 缺失。
        for point in WIRING:
            (root / point.path).write_text("package stub\n", encoding="utf-8")
        pick = WIRING[0]
        (root / pick.path).write_text(
            "package stub\n\n"
            "func (r *ChatRuntime) injectRecall(query, stamp string) {}\n\n"
            "func f(r *ChatRuntime) {\n"
            '\tif err := r.bus.SetSessionLog("x", 0); err != nil {\n\t\treturn\n\t}\n'
            "\tr.injectRecall(\"q\", \"s\")\n\tr.bus.CloseSessionLog()\n}\n",
            encoding="utf-8",
        )
        first_point_missing = check_point(root, pick)
        if first_point_missing:
            failures.append(f"正常片段被误报: {first_point_missing}")

    if failures:
        print("wiring_check self-test: FAIL", file=sys.stderr)
        for item in failures:
            print(f"  {item}", file=sys.stderr)
        return 1
    print(
        f"wiring_check self-test: OK —— {len(WIRING)} 个接线文件、{requirement_count()} 项要求，缺失会被报出"
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="检查 go_hive 的关键接线点是否仍在（离线、仅标准库）。",
    )
    parser.add_argument(
        "--root",
        default=None,
        help="go-hive 模块根目录（含 go.mod）；默认取脚本上一级的 go-hive/",
    )
    parser.add_argument("--list", action="store_true", help="列出全部接线点后退出")
    parser.add_argument("--self-test", action="store_true", help="用临时合成文件验证缺失会被报出")
    args = parser.parse_args(argv)

    if args.list:
        for point in WIRING:
            print(f"{point.path} —— {point.description}")
            for req in point.requirements:
                print(f"    - {req.name} (/…/, ≥{req.minimum})")
        return 0

    if args.self_test:
        return self_test()

    root = Path(args.root).resolve() if args.root else Path(__file__).resolve().parent.parent / "go-hive"
    if not (root / "go.mod").is_file():
        print(f"wiring_check: 根目录无效（找不到 go.mod）: {root}", file=sys.stderr)
        return 2

    problems = run(root)
    if problems:
        print(f"wiring_check: FAIL —— {len(problems)} 项接线缺失（root={root}）")
        for item in problems:
            print(f"  {item}")
        return 1
    print(
        f"wiring_check: OK —— {len(WIRING)} 个接线文件、{requirement_count()} 项要求全部命中（root={root}）"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
