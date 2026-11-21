#!/usr/bin/env python3
"""Field-level verbatim comparison: upstream Python Hive playbook tool vs Go port.

For every scenario this script builds one fixture (script + tracker rows +
colony settings), then runs the SAME script through:

  * the real upstream tool — ``framework.tools.playbook_tools.run_playbook``
    registered against a fake colony/session, with the real upstream
    ``PlaybookRun`` runner and the real ``colony_db`` writer; and
  * the real Go tool — ``colony.RunPlaybook`` via the opt-in
    ``TestPlaybookVerbatimHarness`` Go test.

It then compares the observable surface field by field: tool payload keys and
values, [PLAYBOOK_COMPLETE] text, dispatched worker prompts, spawn-spec
report_schema, temporary colony-cap moves, saved library files, and the
playbook_runs row shape.

Run (from the repo root):

    PYTHONPATH=core uv run --project core python go_hive/scripts/playbook_verbatim_compare.py

The report and raw artifacts land in docs/e2e-assets/playbook-verbatim/.
"""

from __future__ import annotations

import asyncio
import json
import os
import shutil
import sqlite3
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
CORE = ROOT / "core"
GO_MODULE = ROOT / "go_hive" / "go-hive"
OUT_DIR = ROOT / "docs" / "e2e-assets" / "playbook-verbatim"
FIXTURE_ROOT = OUT_DIR / "fixtures"

CONVERGE_SCRIPT = '''\
meta = {
    "name": "verbatim-converge",
    "description": "field-parity fixture: lanes + converge + deadletter",
    "concurrency": 2,
}

async def run(args):
    lane("acct-1", concurrency=1)
    await converge(
        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),
        dispatch=lambda row, i: worker(
            task=f"Process job {row['name']} (id {row['id']})",
            data={"id": row["id"], "name": row["name"]},
            goal=f"Processing {row['name']}",
            lane="acct-1",
            retries=1,
            backoff="linear",
            timeout=60,
            schema={"type": "object", "required": ["marker"], "properties": {"marker": {"type": "string"}}},
        ),
        max_rounds=2,
    )
    log("converge finished")
    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0"), "deadletter": deadletter.size}
'''

SLOW_SCRIPT = '''\
import asyncio

meta = {"name": "verbatim-slow", "concurrency": 2}

async def run(args):
    await asyncio.sleep(0.3)
    await converge(
        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),
        dispatch=lambda row, i: worker(task=f"Process {row['name']}", data={"id": row["id"]}, timeout=60),
        max_rounds=1,
    )
    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0")}
'''

LOAD_ERROR_SCRIPT = '''\
async def run(args):
    return {}
'''

RUN_ERROR_SCRIPT = '''\
meta = {"name": "verbatim-run-error", "concurrency": 1}

async def run(args):
    raise KeyError(0)
'''

SCENARIOS = {
    "converge": {
        "script": CONVERGE_SCRIPT,
        "script_name": "converge.play.py",
        "rows": 3,
        "done_rows": [1, 2],
        "cap": 4,
        "grace": "1.5",
        "mode": "completed",
    },
    "started": {
        "script": SLOW_SCRIPT,
        "script_name": "slow.play.py",
        "rows": 2,
        "done_rows": [1, 2],
        "cap": 4,
        "grace": "0",
        "mode": "started",
    },
    "load_error": {
        "script": LOAD_ERROR_SCRIPT,
        "script_name": "load-error.play.py",
        "rows": 1,
        "done_rows": [],
        "cap": 4,
        "grace": "1.5",
        "mode": "load_error",
    },
    "run_error": {
        "script": RUN_ERROR_SCRIPT,
        "script_name": "run-error.play.py",
        "rows": 1,
        "done_rows": [],
        "cap": 4,
        "grace": "1.5",
        "mode": "run_error",
    },
    "cap_raise": {
        "script": CONVERGE_SCRIPT.replace('"name": "verbatim-converge"', '"name": "verbatim-cap-raise"')
        .replace('"concurrency": 2', '"concurrency": 3')
        .replace("max_rounds=2", "max_rounds=1"),
        "script_name": "cap-raise.play.py",
        "rows": 6,
        "done_rows": [1, 2, 3, 4, 5, 6],
        "cap": 1,
        "grace": "1.5",
        "mode": "completed",
    },
}


# ---------------------------------------------------------------------------
# upstream side
# ---------------------------------------------------------------------------


def _build_fixture(name: str) -> dict[str, Any]:
    scenario = SCENARIOS[name]
    fixture_dir = FIXTURE_ROOT / name
    for side in ("py-colony", "go-colony"):
        shutil.rmtree(fixture_dir / side, ignore_errors=True)
    fixture_dir.mkdir(parents=True, exist_ok=True)
    (fixture_dir / scenario["script_name"]).write_text(scenario["script"], encoding="utf-8")
    (fixture_dir / "fixture.json").write_text(
        json.dumps(
            {
                "script": scenario["script_name"],
                "input": "path",
                "rows": scenario["rows"],
                "done_rows": scenario["done_rows"],
                "cap": scenario["cap"],
                "grace": scenario["grace"],
                "mode": scenario["mode"],
            },
            indent=2,
        ),
        encoding="utf-8",
    )
    return scenario


def _seed_tracker(colony_dir: Path, rows: int) -> Path:
    colony_dir.mkdir(parents=True, exist_ok=True)
    db = colony_dir / "tracker.db"
    con = sqlite3.connect(db)
    con.execute("CREATE TABLE IF NOT EXISTS jobs (id INTEGER PRIMARY KEY, name TEXT, done INTEGER NOT NULL DEFAULT 0)")
    con.executemany("INSERT OR REPLACE INTO jobs (id, name, done) VALUES (?, ?, 0)", [(i, f"job-{i}") for i in range(1, rows + 1)])
    con.commit()
    con.close()
    return db


def run_upstream(name: str) -> None:
    """Child-process entry: run the real upstream playbook tool on the fixture."""
    scenario = SCENARIOS[name]
    fixture_dir = FIXTURE_ROOT / name
    colony_dir = fixture_dir / "py-colony"
    _seed_tracker(colony_dir, scenario["rows"])
    os.environ["HIVE_PLAYBOOK_FAST_GRACE"] = scenario["grace"]
    sys.path.insert(0, str(CORE))

    import framework.host.colony_binding as colony_binding
    from framework.host.colony_binding import ColonyBinding
    from framework.tools import playbook_tools

    binding = ColonyBinding(name="harness", dir=colony_dir, tracker_db=colony_dir / "tracker.db")
    colony_binding.current_binding = lambda: binding  # type: ignore[assignment]

    prompts: list[str] = []
    specs: list[dict[str, Any]] = []
    cap_moves: list[int] = []

    class FakeConfig:
        def __init__(self) -> None:
            self._cap = scenario["cap"]

        @property
        def max_concurrent_workers(self) -> int:
            return self._cap

        @max_concurrent_workers.setter
        def max_concurrent_workers(self, value: int) -> None:
            self._cap = value
            cap_moves.append(value)

    class FakeColony:
        colony_id = "harness"

        def __init__(self) -> None:
            self._config = FakeConfig()
            self._tools: list[Any] = []
            self._workers: dict[str, Any] = {}

        async def spawn_batch(self, tasks, tools_override=None, batch_id=""):  # noqa: ANN001
            ids = []
            for entry in tasks:
                wid = f"worker_{len(prompts) + 1}"
                prompts.append(entry["task"])
                specs.append(
                    {
                        "task": entry.get("task"),
                        "worker_seq": entry.get("worker_seq"),
                        "goal": entry.get("goal"),
                        "profile_name": entry.get("profile_name"),
                        "report_schema": entry.get("report_schema"),
                        "data_keys": sorted((entry.get("data") or {}).keys()),
                    }
                )
                row_id = (entry.get("data") or {}).get("id")
                if row_id in scenario["done_rows"]:
                    con = sqlite3.connect(binding.tracker_db)
                    con.execute("UPDATE jobs SET done=1 WHERE id=?", (row_id,))
                    con.commit()
                    con.close()
                ids.append(wid)
            return ids

        async def wait_for_worker_reports(self, wids, timeout=None):  # noqa: ANN001
            return [{"worker_id": wid, "status": "success", "summary": "ok", "data": {}} for wid in wids]

        async def stop_worker(self, wid):  # noqa: ANN001
            self._workers.pop(wid, None)

    class FakeQueenLoop:
        def __init__(self) -> None:
            self.messages: list[str] = []

        async def inject_event(self, message: str) -> None:
            self.messages.append(message)

    queen_loop = FakeQueenLoop()

    class FakeQueenExecutor:
        node_registry = {"queen": queen_loop}

    class FakeSession:
        def __init__(self) -> None:
            self.colony = FakeColony()
            self.colony_id = "harness"
            self.queen_executor = FakeQueenExecutor()

    session = FakeSession()

    class Registry:
        def __init__(self) -> None:
            self.fns: dict[str, Any] = {}

        def register(self, tool_name, tool, fn):  # noqa: ANN001
            self.fns[tool_name] = fn

    registry = Registry()
    playbook_tools.register_playbook_tools(registry, session)
    run_playbook = registry.fns["run_playbook"]

    async def call() -> str:
        result = await run_playbook({"playbook_path": str(fixture_dir / scenario["script_name"])})
        if '"status": "started"' in result:
            for _ in range(500):
                if queen_loop.messages:
                    break
                await asyncio.sleep(0.02)
        return result

    raw = asyncio.run(call())
    payload = json.loads(raw)

    runs = []
    db = colony_dir / "colony.db"
    if db.exists():
        con = sqlite3.connect(db)
        con.row_factory = sqlite3.Row
        for row in con.execute(
            "SELECT run_id, name, status, dispatched, dead_lettered, error, result_json, logs_json, created_at "
            "FROM playbook_runs ORDER BY created_at DESC"
        ):
            runs.append(dict(row))
        con.close()

    saved = {}
    if (colony_dir / "playbooks").exists():
        for path in sorted((colony_dir / "playbooks").iterdir()):
            if path.is_file():
                saved[path.name] = path.read_text(encoding="utf-8")

    artifact = {
        "payload": payload,
        "payload_raw": raw,
        "prompts": prompts,
        "specs": specs,
        "cap_moves": cap_moves,
        "cap_after": session.colony._config.max_concurrent_workers,
        "notices": queen_loop.messages,
        "saved_files": saved,
        "playbook_runs": runs,
        "colony_dir": str(colony_dir),
    }
    (fixture_dir / "py.json").write_text(json.dumps(artifact, indent=2), encoding="utf-8")


# ---------------------------------------------------------------------------
# go side
# ---------------------------------------------------------------------------


def run_go(name: str) -> None:
    fixture_dir = FIXTURE_ROOT / name
    out = fixture_dir / "go.json"
    env = dict(os.environ)
    env.update(
        {
            "PLAYBOOK_VERBATIM_FIXTURE": str(fixture_dir),
            "PLAYBOOK_VERBATIM_OUT": str(out),
        }
    )
    proc = subprocess.run(
        ["go", "test", "./internal/colony", "-run", "TestPlaybookVerbatimHarness", "-count=1", "-v", "-timeout", "300s"],
        cwd=GO_MODULE,
        env=env,
        capture_output=True,
        text=True,
    )
    (fixture_dir / "go-test.log").write_text(proc.stdout + proc.stderr, encoding="utf-8")
    if proc.returncode != 0 or not out.exists():
        raise RuntimeError(f"Go harness failed for {name}:\n{proc.stdout}\n{proc.stderr}")


# ---------------------------------------------------------------------------
# comparison
# ---------------------------------------------------------------------------

VOLATILE_KEYS = {"run_id", "log_db", "playbook_path", "created_at"}
TRACEBACK_KEYS = {"traceback", "error"}


def _cleanup_scratch(fixture_dir: Path) -> None:
    """Remove the run scratch (colony dirs, go-test log) once the artifacts are
    captured. Kept on failure for debugging."""
    for name in ("py-colony", "go-colony"):
        shutil.rmtree(fixture_dir / name, ignore_errors=True)
    for log in fixture_dir.glob("go-test.log"):
        log.unlink(missing_ok=True)


def _normalize(value: Any, colony_dir: str, run_id: str | None) -> Any:
    if isinstance(value, str):
        out = value.replace(colony_dir, "<COLONY>")
        if run_id:
            out = out.replace(run_id, "<RUN_ID>")
        return out
    if isinstance(value, list):
        return [_normalize(item, colony_dir, run_id) for item in value]
    if isinstance(value, dict):
        return {key: _normalize(item, colony_dir, run_id) for key, item in value.items()}
    return value


def _first_line(text: Any) -> Any:
    if isinstance(text, str):
        for line in text.splitlines():
            if line.strip():
                return line.strip()
    return text


def compare_scenario(name: str) -> dict[str, Any]:
    fixture_dir = FIXTURE_ROOT / name
    py = json.loads((fixture_dir / "py.json").read_text(encoding="utf-8"))
    go = json.loads((fixture_dir / "go.json").read_text(encoding="utf-8"))

    run_id = str(py["payload"].get("run_id") or "")
    go_run_id = str(go["payload"].get("run_id") or "")
    diffs: list[dict[str, Any]] = []
    checks: list[dict[str, Any]] = []

    def py_norm(value: Any) -> Any:
        return _normalize(value, py["colony_dir"], run_id)

    def go_norm(value: Any) -> Any:
        return _normalize(value, go["colony_dir"], go_run_id)

    def check(field: str, py_value: Any, go_value: Any, *, note: str = "") -> None:
        ok = py_value == go_value
        checks.append(
            {
                "field": field,
                "py": py_value,
                "go": go_value,
                "ok": ok,
                "note": note,
            }
        )
        if not ok:
            diffs.append({"field": field, "py": py_value, "go": go_value, "note": note})

    # 1. payload keys first: a missing/extra key is the headline field diff.
    py_keys = sorted(py["payload"].keys())
    go_keys = sorted(go["payload"].keys())
    check("payload.keys", py_keys, go_keys)

    for key in sorted(set(py_keys) & set(go_keys)):
        if key == "run_id":
            check(
                "payload.run_id(shape)",
                (py["payload"][key] or "").startswith("pb_") and len(py["payload"][key]) == 15,
                (go["payload"][key] or "").startswith("pb_") and len(go["payload"][key]) == 15,
                note="fresh ids per run; both are pb_ + 12 hex",
            )
            continue
        if key == "traceback":
            check(
                "payload.traceback(root cause)",
                _first_line(py["payload"].get(key)),
                _first_line(go["payload"].get(key)),
                note="full stack is implementation-specific; first line compared",
            )
            continue
        if key == "error" and name == "run_error":
            check(
                "payload.error",
                py_norm(py["payload"].get(key)),
                go_norm(go["payload"].get(key)),
            )
            check(
                "payload.error(root cause)",
                _first_line(py["payload"].get("traceback")),
                _first_line(go["payload"].get("traceback")),
                note="traceback first line only",
            )
            continue
        check(
            f"payload.{key}",
            py_norm(py["payload"].get(key)),
            go_norm(go["payload"].get(key)),
        )

    # 2. [PLAYBOOK_COMPLETE] — one message upstream, notice summary in Go.
    py_notice = py["notices"][-1] if py["notices"] else None
    go_notice = str((go["notices"][-1] or {}).get("summary") or "") if go["notices"] else None
    check(
        "playbook_complete.text",
        py_norm(py_notice) if py_notice else None,
        go_norm(go_notice) if go_notice else None,
    )

    # 3. worker prompts (same script, same tracker => same prompts).
    check("worker_prompts", sorted(py_norm(py["prompts"])), sorted(go_norm(go["prompts"])), note="dispatch order under concurrency is not deterministic; compared as a set")

    # 4. spawn-spec schema forwarding.
    py_schemas = [spec.get("report_schema") for spec in py["specs"]]
    check("spawn.report_schema", py_schemas, go["schemas"])
    py_goals = [spec.get("goal") for spec in py["specs"]]
    check("spawn.goal", py_goals, go.get("goals"))

    # 5. temporary colony cap moves + final value.
    check("colony.cap_moves", py["cap_moves"], go["cap_moves"])
    check("colony.cap_after", py["cap_after"], go["cap_after"])

    # 6. saved library files (names and bytes).
    check("library.files", sorted(py["saved_files"].keys()), sorted(go["saved_files"].keys()))
    for filename in sorted(set(py["saved_files"]) & set(go["saved_files"])):
        check(f"library.{filename}", py["saved_files"][filename], go["saved_files"][filename])

    # 7. playbook_runs rows, field by field.
    py_rows = py["playbook_runs"]
    go_rows = go["playbook_runs"]
    check("runs.count", len(py_rows), len(go_rows))
    for index in range(min(len(py_rows), len(go_rows))):
        py_row = py_rows[index]
        go_row = go_rows[index]
        check(f"runs[{index}].keys", sorted(py_row.keys()), sorted(go_row.keys()))
        for key in sorted(set(py_row) & set(go_row)):
            if key == "run_id":
                check(f"runs[{index}].run_id", bool(py_row[key]), bool(go_row[key]), note="ids differ by design; both present")
            elif key == "created_at":
                check(
                    f"runs[{index}].created_at(format)",
                    bool(py_row[key]) and py_row[key][4] == "-" and py_row[key].endswith("+00:00"),
                    bool(go_row[key]) and go_row[key][4] == "-" and go_row[key].endswith("+00:00"),
                    note="timestamps normalized",
                )
            elif key in {"result_json", "logs_json"}:
                py_value = json.loads(py_row[key]) if py_row[key] else None
                go_value = json.loads(go_row[key]) if go_row[key] else None
                check(f"runs[{index}].{key}(json)", py_value, go_value)
            elif key == "error":
                py_present = py_row[key] is not None
                go_present = go_row[key] is not None
                check(f"runs[{index}].error(present)", py_present, go_present)
            else:
                check(f"runs[{index}].{key}", py_row[key], go_row[key])

    return {
        "scenario": name,
        "checks": checks,
        "diffs": diffs,
        "py_colony": py["colony_dir"],
        "go_colony": go["colony_dir"],
    }


# ---------------------------------------------------------------------------
# report
# ---------------------------------------------------------------------------


def render_report(results: list[dict[str, Any]]) -> str:
    lines = [
        "# Python playbook 字段级逐字对齐：双侧实测",
        "",
        "> 生成：`go_hive/scripts/playbook_verbatim_compare.py`（每次运行重写本文件）  ",
        "> 上游侧：真实 `core/framework/tools/playbook_tools.py`（真实 `PlaybookRun` + 真实 `colony_db`）  ",
        "> Go 侧：真实 `colony.RunPlaybook`（真实 Python bridge）  ",
        "> 归一化：run_id、colony 路径、时间戳；traceback 只比首行（栈帧必然不同）。",
        "",
        "## 汇总",
        "",
        "| 场景 | 检查项 | 一致 | 差异 |",
        "| --- | ---: | ---: | ---: |",
    ]
    total = 0
    total_ok = 0
    for result in results:
        ok = sum(1 for check in result["checks"] if check["ok"])
        total += len(result["checks"])
        total_ok += ok
        lines.append(f"| {result['scenario']} | {len(result['checks'])} | {ok} | {len(result['checks']) - ok} |")
    lines.append(f"| **合计** | **{total}** | **{total_ok}** | **{total - total_ok}** |")
    lines.append("")

    for result in results:
        lines.append(f"## 场景：{result['scenario']}")
        lines.append("")
        lines.append("| 字段 | 上游 | Go | 结果 | 说明 |")
        lines.append("| --- | --- | --- | --- | --- |")
        for check in result["checks"]:
            status = "一致" if check["ok"] else "**差异**"
            py_text = _cell(check["py"])
            go_text = _cell(check["go"])
            note = check["note"] or ""
            lines.append(f"| `{check['field']}` | {py_text} | {go_text} | {status} | {note} |")
        lines.append("")
        if result["diffs"]:
            lines.append("差异明细：")
            lines.append("")
            for diff in result["diffs"]:
                lines.append(f"- `{diff['field']}`: py={diff['py']!r} go={diff['go']!r} {diff['note']}")
            lines.append("")

    lines.append("## 口径")
    lines.append("")
    lines.append("- `payload.keys` 逐项比较：键名、键数、值（归一化后）都相同才算一致。")
    lines.append("- `playbook_complete.text` 是 queen 实际收到的一段文本；上游由 `_notify_complete` 拼装，Go 由 `FormatPlaybookCompleteNotice` 渲染。")
    lines.append("- `worker_prompts` 是两侧真实送入 worker 的任务串；相同脚本 + 相同 tracker 状态必须逐字相同。")
    lines.append("- `spawn.report_schema` 证明 `worker(schema=...)` 进入了 spawn spec（Go 侧为 `BatchTask.ReportSchema`）。")
    lines.append("- `colony.cap_moves` 是 run 期间对 colony 并发上限的临时改动序列（上游 `_config.max_concurrent_workers`，Go `SetMaxConcurrentWorkers`）。")
    lines.append("- `playbook_runs` 行按字段比较；`result_json`/`logs_json` 解析后比较（Go map 无插入序，键序差异不算字段差异）。")
    lines.append("")
    return "\n".join(lines)


def _cell(value: Any) -> str:
    text = json.dumps(value, ensure_ascii=False, default=str)
    if len(text) > 120:
        text = text[:117] + "..."
    return text.replace("|", "\\|").replace("\n", " ")


def main() -> int:
    if "--real-llm" in sys.argv:
        return run_real_llm_replay()
    if len(sys.argv) >= 3 and sys.argv[1] == "--upstream":
        run_upstream(sys.argv[2])
        return 0

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    FIXTURE_ROOT.mkdir(parents=True, exist_ok=True)
    results = []
    for name in SCENARIOS:
        print(f"== scenario {name}", flush=True)
        _build_fixture(name)
        subprocess.run(
            [sys.executable, str(Path(__file__).resolve()), "--upstream", name],
            check=True,
            env={**os.environ, "PYTHONPATH": str(CORE)},
        )
        run_go(name)
        result = compare_scenario(name)
        results.append(result)
        if not result["diffs"]:
            _cleanup_scratch(FIXTURE_ROOT / name)
        print(f"   checks={len(result['checks'])} diffs={len(result['diffs'])}", flush=True)

    (OUT_DIR / "compare.json").write_text(json.dumps(results, indent=2, ensure_ascii=False), encoding="utf-8")
    report = render_report(results)
    (OUT_DIR / "REPORT.md").write_text(report, encoding="utf-8")
    print(report)

    failed = sum(len(result["diffs"]) for result in results)
    return 1 if failed else 0


# ---------------------------------------------------------------------------
# real-LLM replay: the recorded Go run replayed through the upstream tool
# ---------------------------------------------------------------------------


def run_real_llm_replay() -> int:
    """Replay docs/e2e-assets/playbook-verbatim/real-llm/run.json upstream.

    The artifact is produced by the opt-in Go test
    TestRealLLMPlaybookVerbatimConvergeDeadletterE2E with a real model. The same
    script + the same worker reports are fed to the real upstream tool; the
    payload, worker prompts, saved library files and run-log rows are diffed.
    """
    run_path = OUT_DIR / "real-llm" / "run.json"
    if not run_path.exists():
        print(f"no real-LLM artifact at {run_path}; run the opt-in Go E2E first", file=sys.stderr)
        return 2
    art = json.loads(run_path.read_text(encoding="utf-8"))
    fixture_dir = run_path.parent
    colony_dir = fixture_dir / "py-colony"
    shutil.rmtree(colony_dir, ignore_errors=True)
    reports = art["reports"]
    _seed_tracker(colony_dir, len(reports))

    os.environ["HIVE_PLAYBOOK_FAST_GRACE"] = "0"
    sys.path.insert(0, str(CORE))
    import framework.host.colony_binding as colony_binding
    from framework.host.colony_binding import ColonyBinding
    from framework.tools import playbook_tools

    binding = ColonyBinding(name="harness", dir=colony_dir, tracker_db=colony_dir / "tracker.db")
    colony_binding.current_binding = lambda: binding  # type: ignore[assignment]

    prompts: list[str] = []
    specs: list[dict[str, Any]] = []
    cap_moves: list[int] = []
    pending_reports = list(reports)

    class FakeConfig:
        def __init__(self) -> None:
            self._cap = 4

        @property
        def max_concurrent_workers(self) -> int:
            return self._cap

        @max_concurrent_workers.setter
        def max_concurrent_workers(self, value: int) -> None:
            self._cap = value
            cap_moves.append(value)

    class FakeColony:
        colony_id = "harness"

        def __init__(self) -> None:
            self._config = FakeConfig()
            self._tools: list[Any] = []
            self._workers: dict[str, Any] = {}

        async def spawn_batch(self, tasks, tools_override=None, batch_id=""):  # noqa: ANN001
            ids = []
            for entry in tasks:
                wid = f"worker_{len(prompts) + 1}"
                prompts.append(entry["task"])
                specs.append({"task": entry.get("task"), "goal": entry.get("goal"), "report_schema": entry.get("report_schema")})
                ids.append(wid)
            return ids

        async def wait_for_worker_reports(self, wids, timeout=None):  # noqa: ANN001
            out = []
            for wid in wids:
                report = dict(pending_reports.pop(0)) if pending_reports else {"status": "success", "summary": "ok", "data": {}}
                report.setdefault("worker_id", wid)
                out.append(report)
            return out

        async def stop_worker(self, wid):  # noqa: ANN001
            self._workers.pop(wid, None)

    class FakeQueenLoop:
        def __init__(self) -> None:
            self.messages: list[str] = []

        async def inject_event(self, message: str) -> None:
            self.messages.append(message)

    queen_loop = FakeQueenLoop()

    class FakeQueenExecutor:
        node_registry = {"queen": queen_loop}

    class FakeSession:
        def __init__(self) -> None:
            self.colony = FakeColony()
            self.colony_id = "harness"
            self.queen_executor = FakeQueenExecutor()

    session = FakeSession()

    class Registry:
        def __init__(self) -> None:
            self.fns: dict[str, Any] = {}

        def register(self, tool_name, tool, fn):  # noqa: ANN001
            self.fns[tool_name] = fn

    registry = Registry()
    playbook_tools.register_playbook_tools(registry, session)
    run_playbook = registry.fns["run_playbook"]

    async def call() -> str:
        result = await run_playbook({"playbook": art["script"]})
        if '"status": "started"' in result:
            for _ in range(1000):
                if queen_loop.messages:
                    break
                await asyncio.sleep(0.02)
        return result

    raw = asyncio.run(call())
    payload = json.loads(raw)

    runs = []
    db = colony_dir / "colony.db"
    if db.exists():
        con = sqlite3.connect(db)
        con.row_factory = sqlite3.Row
        for row in con.execute(
            "SELECT run_id, name, status, dispatched, dead_lettered, error, result_json, logs_json, created_at "
            "FROM playbook_runs ORDER BY created_at DESC"
        ):
            runs.append(dict(row))
        con.close()
    saved = {}
    if (colony_dir / "playbooks").exists():
        for path in sorted((colony_dir / "playbooks").iterdir()):
            if path.is_file():
                saved[path.name] = path.read_text(encoding="utf-8")

    py = {
        "payload": payload,
        "prompts": prompts,
        "specs": specs,
        "cap_moves": cap_moves,
        "notices": queen_loop.messages,
        "saved_files": saved,
        "playbook_runs": runs,
        "colony_dir": str(colony_dir),
    }
    go = {
        "payload": art["payload"],
        "prompts": art["prompts"],
        "schemas": [None] * len(art["prompts"]),
        "cap_moves": [],
        "notices": [art.get("notice_text") or ""],
        "saved_files": art.get("saved_files", {}),
        "playbook_runs": art.get("playbook_runs", []),
        "colony_dir": art["colony_dir"],
    }
    (fixture_dir / "py.json").write_text(json.dumps(py, indent=2), encoding="utf-8")
    (fixture_dir / "go.json").write_text(json.dumps(go, indent=2), encoding="utf-8")

    py_run_id = str(py["payload"].get("run_id") or "")
    go_run_id = str(go["payload"].get("run_id") or "")
    checks: list[dict[str, Any]] = []
    diffs: list[dict[str, Any]] = []

    def check(field: str, py_value: Any, go_value: Any, *, note: str = "") -> None:
        ok = py_value == go_value
        checks.append({"field": field, "py": py_value, "go": go_value, "ok": ok, "note": note})
        if not ok:
            diffs.append({"field": field, "py": py_value, "go": go_value, "note": note})

    py_keys = sorted(py["payload"].keys())
    go_keys = sorted(go["payload"].keys())
    check("payload.keys", py_keys, go_keys)
    for key in sorted(set(py_keys) & set(go_keys)):
        if key == "run_id":
            check(
                "payload.run_id(shape)",
                (py["payload"][key] or "").startswith("pb_"),
                (go["payload"][key] or "").startswith("pb_"),
                note="fresh ids per run",
            )
            continue
        check(
            f"payload.{key}",
            _normalize(py["payload"][key], py["colony_dir"], py_run_id),
            _normalize(go["payload"][key], go["colony_dir"], go_run_id),
        )
    check("worker_prompts", sorted(py["prompts"]), sorted(go["prompts"]), note="same script => same task strings")
    py_notice = py["notices"][-1] if py["notices"] else None
    go_notice = go["notices"][-1] if go["notices"] else None
    check(
        "playbook_complete.text",
        _normalize(py_notice, py["colony_dir"], py_run_id) if py_notice else None,
        _normalize(go_notice, go["colony_dir"], go_run_id) if go_notice else None,
    )

    check("library.files", sorted(py["saved_files"].keys()), sorted(go["saved_files"].keys()))
    for filename in sorted(set(py["saved_files"]) & set(go["saved_files"])):
        check(f"library.{filename}", py["saved_files"][filename], go["saved_files"][filename])
    py_rows, go_rows = py["playbook_runs"], go["playbook_runs"]
    check("runs.count", len(py_rows), len(go_rows))
    if py_rows and go_rows:
        for key in ("name", "status", "dispatched", "dead_lettered"):
            check(f"runs[0].{key}", py_rows[0].get(key), go_rows[0].get(key))
        for key in ("result_json", "logs_json"):
            check(
                f"runs[0].{key}(json)",
                json.loads(py_rows[0][key]) if py_rows[0].get(key) else None,
                json.loads(go_rows[0][key]) if go_rows[0].get(key) else None,
            )

    lines = [
        "# 真实 LLM 端到端：同一脚本 + 同一 worker 报告的双侧字段对比",
        "",
        "> 生成：`go_hive/scripts/playbook_verbatim_compare.py`（每次运行重写本文件）  ",
        "> 左侧：真实上游工具（`playbook_tools.run_playbook`）重放 Go 端记录的真实模型 worker 报告。  ",
        "> 右侧：真实 Go 工具 + 真实模型（`TestRealLLMPlaybookVerbatimConvergeDeadletterE2E`）的运行结果。",
        "",
        "| 字段 | 上游重放 | Go 真实运行 | 结果 | 说明 |",
        "| --- | --- | --- | --- | --- |",
    ]
    for item in checks:
        status = "一致" if item["ok"] else "**差异**"
        lines.append(f"| `{item['field']}` | {_cell(item['py'])} | {_cell(item['go'])} | {status} | {item['note']} |")
    lines.append("")
    if not diffs:
        for db in fixture_dir.rglob("*.db"):
            db.unlink(missing_ok=True)
        for log in fixture_dir.glob("go-test.log"):
            log.unlink(missing_ok=True)
    (OUT_DIR / "REPORT-real-llm.md").write_text("\n".join(lines), encoding="utf-8")
    (OUT_DIR / "compare-real-llm.json").write_text(json.dumps({"checks": checks, "diffs": diffs}, indent=2, ensure_ascii=False), encoding="utf-8")
    print("\n".join(lines))
    return 1 if diffs else 0


if __name__ == "__main__":
    sys.exit(main())
