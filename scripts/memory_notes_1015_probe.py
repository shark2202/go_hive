#!/usr/bin/env python3
"""Upstream (Hive 1.0.15) memory-notes push + recall-index probe — pure-function evidence.

Runs the real reference modules in-process (no network, no model endpoint) and
records, as JSON, what the upstream code actually does for:

  A. env/config priority for ``memory_recall`` (env > configuration.json > default)
  B. ``memory_recall.mode=index``: full listing at session start / post-compact,
     changed-only afterwards, no pick (no extra model call), the model opens
     the note with terminal_exec itself
  C. ``memory_recall.mode=wait``: the model's pick is shown before she answers,
     the wait is capped by ``wait_seconds``
  D. recall index: per-queen file, first query builds it, 3s turn cap with the
     build continuing in its thread, deletion is safe (derived file)

Usage (repo root):
    PYTHONPATH=core:tools/src .venv/bin/python go_hive/scripts/memory_notes_1015_probe.py
Output: JSON on stdout and to go_hive/docs/e2e-assets/memory-notes-1015/py-probe.json
"""

from __future__ import annotations

import asyncio
import contextlib
import json
import os
import sqlite3
import sys
import tempfile
import time
from pathlib import Path
from types import SimpleNamespace

# ---------------------------------------------------------------------------
# Infrastructure
# ---------------------------------------------------------------------------

CASES: list[dict] = []


def record(case: str, subject: str, expected, observed, ok: bool, note: str = "") -> None:
    CASES.append(
        {
            "case": case,
            "subject": subject,
            "expected": expected,
            "observed": observed,
            "pass": bool(ok),
            "note": note,
        }
    )


@contextlib.contextmanager
def patched_env(values: dict[str, str | None]):
    saved = {k: os.environ.get(k) for k in values}
    try:
        for k, v in values.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        yield
    finally:
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


def render(source, ctx, point, user_text=None):
    from framework.agent_loop.reminders import ReminderContext

    return asyncio.run(source.render(ReminderContext(point=point, agent_ctx=ctx, user_text=user_text)))


@contextlib.contextmanager
def swap_attr(obj, name: str, value):
    saved = getattr(obj, name)
    setattr(obj, name, value)
    try:
        yield
    finally:
        setattr(obj, name, saved)


def write_note(directory: Path, filename: str, description: str, body: str = "") -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / filename).write_text(
        f"---\nname: {filename[:-3]}\ntype: preference\ndescription: {description}\n---\n{body}\n",
        encoding="utf-8",
    )


# ---------------------------------------------------------------------------
# A. env/config priority
# ---------------------------------------------------------------------------


def probe_config_priority() -> None:
    from framework import config
    from framework.agent_loop.reminders import ReminderPoint  # noqa: F401

    def snapshot(section, env: dict[str, str | None]):
        with patched_env({"HIVE_MEMORY_RECALL": env.get("HIVE_MEMORY_RECALL")}):
            with swap_attr(config, "get_hive_config", lambda: section):
                cfg = config.get_memory_recall_config()
        return {"mode": cfg.mode, "wait_seconds": cfg.wait_seconds}

    checks = [
        ("default when nothing is set", {}, None, {"mode": "index", "wait_seconds": 10.0}),
        ("configuration.json value wins over default", {"memory_recall": {"mode": "wait", "wait_seconds": 4}}, None, {"mode": "wait", "wait_seconds": 4.0}),
        ("env overrides configuration.json", {"memory_recall": {"mode": "wait", "wait_seconds": 4}}, "index", {"mode": "index", "wait_seconds": 4.0}),
        ("invalid config falls back to default", {"memory_recall": {"mode": "someday", "wait_seconds": -1}}, None, {"mode": "index", "wait_seconds": 10.0}),
        ("invalid env falls through to config", {"memory_recall": {"mode": "wait"}}, "nope", {"mode": "wait", "wait_seconds": 10.0}),
        ("env is stripped and lowercased", {"memory_recall": {"mode": "background", "wait_seconds": 2.5}}, " WAIT ", {"mode": "wait", "wait_seconds": 2.5}),
        ("empty env falls through to config", {"memory_recall": {"mode": "background"}}, "", {"mode": "background", "wait_seconds": 10.0}),
        ("non-object section reads as default", {"memory_recall": "index"}, None, {"mode": "index", "wait_seconds": 10.0}),
        ("bool wait_seconds is rejected like upstream", {"memory_recall": {"mode": "wait", "wait_seconds": True}}, None, {"mode": "wait", "wait_seconds": 10.0}),
    ]
    for label, section, env_mode, expected in checks:
        observed = snapshot(section, {"HIVE_MEMORY_RECALL": env_mode})
        record("A.config_priority", label, expected, observed, observed == expected)

    # env mode names come from the same tuple Settings lists.
    record(
        "A.config_priority",
        "MEMORY_RECALL_MODES",
        ["index", "wait", "background"],
        list(config.MEMORY_RECALL_MODES),
        list(config.MEMORY_RECALL_MODES) == ["index", "wait", "background"],
    )


# ---------------------------------------------------------------------------
# B. index mode
# ---------------------------------------------------------------------------


def probe_index_mode(root: Path) -> None:
    from framework import config
    from framework.agent_loop.memory_reminders import (
        INDEX_CHANGED_HEADER,
        INDEX_HEADER,
        MemoryIndexReminderSource,
        MemoryRecallReminderSource,
    )
    from framework.agent_loop.reminders import ReminderPoint
    from framework.agents.queen.recall_selector import memory_index

    shared, own = root / "global", root / "queen"
    write_note(shared, "tea.md", "What Ava drinks", "Ava drinks pu-erh in the morning.")
    write_note(shared, "pets.md", "Ava's cat")
    write_note(own, "garden.md", "Ava's balcony garden")

    index_calls = {"n": 0}

    def index_provider():
        index_calls["n"] += 1
        return memory_index([("Shared with every queen", shared), ("Yours alone", own)])

    pick_calls: list[str] = []

    async def pick(text: str) -> str:  # the model call "wait" mode makes
        pick_calls.append(text)
        return "### tea.md\n\nAva drinks pu-erh."

    ctx = SimpleNamespace(
        is_queen_stream=True,
        memory_index_provider=index_provider,
        memory_recall_provider=pick,
    )
    source = MemoryIndexReminderSource()

    with patched_env({"HIVE_MEMORY_RECALL": "index"}):
        body = render(source, ctx, ReminderPoint.SESSION_START, user_text="Hi")
        expected = (
            f"{INDEX_HEADER}\n\n"
            f"## Shared with every queen ({shared.as_posix()})\n- [preference] pets.md: Ava's cat\n- [preference] tea.md: What Ava drinks\n\n"
            f"## Yours alone ({own.as_posix()})\n- [preference] garden.md: Ava's balcony garden"
        )
        record("B.index_mode", "session start lists every note in full", expected, body, body == expected)
        record(
            "B.index_mode",
            "the listing carries the one-line manifest, not the note body",
            "body text absent",
            "absent" if (body and "pu-erh in the morning" not in body) else "present",
            bool(body) and "pu-erh in the morning" not in body,
            "INDEX_HEADER tells her to open the note with terminal_exec herself",
        )
        record(
            "B.index_mode",
            "a model callable is never invoked in index mode",
            "pick_calls == []",
            pick_calls,
            pick_calls == [],
            "no extra model call: the source only reads the index provider",
        )
        record(
            "B.index_mode",
            "the unchanged listing is not shown again",
            None,
            render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="How are you?"),
            render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="How are you?") is None,
        )

        # Change the notes the way a reflection run would, and pin scan order.
        write_note(shared, "tea.md", "What Ava drinks, morning and night")
        write_note(shared, "work.md", "Where Ava works")
        os.utime(shared / "tea.md", (1_000_000, 1_000_000))
        os.utime(shared / "work.md", (2_000_000, 2_000_000))
        (shared / "pets.md").unlink(missing_ok=True)
        (own / "garden.md").unlink()

        changed = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="What should I drink?")
        expected_changed = (
            f"{INDEX_CHANGED_HEADER}\n\n"
            f"## Shared with every queen ({shared.as_posix()})\n"
            "New or updated:\n"
            "- [preference] work.md: Where Ava works\n"
            "- [preference] tea.md: What Ava drinks, morning and night\n"
            "Removed:\n"
            "- pets.md\n\n"
            f"## Yours alone ({own.as_posix()})\n"
            "Removed:\n"
            "- garden.md"
        )
        record("B.index_mode", "only what changed is shown next", expected_changed, changed, changed == expected_changed)

        again = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="Anything else?")
        record("B.index_mode", "an unchanged changed-listing is not repeated", None, again, again is None)

        after_compact = render(source, ctx, ReminderPoint.POST_COMPACT)
        record(
            "B.index_mode",
            "post-compact lists everything in full again",
            "starts with INDEX_HEADER",
            (after_compact or "")[:40],
            bool(after_compact) and after_compact.startswith(INDEX_HEADER),
        )

        # No notes at all: silent; the first note appearing later is a full listing.
        empty = root / "empty"
        empty_ctx = SimpleNamespace(
            is_queen_stream=True,
            memory_index_provider=lambda: memory_index([("Shared with every queen", empty), ("Yours alone", None)]),
        )
        silent = render(MemoryIndexReminderSource(), empty_ctx, ReminderPoint.SESSION_START, user_text="Hi")
        record("B.index_mode", "no notes -> silent", None, silent, silent is None)

        # applies_to: queen stream with an index provider only.
        applies_source = MemoryIndexReminderSource()
        record(
            "B.index_mode",
            "applies only to a queen stream carrying an index provider",
            [True, False, False],
            [
                applies_source.applies_to(ctx),
                applies_source.applies_to(SimpleNamespace(is_queen_stream=False, memory_index_provider=index_provider)),
                applies_source.applies_to(SimpleNamespace(is_queen_stream=True, memory_index_provider=None)),
            ],
            applies_source.applies_to(ctx)
            and not applies_source.applies_to(SimpleNamespace(is_queen_stream=False, memory_index_provider=index_provider))
            and not applies_source.applies_to(SimpleNamespace(is_queen_stream=True, memory_index_provider=None)),
        )

        # The index source stays silent while "wait" is set, and lists in full after.
        with patched_env({"HIVE_MEMORY_RECALL": None}):
            with swap_attr(config, "get_hive_config", lambda: {"memory_recall": {"mode": "wait"}}):
                silent_wait = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="And for breakfast?")
            with swap_attr(config, "get_hive_config", lambda: {"memory_recall": {"mode": "index"}}):
                relisted = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="And for breakfast?")
        record(
            "B.index_mode",
            "another mode silences it; switching back lists in full",
            "None then INDEX_HEADER",
            [silent_wait, (relisted or "")[:20]],
            silent_wait is None and bool(relisted) and relisted.startswith(INDEX_HEADER),
        )

        # wait-mode source makes no call while index mode is set.
        wait_source = MemoryRecallReminderSource()
        no_wait = render(wait_source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="What tea do I like?")
        record(
            "B.index_mode",
            "the wait-mode source is silent while index is set (no pick call)",
            {"render": None, "pick_calls": []},
            {"render": no_wait, "pick_calls": pick_calls},
            no_wait is None and pick_calls == [],
        )

    record("B.index_mode", "index provider call count (informational)", ">=1", index_calls["n"], index_calls["n"] >= 1)


# ---------------------------------------------------------------------------
# C. wait mode
# ---------------------------------------------------------------------------


def probe_wait_mode() -> None:
    from framework import config
    from framework.agent_loop.memory_reminders import RECALL_HEADER, MemoryRecallReminderSource
    from framework.agent_loop.reminders import ReminderPoint

    def set_mode(mode: str, **section):
        return swap_attr(
            config, "get_hive_config", lambda: {"memory_recall": {"mode": mode, **section}}
        )

    pick_result = "### tea.md\n\nAva drinks pu-erh."

    def make_ctx(provider):
        return SimpleNamespace(is_queen_stream=True, memory_recall_provider=provider)

    with patched_env({"HIVE_MEMORY_RECALL": None}):
        seen: list[str] = []

        async def pick(text: str) -> str:
            seen.append(text)
            return pick_result

        with set_mode("wait"):
            source, ctx = MemoryRecallReminderSource(), make_ctx(pick)
            body = render(source, ctx, ReminderPoint.SESSION_START, user_text="What tea do I like?")
            expected = f"{RECALL_HEADER}\n\n{pick_result}"
            record("C.wait_mode", "the pick is shown before she answers", expected, body, body == expected)
            record("C.wait_mode", "the pick saw the user's message", ["What tea do I like?"], seen, seen == ["What tea do I like?"])

            same = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="And for breakfast?")
            record("C.wait_mode", "an unchanged pick is not repeated", None, same, same is None)

            after_compact = render(source, ctx, ReminderPoint.POST_COMPACT)
            record("C.wait_mode", "post-compact clears the last pick (shows nothing)", None, after_compact, after_compact is None)
            again = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="And for breakfast?")
            record("C.wait_mode", "the same pick is shown again after compaction", "shows pick", bool(again) and again.startswith(RECALL_HEADER), bool(again) and again.startswith(RECALL_HEADER))

            no_text = render(source, ctx, ReminderPoint.USER_PROMPT_SUBMIT, user_text="   ")
            record("C.wait_mode", "an empty message makes no pick call", None, no_text, no_text is None)

        # wait_seconds caps the wait.
        async def slow(text: str) -> str:
            await asyncio.sleep(1.0)
            return "late"

        with set_mode("wait", wait_seconds=0.05):
            started = time.monotonic()
            capped = render(MemoryRecallReminderSource(), make_ctx(slow), ReminderPoint.USER_PROMPT_SUBMIT, user_text="What tea do I like?")
            elapsed = time.monotonic() - started
        record(
            "C.wait_mode",
            "the wait is capped by wait_seconds (0.05)",
            {"render": None, "returned_before_1s": True},
            {"render": capped, "returned_before_1s": elapsed < 1.0},
            capped is None and elapsed < 1.0,
        )

        async def broken(text: str) -> str:
            raise RuntimeError("provider down")

        with set_mode("wait"):
            failed = render(MemoryRecallReminderSource(), make_ctx(broken), ReminderPoint.USER_PROMPT_SUBMIT, user_text="Hello")
        record("C.wait_mode", "a failed pick is swallowed", None, failed, failed is None)

        async def empty(text: str) -> str:
            return ""

        with set_mode("wait"):
            blank = render(MemoryRecallReminderSource(), make_ctx(empty), ReminderPoint.USER_PROMPT_SUBMIT, user_text="Hello")
        record("C.wait_mode", "an empty pick is silent", None, blank, blank is None)

        async def pick2(text: str) -> str:
            return pick_result

        with set_mode("wait"):
            source2, ctx2 = MemoryRecallReminderSource(), make_ctx(pick2)
            first = render(source2, ctx2, ReminderPoint.USER_PROMPT_SUBMIT, user_text="What tea do I like?")
            with set_mode("index"):
                silent = render(source2, ctx2, ReminderPoint.USER_PROMPT_SUBMIT, user_text="What tea do I like?")
            with set_mode("wait"):
                back = render(source2, ctx2, ReminderPoint.USER_PROMPT_SUBMIT, user_text="What tea do I like?")
        record(
            "C.wait_mode",
            "silent under another mode; the pick shows again when wait returns",
            ["shown", None, "shown"],
            [bool(first), silent, bool(back)],
            bool(first) and silent is None and bool(back),
        )

        source3 = MemoryRecallReminderSource()
        record(
            "C.wait_mode",
            "applies only to a queen stream carrying a pick provider",
            [True, False, False],
            [
                source3.applies_to(make_ctx(pick2)),
                source3.applies_to(SimpleNamespace(is_queen_stream=False, memory_recall_provider=pick2)),
                source3.applies_to(SimpleNamespace(is_queen_stream=True, memory_recall_provider=None)),
            ],
            source3.applies_to(make_ctx(pick2))
            and not source3.applies_to(SimpleNamespace(is_queen_stream=False, memory_recall_provider=pick2))
            and not source3.applies_to(SimpleNamespace(is_queen_stream=True, memory_recall_provider=None)),
        )


# ---------------------------------------------------------------------------
# D. recall index
# ---------------------------------------------------------------------------


def probe_recall_index(root: Path) -> None:
    from memory_tools import paths as P
    from memory_tools import recall as R
    from memory_tools import turn_index
    from framework.agent_loop import past_conversations_reminder as PCR
    from framework.agent_loop.past_conversations_reminder import PastConversationsReminderSource
    from framework.agent_loop.reminders import ReminderPoint

    queen = "queen_test"
    other = "queen_other"

    def session(home: Path, queen_id: str, session_id: str, turns: list[tuple[str, str]]) -> Path:
        sdir = home / "queens" / queen_id / "sessions" / session_id
        sdir.mkdir(parents=True)
        events = []
        for i, (role, text) in enumerate(turns):
            if role == "user":
                events.append({"type": "client_input_received", "data": {"content": text}})
            else:
                events.append({"type": "client_output_delta", "data": {"snapshot": text, "iteration": i, "inner_turn": 0}})
        (sdir / "events.jsonl").write_text("".join(json.dumps(e) + "\n" for e in events), encoding="utf-8")
        return sdir

    session(root, queen, "session_20230522_211800_aaaa0001", [("user", "I've been listening to audiobooks during my daily commute, which takes 45 minutes each way."), ("assistant", "That's a great use of the time.")])
    session(root, queen, "session_20230524_143200_aaaa0002", [("user", "What can individuals do to help coral reef conservation?"), ("assistant", "Reduce runoff...")])
    session(root, other, "session_20230525_090000_bbbb0001", [("user", "My penguin collection lives on the top shelf."), ("assistant", "Noted.")])

    with patched_env({"HIVE_HOME": str(root)}):
        index_path = P.turn_index_path("queens", queen)
        record("D.recall_index", "index file is per-queen", str(root / ".message_index" / "recall" / "queens" / "queen_test.sqlite3"), str(index_path), str(index_path).endswith("recall/queens/queen_test.sqlite3"))
        record("D.recall_index", "index does not exist before the first query", False, index_path.exists(), not index_path.exists())

        started = time.monotonic()
        turns = R.recall("queens", queen, "How long is my daily commute to work?")
        first_build_ms = (time.monotonic() - started) * 1000
        record(
            "D.recall_index",
            "first query builds the index and answers",
            ["session_20230522_211800_aaaa0001"],
            [t.session for t in turns],
            [t.session for t in turns] == ["session_20230522_211800_aaaa0001"],
            "first query builds the index; its wall time is reported on stdout",
        )
        record("D.recall_index", "the index file exists after the first query", True, index_path.exists(), index_path.exists())

        started = time.monotonic()
        second = R.recall("queens", queen, "How long is my daily commute to work?")
        warm_ms = (time.monotonic() - started) * 1000
        record(
            "D.recall_index",
            "a warm query reads the same index and returns the same answer",
            [t.session for t in turns],
            [t.session for t in second],
            [t.session for t in second] == [t.session for t in turns],
            "warm query reads the index (no rebuild)",
        )
        print(f"recall index timing: first={first_build_ms:.1f}ms warm={warm_ms:.1f}ms", file=sys.stderr)

        with turn_index.connect("queens", queen) as conn:
            sessions = conn.execute("SELECT name, events_size FROM sessions ORDER BY name").fetchall()
            turn_count = conn.execute("SELECT COUNT(*) FROM turns").fetchone()[0]
        record(
            "D.recall_index",
            "the index tracks every session with its events size",
            [("session_20230522_211800_aaaa0001", sessions[0][1]), ("session_20230524_143200_aaaa0002", sessions[1][1])],
            sessions,
            [row[0] for row in sessions] == ["session_20230522_211800_aaaa0001", "session_20230524_143200_aaaa0002"] and all(row[1] > 0 for row in sessions),
        )
        record("D.recall_index", "turns are indexed", 2, turn_count, turn_count == 2)

        # Per-owner isolation: the other queen recalls its own index only.
        other_index = P.turn_index_path("queens", other)
        other_turns = R.recall("queens", other, "Where does my penguin collection live?")
        record(
            "D.recall_index",
            "each owner keeps its own index file",
            {"own": True, "distinct": True},
            {"own": other_index.exists(), "distinct": str(other_index) != str(index_path)},
            other_index.exists() and str(other_index) != str(index_path),
        )
        record(
            "D.recall_index",
            "queen A's recall never sees queen B's sessions",
            ["session_20230525_090000_bbbb0001"],
            [t.session for t in other_turns],
            [t.session for t in other_turns] == ["session_20230525_090000_bbbb0001"],
        )
        cross = R.recall("queens", queen, "Where does my penguin collection live?")
        record("D.recall_index", "cross-owner leak is impossible", [], [t.session for t in cross], cross == [])

        # Deletion is safe: the file is derived.
        index_path.unlink()
        record("D.recall_index", "the index can be deleted", False, index_path.exists(), not index_path.exists())
        rebuilt = R.recall("queens", queen, "How long is my daily commute to work?")
        record(
            "D.recall_index",
            "a deleted index is rebuilt with the same results",
            [t.session for t in turns],
            [t.session for t in rebuilt],
            [t.session for t in rebuilt] == [t.session for t in turns],
        )
        record("D.recall_index", "rebuilt index exists again", True, index_path.exists(), index_path.exists())

        # 3s cap: the reminder source waits RECALL_TIMEOUT_S for the thread; a
        # first build that runs longer keeps going while the turn proceeds.
        record("D.recall_index", "default turn cap is 3s", 3.0, PCR.RECALL_TIMEOUT_S, PCR.RECALL_TIMEOUT_S == 3.0)

        state = {"done": False}

        def slow_provider(text: str) -> str:
            time.sleep(0.6)
            state["done"] = True
            return "Excerpts..."

        original = PCR.RECALL_TIMEOUT_S
        PCR.RECALL_TIMEOUT_S = 0.2
        try:
            ctx = SimpleNamespace(
                is_queen_stream=True,
                past_conversation_recall_provider=slow_provider,
            )
            # A dedicated loop, so asyncio.run's executor-shutdown wait does not
            # hide when the turn itself resumed: the reference returns at the
            # cap while the build thread keeps running.
            loop = asyncio.new_event_loop()
            started = time.monotonic()
            try:
                from framework.agent_loop.reminders import ReminderContext

                capped = loop.run_until_complete(
                    PastConversationsReminderSource().render(
                        ReminderContext(point=ReminderPoint.USER_PROMPT_SUBMIT, agent_ctx=ctx, user_text="How long is my commute to work?")
                    )
                )
            finally:
                elapsed = time.monotonic() - started
                done_at_return = state["done"]
                time.sleep(0.6)
                done_later = state["done"]
                loop.close()
        finally:
            PCR.RECALL_TIMEOUT_S = original
        record(
            "D.recall_index",
            "a first build past the cap loses only that turn; the thread keeps going",
            {"render": None, "done_at_return": False, "done_later": True},
            {"render": capped, "returned_at_cap": elapsed < 0.6, "done_at_return": done_at_return, "done_later": done_later},
            capped is None and elapsed < 0.6 and not done_at_return and done_later,
        )

        short = render(
            PastConversationsReminderSource(),
            SimpleNamespace(is_queen_stream=True, past_conversation_recall_provider=lambda text: "Excerpts..."),
            ReminderPoint.USER_PROMPT_SUBMIT,
            user_text="How long is my commute to work?",
        )
        record("D.recall_index", "a fast provider's excerpts come through", "Excerpts...", short, short == "Excerpts...")

        tiny = render(PastConversationsReminderSource(), SimpleNamespace(is_queen_stream=True, past_conversation_recall_provider=lambda text: "Excerpts..."), ReminderPoint.USER_PROMPT_SUBMIT, user_text="ok")
        record("D.recall_index", "messages under 8 chars are not recalled", None, tiny, tiny is None)

    # env override of the cap, imported fresh in a subprocess.
    env_cap = run_python(
        "from framework.agent_loop.past_conversations_reminder import RECALL_TIMEOUT_S; print(RECALL_TIMEOUT_S)",
        env={"HIVE_PAST_RECALL_TIMEOUT": "0.5"},
    ).strip()
    record("D.recall_index", "HIVE_PAST_RECALL_TIMEOUT overrides the 3s cap", "0.5", env_cap, env_cap == "0.5")


def run_python(code: str, env: dict[str, str]) -> str:
    import subprocess

    merged = dict(os.environ)
    merged.update(env)
    out = subprocess.run(
        [sys.executable, "-c", code],
        capture_output=True,
        text=True,
        env=merged,
        cwd=str(Path(__file__).resolve().parents[2] / "core"),
    )
    if out.returncode != 0:
        return f"ERROR: {out.stderr.strip()[:200]}"
    return out.stdout


# ---------------------------------------------------------------------------


def main() -> int:
    from framework import config  # noqa: F401  (import check early)

    root = Path(tempfile.mkdtemp(prefix="memory-notes-probe-"))
    probe_config_priority()
    probe_index_mode(root / "index-mode")
    probe_wait_mode()
    probe_recall_index(root / "recall-index")

    failed = [case for case in CASES if not case["pass"]]
    payload = {
        "probe": "memory_notes_1015_probe",
        "source": "core/framework/agent_loop/memory_reminders.py + agents/queen/recall_selector.py + tools/src/memory_tools/{recall,turn_index}.py",
        "cases": CASES,
        "summary": {"total": len(CASES), "passed": len(CASES) - len(failed), "failed": len(failed)},
    }
    out_dir = Path(__file__).resolve().parents[1] / "docs" / "e2e-assets" / "memory-notes-1015"
    out_dir.mkdir(parents=True, exist_ok=True)
    out_file = out_dir / "py-probe.json"
    # The temp root is scaffolding, not evidence: normalize it so the document
    # reads the same on any machine (the measured timings in `note` still vary).
    document = json.dumps(payload, indent=2, ensure_ascii=False) + "\n"
    document = document.replace(str(root), "<tmp>")
    out_file.write_text(document, encoding="utf-8")
    print(json.dumps(payload["summary"], ensure_ascii=False))
    for case in failed:
        print("FAIL", case["case"], case["subject"], "observed:", json.dumps(case["observed"], ensure_ascii=False)[:300])
    print("evidence:", out_file)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
