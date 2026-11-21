#!/usr/bin/env python3
"""Freeze the session SSE frame contract: Python reference vs Go target.

The dual-run probe (``sse_payload_parity.py``) captures the frames both
implementations stream for one fixed scenario and reports *shape* differences.
This script turns those captures into a frame-level golden:

  go_hive/contracts/sse-frames/reference/{live,replay,history,stop}.json
  go_hive/contracts/sse-frames/target/{live,replay,history,stop}.json
  go_hive/contracts/sse-frames/diff.json
  go_hive/contracts/sse-frames/index.json
  go_hive/contracts/sse-frames/summary.txt

Every captured frame is canonicalised: volatile values (seq, timestamps,
session/execution/correlation ids, durations) become placeholders while field
presence, field order, event type, event order and enum values are kept. The
golden therefore replays deterministically from the committed captures:

    uv run python go_hive/scripts/sse_frames_canonical.py           # write
    uv run python go_hive/scripts/sse_frames_canonical.py --check   # verify
    uv run python go_hive/scripts/sse_frames_canonical.py --refresh # probe + write

``--check`` re-canonicalises the committed captures in memory and fails when
either the golden files or the assertion verdict moved. It never starts a
server and never touches the network.

Documented transforms applied before comparing (and recorded in diff.json):

* Go-only per-turn loop envelope frames (execution_started/completed/failed/
  paused, node_loop_started/completed) are dropped from the target. The
  session-start block is kept because it precedes the first client input: the
  Python queen runs one permanent loop, Go runs one bounded loop per turn.
* session_title_updated is asynchronous on both sides; its frames are compared
  as a multiset (type + payload) and its position is explicitly non-contract.
* The declared value-level exceptions below are masked to a placeholder, with
  the observed values recorded in diff.json for review.

Standard library only.
"""

from __future__ import annotations

import argparse
import difflib
import hashlib
import json
import shutil
import subprocess
import sys
from datetime import datetime
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
GO_HIVE = REPO_ROOT / "go_hive"
EVIDENCE_DIR = GO_HIVE / "docs" / "e2e-assets" / "sse-payload-parity"
FRAMES_DIR = GO_HIVE / "contracts" / "sse-frames"
PROBE = GO_HIVE / "scripts" / "sse_payload_parity.py"

SCHEMA = "hive-sse-frames/1"
DIFF_SCHEMA = "hive-sse-frames-diff/1"
STREAMS = ("live", "replay", "history", "stop")

# Implementation detail of the Go port: one bounded agent loop per chat turn
# emits this envelope, while the Python queen runs one permanent node loop and
# emits it only once in the session-start block.
GO_ONLY_ENVELOPE = (
    "execution_started",
    "execution_completed",
    "execution_failed",
    "execution_paused",
    "node_loop_started",
    "node_loop_completed",
)

# Async producers on both sides: the title job races the turn tail, so only its
# payload and multiplicity are contractual, never its position.
ASYNC_POSITION_TYPES = ("session_title_updated",)

ENVELOPE_FIELDS = (
    "stream_id",
    "node_id",
    "execution_id",
    "correlation_id",
    "colony_id",
    "run_id",
    "seq",
    "timestamp",
)

# Volatile leaf keys; the probe already replaces most of them at capture time,
# but the canonicaliser is also the guard for captures produced elsewhere.
VOLATILE_KEYS = {
    "seq",
    "snapshot_seq",
    "last_event_seq",
    "event_seq",
    "tool_use_id",
    "call_id",
    "timestamp",
    "started_at",
    "finished_at",
    "completed_at",
    "created_at",
    "updated_at",
    "id",
    "session_id",
    "execution_id",
    "colony_id",
    "correlation_id",
    "run_id",
    "loaded_at",
    "uptime_seconds",
    "port",
    "pid",
    "path",
    "agent_path",
    "home",
    "root",
    "cwd",
    "duration_ms",
    "elapsed_ms",
    "latency_ms",
    "took_ms",
    "wall_ms",
}

MASK = "<exception>"

# Declared residual differences. Every entry must be observed in the golden
# captures (a stale entry fails --check), and masking it must leave the frame
# sequences identical; anything else is an undeclared difference.
EXCEPTIONS: list[dict[str, Any]] = [
    # ``loop-max-iterations`` and ``system-prefix-sha`` retired by the
    # api-value-probes lane: the opening node_loop_started now carries the
    # reference's persistent-loop bound (999999, or HIVE_MAX_ITER when capped;
    # the Go-only per-turn frames are dropped by the envelope transform), and
    # the static system prompt now hashes equal (692b774c98c7 on both sides).
    # Both paths are compared strictly again — a re-divergence fails the gate.
    {
        "id": "loop-iteration-counter",
        "frame_type": "node_loop_iteration",
        "path": "data.iteration",
        "class": "intentional-structural",
        "reason": (
            "Python counts iterations across the whole session; Go restarts the "
            "per-turn loop, so its iteration resets each turn."
        ),
    },
    {
        "id": "turn-complete-iteration",
        "frame_type": "llm_turn_complete",
        "path": "data.iteration",
        "class": "intentional-structural",
        "reason": "Same counter as loop-iteration-counter: session-cumulative vs per-turn.",
    },
    {
        "id": "output-delta-iteration",
        "frame_type": "client_output_delta",
        "path": "data.iteration",
        "class": "intentional-structural",
        "reason": "Same counter as loop-iteration-counter: session-cumulative vs per-turn.",
    },
    {
        "id": "model-provider-prefix",
        "frame_type": "llm_turn_complete",
        "path": "data.model",
        "class": "value-level",
        "reason": (
            "Python reports the litellm-normalised name (provider/model); Go "
            "reports the configured model string without the provider prefix."
        ),
    },
    {
        "id": "context-usage-ratio",
        "frame_type": "context_usage_updated",
        "path": "data.usage_ratio",
        "class": "value-level",
        "reason": "Derived from the prompt/tool-definition char counts, which still differ.",
    },
    {
        "id": "context-usage-pct",
        "frame_type": "context_usage_updated",
        "path": "data.usage_pct",
        "class": "value-level",
        "reason": "Rounded usage_ratio; see context-usage-ratio.",
    },
    {
        "id": "context-usage-estimated-tokens",
        "frame_type": "context_usage_updated",
        "path": "data.estimated_tokens",
        "class": "value-level",
        "reason": "Char-based projection of the still-divergent prompt and tool definitions.",
    },
    {
        "id": "context-usage-message-count",
        "frame_type": "context_usage_updated",
        "path": "data.message_count",
        "class": "value-level",
        "reason": (
            "The opening readout sees one seeded message on the Python side and "
            "zero on the Go side; every later readout matches (pinned by the "
            "post_tool_call/post_tool_results frame positions)."
        ),
    },
    {
        "id": "context-usage-breakdown",
        "frame_type": "context_usage_updated",
        "path": "data.breakdown.*",
        "class": "value-level",
        "reason": (
            "Component char counts of the prompt/tool definitions; the system "
            "prompt and tool JSON still differ (prompt/tool parity lanes)."
        ),
    },
]


# ---------------------------------------------------------------------------
# canonicalisation
# ---------------------------------------------------------------------------


class GoldenError(RuntimeError):
    pass


def now_iso() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    digest.update(path.read_bytes())
    return digest.hexdigest()


def canonicalize_value(value: Any, key: str = "") -> Any:
    """Replace volatile leaf values, preserving structure, presence and types."""
    if isinstance(value, dict):
        return {name: canonicalize_value(item, name) for name, item in value.items()}
    if isinstance(value, list):
        return [canonicalize_value(item, key) for item in value]
    volatile = key in VOLATILE_KEYS or key.endswith("_at")
    if volatile and value is not None:
        return f"<{key}>"
    return value


def canonicalize_frame(frame: dict[str, Any], stream: str, index: int) -> dict[str, Any]:
    envelope = {}
    for field in ENVELOPE_FIELDS:
        if field in ("execution_id", "correlation_id", "run_id"):
            if field == "run_id":
                envelope[field] = "<run_id>" if "run_id" in frame else "<absent>"
            else:
                envelope[field] = f"<{field}>" if frame.get(field) is not None else None
            continue
        if field == "seq":
            envelope[field] = "<seq>" if frame.get(field) is not None else None
            continue
        if field == "timestamp":
            envelope[field] = "<timestamp>"
            continue
        envelope[field] = canonicalize_value(frame.get(field), field)
    return {
        "stream": stream,
        "index": index,
        "event": frame.get("event"),
        "type": frame.get("type"),
        "envelope": envelope,
        "data": canonicalize_value(frame.get("data") or {}),
    }


def load_capture(path: Path) -> dict[str, Any]:
    if not path.is_file():
        raise GoldenError(f"capture missing: {path} (run with --refresh first)")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise GoldenError(f"capture is not a JSON object: {path}")
    return data


def capture_frames(side: dict[str, Any], stream: str) -> list[dict[str, Any]]:
    if stream == "history":
        frames = side.get("history")
    else:
        captures = side.get("captures") or {}
        frames = (captures.get(stream) or {}).get("frames")
    if not isinstance(frames, list):
        raise GoldenError(f"capture has no {stream} frames for side {side.get('side')!r}")
    return frames


def canonical_stream(side: dict[str, Any], stream: str) -> list[dict[str, Any]]:
    return [
        canonicalize_frame(frame, stream, index)
        for index, frame in enumerate(capture_frames(side, stream))
    ]


# ---------------------------------------------------------------------------
# documented transforms
# ---------------------------------------------------------------------------


def first_index(frames: list[dict[str, Any]], event_type: str) -> int | None:
    for index, frame in enumerate(frames):
        if frame.get("type") == event_type:
            return index
    return None


def split_core(
    stream: str, frames: list[dict[str, Any]], side: str
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Apply the documented structural transform and describe what was dropped.

    Go's per-turn loop envelope only appears after the first client input; the
    session-start block (which precedes it) belongs to both implementations.
    """
    cutoff = first_index(frames, "client_input_received")
    if cutoff is None:
        cutoff = 0
    kept: list[dict[str, Any]] = []
    dropped_envelope: list[dict[str, Any]] = []
    async_frames: list[dict[str, Any]] = []
    for index, frame in enumerate(frames):
        if index > cutoff and frame.get("type") in GO_ONLY_ENVELOPE:
            dropped_envelope.append({"index": index, "type": frame.get("type")})
            continue
        if frame.get("type") in ASYNC_POSITION_TYPES:
            async_frames.append(frame)
            continue
        kept.append(frame)
    transform = {
        "side": side,
        "stream": stream,
        "client_input_cutoff": cutoff,
        "dropped_go_envelope": dropped_envelope,
        "async_position_frames": [
            {"index": frame.get("index"), "type": frame.get("type")} for frame in async_frames
        ],
        "frames_before": len(frames),
        "frames_after": len(kept),
    }
    return kept, transform


# ---------------------------------------------------------------------------
# exceptions
# ---------------------------------------------------------------------------


def mask_subtree(value: Any) -> Any:
    if isinstance(value, dict):
        return {key: mask_subtree(item) for key, item in value.items()}
    if isinstance(value, list):
        return [mask_subtree(item) for item in value]
    return MASK


def leaf_count(value: Any) -> int:
    if isinstance(value, dict):
        return sum(leaf_count(item) for item in value.values())
    if isinstance(value, list):
        return sum(leaf_count(item) for item in value)
    return 1


def mask_at(value: Any, segments: list[str]) -> tuple[Any, int]:
    """Replace the value at ``segments`` with MASK; return (value, leaves).

    ``*`` as a segment masks every leaf of the remaining subtree while keeping
    the container shape, so a missing/extra key still shows up as a diff.
    """
    if not segments:
        return mask_subtree(value), leaf_count(value)
    head, rest = segments[0], segments[1:]
    if head == "*":
        return mask_subtree(value), leaf_count(value)
    is_array = head.endswith("[]")
    key = head[:-2] if is_array else head
    if not isinstance(value, dict) or key not in value:
        return value, 0
    child = value[key]
    if is_array:
        if not isinstance(child, list):
            return value, 0
        masked: list[Any] = []
        count = 0
        for item in child:
            masked_item, item_count = mask_at(item, rest)
            masked.append(masked_item)
            count += item_count
        if count == 0:
            return value, 0
        out = dict(value)
        out[key] = masked
        return out, count
    masked_child, count = mask_at(child, rest)
    if count == 0:
        return value, 0
    out = dict(value)
    out[key] = masked_child
    return out, count


def collect_leaves(value: Any, out: list[Any]) -> None:
    if isinstance(value, dict):
        for item in value.values():
            collect_leaves(item, out)
        return
    if isinstance(value, list):
        for item in value:
            collect_leaves(item, out)
        return
    out.append(value)


def collect_at(value: Any, segments: list[str], out: list[Any]) -> None:
    if not segments or segments[0] == "*":
        collect_leaves(value, out)
        return
    head, rest = segments[0], segments[1:]
    is_array = head.endswith("[]")
    key = head[:-2] if is_array else head
    if not isinstance(value, dict) or key not in value:
        return
    child = value[key]
    if is_array and isinstance(child, list):
        for item in child:
            collect_at(item, rest, out)
        return
    if not is_array:
        collect_at(child, rest, out)


def exception_segments(exception: dict[str, Any]) -> list[str]:
    return str(exception["path"]).split(".")


def masked_frame(frame: dict[str, Any]) -> dict[str, Any]:
    out = frame
    for exception in EXCEPTIONS:
        if out.get("type") != exception["frame_type"]:
            continue
        out, _ = mask_at(out, exception_segments(exception))
    return out


def frame_signature(frame: dict[str, Any]) -> str:
    payload = {
        "type": frame.get("type"),
        "event": frame.get("event"),
        "envelope": frame.get("envelope"),
        "data": frame.get("data"),
    }
    return json.dumps(payload, sort_keys=True, ensure_ascii=False)


def exception_report(sides: dict[str, list[dict[str, Any]]]) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for exception in EXCEPTIONS:
        row: dict[str, Any] = {
            "id": exception["id"],
            "frame_type": exception["frame_type"],
            "path": exception["path"],
            "class": exception["class"],
            "reason": exception["reason"],
        }
        observed: dict[str, list[str]] = {}
        for side, frames in sides.items():
            values: list[Any] = []
            matched = 0
            for frame in frames:
                if frame.get("type") != exception["frame_type"]:
                    continue
                found: list[Any] = []
                collect_at(frame, exception_segments(exception), found)
                if found:
                    matched += 1
                    values.extend(found)
            observed[side] = [json.dumps(value, ensure_ascii=False) for value in values]
            row[f"{side}_frames_matched"] = matched
        row["reference_values"] = sorted(set(observed.get("reference", [])))
        row["target_values"] = sorted(set(observed.get("target", [])))
        row["values_equal_this_run"] = row["reference_values"] == row["target_values"]
        row["observed"] = bool(row["reference_frames_matched"] or row["target_frames_matched"])
        rows.append(row)
    return rows


# ---------------------------------------------------------------------------
# comparison
# ---------------------------------------------------------------------------


def summary_ops(reference: list[dict[str, Any]], target: list[dict[str, Any]]) -> list[dict[str, Any]]:
    ref_types = [str(frame.get("type")) for frame in reference]
    tgt_types = [str(frame.get("type")) for frame in target]
    return [
        {
            "op": op,
            "reference": ref_types[i1:i2],
            "target": tgt_types[j1:j2],
        }
        for op, i1, i2, j1, j2 in difflib.SequenceMatcher(a=ref_types, b=tgt_types).get_opcodes()
        if op != "equal"
    ]


def diff_stream(
    stream: str,
    reference: list[dict[str, Any]],
    target: list[dict[str, Any]],
) -> dict[str, Any]:
    ref_masked = [masked_frame(frame) for frame in reference]
    tgt_masked = [masked_frame(frame) for frame in target]
    ref_signatures = [frame_signature(frame) for frame in ref_masked]
    tgt_signatures = [frame_signature(frame) for frame in tgt_masked]
    equal = ref_signatures == tgt_signatures
    undeclared: list[dict[str, Any]] = []
    if not equal:
        matcher = difflib.SequenceMatcher(a=ref_signatures, b=tgt_signatures)
        for op, i1, i2, j1, j2 in matcher.get_opcodes():
            if op == "equal":
                continue
            for offset in range(max(i2 - i1, j2 - j1)):
                ref_frame = reference[i1 + offset] if i1 + offset < i2 else None
                tgt_frame = target[j1 + offset] if j1 + offset < j2 else None
                undeclared.append(
                    {
                        "op": op,
                        "reference": None if ref_frame is None else compact_frame(ref_frame),
                        "target": None if tgt_frame is None else compact_frame(tgt_frame),
                    }
                )
                if len(undeclared) >= 20:
                    break
            if len(undeclared) >= 20:
                break
    return {
        "stream": stream,
        "reference_frames": len(reference),
        "target_frames": len(target),
        "equal_after_documented_transforms": equal,
        "reference_types": [frame.get("type") for frame in reference],
        "target_types": [frame.get("type") for frame in target],
        "sequence_ops": summary_ops(reference, target),
        "undeclared_diffs": undeclared,
    }


def compact_frame(frame: dict[str, Any]) -> dict[str, Any]:
    return {
        "type": frame.get("type"),
        "index": frame.get("index"),
        "envelope": frame.get("envelope"),
        "data": frame.get("data"),
    }


def is_subsequence(needles: list[dict[str, Any]], haystack: list[dict[str, Any]]) -> bool:
    cursor = 0
    for needle in needles:
        signature = frame_signature(masked_frame(needle))
        found = False
        while cursor < len(haystack):
            candidate = haystack[cursor]
            cursor += 1
            if frame_signature(masked_frame(candidate)) == signature:
                found = True
                break
        if not found:
            return False
    return True


def judge_rows(frames: list[dict[str, Any]]) -> list[dict[str, Any]]:
    rows = []
    for frame in frames:
        if frame.get("type") != "judge_verdict":
            continue
        data = frame.get("data") or {}
        rows.append(
            {
                key: data.get(key)
                for key in ("action", "feedback", "judge_type", "iteration")
            }
        )
    return rows


def build_assertions(
    cores: dict[str, dict[str, list[dict[str, Any]]]],
    canonical: dict[str, dict[str, list[dict[str, Any]]]],
    stream_diffs: dict[str, dict[str, Any]],
    exceptions: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    assertions: list[dict[str, Any]] = []

    for stream in ("live", "replay", "history"):
        assertions.append(
            {
                "id": f"{stream}-core-equal",
                "required": True,
                "ok": stream_diffs[stream]["equal_after_documented_transforms"],
                "detail": (
                    f"{stream}: {stream_diffs[stream]['reference_frames']} reference vs "
                    f"{stream_diffs[stream]['target_frames']} target frames after the documented transforms"
                ),
            }
        )

    # Shutdown tail: DELETE closes the session while the permanent Python loop
    # may still publish one last state frame; Go's bounded turn is already
    # closed. Recorded, not asserted.
    stop_ok = stream_diffs["stop"]["equal_after_documented_transforms"]
    if not stop_ok and len(stream_diffs["stop"]["sequence_ops"]) == 1:
        op = stream_diffs["stop"]["sequence_ops"][0]
        stop_ok = op["op"] == "delete" and op["target"] == []
    assertions.append(
        {
            "id": "stop-tail-informational",
            "required": False,
            "ok": stop_ok,
            "detail": (
                "DELETE is a shutdown tail: the reference permanent loop may publish a "
                "final loop_state_changed after the last parked state; Go has already "
                "closed its bounded turn. Canonical frames are stored, order is not contractual."
            ),
        }
    )

    replay_first = {
        side: (cores[side]["replay"][0]["type"] if cores[side]["replay"] else None)
        for side in ("reference", "target")
    }
    assertions.append(
        {
            "id": "replay-first-frame-snapshot",
            "required": True,
            "ok": replay_first["reference"] == "session_snapshot" and replay_first["target"] == "session_snapshot",
            "detail": f"first replayed frame: reference={replay_first['reference']!r} target={replay_first['target']!r}",
        }
    )

    for side in ("reference", "target"):
        assertions.append(
            {
                "id": f"replay-subsequence-of-history-{side}",
                "required": True,
                "ok": is_subsequence(cores[side]["replay"][1:], cores[side]["history"]),
                "detail": f"{side}: every replayed frame appears in history, in order",
            }
        )

    ref_judge = judge_rows(cores["reference"]["history"])
    tgt_judge = judge_rows(cores["target"]["history"])
    assertions.append(
        {
            "id": "judge-verdicts-equal",
            "required": True,
            "ok": ref_judge == tgt_judge,
            "detail": f"reference={ref_judge} target={tgt_judge}",
        }
    )

    async_signatures: dict[str, list[str]] = {}
    async_positions: dict[str, list[dict[str, Any]]] = {}
    for side in ("reference", "target"):
        signatures: list[str] = []
        positions: list[dict[str, Any]] = []
        for stream in ("live", "replay", "history", "stop"):
            for frame in canonical[side].get(stream, []):
                if frame.get("type") in ASYNC_POSITION_TYPES:
                    signatures.append(frame_signature(masked_frame(frame)))
                    positions.append({"stream": stream, "index": frame.get("index"), "type": frame.get("type")})
        async_signatures[side] = sorted(signatures)
        async_positions[side] = positions
    assertions.append(
        {
            "id": "async-title-multiset-equal",
            "required": True,
            "ok": async_signatures["reference"] == async_signatures["target"] and len(async_signatures["target"]) > 0,
            "detail": (
                "session_title_updated payload/multiplicity match; positions are non-contract "
                f"(reference={async_positions['reference']} target={async_positions['target']})"
            ),
        }
    )

    assertions.append(
        {
            "id": "exceptions-observed",
            "required": True,
            "ok": all(row["observed"] for row in exceptions),
            "detail": "; ".join(
                f"{row['id']}: ref={row['reference_frames_matched']} tgt={row['target_frames_matched']}"
                for row in exceptions
            ),
        }
    )
    return assertions


def verdict(assertions: list[dict[str, Any]], stream_diffs: dict[str, dict[str, Any]]) -> str:
    for assertion in assertions:
        if assertion["required"] and not assertion["ok"]:
            return "FAIL"
    for stream in ("live", "replay", "history"):
        if not stream_diffs[stream]["equal_after_documented_transforms"]:
            return "FAIL"
    return "PASS"


# ---------------------------------------------------------------------------
# build / write
# ---------------------------------------------------------------------------


def build_golden(capture_paths: dict[str, Path]) -> dict[str, Any]:
    captures = {side: load_capture(path) for side, path in capture_paths.items()}
    canonical: dict[str, dict[str, list[dict[str, Any]]]] = {}
    cores: dict[str, dict[str, list[dict[str, Any]]]] = {}
    transforms: dict[str, dict[str, Any]] = {}
    for side in ("reference", "target"):
        canonical[side] = {}
        cores[side] = {}
        transforms[side] = {}
        for stream in STREAMS:
            frames = canonical_stream(captures[side], stream)
            canonical[side][stream] = frames
            core, transform = split_core(stream, frames, side)
            cores[side][stream] = core
            transforms[side][stream] = transform

    exceptions = exception_report(
        {side: [frame for stream in STREAMS for frame in cores[side][stream]] for side in ("reference", "target")}
    )
    stream_diffs = {
        stream: diff_stream(stream, cores["reference"][stream], cores["target"][stream])
        for stream in STREAMS
    }
    assertions = build_assertions(cores, canonical, stream_diffs, exceptions)
    overall = verdict(assertions, stream_diffs)

    totals = {
        "frames_reference": sum(len(canonical["reference"][stream]) for stream in STREAMS),
        "frames_target": sum(len(canonical["target"][stream]) for stream in STREAMS),
        "core_frames_reference": sum(len(cores["reference"][stream]) for stream in STREAMS),
        "core_frames_target": sum(len(cores["target"][stream]) for stream in STREAMS),
        "dropped_go_envelope": sum(
            len(transforms["target"][stream]["dropped_go_envelope"]) for stream in STREAMS
        ),
    }
    diff = {
        "schema": DIFF_SCHEMA,
        "generated_at": now_iso(),
        "reference": {
            "implementation": captures["reference"].get("implementation"),
            "version": captures["reference"].get("version"),
        },
        "target": {
            "implementation": captures["target"].get("implementation"),
            "version": captures["target"].get("version"),
        },
        "transforms": {
            "go_only_envelope_types": list(GO_ONLY_ENVELOPE),
            "async_position_types": list(ASYNC_POSITION_TYPES),
            "rule": (
                "Drop Go-only envelope frames that appear after the first "
                "client_input_received (the session-start block precedes it); keep "
                "everything else in captured order."
            ),
        },
        "streams": stream_diffs,
        "exceptions": exceptions,
        "assertions": assertions,
        "totals": totals,
        "verdict": overall,
        "detail": transforms,
    }
    index = {
        "schema": SCHEMA,
        "generated_at": now_iso(),
        "scenario": (
            "create queen session -> opening turn settles -> subscribe -> text turn -> "
            "ask_user turn -> answer -> reconnect replay -> stop"
        ),
        "captures": {
            side: {
                "file": str(capture_paths[side].relative_to(REPO_ROOT)),
                "sha256": sha256_file(capture_paths[side]),
            }
            for side in ("reference", "target")
        },
        "reference": diff["reference"],
        "target": diff["target"],
        "streams": {
            stream: {
                "reference_frames": len(canonical["reference"][stream]),
                "target_frames": len(canonical["target"][stream]),
                "reference_core_frames": len(cores["reference"][stream]),
                "target_core_frames": len(cores["target"][stream]),
            }
            for stream in STREAMS
        },
        "canonical_rules": [
            "volatile leaves (seq, ids, timestamps, durations) -> <key> placeholders; presence and type kept",
            "frame order, event names, field sets and enum values kept verbatim",
            "numeric values kept verbatim (int vs float spelling is observable)",
            "session_title_updated position is non-contract; payload and multiplicity are",
        ],
        "transforms": diff["transforms"],
        "totals": totals,
        "verdict": overall,
    }
    return {
        "captures": captures,
        "canonical": canonical,
        "cores": cores,
        "diff": diff,
        "index": index,
        "summary": render_summary(diff, index),
    }


def render_summary(diff: dict[str, Any], index: dict[str, Any]) -> str:
    lines: list[str] = []
    lines.append("SSE frame golden — Python reference vs Go target")
    lines.append(f"generated: {diff['generated_at']}")
    lines.append(
        f"reference: {diff['reference']['version']}    target: {diff['target']['version']}"
    )
    totals = diff["totals"]
    lines.append(
        f"frames: reference={totals['frames_reference']} target={totals['frames_target']}    "
        f"core: reference={totals['core_frames_reference']} target={totals['core_frames_target']}    "
        f"dropped Go envelope={totals['dropped_go_envelope']}"
    )
    lines.append("")
    lines.append("== stream comparison (after documented transforms) ==")
    for stream, entry in diff["streams"].items():
        status = "EQUAL" if entry["equal_after_documented_transforms"] else "DIFF"
        lines.append(
            f"{stream:8s} {status:5s} reference={entry['reference_frames']:3d} target={entry['target_frames']:3d}"
        )
        for op in entry["sequence_ops"]:
            lines.append(f"    {op['op']}: reference={op['reference']} target={op['target']}")
    lines.append("")
    lines.append("== assertions ==")
    for assertion in diff["assertions"]:
        mark = "PASS" if assertion["ok"] else ("SKIP" if not assertion["required"] else "FAIL")
        lines.append(f"{mark:4s} {assertion['id']}: {assertion['detail']}")
    lines.append("")
    lines.append("== declared value-level / structural exceptions ==")
    for row in diff["exceptions"]:
        lines.append(
            f"- {row['id']} ({row['class']}): {row['frame_type']}.{row['path']}"
        )
        lines.append(f"    reference={row['reference_values']} target={row['target_values']}")
        lines.append(f"    {row['reason']}")
    lines.append("")
    lines.append(f"VERDICT: {diff['verdict']}")
    return "\n".join(lines) + "\n"


def golden_payload(side: str, stream: str, capture: dict[str, Any], frames: list[dict[str, Any]]) -> dict[str, Any]:
    return {
        "schema": SCHEMA,
        "side": side,
        "stream": stream,
        "implementation": capture.get("implementation"),
        "version": capture.get("version"),
        "frame_count": len(frames),
        "frames": frames,
    }


def write_golden(bundle: dict[str, Any], out_dir: Path) -> None:
    out_dir.mkdir(parents=True, exist_ok=True)
    for side in ("reference", "target"):
        side_dir = out_dir / side
        side_dir.mkdir(parents=True, exist_ok=True)
        for stream in STREAMS:
            payload = golden_payload(
                side,
                stream,
                bundle["captures"][side],
                bundle["canonical"][side][stream],
            )
            (side_dir / f"{stream}.json").write_text(
                json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
                encoding="utf-8",
            )
    (out_dir / "diff.json").write_text(
        json.dumps(bundle["diff"], ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    (out_dir / "index.json").write_text(
        json.dumps(bundle["index"], ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    (out_dir / "summary.txt").write_text(bundle["summary"], encoding="utf-8")


# ---------------------------------------------------------------------------
# check
# ---------------------------------------------------------------------------


def strip_volatile(document: dict[str, Any]) -> dict[str, Any]:
    out = dict(document)
    out.pop("generated_at", None)
    return out


def render(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True)


def check_golden(bundle: dict[str, Any], out_dir: Path) -> list[str]:
    problems: list[str] = []
    for side in ("reference", "target"):
        for stream in STREAMS:
            path = out_dir / side / f"{stream}.json"
            if not path.is_file():
                problems.append(f"missing golden file: {path}")
                continue
            committed = json.loads(path.read_text(encoding="utf-8"))
            expected = golden_payload(
                side,
                stream,
                bundle["captures"][side],
                bundle["canonical"][side][stream],
            )
            if render(committed) != render(expected):
                problems.append(f"golden drift: {path} (recapture with --refresh)")
    for name, expected in (("diff.json", bundle["diff"]), ("index.json", bundle["index"])):
        path = out_dir / name
        if not path.is_file():
            problems.append(f"missing golden file: {path}")
            continue
        committed = json.loads(path.read_text(encoding="utf-8"))
        if render(strip_volatile(committed)) != render(strip_volatile(expected)):
            problems.append(f"golden drift: {path} (recapture with --refresh)")
    summary_path = out_dir / "summary.txt"
    if not summary_path.is_file():
        problems.append(f"missing golden file: {summary_path}")
    if bundle["diff"]["verdict"] != "PASS":
        problems.append(f"assertion verdict is {bundle['diff']['verdict']}")
        for assertion in bundle["diff"]["assertions"]:
            if assertion["required"] and not assertion["ok"]:
                problems.append(f"failed assertion {assertion['id']}: {assertion['detail']}")
        for stream in ("live", "replay", "history"):
            entry = bundle["diff"]["streams"][stream]
            if not entry["equal_after_documented_transforms"]:
                problems.append(f"undeclared {stream} differences: {entry['undeclared_diffs']}")
    return problems


# ---------------------------------------------------------------------------
# refresh
# ---------------------------------------------------------------------------


def refresh_captures(captures_dir: Path) -> None:
    """Re-run the dual-run probe, forcing a fresh Go binary.

    sse_payload_parity.py reuses dist/hive-sse-parity when it exists, so a
    stale binary would silently freeze the previous build. Delete it first.
    """
    try:
        sys.path.insert(0, str(PROBE.parent))
        import sse_payload_parity as parity  # noqa: PLC0415 - optional refresh path
    except Exception as exc:  # noqa: BLE001 - report, do not mask
        raise GoldenError(f"cannot import {PROBE}: {exc}") from exc
    binary = getattr(parity, "GO_BINARY", None)
    if binary is not None and Path(binary).is_file():
        Path(binary).unlink()
    captures_dir.mkdir(parents=True, exist_ok=True)
    command = [sys.executable, str(PROBE), "--out", str(captures_dir)]
    print(f"[sse-golden] refreshing captures: {' '.join(command)}")
    proc = subprocess.run(command, cwd=str(REPO_ROOT), text=True)
    if proc.returncode != 0:
        raise GoldenError(f"sse_payload_parity.py exited {proc.returncode}")


# ---------------------------------------------------------------------------
# entry point
# ---------------------------------------------------------------------------


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Freeze the session SSE frame golden")
    parser.add_argument("--captures", default=str(EVIDENCE_DIR), help="capture directory")
    parser.add_argument("--out", default=str(FRAMES_DIR), help="golden output directory")
    parser.add_argument(
        "--refresh",
        action="store_true",
        help="re-run sse_payload_parity.py (fresh Go binary) before writing the golden",
    )
    parser.add_argument("--check", action="store_true", help="verify the committed golden; never writes")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    captures_dir = Path(args.captures).expanduser().resolve()
    out_dir = Path(args.out).expanduser().resolve()
    try:
        if args.refresh:
            refresh_captures(captures_dir)
        capture_paths = {
            side: captures_dir / f"capture-{side}.json" for side in ("reference", "target")
        }
        bundle = build_golden(capture_paths)
        if args.check:
            problems = check_golden(bundle, out_dir)
            if problems:
                print("SSE frame golden --check FAILED")
                for problem in problems:
                    print(f"  - {problem}")
                return 1
            print(
                "SSE frame golden --check OK: "
                f"{bundle['diff']['totals']['frames_reference']} reference / "
                f"{bundle['diff']['totals']['frames_target']} target frames, "
                f"{len(bundle['diff']['exceptions'])} declared exceptions, "
                f"verdict={bundle['diff']['verdict']}"
            )
            return 0
        write_golden(bundle, out_dir)
        print(bundle["summary"])
        print(f"[sse-golden] wrote {out_dir}")
        return 0 if bundle["diff"]["verdict"] == "PASS" else 1
    except GoldenError as exc:
        print(f"[sse-golden] {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
