#!/usr/bin/env python3
"""QC 结果检验：对交付物做独立检验并给出 PASS/FAIL。

用法：qc_check.py [--full] [--smoke] [--strict-surface]
  --full            额外跑 make ci（约 2-3 分钟）
  --smoke           额外跑真实二进制冒烟（约 20 秒）
  --strict-surface  表面审计转为强制：有未豁免缺失 / 卫生错误即 FAIL。
                    默认只报告，避免已知的 skills 路由缺口让 QC 永久红。
"""
import argparse
import json
import platform as platform_module
import re
import socket
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path

# Resolve from this file (go_hive/scripts/qc_check.py) so the gate inspects the
# checkout it runs from, including git worktrees, instead of a hardcoded tree.
ROOT = Path(__file__).resolve().parents[2]
MOD = ROOT / "go_hive/go-hive"
OUT = ROOT / "go_hive/QC_REPORT.md"


def free_port() -> int:
    """Pick an ephemeral port so the smoke run never collides with a service
    someone else already bound (8801 is a common dev port)."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def run(cmd, cwd=MOD, timeout=600):
    p = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout, shell=isinstance(cmd, str))
    return p.returncode, (p.stdout + p.stderr)


def local_target():
    system = platform_module.system().lower()
    os_name = {"darwin": "darwin", "linux": "linux", "windows": "windows"}.get(system, system)
    machine = platform_module.machine().lower()
    arch = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "amd64": "amd64"}.get(machine, machine)
    return os_name, arch


def archive_members(archive: Path) -> dict[str, str]:
    """Map basename -> in-archive name for every regular file."""
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as zf:
            return {Path(info.filename).name: info.filename for info in zf.infolist() if not info.is_dir()}
    with tarfile.open(archive, "r:gz") as tf:
        return {Path(member.name).name: member.name for member in tf.getmembers() if member.isfile()}


def archive_read(archive: Path, member: str) -> bytes:
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as zf:
            return zf.read(member)
    with tarfile.open(archive, "r:gz") as tf:
        handle = tf.extractfile(member)
        if handle is None:
            raise ValueError(f"{member} is not a regular file")
        with handle:
            return handle.read()


# The release contract: every archive carries these four binaries at its root,
# with the same names on every platform (`.exe` on Windows). `hive serve`
# resolves `browserbridge` next to the `hive` executable and `hive-browser` is
# the CLI that drives it, so a missing member means the unpacked release can
# never start the browser bridge.
RELEASE_BINARIES = {
    ".tar.gz": ["hive", "hive-cred", "hive-browser", "browserbridge"],
    ".zip": ["hive.exe", "hive-cred.exe", "hive-browser.exe", "browserbridge.exe"],
}


def check_release_archives():
    """C4 archive contract: every archive carries hive + hive-cred +
    hive-browser + browserbridge, its inner SHA256SUMS covers all four, and
    the local-platform binaries actually run.

    The local check deliberately executes `hive-browser help` and
    `browserbridge -version` from the unpacked archive: both are offline-safe
    entry points, so a broken/absent browser host fails the gate instead of
    only failing at release-extraction time.
    """
    release_dir = MOD / "dist/release"
    archives = sorted(release_dir.glob("hive-*.tar.gz")) + sorted(release_dir.glob("hive-*.zip"))
    problems = []
    if not archives:
        return False, f"未找到归档：{release_dir}"

    for archive in archives:
        wanted = RELEASE_BINARIES[".zip" if archive.suffix == ".zip" else ".tar.gz"]
        try:
            members = archive_members(archive)
            for name in wanted:
                if name not in members:
                    problems.append(f"{archive.name} 缺 {name}")
            if "SHA256SUMS" not in members:
                problems.append(f"{archive.name} 缺 SHA256SUMS")
            else:
                sums = archive_read(archive, members["SHA256SUMS"]).decode("utf-8", "replace")
                for name in wanted:
                    if not re.search(rf"(?m)^[0-9a-fA-F]{{64}}\s+\*?\.?/?{re.escape(name)}$", sums):
                        problems.append(f"{archive.name} 的 SHA256SUMS 未覆盖 {name}")
        except (OSError, tarfile.TarError, zipfile.BadZipFile, ValueError) as exc:
            problems.append(f"{archive.name} 无法读取：{exc}")

    os_name, arch = local_target()
    ext = "zip" if os_name == "windows" else "tar.gz"
    local_archives = sorted(release_dir.glob(f"hive-*-{os_name}-{arch}.{ext}"))
    local_ok = False
    local_evidence = "无本地归档"
    if local_archives:
        archive = local_archives[-1]
        names = RELEASE_BINARIES[f".{ext}"]
        try:
            members = archive_members(archive)
            missing = [name for name in names if name not in members]
            if missing:
                local_evidence = f"{archive.name} 缺 {', '.join(missing)}"
            else:
                details = []
                with tempfile.TemporaryDirectory() as tmp:
                    for name in names:
                        binary = Path(tmp) / name
                        binary.write_bytes(archive_read(archive, members[name]))
                        binary.chmod(0o755)
                    binaries = (
                        ("hive", names[0], ["--version"], "hive "),
                        ("hive-cred", names[1], ["--help"], "hive-cred"),
                        ("hive-browser", names[2], ["help"], "hive-browser"),
                        ("browserbridge", names[3], ["-version"], ""),
                    )
                    for label, binary_name, args, needle in binaries:
                        proc = subprocess.run(
                            [str(Path(tmp) / binary_name)] + args,
                            capture_output=True, text=True, timeout=60,
                        )
                        output = (proc.stdout + proc.stderr).strip()
                        passed = proc.returncode == 0 and needle.lower() in output.lower()
                        details.append(f"{label} exit={proc.returncode}" + ("" if passed else f" ({output[:80]})"))
                        local_ok = passed if not details[:-1] else local_ok and passed
                local_evidence = f"{archive.name}；" + "；".join(details)
        except (OSError, tarfile.TarError, zipfile.BadZipFile, ValueError, subprocess.SubprocessError) as exc:
            local_evidence = f"{archive.name} 运行失败：{exc}"

    evidence = f"{len(archives)} 个归档；问题={problems or '无'}；本地 {os_name}/{arch} {local_evidence}"
    return not problems and local_ok, evidence


def check_surface_audit(strict: bool):
    """C8：前端/Python 声明的入口在 Go 是否真的注册；仓库卫生一并检查。

    默认报告模式（恒 PASS，但证据里带真实缺口数），因为 skills 路由整组
    仍属于 skills-api lane 的待修项；加 --strict-surface 才把缺口变成阻断。
    """
    script = ROOT / "go_hive/scripts/surface_audit.py"
    if not script.exists():
        return False, f"缺脚本：{script}"
    cmd = [sys.executable, str(script), "--json", "--write-report", "--root", str(ROOT)]
    allowlist = ROOT / "go_hive/scripts/surface_audit_allowlist.json"
    if allowlist.exists():
        cmd += ["--allowlist", str(allowlist)]
    code, out = run(cmd, cwd=ROOT, timeout=120)
    try:
        data = json.loads(out)
    except json.JSONDecodeError:
        return False, f"输出不是 JSON（exit={code}）：{out.strip()[:160]}"
    counts = data["counts"]
    hygiene = counts["empty_dirs"] + counts["lane_named_files"] + counts["dangling_waivers"]
    evidence = (
        f"前端缺失 {counts['frontend_missing']}（待修 {counts['frontend_pending']}）；"
        f"Python 缺失 {counts['python_missing']}（待修 {counts['python_pending']}）；"
        f"卫生问题 {hygiene}；Go 多余 {counts['go_extra']}（仅提示）；"
        f"报告 go_hive/docs/surface-audit.md"
    )
    if strict:
        return code == 0 and bool(data.get("ok")), evidence + "；strict"
    return code == 0, evidence + "；报告模式"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--full", action="store_true")
    ap.add_argument("--smoke", action="store_true")
    ap.add_argument("--strict-surface", action="store_true", help="表面审计强制（默认；保留以兼容旧命令）")
    ap.add_argument("--report-surface", action="store_true", help="表面审计只报告，不阻断")
    args = ap.parse_args()
    strict_surface = not args.report_surface

    checks = []

    # C2 parity 引用可解析（退出码为准，注解数作展示）
    code, out = run(["bash", "../scripts/check_parity.sh"])
    m = re.search(r"(\d+)\s*/\s*(\d+)", out) or re.search(r"(\d+)\s*个测试", out)
    code2, out2 = run(["bash", "../scripts/check_parity_targets.sh"])
    missing = re.search(r"missing files:\s*(\d+).*missing tests:\s*(\d+)", out2)
    refs_ok = code2 == 0 and (not missing or (missing.group(1) == "0" and missing.group(2) == "0"))
    checks.append(("C2 parity 引用", code == 0 and refs_ok,
                   f"annotation_exit={code}；{m.group(0) if m else 'n/a'}；refs {'0 missing' if refs_ok else '有失效'}"))

    # C4 制品（区分 embed 二进制与磁盘依赖）
    dist = MOD / "dist"
    bins = sorted(p.name for p in dist.glob("hive*")) if dist.exists() else []
    platform_ok = sum(1 for b in bins if any(k in b for k in ("darwin", "linux", "windows"))) >= 6
    embed_bin = dist / "hive-embed"
    embed_script = ROOT / "go_hive/scripts/embed_ui.sh"
    has_disk_dist = (MOD / "frontend/dist/index.html").exists()
    # UI 可分发性 = 有磁盘产物且能构建 embed（本地不必已产出 hive-embed，release.sh 按平台现场构建）
    ui_shippable = has_disk_dist and embed_script.exists()
    version_bin = dist / "hive"
    if not version_bin.exists():
        os_name, arch = local_target()
        suffix = ".exe" if os_name == "windows" else ""
        cross_bin = dist / f"hive-{os_name}-{arch}{suffix}"
        version_bin = cross_bin if cross_bin.exists() else None
    if version_bin is not None:
        code, out = run([str(version_bin), "--version"])
        version_evidence = out.strip()
    else:
        version_evidence = "无可运行产物"
    checks.append(("C4 六平台产物", platform_ok, f"{len(bins)} 个产物；version={version_evidence}"))
    checks.append(("C4b UI 可分发性", ui_shippable,
                   f"磁盘 dist={'有' if has_disk_dist else '无'}；embed 脚本={'有' if embed_script.exists() else '无'}；hive-embed={'有' if embed_bin.exists() else '未构建'}"))
    archive_ok, archive_ev = check_release_archives()
    checks.append(("C4c 归档含四个二进制", archive_ok, archive_ev))

    # C5 cgo
    code, out = run(["bash", "-c", "grep -rl 'import \"C\"' --include='*.go' . | wc -l"])
    cgo = int(out.strip() or 0)
    checks.append(("C5 无 cgo", cgo == 0, f"import \"C\" 文件 {cgo} 个"))

    # C6 文档口径一致
    docs = ["README.md", "DELIVERY.md", "COORDINATION.md", "11-product-requirements.md", "INTEGRATION_BACKLOG.md"]
    stale = []
    for d in docs:
        p = ROOT / "go_hive" / d
        if p.exists() and "811 action" in p.read_text():
            stale.append(d)
    checks.append(("C6 文档口径", not stale, f"残留 811 口径：{stale or '无'}"))

    # C7 回归趋势（对照检查点台账，而非同批进程写的采样）
    tests = int(run(["bash", "-c", "grep -rhoE '^func Test[A-Za-z0-9_]+' --include='*_test.go' . | wc -l"])[1].strip() or 0)
    ledger = ROOT / "go_hive/ledger.csv"
    peak = 0
    if ledger.exists():
        for line in ledger.read_text().splitlines()[1:]:
            parts = line.split(",")
            if len(parts) > 3 and parts[3].isdigit():
                peak = max(peak, int(parts[3]))
    checks.append(("C7 测试回归", tests >= peak, f"当前 {tests}，台账峰值 {peak}"))

    # C8 表面审计（默认强制；--report-surface 只报告）
    surface_ok, surface_ev = check_surface_audit(strict_surface)
    label = "C8 表面审计（强制）" if strict_surface else "C8 表面审计（报告）"
    checks.append((label, surface_ok, surface_ev))

    if args.full:
        code, out = run(["make", "ci"], timeout=900)
        checks.append(("C1 make ci", code == 0, (out.strip().splitlines() or [""])[-1][:120]))

    if args.smoke:
        smoke_port = free_port()
        code, out = run(["bash", "../scripts/smoke.sh", str(smoke_port)], timeout=300)
        checks.append(("C3 运行时冒烟", code == 0, (out.strip().splitlines() or [""])[-2][:120]))

    ok = sum(1 for _, v, _ in checks if v)
    lines = ["# QC 结果检验", "", f"| 检验 | 结果 | 证据 |", "| --- | --- | --- |"]
    for name, good, ev in checks:
        lines.append(f"| {name} | {'PASS' if good else 'FAIL'} | {ev} |")
    lines += ["", f"**{ok}/{len(checks)} PASS**", ""]
    if ok != len(checks):
        lines.append("失败项必须阻断合并/发布，修复后重跑。")
    OUT.write_text("\n".join(lines) + "\n")
    print("\n".join(lines))
    return 0 if ok == len(checks) else 1


if __name__ == "__main__":
    raise SystemExit(main())
