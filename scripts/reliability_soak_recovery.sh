#!/usr/bin/env bash
# reliability_soak_recovery.sh — repeatable real-process reliability evidence.
#
# Two axes, one real `hive serve` process tree built from this checkout:
#   A. crash recovery: create a session, start a long terminal task, `kill -9`
#      the server mid-turn, restart it, then verify the conversation, the
#      completed tool results and a parked ask_user question all come back from
#      disk and that the session keeps working.
#   B. concurrency soak: 10 live sessions x 5 real-LLM turns each with at most
#      3 sessions active at a time; verify 0 request/turn errors, no deadlock
#      (outside health poller), bounded RSS, and no dropped SSE boundary events
#      (every persisted execution_completed id must reach the subscriber).
#
# Usage:
#   go_hive/scripts/reliability_soak_recovery.sh
#   SOAK_SESSIONS=2 SOAK_ROUNDS=2 SOAK_CONCURRENCY=2 RELIABILITY_PORT=8801 \
#     go_hive/scripts/reliability_soak_recovery.sh          # fast smoke run
#
# Env overrides:
#   RELIABILITY_WORK    scratch dir for the binary + disposable HIVE_HOME
#                       (default: mktemp -d; session data stays out of the repo)
#   RELIABILITY_OUT     evidence dir
#                       (default: go_hive/docs/e2e-assets/reliability-2026-11-22)
#   RELIABILITY_PORT    listen port (default 8797; the next free port is used
#                       when it is taken)
#   SOAK_SESSIONS=10 SOAK_ROUNDS=5 SOAK_CONCURRENCY=3
#   HIVE_ENV_FILE       key env file (default ~/.hive-go/env, sourced silently)
#   HIVE_CONFIG_JSON    LLM config copied into the disposable home
#                       (default ~/.hive-go/configuration.json)
#
# The run uses the real provider configured by HIVE_CONFIG_JSON and refuses to
# start when the referenced api_key_env_var is empty.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
GODIR="$REPO_DIR/go-hive"
OUT="${RELIABILITY_OUT:-$REPO_DIR/docs/e2e-assets/reliability-2026-11-22}"
WORK="${RELIABILITY_WORK:-$(mktemp -d "${TMPDIR:-/tmp}/hive-reliability.XXXXXX")}"
PORT="${RELIABILITY_PORT:-8797}"
SOAK_SESSIONS="${SOAK_SESSIONS:-10}"
SOAK_ROUNDS="${SOAK_ROUNDS:-5}"
SOAK_CONCURRENCY="${SOAK_CONCURRENCY:-3}"
HIVE_ENV_FILE="${HIVE_ENV_FILE:-$HOME/.hive-go/env}"
HIVE_CONFIG_JSON="${HIVE_CONFIG_JSON:-$HOME/.hive-go/configuration.json}"

HIVE_HOME_DIR="$WORK/hive-home"
BIN="$WORK/hive"
HELPER="$WORK/rel_helper.py"
BASE=""
SERVE_PID=""

PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0

say() { printf '%s\n' "$*"; }
section() { printf '\n=== %s ===\n' "$*"; }

now_iso() { date '+%Y-%m-%dT%H:%M:%S%z'; }

record() { # record <PASS|FAIL|SKIP> <name> <detail...>
  local status="$1" name="$2"; shift 2
  local detail="$*"
  detail="${detail//$'\t'/ }"
  detail="${detail//$'\n'/ }"
  printf '%s\t%s\t%s\n' "$status" "$name" "$detail" >> "$OUT/checks.tsv"
  case "$status" in
    PASS) PASS_COUNT=$((PASS_COUNT + 1));;
    FAIL) FAIL_COUNT=$((FAIL_COUNT + 1));;
    SKIP) SKIP_COUNT=$((SKIP_COUNT + 1));;
  esac
  printf '[%s] %s%s\n' "$status" "$name" "${detail:+ — $detail}"
}

die() {
  say "FATAL: $*"
  record FAIL fatal "$*"
  exit 2
}

port_free() {
  python3 - "$1" <<'PY'
import socket, sys
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
try:
    s.bind(("127.0.0.1", int(sys.argv[1])))
except OSError:
    sys.exit(1)
finally:
    s.close()
PY
}

pick_port() {
  local p="$1"
  while [ "$p" -lt 8899 ]; do
    if port_free "$p"; then printf '%s' "$p"; return 0; fi
    p=$((p + 1))
  done
  return 1
}

json_field() { # json_field <file> <python-expression over d>
  python3 - "$1" "$2" <<'PY'
import json, sys
path, expr = sys.argv[1], sys.argv[2]
try:
    d = json.load(open(path, encoding="utf-8"))
except Exception:
    print("")
    raise SystemExit(0)
try:
    v = eval(expr, {"d": d})  # harness-fixed expressions only
    print("" if v is None else v)
except Exception:
    print("")
PY
}

write_helper() {
  cat > "$HELPER" <<'PY'
#!/usr/bin/env python3
"""Read-only probes over a Hive session directory (reliability harness helper)."""
import glob
import hashlib
import json
import os
import re
import sys
import time
from collections import Counter
from datetime import datetime


def session_dir(home, sid):
    hits = glob.glob(os.path.join(home, "queens", "*", "sessions", sid))
    return hits[0] if hits else ""


def parts(d):
    pdir = os.path.join(d, "conversations", "parts")
    out = []
    if os.path.isdir(pdir):
        for name in sorted(os.listdir(pdir)):
            if not name.endswith(".json"):
                continue
            try:
                with open(os.path.join(pdir, name), encoding="utf-8") as fh:
                    out.append((name, json.load(fh)))
            except Exception:
                continue
    return out


def events(d):
    path = os.path.join(d, "events.jsonl")
    out = []
    if os.path.exists(path):
        with open(path, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except Exception:
                    continue
    return out


def cursor(d):
    path = os.path.join(d, "conversations", "cursor.json")
    try:
        with open(path, encoding="utf-8") as fh:
            data = json.load(fh)
        return data if isinstance(data, dict) else {}
    except Exception:
        return {}


def dangling(plist):
    calls, results = set(), set()
    for _, p in plist:
        if p.get("role") == "assistant":
            for tc in p.get("tool_calls") or []:
                if isinstance(tc, dict) and tc.get("id"):
                    calls.add(tc["id"])
        if p.get("role") == "tool" and p.get("tool_use_id"):
            results.add(p["tool_use_id"])
    return sorted(calls - results)


def event_counts(evs):
    c = Counter(e.get("type") for e in evs)
    return {
        "execution_started": c.get("execution_started", 0),
        "execution_completed": c.get("execution_completed", 0),
        "execution_failed": c.get("execution_failed", 0),
        "tool_call_started": c.get("tool_call_started", 0),
        "tool_call_completed": c.get("tool_call_completed", 0),
        "client_input_received": c.get("client_input_received", 0),
        "client_input_committed": c.get("client_input_committed", 0),
    }


def emit(obj, code=0):
    print(json.dumps(obj, ensure_ascii=False, sort_keys=True))
    sys.exit(code)


def last_assistant_text(d):
    best = ""
    for _, p in parts(d):
        if p.get("role") == "assistant":
            best = (p.get("content") or "").strip()
    return best


def cmd_session_dir(home, sid):
    d = session_dir(home, sid)
    if not d:
        sys.exit(1)
    print(d)


def cmd_parts(d, *needles):
    plist = parts(d)
    roles = Counter(p.get("role") for _, p in plist)
    pdir = os.path.join(d, "conversations", "parts")
    hashes = {}
    for name, _ in plist:
        with open(os.path.join(pdir, name), "rb") as fh:
            hashes[name] = hashlib.sha256(fh.read()).hexdigest()
    found = {n: False for n in needles}
    for _, p in plist:
        text = p.get("content") or ""
        for n in needles:
            if n and n in text:
                found[n] = True
    emit({"count": len(plist), "roles": dict(roles), "sha256": hashes,
          "dangling": dangling(plist), "needles": found,
          "last_role": plist[-1][1].get("role") if plist else None})


def cmd_verify_parts(pre_path, d):
    with open(pre_path, encoding="utf-8") as fh:
        pre = json.load(fh)
    pdir = os.path.join(d, "conversations", "parts")
    missing, changed = [], []
    for name, digest in (pre.get("sha256") or {}).items():
        path = os.path.join(pdir, name)
        if not os.path.exists(path):
            missing.append(name)
            continue
        with open(path, "rb") as fh:
            if hashlib.sha256(fh.read()).hexdigest() != digest:
                changed.append(name)
    emit({"missing": missing, "changed": changed, "pre_count": pre.get("count")})


def cmd_stats(d, token_prefix=""):
    plist = parts(d)
    evs = events(d)
    roles = Counter(p.get("role") for _, p in plist)
    tokens = {}
    if token_prefix:
        for _, p in plist:
            if p.get("role") != "assistant":
                continue
            text = p.get("content") or ""
            for m in re.findall(re.escape(token_prefix) + r"[\w-]+", text):
                tokens[m] = True
    seqs = [e.get("seq") for e in evs if isinstance(e.get("seq"), int)]
    emit({"parts": len(plist), "roles": dict(roles), "dangling": dangling(plist),
          "events_lines": len(evs), "seq_min": min(seqs) if seqs else None,
          "seq_max": max(seqs) if seqs else None, "tokens": sorted(tokens),
          "event_counts": event_counts(evs),
          "last_assistant": last_assistant_text(d)[:300]})


def cmd_last_reply(d):
    emit({"reply": last_assistant_text(d)[:300].replace("\n", " ")})


def cmd_wait_turn(d, target, timeout):
    target = int(target)
    deadline = time.time() + float(timeout)
    while True:
        counts = event_counts(events(d))
        done = counts["execution_completed"] + counts["execution_failed"]
        if done >= target:
            emit({"done": True, "completed": counts["execution_completed"],
                  "failed": counts["execution_failed"]})
        if time.time() >= deadline:
            emit({"done": False, "completed": counts["execution_completed"],
                  "failed": counts["execution_failed"]}, code=1)
        time.sleep(0.5)


def cmd_turn_count(d):
    counts = event_counts(events(d))
    emit({"completed": counts["execution_completed"],
          "failed": counts["execution_failed"],
          "total": counts["execution_completed"] + counts["execution_failed"]})


def cmd_wait_token(d, token, timeout):
    """Wait until an assistant message carries the round token.

    Completion detection keyed on the reply content, so an unrelated auto-turn
    (stall-check reminder) cannot satisfy the wait for this round.
    """
    deadline = time.time() + float(timeout)
    while True:
        for _, p in parts(d):
            if p.get("role") == "assistant" and token in (p.get("content") or ""):
                emit({"done": True, "reply": (p.get("content") or "")[:200]})
        if time.time() >= deadline:
            emit({"done": False}, code=1)
        time.sleep(0.5)


def cmd_wait_park(d, reason, timeout):
    deadline = time.time() + float(timeout)
    while True:
        cur = cursor(d)
        pending = cur.get("pending_input")
        if isinstance(pending, dict) and pending.get("reason") == reason:
            emit({"done": True, "cursor": cur})
        if time.time() >= deadline:
            emit({"done": False, "cursor": cur}, code=1)
        time.sleep(0.5)


def cmd_wait_crash(d, token, timeout):
    started = time.time()
    deadline = started + float(timeout)
    while True:
        plist = parts(d)
        counts = event_counts(events(d))
        marker = any(p.get("role") == "tool" and token in (p.get("content") or "")
                     for _, p in plist)
        dang = dangling(plist)
        in_flight = counts["execution_started"] > counts["execution_completed"] + counts["execution_failed"]
        state = {"marker": marker, "in_flight": in_flight, "dangling": dang,
                 "waited_s": round(time.time() - started, 1), "counts": counts}
        hard = marker and in_flight and bool(dang)
        soft = marker and in_flight and time.time() - started > 45
        if hard or soft:
            state["done"] = True
            state["mode"] = "hard" if hard else "soft"
            emit(state)
        if time.time() >= deadline:
            state["done"] = False
            emit(state, code=1)
        time.sleep(1)


def cmd_pre_events(d):
    evs = events(d)
    seqs = [e.get("seq") for e in evs if isinstance(e.get("seq"), int)]
    path = os.path.join(d, "events.jsonl")
    size = os.path.getsize(path) if os.path.exists(path) else 0
    emit({"lines": len(evs), "bytes": size,
          "seq_min": min(seqs) if seqs else None,
          "seq_max": max(seqs) if seqs else None})


def cmd_sse_compare(events_path, sse_path, types_csv, since=None):
    wanted = {t for t in types_csv.split(",") if t}
    sent_ids, sent_counts = set(), Counter()
    with open(events_path, encoding="utf-8", errors="replace") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                e = json.loads(line)
            except Exception:
                continue
            t = e.get("type")
            if wanted and t not in wanted:
                continue
            if since and (e.get("timestamp") or "") < since:
                # Emitted before this subscriber attached: not its frame to carry.
                continue
            sent_counts[t] += 1
            if t == "execution_completed" and e.get("execution_id"):
                sent_ids.add(e["execution_id"])
    recv_counts, recv_ids = Counter(), set()
    frames = 0
    if os.path.exists(sse_path):
        with open(sse_path, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if not line.startswith("data: "):
                    continue
                try:
                    e = json.loads(line[6:])
                except Exception:
                    continue
                t = e.get("type")
                if t == "session_snapshot":
                    continue
                if wanted and t not in wanted:
                    continue
                frames += 1
                recv_counts[t] += 1
                if t == "execution_completed" and e.get("execution_id"):
                    recv_ids.add(e["execution_id"])
    emit({"frames": frames, "sent_by_type": dict(sent_counts),
          "recv_by_type": dict(recv_counts),
          "sent_completed_ids": len(sent_ids), "recv_completed_ids": len(recv_ids),
          "missing_completed_ids": sorted(sent_ids - recv_ids),
          "extra_completed_ids": len(recv_ids - sent_ids)})


def cmd_rss(csv_path):
    rows = []
    if os.path.exists(csv_path):
        with open(csv_path, encoding="utf-8") as fh:
            for line in fh:
                cols = line.strip().split(",")
                if len(cols) == 2 and cols[1].isdigit():
                    rows.append(int(cols[1]))
    if not rows:
        emit({"rows": 0})
    emit({"rows": len(rows), "start_kb": rows[0], "end_kb": rows[-1],
          "min_kb": min(rows), "max_kb": max(rows), "growth_kb": rows[-1] - rows[0]})


def cmd_health(csv_path):
    total = ok = 0
    worst = 0.0
    if os.path.exists(csv_path):
        with open(csv_path, encoding="utf-8") as fh:
            for line in fh:
                cols = line.strip().split(",")
                if len(cols) != 3:
                    continue
                total += 1
                if cols[1] == "200":
                    ok += 1
                try:
                    worst = max(worst, float(cols[2]))
                except ValueError:
                    pass
    emit({"polls": total, "ok": ok, "failed": total - ok, "worst_ms": round(worst, 1)})


def cmd_latency(tsv_path):
    vals = []
    if os.path.exists(tsv_path):
        with open(tsv_path, encoding="utf-8") as fh:
            for line in fh:
                cols = line.rstrip("\n").split("\t")
                if len(cols) >= 7 and cols[5] == "ok":
                    try:
                        vals.append(float(cols[6]) / 1000.0)  # ms -> s
                    except ValueError:
                        pass
    vals.sort()
    if not vals:
        emit({"rounds": 0})
    emit({"rounds": len(vals), "min_s": vals[0], "p50_s": vals[len(vals) // 2],
          "max_s": vals[-1], "total_s": round(sum(vals), 1)})


def cmd_summary(out_dir):
    checks = []
    path = os.path.join(out_dir, "checks.tsv")
    if os.path.exists(path):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                cols = line.rstrip("\n").split("\t")
                if len(cols) >= 2:
                    checks.append((cols[0], cols[1], cols[2] if len(cols) > 2 else ""))

    def metrics(name):
        result = {}
        mpath = os.path.join(out_dir, name)
        if os.path.exists(mpath):
            with open(mpath, encoding="utf-8") as fh:
                for line in fh:
                    cols = line.rstrip("\n").split("\t", 1)
                    if len(cols) == 2:
                        result[cols[0]] = cols[1]
        return result

    crash = metrics("crash-recovery/metrics.tsv")
    park = metrics("park/metrics.tsv")
    soak = metrics("soak/metrics.tsv")
    failed = [c for c in checks if c[0] == "FAIL"]
    skipped = [c for c in checks if c[0] == "SKIP"]
    passed = [c for c in checks if c[0] == "PASS"]

    def table(predicate):
        rows = [c for c in checks if predicate(c[1])]
        if not rows:
            return "_none_"
        lines = ["| # | Check | Result | Detail |", "|---|---|---|---|"]
        for i, (status, name, detail) in enumerate(rows, 1):
            mark = {"PASS": "PASS", "FAIL": "**FAIL**", "SKIP": "SKIP"}.get(status, status)
            lines.append(f"| {i} | `{name}` | {mark} | {detail.replace('|', chr(92) + '|')} |")
        return "\n".join(lines)

    def line(label, value, default="n/a"):
        return f"- {label}: **{value or default}**"

    md = []
    md.append("# Reliability: crash recovery + concurrency soak")
    md.append("")
    md.append(f"Run: `{datetime.now().astimezone().isoformat(timespec='seconds')}`  ")
    md.append(f"Binary: `{(crash.get('binary_sha256') or 'n/a')[:16]}` built from `{crash.get('git_head', 'n/a')}`  ")
    md.append(f"Module: `go_hive/go-hive`  ")
    md.append(f"LLM: `{crash.get('model', 'n/a')}` via `{crash.get('provider', 'n/a')}`  ")
    md.append(f"Port: `{crash.get('port', 'n/a')}`; disposable HIVE_HOME outside the repo (not committed)")
    md.append("")
    md.append("## Verdict")
    md.append("")
    md.append(f"**{len(passed)} PASS / {len(failed)} FAIL / {len(skipped)} SKIP**"
              + (" — reliability evidence complete." if not failed else " — see failed checks below."))
    md.append("")
    md.append("## A. Crash recovery (kill -9 mid-turn)")
    md.append("")
    md.append(line("Crash session", crash.get("session_id")))
    md.append(line("Kill point", crash.get("kill_point")))
    md.append(line("Parts before / after restart", f"{crash.get('parts_before', '?')} / {crash.get('parts_after', '?')}"))
    md.append(line("Tool result before / after", f"{crash.get('result_before', '?')} / {crash.get('result_after', '?')}"))
    md.append(line("events.jsonl lines before / after", f"{crash.get('events_lines_before', '?')} / {crash.get('events_lines_after', '?')}"))
    md.append(line("Continuation reply", crash.get("continue_reply")))
    md.append("")
    md.append(table(lambda n: n.startswith("cr_")))
    md.append("")
    md.append("## A2. Parked ask_user survives restart")
    md.append("")
    md.append(line("Park session", park.get("session_id")))
    md.append(line("Parked question", park.get("question")))
    md.append(line("Answer reply", park.get("answer_reply")))
    md.append("")
    md.append(table(lambda n: n.startswith("pk_")))
    md.append("")
    md.append("## B. Concurrency soak")
    md.append("")
    md.append(line("Sessions x rounds (max active)", f"{soak.get('sessions', '?')} x {soak.get('rounds', '?')} (max {soak.get('concurrency', '?')})"))
    md.append(line("Turns completed / failed / timed out", f"{soak.get('turns_done', '?')} / {soak.get('turns_failed', '?')} / {soak.get('turns_timeout', '?')}"))
    md.append(line("Turn latency (min / p50 / max)", f"{soak.get('latency_min_s', '?')}s / {soak.get('latency_p50_s', '?')}s / {soak.get('latency_max_s', '?')}s"))
    md.append(line("RSS start / end / max", f"{soak.get('rss_start_mb', '?')} MB / {soak.get('rss_end_mb', '?')} MB / {soak.get('rss_max_mb', '?')} MB"))
    md.append(line("SSE boundary frames: persisted / received", f"{soak.get('sse_persisted', '?')} / {soak.get('sse_received', '?')}"))
    md.append(line("Missing execution ids", soak.get("sse_missing", "?")))
    md.append(line("Health polls ok / failed", f"{soak.get('health_ok', '?')} / {soak.get('health_failed', '?')}"))
    md.append("")
    md.append(table(lambda n: n.startswith("soak_")))
    md.append("")
    md.append("## Preflight")
    md.append("")
    md.append(table(lambda n: not (n.startswith("cr_") or n.startswith("pk_") or n.startswith("soak_"))))
    md.append("")
    with open(os.path.join(out_dir, "summary.md"), "w", encoding="utf-8") as fh:
        fh.write("\n".join(md) + "\n")
    emit({"checks": len(checks), "pass": len(passed), "fail": len(failed), "skip": len(skipped)})


def main():
    cmd = sys.argv[1]
    args = sys.argv[2:]
    handlers = {
        "session-dir": cmd_session_dir,
        "parts": cmd_parts,
        "verify-parts": cmd_verify_parts,
        "stats": cmd_stats,
        "last-reply": cmd_last_reply,
        "wait-turn": cmd_wait_turn,
        "turn-count": cmd_turn_count,
        "wait-token": cmd_wait_token,
        "wait-park": cmd_wait_park,
        "wait-crash": cmd_wait_crash,
        "pre-events": cmd_pre_events,
        "sse-compare": cmd_sse_compare,
        "rss": cmd_rss,
        "health": cmd_health,
        "latency": cmd_latency,
        "summary": cmd_summary,
    }
    fn = handlers.get(cmd)
    if fn is None:
        raise SystemExit(f"unknown helper command: {cmd}")
    fn(*args)


if __name__ == "__main__":
    main()
PY
}

start_serve() { # start_serve <label>
  local label="$1"
  (
    cd "$GODIR" || exit 3
    set +x
    if [ -f "$HIVE_ENV_FILE" ]; then . "$HIVE_ENV_FILE" >/dev/null 2>&1; fi
    export HIVE_HOME="$HIVE_HOME_DIR"
    exec "$BIN" serve --port "$PORT" >"$OUT/$label.out" 2>"$OUT/$label.err"
  ) &
  SERVE_PID=$!
  printf '%s\n' "$SERVE_PID" > "$WORK/serve-$label.pid"
}

wait_health() { # wait_health <seconds>
  local limit="${1:-60}" i
  for i in $(seq 1 "$limit"); do
    if curl -fsS -m 2 "$BASE/api/health" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

kill9_serve() { # kill9_serve <label> <pid-file>
  local label="$1" pidfile="$2" pid i
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  [ -n "$pid" ] || return 1
  kill -9 "$pid" 2>/dev/null || true
  for i in $(seq 1 50); do
    if ! kill -0 "$pid" 2>/dev/null; then
      printf 'killed -9 pid=%s label=%s at %s\n' "$pid" "$label" "$(now_iso)" >> "$OUT/crash-recovery/kill-log.txt"
      return 0
    fi
    sleep 0.2
  done
  return 1
}

create_session() { # create_session <out-file>
  curl -sS -m 120 -X POST "$BASE/api/sessions" \
    -H 'Content-Type: application/json' -d '{}' -o "$1"
  json_field "$1" 'd.get("session_id","")'
}

session_dir_of() { python3 "$HELPER" session-dir "$HIVE_HOME_DIR" "$1" 2>/dev/null; }

wait_opening_park() { python3 "$HELPER" wait-park "$1" turn_done "${2:-120}" >/dev/null 2>&1; }

preflight() {
  section "Preflight"
  mkdir -p "$OUT" "$WORK"
  : > "$OUT/checks.tsv"
  rm -rf "$OUT/crash-recovery" "$OUT/park" "$OUT/soak"
  mkdir -p "$OUT/crash-recovery" "$OUT/park" "$OUT/soak"
  : > "$OUT/crash-recovery/metrics.tsv"
  : > "$OUT/park/metrics.tsv"
  : > "$OUT/soak/metrics.tsv"
  : > "$OUT/soak/sessions.tsv"
  : > "$OUT/soak/turns.tsv"
  write_helper
  command -v curl >/dev/null || die "curl is required"
  command -v python3 >/dev/null || die "python3 is required"
  command -v go >/dev/null || die "go is required"
  [ -f "$HIVE_CONFIG_JSON" ] || die "missing LLM config $HIVE_CONFIG_JSON"

  PORT="$(pick_port "$PORT")" || die "no free port near $RELIABILITY_PORT"
  BASE="http://127.0.0.1:$PORT"
  say "workdir:  $WORK"
  say "evidence: $OUT"
  say "port:     $PORT"
  say "soak:     ${SOAK_SESSIONS} sessions x ${SOAK_ROUNDS} rounds, concurrency ${SOAK_CONCURRENCY}"

  section "Build"
  (
    cd "$GODIR" || exit 3
    export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
    go build -o "$BIN" ./cmd/hive
  ) >"$OUT/build.out" 2>&1
  if [ ! -x "$BIN" ]; then
    tail -20 "$OUT/build.out" 2>/dev/null
    die "go build failed (see $OUT/build.out)"
  fi
  local sha head
  sha="$(shasum -a 256 "$BIN" | awk '{print $1}')"
  head="$(cd "$GODIR" && git rev-parse HEAD 2>/dev/null || echo unknown)"
  say "binary sha256: $sha"
  say "git head:      $head"

  # The home is disposable and tool-owned: a re-run with the same
  # RELIABILITY_WORK starts from a clean tree instead of inheriting sessions.
  if [ -d "$HIVE_HOME_DIR" ]; then rm -rf "$HIVE_HOME_DIR"; fi
  mkdir -p "$HIVE_HOME_DIR/logs" "$HIVE_HOME_DIR/queens"
  cp "$HIVE_CONFIG_JSON" "$HIVE_HOME_DIR/configuration.json"
  shasum -a 256 "$HIVE_HOME_DIR/configuration.json" > "$OUT/config.sha256" 2>/dev/null || true

  (
    set +x
    if [ -f "$HIVE_ENV_FILE" ]; then . "$HIVE_ENV_FILE" >/dev/null 2>&1; fi
    keyvar="$(python3 - "$HIVE_HOME_DIR/configuration.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
print((c.get("llm") or {}).get("api_key_env_var", "") or "")
PY
)"
    if [ -n "$keyvar" ] && [ -z "${!keyvar:-}" ]; then
      echo "missing-key:$keyvar"
    fi
  ) > "$WORK/keycheck.txt"
  if grep -q '^missing-key:' "$WORK/keycheck.txt" 2>/dev/null; then
    die "LLM key env var $(sed 's/missing-key://' "$WORK/keycheck.txt") is empty; source $HIVE_ENV_FILE first"
  fi

  local model provider
  model="$(python3 - "$HIVE_HOME_DIR/configuration.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
print((c.get("llm") or {}).get("model", ""))
PY
)"
  provider="$(python3 - "$HIVE_HOME_DIR/configuration.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
llm = c.get("llm") or {}
print((llm.get("provider", "") + " " + llm.get("api_base", "")).strip())
PY
)"
  printf 'binary_sha256\t%s\ngit_head\t%s\nmodel\t%s\nprovider\t%s\nport\t%s\nrun_started\t%s\n' \
    "$sha" "$head" "$model" "$provider" "$PORT" "$(now_iso)" >> "$OUT/crash-recovery/metrics.tsv"
  record PASS preflight "binary built from $head; model=$model; port=$PORT"
}

phase_crash() {
  section "A. Crash recovery (kill -9 mid-turn)"
  start_serve serve-boot1
  if ! wait_health 90; then
    record FAIL cr_boot1 "server never became healthy"
    tail -20 "$OUT/serve-boot1.err" 2>/dev/null
    return 1
  fi
  record PASS cr_boot1 "serve healthy on $BASE"

  local sid sdir
  sid="$(create_session "$OUT/crash-recovery/session-create.json")"
  [ -n "$sid" ] || { record FAIL cr_create "no session_id in create response"; return 1; }
  sdir="$(session_dir_of "$sid")"
  [ -n "$sdir" ] || { record FAIL cr_create "session dir not found for $sid"; return 1; }
  printf '%s\n' "$sid" > "$WORK/crash-sid"
  say "crash session: $sid"
  wait_opening_park "$sdir" 120 || true

  local tok="RELCRASH$(date +%s)$$"
  printf '%s\n' "$tok" > "$WORK/crash-token"
  python3 - "$tok" > "$WORK/crash-prompt.json" <<'PY'
import json, sys
tok = sys.argv[1]
msg = (
    "Run exactly these terminal steps, in order, and nothing else. "
    f'Step 1: call terminal_exec with {{"command": "echo CRASH_PRE_{tok}"}}. '
    f'Step 2: call terminal_exec with {{"command": "sleep 180; echo CRASH_POST_{tok}", "auto_background_after_sec": 5}}. '
    'Step 3: if step 2 returned a job_id, call terminal_job_logs with '
    '{"wait_until_exit": true, "wait_timeout_sec": 200} and keep waiting until that job exits. '
    "Do not call ask_user. Only after the job exits, reply exactly "
    f"CRASH_DONE_{tok}."
)
print(json.dumps({"message": msg}))
PY
  curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" \
    -H 'Content-Type: application/json' --data-binary "@$WORK/crash-prompt.json" \
    -o "$OUT/crash-recovery/chat-long.json"
  record PASS cr_long_prompt "delivered long terminal task prompt (token $tok)"

  local crash_state="$OUT/crash-recovery/crash-point.json"
  if ! python3 "$HELPER" wait-crash "$sdir" "$tok" 150 > "$crash_state"; then
    python3 - "$tok" > "$WORK/crash-retry.json" <<'PY'
import json, sys
tok = sys.argv[1]
msg = (f"Use terminal_exec now. Call it with {{\"command\": \"echo CRASH_PRE_{tok}\"}}. "
       f"Then call terminal_exec with {{\"command\": \"sleep 180\", \"auto_background_after_sec\": 5}} "
       "and call terminal_job_logs with wait_until_exit=true to wait for the job. "
       "Do not answer with text before the job exits.")
print(json.dumps({"message": msg}))
PY
    curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" \
      -H 'Content-Type: application/json' --data-binary "@$WORK/crash-retry.json" \
      -o "$OUT/crash-recovery/chat-long-retry.json"
    python3 "$HELPER" wait-crash "$sdir" "$tok" 150 > "$crash_state" || true
  fi
  if [ "$(json_field "$crash_state" 'd.get("done")')" = "True" ]; then
    record PASS cr_mid_turn "in-flight tool call observed: $(json_field "$crash_state" 'd.get("counts",{})')"
  else
    record FAIL cr_mid_turn "could not reach a mid-turn in-flight tool call: $(head -c 300 "$crash_state" 2>/dev/null)"
  fi
  printf 'kill_point\tmid-turn, in-flight tool call (%s)\n' "$(json_field "$crash_state" 'd.get("mode","")')" >> "$OUT/crash-recovery/metrics.tsv"

  python3 "$HELPER" parts "$sdir" "$tok" > "$OUT/crash-recovery/pre-kill-parts.json"
  cp "$sdir/conversations/cursor.json" "$OUT/crash-recovery/pre-kill-cursor.json" 2>/dev/null || true
  cp "$sdir/events.jsonl" "$OUT/crash-recovery/pre-kill-events.jsonl" 2>/dev/null || true
  python3 "$HELPER" pre-events "$sdir" > "$OUT/crash-recovery/pre-kill-events-summary.json"
  local parts_before lines_before result_before
  parts_before="$(json_field "$OUT/crash-recovery/pre-kill-parts.json" 'd.get("count")')"
  lines_before="$(json_field "$OUT/crash-recovery/pre-kill-events-summary.json" 'd.get("lines")')"
  result_before="$(json_field "$OUT/crash-recovery/pre-kill-parts.json" 'd.get("needles",{}).get("'$tok'")')"
  printf 'session_id\t%s\nparts_before\t%s\nevents_lines_before\t%s\nresult_before\t%s\n' \
    "$sid" "$parts_before" "$lines_before" "$result_before" >> "$OUT/crash-recovery/metrics.tsv"
  say "pre-kill: parts=$parts_before events=$lines_before tool_result=$result_before"

  if kill9_serve boot1 "$WORK/serve-serve-boot1.pid"; then
    record PASS cr_kill9 "SIGKILL during the turn"
  else
    record FAIL cr_kill9 "server pid did not die"
  fi
  pkill -f "CRASH_POST_$tok" >/dev/null 2>&1 || true
  sleep 1

  start_serve serve-boot2
  if wait_health 90; then
    record PASS cr_boot2 "restarted serve healthy on $BASE"
  else
    record FAIL cr_boot2 "restarted serve never became healthy"
    tail -20 "$OUT/serve-boot2.err" 2>/dev/null
    return 1
  fi

  curl -sS -m 30 "$BASE/api/sessions/history" -o "$OUT/crash-recovery/cold-listing.json"
  local cold_ok
  cold_ok="$(python3 - "$OUT/crash-recovery/cold-listing.json" "$sid" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    print("parse-error"); raise SystemExit(0)
for s in d.get("sessions") or []:
    if s.get("session_id") == sys.argv[2]:
        print("yes" if s.get("has_messages") and s.get("cold") else f"has_messages={s.get('has_messages')} cold={s.get('cold')}")
        break
else:
    print("missing")
PY
)"
  if [ "$cold_ok" = "yes" ]; then
    record PASS cr_cold_listing "history lists $sid cold with messages"
  else
    record FAIL cr_cold_listing "history listing: $cold_ok"
  fi

  python3 - "$sid" > "$WORK/resume-crash.json" <<'PY'
import json, sys
print(json.dumps({"queen_resume_from": sys.argv[1]}))
PY
  curl -sS -m 120 -X POST "$BASE/api/sessions" -H 'Content-Type: application/json' \
    --data-binary "@$WORK/resume-crash.json" -o "$OUT/crash-recovery/resume.json"
  local resumed_id
  resumed_id="$(json_field "$OUT/crash-recovery/resume.json" 'd.get("session_id","")')"
  if [ "$resumed_id" = "$sid" ]; then
    record PASS cr_resume "queen_resume_from=$sid reopened the same session id"
  else
    record FAIL cr_resume "resume returned '$resumed_id' (wanted $sid): $(head -c 300 "$OUT/crash-recovery/resume.json")"
  fi
  sdir="$(session_dir_of "$sid")"

  # Observation (not a pass/fail criterion): after a cold resume the fresh bus
  # carries no events, so /snapshot cannot show the restored wait until the
  # next turn — recorded here for the report's parity notes.
  curl -sS -m 10 "$BASE/api/sessions/$sid/snapshot" -o "$OUT/crash-recovery/post-resume-snapshot.json" 2>/dev/null || true
  cp "$sdir/conversations/cursor.json" "$OUT/crash-recovery/post-resume-cursor.json" 2>/dev/null || true
  printf 'post_resume_snapshot_park\t%s\npost_resume_cursor_pending\t%s\n' \
    "$(json_field "$OUT/crash-recovery/post-resume-snapshot.json" 'd.get("park_reason")')" \
    "$(json_field "$OUT/crash-recovery/post-resume-cursor.json" '(d.get("pending_input") or {}).get("reason","none")')" \
    >> "$OUT/crash-recovery/metrics.tsv"

  python3 "$HELPER" verify-parts "$OUT/crash-recovery/pre-kill-parts.json" "$sdir" > "$OUT/crash-recovery/parts-verify.json"
  local missing changed
  missing="$(json_field "$OUT/crash-recovery/parts-verify.json" 'len(d.get("missing") or [])')"
  changed="$(json_field "$OUT/crash-recovery/parts-verify.json" 'len(d.get("changed") or [])')"
  if [ "$missing" = "0" ] && [ "$changed" = "0" ]; then
    record PASS cr_parts_intact "all $parts_before pre-crash parts present and byte-identical"
  else
    record FAIL cr_parts_intact "missing=$missing changed=$changed"
  fi
  python3 "$HELPER" parts "$sdir" "$tok" > "$OUT/crash-recovery/post-resume-parts.json"
  local result_after parts_after
  result_after="$(json_field "$OUT/crash-recovery/post-resume-parts.json" 'd.get("needles",{}).get("'$tok'")')"
  parts_after="$(json_field "$OUT/crash-recovery/post-resume-parts.json" 'd.get("count")')"
  if [ "$result_after" = "True" ]; then
    record PASS cr_tool_result "completed echo tool result ($tok) recovered from disk"
  else
    record FAIL cr_tool_result "echo tool result missing after resume"
  fi

  python3 "$HELPER" pre-events "$sdir" > "$OUT/crash-recovery/post-resume-events-summary.json"
  local lines_after first_line_ok=0
  lines_after="$(json_field "$OUT/crash-recovery/post-resume-events-summary.json" 'd.get("lines")')"
  if [ -f "$OUT/crash-recovery/pre-kill-events.jsonl" ] && [ -f "$sdir/events.jsonl" ]; then
    if head -1 "$OUT/crash-recovery/pre-kill-events.jsonl" | cmp -s - <(head -1 "$sdir/events.jsonl"); then first_line_ok=1; fi
  fi
  if [ "$lines_after" -ge "$lines_before" ] && [ "$first_line_ok" = "1" ]; then
    record PASS cr_events_intact "events.jsonl $lines_before -> $lines_after lines, head preserved"
  else
    record FAIL cr_events_intact "events.jsonl $lines_before -> $lines_after, head_intact=$first_line_ok"
  fi
  printf 'parts_after\t%s\nevents_lines_after\t%s\nresult_after\t%s\n' "$parts_after" "$lines_after" "$result_after" >> "$OUT/crash-recovery/metrics.tsv"

  # The recovered tool result must be usable by the model, not just on disk.
  python3 - > "$WORK/continue-crash.json" <<'PY'
import json
print(json.dumps({"message": "Earlier in this session I asked you to run an echo command. What was its exact stdout? Reply with only that exact text, nothing else."}))
PY
  local before_turn target
  before_turn="$(python3 "$HELPER" turn-count "$sdir" > "$WORK/crash-precontinue-count.json"; json_field "$WORK/crash-precontinue-count.json" 'd.get("total",0)')"
  target=$(( ${before_turn:-0} + 1 ))
  curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" -H 'Content-Type: application/json' \
    --data-binary "@$WORK/continue-crash.json" -o "$OUT/crash-recovery/continue.json"
  python3 "$HELPER" wait-turn "$sdir" "$target" 150 > "$OUT/crash-recovery/continue-turn.json" || true
  python3 "$HELPER" stats "$sdir" "$tok" > "$OUT/crash-recovery/continue-stats.json"
  local reply_snip
  reply_snip="$(json_field "$OUT/crash-recovery/continue-stats.json" 'd.get("last_assistant","")')"
  if [ -n "$reply_snip" ] && printf '%s' "$reply_snip" | grep -Fq "$tok"; then
    record PASS cr_tool_result_usable "post-restart reply used the recovered tool output: $reply_snip"
  elif [ -n "$reply_snip" ]; then
    record FAIL cr_tool_result_usable "reply did not contain the recovered token: $reply_snip"
  else
    record FAIL cr_tool_result_usable "no assistant reply after resume"
  fi
  printf 'continue_reply\t%s\n' "$reply_snip" >> "$OUT/crash-recovery/metrics.tsv"

  local boot2_ok=0 trace_ok=0
  grep -q "Hive API server running" "$OUT/serve-boot2.out" 2>/dev/null && boot2_ok=1
  if grep -q "POST /api/sessions -> 201" "$HIVE_HOME_DIR/logs/hive-server.log" 2>/dev/null \
     && grep -q "/api/sessions/$sid/chat" "$HIVE_HOME_DIR/logs/hive-server.log" 2>/dev/null; then
    trace_ok=1
  fi
  if [ "$boot2_ok" = 1 ] && [ "$trace_ok" = 1 ]; then
    record PASS cr_log_traces "second boot banner + resume access in hive-server.log"
  else
    record FAIL cr_log_traces "boot2_banner=$boot2_ok resume_access=$trace_ok"
  fi
}

phase_park() {
  section "A2. Parked ask_user survives restart"
  local sid sdir
  sid="$(create_session "$OUT/park/session-create.json")"
  [ -n "$sid" ] || { record FAIL pk_create "no session_id"; return 1; }
  sdir="$(session_dir_of "$sid")"
  [ -n "$sdir" ] || { record FAIL pk_create "session dir not found"; return 1; }
  printf '%s\n' "$sid" > "$WORK/park-sid"
  wait_opening_park "$sdir" 120 || true

  local q="What color should the widget be? Answer with one word."
  python3 - "$q" > "$WORK/park-prompt.json" <<'PY'
import json, sys
q = sys.argv[1]
print(json.dumps({"message": f'Call the ask_user tool exactly once with this question: "{q}" Do not call any other tool. Then stop and wait for my answer.'}))
PY
  curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" -H 'Content-Type: application/json' \
    --data-binary "@$WORK/park-prompt.json" -o "$OUT/park/chat.json"
  local park_state="$OUT/park/park-cursor.json"
  if ! python3 "$HELPER" wait-park "$sdir" ask_user 150 > "$park_state"; then
    python3 - > "$WORK/park-retry.json" <<'PY'
import json
print(json.dumps({"message": "You must call the ask_user tool now, exactly once, and wait for my answer. Do not reply with text."}))
PY
    curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" -H 'Content-Type: application/json' \
      --data-binary "@$WORK/park-retry.json" -o "$OUT/park/chat-retry.json"
    python3 "$HELPER" wait-park "$sdir" ask_user 150 > "$park_state" || true
  fi
  local parked pending_before
  parked="$(json_field "$park_state" 'd.get("done")')"
  pending_before="$(json_field "$park_state" '(d.get("cursor") or {}).get("pending_input") or {}')"
  if [ "$parked" = "True" ]; then
    record PASS pk_park_written "cursor.pending_input reason=ask_user persisted"
  else
    record FAIL pk_park_written "no ask_user park: $(head -c 300 "$park_state" 2>/dev/null)"
  fi
  printf 'session_id\t%s\nquestion\t%s\npending_before\t%s\n' "$sid" "$q" "$pending_before" >> "$OUT/park/metrics.tsv"

  if ! kill9_serve park "$WORK/serve-serve-boot2.pid"; then
    record FAIL pk_kill9 "server pid did not die"
    return 1
  fi
  record PASS pk_kill9 "SIGKILL while parked on ask_user"
  sleep 1
  start_serve serve-boot3
  if wait_health 90; then
    record PASS pk_boot3 "restarted serve healthy on $BASE"
  else
    record FAIL pk_boot3 "restart failed"
    return 1
  fi
  python3 - "$sid" > "$WORK/resume-park.json" <<'PY'
import json, sys
print(json.dumps({"queen_resume_from": sys.argv[1]}))
PY
  curl -sS -m 120 -X POST "$BASE/api/sessions" -H 'Content-Type: application/json' \
    --data-binary "@$WORK/resume-park.json" -o "$OUT/park/resume.json"
  local resumed_id
  resumed_id="$(json_field "$OUT/park/resume.json" 'd.get("session_id","")')"
  if [ "$resumed_id" = "$sid" ]; then
    record PASS pk_resume "resumed the parked session under the same id"
  else
    record FAIL pk_resume "resume returned '$resumed_id'"
  fi
  sdir="$(session_dir_of "$sid")"
  cp "$sdir/conversations/cursor.json" "$OUT/park/resume-cursor.json" 2>/dev/null || true
  curl -sS -m 10 "$BASE/api/sessions/$sid/snapshot" -o "$OUT/park/resume-snapshot.json" 2>/dev/null || true
  printf 'post_resume_snapshot_park\t%s\n' \
    "$(json_field "$OUT/park/resume-snapshot.json" 'd.get("park_reason")')" >> "$OUT/park/metrics.tsv"
  local reason_after q_after
  reason_after="$(json_field "$OUT/park/resume-cursor.json" '(d.get("pending_input") or {}).get("reason","")')"
  q_after="$(json_field "$OUT/park/resume-cursor.json" '(d.get("pending_input") or {}).get("questions") or []')"
  if [ "$reason_after" = "ask_user" ] && printf '%s' "$q_after" | grep -Fq "widget"; then
    record PASS pk_park_restored "pending_input ask_user + question text survived resume"
  else
    record FAIL pk_park_restored "reason='$reason_after' questions=$q_after"
  fi

  local before_turn target
  before_turn="$(python3 "$HELPER" turn-count "$sdir" > "$WORK/park-preturn-count.json"; json_field "$WORK/park-preturn-count.json" 'd.get("total",0)')"
  target=$(( ${before_turn:-0} + 1 ))
  curl -sS -m 60 -X POST "$BASE/api/sessions/$sid/chat" -H 'Content-Type: application/json' \
    -d '{"message":"blue"}' -o "$OUT/park/answer.json"
  python3 "$HELPER" wait-turn "$sdir" "$target" 150 > "$OUT/park/answer-turn.json" || true
  python3 "$HELPER" last-reply "$sdir" > "$OUT/park/reply.json"
  local reply
  reply="$(json_field "$OUT/park/reply.json" 'd.get("reply","")')"
  cp "$sdir/conversations/cursor.json" "$OUT/park/answer-cursor.json" 2>/dev/null || true
  local reason_end pending_end
  reason_end="$(json_field "$OUT/park/answer-cursor.json" 'd.get("park_reason","")')"
  pending_end="$(json_field "$OUT/park/answer-cursor.json" '(d.get("pending_input") or {}).get("reason","none")')"
  if [ -n "$reply" ] && [ "$reason_end" != "ask_user" ]; then
    record PASS pk_answer_continues "answer unblocked the restored wait: $reply"
  else
    record FAIL pk_answer_continues "reply='$reply' park_reason='$reason_end'"
  fi
  if [ "$reason_end" = "turn_done" ] || [ "$pending_end" = "none" ]; then
    record PASS pk_park_cleared "pending ask_user cleared after the answer"
  else
    record FAIL pk_park_cleared "park_reason='$reason_end' pending='$pending_end'"
  fi
  printf 'answer_reply\t%s\nreason_after_answer\t%s\n' "$reply" "$reason_end" >> "$OUT/park/metrics.tsv"
}

soak_session() { # soak_session <index> <sid> <sdir> <rows-tsv>
  local idx="$1" sid="$2" sdir="$3" rows="$4" r code status token before_failed after_failed
  for r in $(seq 1 "$SOAK_ROUNDS"); do
    token="SOAK-$idx-R$r"
    python3 - "$token" > "$WORK/soak-prompt-$idx-$r.json" <<'PY'
import json, sys
print(json.dumps({"message": f"Reply with exactly: {sys.argv[1]}. No tools. No extra words."}))
PY
    python3 "$HELPER" stats "$sdir" > "$WORK/soak-preturn-$idx-$r.json"
    before_failed="$(json_field "$WORK/soak-preturn-$idx-$r.json" 'd.get("event_counts",{}).get("execution_failed",0)')"
    local start end latency_ms
    start="$(python3 -c 'import time;print(int(time.time()*1000))')"
    code="$(curl -sS -m 60 -o "$WORK/soak-http-$idx-$r.txt" -w '%{http_code}' \
      -X POST "$BASE/api/sessions/$sid/chat" -H 'Content-Type: application/json' \
      --data-binary "@$WORK/soak-prompt-$idx-$r.json" 2>/dev/null)"
    status=""
    if [ "$code" = "200" ]; then
      # The answer token is the completion boundary: an unrelated auto-turn
      # (stall-check reminder) cannot satisfy it, unlike a raw turn counter.
      if python3 "$HELPER" wait-token "$sdir" "$token" 150 > "$WORK/soak-turn-$idx-$r.json"; then
        python3 "$HELPER" stats "$sdir" > "$WORK/soak-postturn-$idx-$r.json"
        after_failed="$(json_field "$WORK/soak-postturn-$idx-$r.json" 'd.get("event_counts",{}).get("execution_failed",0)')"
        if [ "${after_failed:-0}" != "${before_failed:-0}" ]; then
          status="turn_failed"
        else
          status="ok"
        fi
      else
        status="turn_timeout"
      fi
    else
      status="http_error"
    fi
    end="$(python3 -c 'import time;print(int(time.time()*1000))')"
    latency_ms=$((end - start))
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$idx" "$sid" "$r" "$code" "$end" "$status" "$latency_ms" >> "$rows"
    if [ "$status" != "ok" ]; then
      printf 'session %s round %s: %s (http=%s)\n' "$idx" "$r" "$status" "$code" >> "$WORK/soak-failures.txt"
      break
    fi
  done
}

phase_soak() {
  section "B. Concurrency soak (${SOAK_SESSIONS} sessions x ${SOAK_ROUNDS} rounds, max ${SOAK_CONCURRENCY})"
  local i sid sdir sid_list="$WORK/soak-sids.txt"
  : > "$sid_list"
  : > "$WORK/sse-pids.txt"
  : > "$WORK/soak-failures.txt"
  rm -f "$WORK"/soak-run-*.flag 2>/dev/null || true

  # Create all sessions first. Opening turns are LLM work too, so create in
  # waves of SOAK_CONCURRENCY and let each wave's opening turn park before the
  # next wave starts.
  local batch=0
  for i in $(seq 1 "$SOAK_SESSIONS"); do
    sid="$(create_session "$WORK/soak-create-$i.json")"
    if [ -z "$sid" ]; then
      record FAIL soak_create "session $i create failed: $(head -c 200 "$WORK/soak-create-$i.json" 2>/dev/null)"
      continue
    fi
    sdir="$(session_dir_of "$sid")"
    printf '%s\t%s\t%s\n' "$i" "$sid" "$sdir" >> "$sid_list"
    printf '%s\t%s\n' "$i" "$sid" >> "$OUT/soak/sessions.tsv"
    batch=$((batch + 1))
    if [ "$batch" -ge "$SOAK_CONCURRENCY" ]; then
      wait_opening_park "$sdir" 120 || true
      batch=0
    else
      sleep 1
    fi
  done
  while IFS=$'\t' read -r i sid sdir; do
    wait_opening_park "$sdir" 120 || true
  done < "$sid_list"

  local created
  created="$(wc -l < "$sid_list" | tr -d ' ')"
  if [ "$created" = "$SOAK_SESSIONS" ]; then
    record PASS soak_create "$created/$SOAK_SESSIONS sessions live (opening turns done)"
  else
    record FAIL soak_create "$created/$SOAK_SESSIONS sessions created"
  fi

  # Subscribe SSE per session (boundary types only, keeps evidence small).
  local sse_types="execution_started,execution_completed,execution_failed,client_input_received,client_input_committed,tool_call_started,tool_call_completed,client_input_requested"
  local sse_since
  sse_since="$(python3 -c 'from datetime import datetime;print(datetime.now().strftime("%Y-%m-%dT%H:%M:%S.%f"))')"
  printf '%s\n' "$sse_since" > "$WORK/sse-since.txt"
  while IFS=$'\t' read -r i sid sdir; do
    curl -sS -N -m 2400 "$BASE/api/sessions/$sid/events?types=$sse_types" \
      > "$OUT/soak/sse-$i-$sid.sse.jsonl" 2>&1 &
    printf '%s\t%s\n' "$i" "$!" >> "$WORK/sse-pids.txt"
  done < "$sid_list"
  sleep 1

  : > "$OUT/soak/rss.csv"
  : > "$OUT/soak/health.csv"
  ( while kill -0 "$SERVE_PID" 2>/dev/null; do
      printf '%s,%s\n' "$(date +%s)" "$(ps -o rss= -p "$SERVE_PID" 2>/dev/null | tr -d ' ')" >> "$OUT/soak/rss.csv"
      sleep 1
    done ) &
  local rss_pid=$!
  ( while kill -0 "$SERVE_PID" 2>/dev/null; do
      _t0="$(python3 -c 'import time;print(time.time())')"
      _code="$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "$BASE/api/health" 2>/dev/null || echo 000)"
      _ms="$(python3 -c "import time;print(round((time.time()-float('$_t0'))*1000,1))")"
      printf '%s,%s,%s\n' "$(date +%s)" "$_code" "$_ms" >> "$OUT/soak/health.csv"
      sleep 5
    done ) &
  local health_pid=$!

  local soak_start t_start t_end duration
  soak_start="$(now_iso)"
  t_start="$(date +%s)"
  say "soak started at $soak_start"

  # Launch per-session workers; at most SOAK_CONCURRENCY alive at once.
  # (wait-by-pid keeps the long-lived samplers out of the wait.)
  local wpids=""
  while IFS=$'\t' read -r i sid sdir; do
    while [ "$(ls "$WORK"/soak-run-*.flag 2>/dev/null | wc -l | tr -d ' ')" -ge "$SOAK_CONCURRENCY" ]; do sleep 0.5; done
    : > "$WORK/soak-run-$i.flag"
    ( soak_session "$i" "$sid" "$sdir" "$OUT/soak/turns.tsv"; rm -f "$WORK/soak-run-$i.flag" ) &
    wpids="$wpids $!"
  done < "$sid_list"
  local pid
  for pid in $wpids; do wait "$pid" 2>/dev/null || true; done

  t_end="$(date +%s)"
  duration=$((t_end - t_start))
  say "soak rounds finished in ${duration}s"

  printf '%s,%s\n' "$(date +%s)" "$(ps -o rss= -p "$SERVE_PID" 2>/dev/null | tr -d ' ')" >> "$OUT/soak/rss.csv"
  # Snapshot what had been persisted by the time the subscribers were still
  # listening; a turn that completes after this point is not a dropped frame.
  sleep 2
  while IFS=$'\t' read -r i sid sdir; do
    cp "$sdir/events.jsonl" "$OUT/soak/events-snapshot-$i.jsonl" 2>/dev/null || true
  done < "$sid_list"
  while IFS=$'\t' read -r i pid; do kill "$pid" >/dev/null 2>&1 || true; done < "$WORK/sse-pids.txt"
  kill "$rss_pid" "$health_pid" >/dev/null 2>&1 || true
  sleep 1

  local total done failed timeout http_bad
  total="$(awk 'NF' "$OUT/soak/turns.tsv" | wc -l | tr -d ' ')"
  done="$(awk -F'\t' '$6=="ok"' "$OUT/soak/turns.tsv" | wc -l | tr -d ' ')"
  failed="$(awk -F'\t' '$6=="turn_failed"' "$OUT/soak/turns.tsv" | wc -l | tr -d ' ')"
  timeout="$(awk -F'\t' '$6=="turn_timeout"' "$OUT/soak/turns.tsv" | wc -l | tr -d ' ')"
  http_bad="$(awk -F'\t' '$6=="http_error"' "$OUT/soak/turns.tsv" | wc -l | tr -d ' ')"

  local exec_failed=0
  while IFS=$'\t' read -r i sid sdir; do
    local v
    python3 "$HELPER" stats "$sdir" > "$WORK/soak-stats-$i.json"
    v="$(json_field "$WORK/soak-stats-$i.json" 'd.get("event_counts",{}).get("execution_failed",0)')"
    exec_failed=$((exec_failed + ${v:-0}))
  done < "$sid_list"

  local expected_rounds=$((created * SOAK_ROUNDS))
  if [ "$done" = "$expected_rounds" ] && [ "$http_bad" = "0" ] && [ "$timeout" = "0" ] && [ "$failed" = "0" ] && [ "$exec_failed" = "0" ]; then
    record PASS soak_rounds "$done/$expected_rounds turns completed; http_bad=0 timeout=0 turn_failed=0 execution_failed=0"
  else
    record FAIL soak_rounds "$done/$expected_rounds completed; http_bad=$http_bad timeout=$timeout turn_failed=$failed execution_failed=$exec_failed"
  fi
  printf 'sessions\t%s\nrounds\t%s\nconcurrency\t%s\nturns_planned\t%s\nturns_done\t%s\nturns_failed\t%s\nturns_timeout\t%s\nturns_http_error\t%s\nexecution_failed\t%s\nduration_s\t%s\n' \
    "$created" "$SOAK_ROUNDS" "$SOAK_CONCURRENCY" "$expected_rounds" "$done" "$failed" "$timeout" "$http_bad" "$exec_failed" "$duration" >> "$OUT/soak/metrics.tsv"

  python3 "$HELPER" health "$OUT/soak/health.csv" > "$OUT/soak/health.json"
  local hok hfail
  hok="$(json_field "$OUT/soak/health.json" 'd.get("ok")')"
  hfail="$(json_field "$OUT/soak/health.json" 'd.get("failed")')"
  if [ "$hfail" = "0" ] && [ "$hok" != "0" ]; then
    record PASS soak_live "health poller $hok/$hok OK during soak (worst $(json_field "$OUT/soak/health.json" 'd.get("worst_ms")') ms)"
  else
    record FAIL soak_live "health poller ok=$hok failed=$hfail"
  fi
  printf 'health_ok\t%s\nhealth_failed\t%s\n' "$hok" "$hfail" >> "$OUT/soak/metrics.tsv"

  python3 "$HELPER" rss "$OUT/soak/rss.csv" > "$OUT/soak/rss.json"
  local rs re rm rg
  rs="$(json_field "$OUT/soak/rss.json" 'd.get("start_kb")')"
  re="$(json_field "$OUT/soak/rss.json" 'd.get("end_kb")')"
  rm="$(json_field "$OUT/soak/rss.json" 'd.get("max_kb")')"
  rg="$(json_field "$OUT/soak/rss.json" 'd.get("growth_kb")')"
  if [ -z "$rg" ]; then
    record FAIL soak_rss "no RSS samples"
  elif [ "$rg" -lt $((512 * 1024)) ]; then
    record PASS soak_rss "RSS start $(( ${rs:-0} / 1024 )) MB -> end $(( ${re:-0} / 1024 )) MB (max $(( ${rm:-0} / 1024 )) MB, growth $(( ${rg:-0} / 1024 )) MB)"
  else
    record FAIL soak_rss "RSS growth $(( ${rg:-0} / 1024 )) MB exceeds 512 MB bound (start $(( ${rs:-0} / 1024 )) MB)"
  fi
  printf 'rss_start_mb\t%s\nrss_end_mb\t%s\nrss_max_mb\t%s\nrss_growth_mb\t%s\n' \
    "$(( ${rs:-0} / 1024 ))" "$(( ${re:-0} / 1024 ))" "$(( ${rm:-0} / 1024 ))" "$(( ${rg:-0} / 1024 ))" >> "$OUT/soak/metrics.tsv"

  local missing_total=0 sent_total=0 recv_total=0
  local per="$OUT/soak/per-session.tsv"
  : > "$per"
  while IFS=$'\t' read -r i sid sdir; do
    python3 "$HELPER" sse-compare "$OUT/soak/events-snapshot-$i.jsonl" "$OUT/soak/sse-$i-$sid.sse.jsonl" \
      "execution_started,execution_completed,execution_failed,client_input_received,client_input_committed,tool_call_started,tool_call_completed,client_input_requested" \
      "$sse_since" \
      > "$OUT/soak/sse-compare-$i.json"
    local s r m
    s="$(json_field "$OUT/soak/sse-compare-$i.json" 'd.get("sent_completed_ids")')"
    r="$(json_field "$OUT/soak/sse-compare-$i.json" 'd.get("recv_completed_ids")')"
    m="$(json_field "$OUT/soak/sse-compare-$i.json" 'len(d.get("missing_completed_ids") or [])')"
    sent_total=$((sent_total + ${s:-0}))
    recv_total=$((recv_total + ${r:-0}))
    missing_total=$((missing_total + ${m:-0}))
    printf '%s\t%s\t%s\t%s\t%s\n' "$i" "$sid" "${s:-0}" "${r:-0}" "${m:-0}" >> "$per"
  done < "$sid_list"
  if [ "$missing_total" = "0" ] && [ "$sent_total" -ge "$expected_rounds" ]; then
    record PASS soak_no_dropped "persisted execution_completed=$sent_total, received=$recv_total, missing=0"
  else
    record FAIL soak_no_dropped "persisted=$sent_total received=$recv_total missing=$missing_total (need 0, >=$expected_rounds persisted)"
  fi
  printf 'sse_persisted\t%s\nsse_received\t%s\nsse_missing\t%s\nsse_since\t%s\n' "$sent_total" "$recv_total" "$missing_total" "$sse_since" >> "$OUT/soak/metrics.tsv"

  local tok_hits=0 expected_tokens=$((created * SOAK_ROUNDS))
  while IFS=$'\t' read -r i sid sdir; do
    python3 "$HELPER" stats "$sdir" "SOAK-" > "$WORK/soak-tokens-$i.json"
    local c
    c="$(json_field "$WORK/soak-tokens-$i.json" 'len(d.get("tokens") or [])')"
    tok_hits=$((tok_hits + ${c:-0}))
  done < "$sid_list"
  if [ "$tok_hits" -ge "$expected_tokens" ]; then
    record PASS soak_tokens "$tok_hits/$expected_tokens round answer tokens persisted in transcripts"
  else
    record FAIL soak_tokens "$tok_hits/$expected_tokens round answer tokens persisted"
  fi

  python3 "$HELPER" latency "$OUT/soak/turns.tsv" > "$OUT/soak/latency.json"
  printf 'latency_min_s\t%s\nlatency_p50_s\t%s\nlatency_max_s\t%s\nlatency_total_s\t%s\n' \
    "$(json_field "$OUT/soak/latency.json" 'd.get("min_s")')" \
    "$(json_field "$OUT/soak/latency.json" 'd.get("p50_s")')" \
    "$(json_field "$OUT/soak/latency.json" 'd.get("max_s")')" \
    "$(json_field "$OUT/soak/latency.json" 'd.get("total_s")')" >> "$OUT/soak/metrics.tsv"

  if kill -0 "$SERVE_PID" 2>/dev/null && curl -fsS -m 3 "$BASE/api/health" >/dev/null 2>&1; then
    record PASS soak_alive "serve still alive and healthy after soak"
  else
    record FAIL soak_alive "serve died or unhealthy after soak"
  fi
  printf 'soak_started\t%s\nsoak_finished\t%s\n' "$soak_start" "$(now_iso)" >> "$OUT/soak/metrics.tsv"
}

finalize() {
  section "Finalize"
  python3 "$HELPER" summary "$OUT" > "$OUT/results.json"
  if [ "$FAIL_COUNT" = "0" ]; then
    say ""
    say "RESULT: PASS ($PASS_COUNT checks, 0 failed, $SKIP_COUNT skipped)"
    say "evidence: $OUT/summary.md"
    return 0
  fi
  say ""
  say "RESULT: FAIL ($PASS_COUNT passed, $FAIL_COUNT failed, $SKIP_COUNT skipped)"
  say "see $OUT/checks.tsv and $OUT/summary.md"
  return 1
}

main() {
  preflight || exit 2
  phase_crash
  phase_park
  phase_soak
  if [ "${RELIABILITY_KEEP_SERVE:-0}" != "1" ] && [ -n "$SERVE_PID" ]; then
    kill "$SERVE_PID" >/dev/null 2>&1 || true
  fi
  finalize
}

main "$@"
