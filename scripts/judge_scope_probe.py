#!/usr/bin/env python3
"""judge 面可达性探针：静态核对 docs/judge-parity.md 的「有宿主 / 无宿主 / 无调用方」断言。

为什么需要：judge 面的登记结论里有一半是「不存在」——Go 没有 graph 宿主、没有
output-key accumulator、SubagentJudge 在参考侧没有生产调用方。这类结论最容易
过期：有人接线之后，登记册仍然写着「范围外」，登记就变成了挡箭牌。本脚本把这些
断言变成可复核的计数（去注释：注释里提到符号不算），任何一条与登记不符即退出 1。

用法:
    uv run --no-project python go_hive/scripts/judge_scope_probe.py             # 静态核对
    uv run --no-project python go_hive/scripts/judge_scope_probe.py --list
    uv run --no-project python go_hive/scripts/judge_scope_probe.py --verbose   # 打印命中位置
    uv run --no-project python go_hive/scripts/judge_scope_probe.py --dynamic   # 另跑 Python 侧真实 AgentLoop

退出码: 0 = 全部与登记一致；1 = 有断言不成立；2 = 根目录/环境无效。
"""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path

SCRIPT = Path(__file__).resolve()
GO_HIVE = SCRIPT.parent.parent                     # go_hive/
REPO_ROOT = GO_HIVE.parent                         # 仓库根
sys.path.insert(0, str(SCRIPT.parent))
from wiring_check import strip_comments  # noqa: E402  (Go 词法去注释，复用同一实现)


def strip_python_comments(src: str) -> str:
    """去掉 # 注释与三引号字符串体以外的注释（字符串里出现符号不算调用）。

    只处理注释：字符串字面量保留，避免把 prompt 文案里的关键词当成调用。
    """
    out: list[str] = []
    i, n = 0, len(src)
    quote = ""
    triple = False
    while i < n:
        ch = src[i]
        if quote:
            if triple and src.startswith(quote * 3, i):
                out.append(quote * 3)
                i += 3
                quote = ""
                triple = False
                continue
            out.append(ch)
            if not triple and ch == "\\" and i + 1 < n:
                out.append(src[i + 1])
                i += 2
                continue
            if not triple and ch == quote:
                quote = ""
            i += 1
            continue
        if ch in ("'", '"'):
            if src.startswith(ch * 3, i):
                quote = ch
                triple = True
                out.append(ch * 3)
                i += 3
                continue
            quote = ch
            out.append(ch)
            i += 1
            continue
        if ch == "#":
            while i < n and src[i] != "\n":
                i += 1
            continue
        out.append(ch)
        i += 1
    return "".join(out)


def python_files(root: Path, *, include_tests: bool) -> list[Path]:
    files = []
    for path in sorted((root / "core").rglob("*.py")):
        rel = path.relative_to(root).as_posix()
        if "/tests/" in rel or rel.endswith("_test.py"):
            if not include_tests:
                continue
        files.append(path)
    return files


def go_files(root: Path, *, include_tests: bool = False) -> list[Path]:
    module = root / "go_hive" / "go-hive" / "internal"
    files = []
    for path in sorted(module.rglob("*.go")):
        if path.name.endswith("_test.go") and not include_tests:
            continue
        files.append(path)
    return files


def count_in(files: list[Path], pattern: str, *, strip) -> tuple[int, list[str]]:
    rx = re.compile(pattern, re.MULTILINE)
    total = 0
    hits: list[str] = []
    for path in files:
        text = strip(path.read_text(encoding="utf-8", errors="replace"))
        for match in rx.finditer(text):
            total += 1
            line = text[: match.start()].count("\n") + 1
            hits.append(f"{path}:{line}")
    return total, hits


@dataclass(frozen=True)
class Claim:
    id: str
    statement: str
    check: object  # Callable[[Path], list[str]]


def _scan_core(root: Path, pattern: str, *, include_tests: bool = False) -> tuple[int, list[str]]:
    return count_in(python_files(root, include_tests=include_tests), pattern, strip=strip_python_comments)


def _scan_go(root: Path, pattern: str, *, include_tests: bool = False) -> tuple[int, list[str]]:
    return count_in(go_files(root, include_tests=include_tests), pattern, strip=strip_comments)


def claim_py_subagent_no_production_caller(root: Path) -> list[str]:
    total, hits = _scan_core(root, r"\bSubagentJudge\(")
    if total:
        return [f"SubagentJudge 出现 {total} 次生产侧构造（登记为 0）：{hits[:5]}"]
    return []


def claim_py_accumulator_never_written(root: Path) -> list[str]:
    total, hits = _scan_core(root, r"\baccumulator\.set\(")
    if total:
        return [f"OutputAccumulator.set 出现 {total} 次调用（登记为 0 个写入点）：{hits[:5]}"]
    return []


def claim_py_set_output_refused(root: Path) -> list[str]:
    path = root / "core/framework/agent_loop/agent_loop.py"
    text = path.read_text(encoding="utf-8", errors="replace")
    if "set_output is no longer available" not in text:
        return ["agent_loop.py 不再拒绝 set_output：Level 2 隐式门的输入可能重新可达，登记需复核"]
    return []


def claim_py_outputs_set_never_filled(root: Path) -> list[str]:
    total, hits = _scan_core(root, r"\boutputs_set_this_turn\.append")
    if total:
        return [f"outputs_set_this_turn 有 {total} 个写入点（登记为 0）：{hits[:5]}"]
    return []


def claim_py_2b_single_call_site(root: Path) -> list[str]:
    total, hits = _scan_core(root, r"await evaluate_phase_completion\(")
    if total != 1:
        return [f"evaluate_phase_completion 调用点 {total} 个（登记为 1）：{hits[:5]}"]
    if "judge_pipeline.py" not in hits[0]:
        return [f"唯一调用点不在 judge_pipeline.py：{hits[0]}"]
    return []


def claim_py_no_custom_judge_injection(root: Path) -> list[str]:
    """core/framework 里所有 AgentLoop(...) 构造都不得注入非 None 的 judge。

    Uses ast, not a regex: ``skip_judge=True`` must not read as ``judge=True``,
    and a docstring that spells an AgentLoop call must not count as a call site.
    """
    import ast

    problems: list[str] = []
    for path in python_files(root, include_tests=False):
        if "/tests/" in path.as_posix():
            continue
        try:
            tree = ast.parse(path.read_text(encoding="utf-8", errors="replace"))
        except SyntaxError as exc:  # pragma: no cover - only on a broken tree
            problems.append(f"{path}: 语法错误，无法核对 judge 注入（{exc}）")
            continue
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            func = node.func
            name = func.id if isinstance(func, ast.Name) else getattr(func, "attr", "")
            if name != "AgentLoop":
                continue
            for keyword in node.keywords:
                if keyword.arg != "judge":
                    continue
                value = keyword.value
                if isinstance(value, ast.Constant) and value.value is None:
                    continue
                problems.append(f"{path}:{node.lineno} 注入 judge=<非 None>")
    return problems


def claim_py_criteria_config_reachable(root: Path) -> list[str]:
    problems: list[str] = []
    loader = (root / "core/framework/loader/agent_loader.py").read_text(encoding="utf-8", errors="replace")
    if '"success_criteria": nc.success_criteria' not in loader:
        problems.append("agent_loader.py 不再把 node success_criteria 传进 NodeSpec：Level 2b 触发条件不可配置")
    node = (root / "core/framework/orchestrator/node.py").read_text(encoding="utf-8", errors="replace")
    if "success_criteria: str | None = Field(" not in node:
        problems.append("orchestrator/node.py 的 NodeSpec 没有 success_criteria 字段")
    return problems


def claim_py_prompt_matches_golden(root: Path) -> list[str]:
    from judge_prompt_golden import extract_python_template

    py = (root / "core/framework/testing/llm_judge.py").read_text(encoding="utf-8", errors="replace")
    golden_path = root / "go_hive/go-hive/internal/llmjudge/testdata/prompt-parity/judge.prompt.txt"
    if not golden_path.is_file():
        return [f"golden 缺失：{golden_path}"]
    template = extract_python_template(py)
    golden = golden_path.read_text(encoding="utf-8")
    if template != golden:
        return ["Python judge prompt 模板与 golden 不一致"]
    return []


def claim_go_no_graph_host(root: Path) -> list[str]:
    problems: list[str] = []
    imports, hits = _scan_go(root, r"hive-go/internal/orchestrator")
    if imports:
        problems.append(f"有 {imports} 处 import internal/orchestrator：{hits[:5]}（登记为无人引用、无 graph 运行时）")
    types, thits = _scan_go(root, r"type\s+(GraphSpec|GraphRun|EntryNode|EdgeSpec)\b")
    if types:
        problems.append(f"出现 graph 运行时类型 {types} 处：{thits[:5]}")
    return problems


def claim_go_no_output_accumulator(root: Path) -> list[str]:
    problems: list[str] = []
    acc, hits = _scan_go(root, r"type\s+OutputAccumulator\b")
    if acc:
        problems.append(f"Go 出现 OutputAccumulator 类型：{hits[:5]}（登记为无 accumulator 宿主）")
    gate, ghits = _scan_go(root, r"MissingOutputKeys|missing_output_keys|Required outputs not yet produced")
    if gate:
        problems.append(f"Go 出现 output-key 缺失门 {gate} 处：{ghits[:5]}")
    return problems


def claim_go_set_output_unwired(root: Path) -> list[str]:
    problems: list[str] = []
    for symbol in ("BuildSetOutputTool", "HandleSetOutput", "SyntheticPhaseToolBuilders"):
        total, hits = _scan_go(root, rf"\b{symbol}\b")
        if total != 1:
            problems.append(f"{symbol} 出现 {total} 次（登记为仅定义、无调用方）：{hits[:5]}")
    return problems


def claim_go_judge_wired(root: Path) -> list[str]:
    problems: list[str] = []
    for pattern, minimum, what in (
        (r"\.ConfigureJudge\(", 3, "ConfigureJudge 调用点"),
        (r"\.shouldJudge\(", 2, "shouldJudge 调用点"),
        (r"\.evaluateJudge\(", 2, "evaluateJudge 调用点"),
    ):
        total, hits = _scan_go(root, pattern)
        if total < minimum:
            problems.append(f"{what} 只剩 {total} 处（登记 ≥{minimum}）：{hits}")
    return problems


def claim_go_judge_gated(root: Path) -> list[str]:
    text = strip_comments((root / "go_hive/go-hive/internal/agentloop/judge.go").read_text(encoding="utf-8"))
    problems: list[str] = []
    if "spec.SkipJudge || !hasSuccessCriteria(spec)" not in text:
        problems.append("ConfigureJudge 不再按 skip_judge/无 criteria 关掉 judge")
    if "agentCtx.AgentSpec.SkipJudge || !hasSuccessCriteria(agentCtx.AgentSpec)" not in text:
        problems.append("shouldJudge 不再按 skip_judge/无 criteria 关掉判定")
    return problems


def claim_go_implicit_verdict_event(root: Path) -> list[str]:
    text = strip_comments((root / "go_hive/go-hive/internal/server/chat_implicit_judge.go").read_text(encoding="utf-8"))
    problems: list[str] = []
    if "EmitJudgeVerdict(" not in text:
        problems.append("chat_implicit_judge.go 不再发 judge_verdict")
    if '"implicit"' not in text:
        problems.append('chat_implicit_judge.go 不再用 judge_type="implicit"')
    return problems


def claim_go_iteration_feedback_is_grace(root: Path) -> list[str]:
    problems: list[str] = []
    loop = strip_comments((root / "go_hive/go-hive/internal/agentloop/loop.go").read_text(encoding="utf-8"))
    if "graceReminderBody" not in loop or "announceGrace(" not in loop:
        problems.append("loop.go 的迭代预算收尾（grace）被改动：迭代余量反馈位置需重新审计")
    urgency, hits = _scan_go(root, r"iterations left|iterations remaining")
    if urgency:
        problems.append(f"Go 出现迭代余量 urgency 文案 {urgency} 处：{hits[:5]}（登记为不移植该机制）")
    return problems


def claim_go_prompt_template_is_golden(root: Path) -> list[str]:
    golden_path = root / "go_hive/go-hive/internal/llmjudge/testdata/prompt-parity/judge.prompt.txt"
    go_path = root / "go_hive/go-hive/internal/llmjudge/judge.go"
    if not golden_path.is_file() or not go_path.is_file():
        return ["提示：judge.go 或 golden 缺失"]
    golden = golden_path.read_text(encoding="utf-8")
    go = strip_comments(go_path.read_text(encoding="utf-8"))
    fragments = [f for f in golden.split("%s") if f.strip()]
    if len(golden.split("%s")) - 1 != 4:
        return ["golden 的 %s 占位符不是 4 个"]
    missing = [f for f in fragments if f not in go]
    if missing:
        return [f"judge.go 模板缺少 golden 片段：{missing[:2]!r}"]
    return []


CLAIMS: tuple[Claim, ...] = (
    Claim("py-subagent-no-production-caller", "SubagentJudge 在 core 生产代码里 0 个构造调用（仅测试）", claim_py_subagent_no_production_caller),
    Claim("py-accumulator-never-written", "OutputAccumulator.set 0 个调用点（Level 2 隐式门输入恒空）", claim_py_accumulator_never_written),
    Claim("py-set-output-refused", "set_output 在活循环里被拒绝", claim_py_set_output_refused),
    Claim("py-outputs-set-never-filled", "outputs_set_this_turn 0 个写入点", claim_py_outputs_set_never_filled),
    Claim("py-2b-single-call-site", "evaluate_phase_completion 仅 1 个调用点（judge_pipeline.py）", claim_py_2b_single_call_site),
    Claim("py-no-custom-judge-injection", "core/framework 无任何 AgentLoop(judge=<非 None>) 注入", claim_py_no_custom_judge_injection),
    Claim("py-criteria-config-reachable", "node success_criteria 可由 agent 配置抵达 NodeSpec", claim_py_criteria_config_reachable),
    Claim("py-prompt-matches-golden", "Python judge prompt 模板与 golden 逐字一致", claim_py_prompt_matches_golden),
    Claim("go-no-graph-host", "Go 无 graph 运行时类型、internal/orchestrator 无人 import", claim_go_no_graph_host),
    Claim("go-no-output-accumulator", "Go 无 OutputAccumulator、无 output-key 缺失门", claim_go_no_output_accumulator),
    Claim("go-set-output-unwired", "set_output 构建器/处理器仅定义、无调用方", claim_go_set_output_unwired),
    Claim("go-judge-wired", "judge 由生产代码接线（ConfigureJudge/shouldJudge/evaluateJudge）", claim_go_judge_wired),
    Claim("go-judge-gated", "judge 受 skip_judge 与 criteria 双重门控", claim_go_judge_gated),
    Claim("go-implicit-verdict-event", "skip_judge 会话仍发 judge_verdict(implicit)", claim_go_implicit_verdict_event),
    Claim("go-iteration-feedback-is-grace", "迭代预算反馈是 grace 收尾，无 urgency 文案", claim_go_iteration_feedback_is_grace),
    Claim("go-prompt-template-is-golden", "Go judge 模板覆盖 golden 的全部片段", claim_go_prompt_template_is_golden),
)


def run_static(root: Path, *, verbose: bool) -> list[str]:
    problems: list[str] = []
    for claim in CLAIMS:
        violations = claim.check(root)  # type: ignore[operator]
        status = "PASS" if not violations else "FAIL"
        print(f"[{status}] {claim.id}: {claim.statement}")
        if violations:
            problems.extend(f"{claim.id}: {v}" for v in violations)
        if verbose and not violations:
            hits = _claim_hits(root, claim.id)
            for hit in hits[:6]:
                print(f"         · {hit}")
    return problems


def _claim_hits(root: Path, claim_id: str) -> list[str]:
    """verbose 模式下展示命中位置，便于人工核对。"""
    probes = {
        "py-2b-single-call-site": ("core", r"await evaluate_phase_completion\("),
        "py-criteria-config-reachable": ("core", r'"success_criteria": nc\.success_criteria'),
        "go-judge-wired": ("go", r"\.ConfigureJudge\("),
        "go-set-output-unwired": ("go", r"\bBuildSetOutputTool\b"),
        "go-iteration-feedback-is-grace": ("go", r"graceReminderBody"),
    }
    probe = probes.get(claim_id)
    if probe is None:
        return []
    scope, pattern = probe
    if scope == "core":
        _, hits = _scan_core(root, pattern)
    else:
        _, hits = _scan_go(root, pattern)
    return hits


def run_dynamic(root: Path) -> int:
    """跑真实 AgentLoop，证明参考侧的 Level 2/2b 触发与 output-key 门现状。"""
    import asyncio

    core = root / "core"
    if str(core) not in sys.path:
        sys.path.insert(0, str(core))
    try:
        import framework  # noqa: F401
    except Exception as exc:  # pragma: no cover - 环境缺失时给出明确指引
        print(f"judge_scope_probe --dynamic: 无法 import framework（{exc}）。", file=sys.stderr)
        print(f"  请用仓库 Python 环境并设 PYTHONPATH={core}", file=sys.stderr)
        return 2

    from judge_python_probe import run_cases

    results = asyncio.run(run_cases())
    for line in results:
        print(f"  {line}")
    print("judge_scope_probe --dynamic: OK —— 3 个用例全部符合登记")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="静态/动态核对 judge 面登记断言（离线，仅标准库）。")
    parser.add_argument("--root", default=None, help="仓库根（默认取脚本上两级）")
    parser.add_argument("--list", action="store_true", help="列出全部断言后退出")
    parser.add_argument("--verbose", action="store_true", help="打印命中位置")
    parser.add_argument("--dynamic", action="store_true", help="另跑 Python 侧真实 AgentLoop 的三个用例")
    args = parser.parse_args(argv)

    if args.list:
        for claim in CLAIMS:
            print(f"{claim.id}: {claim.statement}")
        return 0

    root = Path(args.root).resolve() if args.root else REPO_ROOT
    if not (root / "core").is_dir() or not (root / "go_hive" / "go-hive" / "go.mod").is_file():
        print(f"judge_scope_probe: 根目录无效（缺 core/ 或 go_hive/go-hive/go.mod）: {root}", file=sys.stderr)
        return 2

    problems = run_static(root, verbose=args.verbose)
    if problems:
        print(f"judge_scope_probe: FAIL —— {len(problems)} 条断言与登记不符（root={root}）")
        for item in problems:
            print(f"  {item}")
        return 1
    print(f"judge_scope_probe: OK —— {len(CLAIMS)} 条静态断言全部与 docs/judge-parity.md 一致（root={root}）")

    if args.dynamic:
        return run_dynamic(root)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
