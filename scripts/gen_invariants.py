#!/usr/bin/env python3
"""Render and validate the durable go_hive ↔ Hive invariant registry.

Source of truth:
  go_hive/contracts/invariants.json

Generated outputs:
  go_hive/docs/invariants.html
  go_hive/docs/invariants.json

The generator does not execute tests or the service. It verifies that every
declared evidence path exists, resolves JSON selectors when provided, records
SHA-256/byte provenance, and fails closed on missing/ambiguous definitions.

Usage:
  uv run --no-project python3 go_hive/scripts/gen_invariants.py
  uv run --no-project python3 go_hive/scripts/gen_invariants.py --check
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import re
import sys
from collections import Counter
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve()
ROOT = SCRIPT.parents[2]
SOURCE = ROOT / "go_hive" / "contracts" / "invariants.json"
OUT_JSON = ROOT / "go_hive" / "docs" / "invariants.json"
OUT_HTML = ROOT / "go_hive" / "docs" / "invariants.html"
ID_RE = re.compile(r"^INV-[A-Z0-9]+-\d{2}$")


def load_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def resolve_selector(data: Any, selector: str) -> bool:
    cur = data
    for part in selector.split("."):
        if not isinstance(cur, dict) or part not in cur:
            return False
        cur = cur[part]
    return True


def build() -> dict[str, Any]:
    source_bytes = SOURCE.read_bytes()
    source = json.loads(source_bytes.decode("utf-8"))
    errors: list[str] = []

    if source.get("schema") != "hive-invariants/1":
        errors.append("source.schema must be hive-invariants/1")
    levels = source.get("levels") or {}
    statuses = source.get("statuses") or {}
    invariants = source.get("invariants") or []
    if not isinstance(invariants, list) or not invariants:
        errors.append("source.invariants must be a non-empty list")

    seen_ids: set[str] = set()
    manifest: dict[str, dict[str, Any]] = {}
    rendered: list[dict[str, Any]] = []

    for inv in invariants:
        iid = inv.get("id", "")
        if not ID_RE.match(iid):
            errors.append(f"{iid or '<missing>'}: invalid id")
        if iid in seen_ids:
            errors.append(f"{iid}: duplicate id")
        seen_ids.add(iid)
        level = inv.get("level")
        status = inv.get("status")
        if level not in levels:
            errors.append(f"{iid}: unknown level {level!r}")
        if status not in statuses:
            errors.append(f"{iid}: unknown status {status!r}")
        for key in ("surface", "title", "rule", "current", "check"):
            if not isinstance(inv.get(key), str) or not inv[key].strip():
                errors.append(f"{iid}: missing/empty {key}")
        if not isinstance(inv.get("observable"), list) or not inv["observable"]:
            errors.append(f"{iid}: observable must be a non-empty list")
        if not isinstance(inv.get("exceptions"), list):
            errors.append(f"{iid}: exceptions must be a list")
        evidence = inv.get("evidence") or []
        if not isinstance(evidence, list) or not evidence:
            errors.append(f"{iid}: evidence must be non-empty")
            evidence = []
        enriched_evidence = []
        for ev in evidence:
            rel = ev.get("path", "")
            path = ROOT / rel
            if not rel or not path.is_file():
                errors.append(f"{iid}: evidence path not found: {rel!r}")
                continue
            digest = sha256(path)
            size = path.stat().st_size
            selector = ev.get("selector")
            if selector:
                if path.suffix != ".json":
                    errors.append(f"{iid}: selector on non-JSON evidence: {rel}")
                else:
                    try:
                        data = load_json(path)
                    except Exception as exc:
                        errors.append(f"{iid}: cannot parse JSON evidence {rel}: {exc}")
                        data = {}
                    if isinstance(data, dict) and not resolve_selector(data, selector):
                        errors.append(f"{iid}: selector not found in {rel}: {selector}")
            manifest.setdefault(rel, {
                "id": f"artifact:{rel}",
                "path": rel,
                "sha256": digest,
                "bytes": size,
            })
            enriched_evidence.append({
                "path": rel,
                "kind": ev.get("kind", "artifact"),
                "selector": selector,
                "sha256": digest,
                "bytes": size,
                **({"anchor": ev["anchor"]} if ev.get("anchor") else {}),
            })
        rendered.append({
            **inv,
            "trace_id": f"inv:{iid}",
            "evidence": enriched_evidence,
        })

    if errors:
        raise ValueError("; ".join(errors))

    by_level = Counter(inv["level"] for inv in rendered)
    by_status = Counter(inv["status"] for inv in rendered)
    by_surface = Counter(inv["surface"] for inv in rendered)
    source_sha = hashlib.sha256(source_bytes).hexdigest()
    return {
        "schema": "hive-invariants-generated/1",
        "source": str(SOURCE.relative_to(ROOT)),
        "source_sha256": source_sha,
        "source_bytes": len(source_bytes),
        "summary": {
            "total": len(rendered),
            "by_level": dict(sorted(by_level.items())),
            "by_status": dict(sorted(by_status.items())),
            "by_surface": dict(sorted(by_surface.items())),
        },
        "invariants": rendered,
        "evidence_manifest": [manifest[k] for k in sorted(manifest)],
        "maintenance": source.get("maintenance", {}),
        "boundary": "The registry records declared invariants and evidence freshness. A status is not a fresh behavioral test unless its check/evidence says so.",
    }


def status_class(status: str) -> str:
    return {
        "aligned": "ok",
        "partial": "warn",
        "not_frozen": "bad",
        "unverified": "info",
    }.get(status, "info")


def render_html(matrix: dict[str, Any]) -> str:
    s = matrix["summary"]
    cards = [
        ("Invariants", s["total"]),
        ("Hard", s["by_level"].get("hard", 0)),
        ("Scoped", s["by_level"].get("scoped", 0)),
        ("Candidate", s["by_level"].get("candidate", 0)),
        ("Aligned", s["by_status"].get("aligned", 0)),
        ("Partial", s["by_status"].get("partial", 0)),
        ("Not frozen", s["by_status"].get("not_frozen", 0)),
    ]
    card_html = "".join(
        f'<div class="card"><b>{html.escape(str(v))}</b><span>{html.escape(k)}</span></div>'
        for k, v in cards
    )
    rows = []
    for inv in matrix["invariants"]:
        ev = "<br>".join(
            f'<code>{html.escape(e["path"])}</code>'
            + (f'<span class="sub"> :: {html.escape(e["selector"])}</span>' if e.get("selector") else "")
            + f'<span class="sub"> · {html.escape(e["sha256"][:12])}…</span>'
            for e in inv["evidence"]
        )
        exc = "<br>".join(html.escape(x) for x in inv.get("exceptions", [])) or "—"
        scope = f'<div class="sub">scope: {html.escape(inv["scope"])}</div>' if inv.get("scope") else ""
        rows.append(
            "<tr>"
            f'<td><code>{html.escape(inv["id"])}</code><div class="sub">{html.escape(inv["trace_id"])}</div></td>'
            f'<td><code>{html.escape(inv["surface"])}</code></td>'
            f'<td><b>{html.escape(inv["title"])}</b>{scope}<div class="small">{html.escape(inv["rule"])}</div></td>'
            f'<td><span class="pill {status_class(inv["level"]) if inv["level"] == "candidate" else "info"}">{html.escape(inv["level"])}</span></td>'
            f'<td><span class="pill {status_class(inv["status"])}">{html.escape(inv["status"])}</span><div class="small">{html.escape(inv["current"])}</div></td>'
            f'<td class="small">{ev}</td>'
            f'<td class="small">{exc}<div class="sub">check: {html.escape(inv["check"])}</div></td>'
            "</tr>"
        )
    manifest_rows = "".join(
        f'<tr><td><code>{html.escape(e["id"])}</code></td><td class="small"><code>{html.escape(e["path"])}</code></td><td class="num">{e["bytes"]}</td><td class="small"><code>{html.escape(e["sha256"][:16])}…</code></td></tr>'
        for e in matrix["evidence_manifest"]
    )
    rules = "".join(f"<li>{html.escape(x)}</li>" for x in (matrix["maintenance"].get("rules") or []))
    commands = matrix["maintenance"].get("commands") or {}
    command_rows = "".join(
        f'<tr><td><code>{html.escape(k)}</code></td><td><code>{html.escape(v)}</code></td></tr>'
        for k, v in commands.items()
    )
    return f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>go_hive ↔ Hive 不变量注册表</title>
<style>
:root{{--bg:#0b1020;--panel:#121a2e;--panel2:#0f1627;--ink:#e7edf8;--muted:#94a3b8;--line:#263450;--accent:#67e8f9;--ok:#4ade80;--warn:#fbbf24;--bad:#fb7185;--info:#93c5fd}}
*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--ink);font:14px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}}main{{max-width:1600px;margin:auto;padding:28px 22px 70px}}h1{{font-size:27px;margin:0 0 8px}}h2{{font-size:18px;margin:30px 0 10px;border-bottom:1px solid var(--line);padding-bottom:7px}}p,li{{color:var(--muted)}}a{{color:var(--accent)}}code{{color:#c4b5fd}}.sub{{color:var(--muted);font-size:11.5px}}.small{{font-size:11.5px;color:var(--muted);max-width:440px}}
.cards{{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:12px;margin:18px 0}}.card{{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:14px}}.card b{{display:block;font-size:24px;color:var(--accent)}}.card span{{color:var(--muted);font-size:12px}}
.tablewrap{{overflow:auto;border:1px solid var(--line);border-radius:10px;background:var(--panel2)}}table{{border-collapse:collapse;width:100%;min-width:1400px}}th,td{{border-bottom:1px solid var(--line);padding:9px 10px;text-align:left;vertical-align:top}}th{{position:sticky;top:0;background:#16213a;color:#dbeafe;font-size:11px;white-space:nowrap;z-index:1}}.num{{text-align:right;font-variant-numeric:tabular-nums}}
.pill{{display:inline-block;font-size:11px;border:1px solid var(--line);border-radius:999px;padding:2px 8px;white-space:nowrap}}.pill.ok{{color:var(--ok);border-color:#1d4b2c;background:#0f2016}}.pill.warn{{color:var(--warn);border-color:#5b4614;background:#211b0b}}.pill.bad{{color:var(--bad);border-color:#591f2c;background:#211016}}.pill.info{{color:var(--info);border-color:#27456d;background:#101d30}}
.note{{border-left:3px solid var(--warn);background:#171c30;padding:12px 14px;border-radius:0 7px 7px 0;margin:14px 0}}ul{{margin:6px 0 6px 20px;padding:0}}li{{margin:5px 0}}
@media(max-width:720px){{main{{padding:20px 12px}}h1{{font-size:22px}}}}
</style></head><body><main>
<header><h1>go_hive ↔ Hive 不变量注册表</h1>
<p>判定线：<b>模型、客户端、持久化数据或 CLI 任一方可观察的差异，必须逐字一致或显式登记</b>。本页只声明不变量与证据，不把“有对应物”当成行为等价。</p></header>
<div class="cards">{card_html}</div>
<div class="note"><b>边界：</b>status 是登记时的证据状态，不是本轮新跑的行为测试；每个 INV 的证据文件带 SHA-256，证据变更后生成器 <code>--check</code> 会失败。相关页：<a href="consistency-matrix.html">三轴一致性矩阵</a> · <a href="module-interfaces.html">模块接口与不变量</a> · <a href="invariants.json">机器可读 JSON</a>。</div>
<h2>① 不变量</h2>
<div class="tablewrap"><table><thead><tr><th>ID</th><th>面</th><th>规则 / scope</th><th>级别</th><th>状态 / 当前证据</th><th>证据</th><th>例外 / 校验</th></tr></thead><tbody>{''.join(rows)}</tbody></table></div>
<h2>② 证据清单</h2>
<p>每个 artifact 和 selector 的 SHA-256 由生成器现场计算；完整清单见 <a href="invariants.json">invariants.json</a> 的 <code>evidence_manifest</code>。</p>
<div class="tablewrap"><table><thead><tr><th>ID</th><th>Path</th><th>Bytes</th><th>SHA-256</th></tr></thead><tbody>{manifest_rows}</tbody></table></div>
<h2>③ 维护规则</h2>
<ul>{rules}</ul>
<div class="tablewrap"><table><thead><tr><th>命令</th><th>用途</th></tr></thead><tbody>{command_rows}</tbody></table></div>
<p class="sub">source: <code>{html.escape(matrix['source'])}</code> · source sha256: <code>{html.escape(matrix['source_sha256'][:16])}…</code> · generated by <code>gen_invariants.py</code></p>
</main></body></html>
"""


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="fail if generated outputs are stale")
    args = ap.parse_args()
    try:
        matrix = build()
    except Exception as exc:
        print(f"invariants: FAIL: {exc}", file=sys.stderr)
        return 1
    payload = json.dumps(matrix, ensure_ascii=False, indent=2, sort_keys=False) + "\n"
    page = render_html(matrix)
    if args.check:
        bad = []
        if not OUT_JSON.exists() or OUT_JSON.read_text(encoding="utf-8") != payload:
            bad.append(OUT_JSON)
        if not OUT_HTML.exists() or OUT_HTML.read_text(encoding="utf-8") != page:
            bad.append(OUT_HTML)
        if bad:
            print("invariants: DRIFT: " + ", ".join(str(p) for p in bad), file=sys.stderr)
            return 1
        print("invariants: OK (" + ", ".join(f"{k}={v}" for k, v in matrix["summary"]["by_status"].items()) + ")")
        return 0
    OUT_JSON.write_text(payload, encoding="utf-8")
    OUT_HTML.write_text(page, encoding="utf-8")
    print(f"invariants: wrote {OUT_JSON.relative_to(ROOT)} and {OUT_HTML.relative_to(ROOT)}")
    print("summary:", matrix["summary"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
