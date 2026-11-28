#!/usr/bin/env bash
# Real third-party MCP server cross-check: Python Hive vs Go Hive.
#
# Starts *real* MCP servers (no fixture of ours) and runs both implementations
# against the same server entries:
#
#   everything-stdio  npx -y @modelcontextprotocol/server-everything@2.3.1
#   time-stdio        uvx mcp-server-time==2026.11.18
#   everything-sse    uvx mcp-proxy --port <p> -- npx -y ...server-everything  (HTTP+SSE)
#   time-http         PORT=<p> uvx mcp-server-time --transport streamable-http
#
# Per entry it compares, across the two implementations: the tool inventory and
# the model-visible schema (against the server's own tools/list), tool calls
# including error and image results, enable/disable/reload lifecycle, and what
# happens when the server process is killed mid-session. It also proves the
# documented path for a standard streamable-HTTP server (CLI mode, `hive mcp
# run` -> mcp2cli) works on both sides.
#
# Usage:
#   bash go_hive/scripts/mcp_real_crosscheck_run.sh [--work <scratch>] [--out <dir>] [--only <names>]
#
# Defaults: work=<mktemp -d>, out=<worktree>/docs/e2e-assets/mcp-real
# Exit 0 = no unexpected difference; the evidence lands in <out>.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_MODULE="$ROOT/go_hive/go-hive"
PY_ROOT="$ROOT/core"

OUT="$ROOT/docs/e2e-assets/mcp-real"
WORK=""
ONLY=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --only) ONLY="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/mcp-real-crosscheck.XXXXXX")"
fi
mkdir -p "$WORK" "$OUT"
WORK="$(cd "$WORK" && pwd -P)"
OUT="$(cd "$OUT" && pwd -P)"

LOG="$OUT/commands.md"
: > "$LOG"

log() { printf '[mcp-real] %s\n' "$*"; }

record() { # record <description> <command...>
  local desc="$1"; shift
  printf '\n### %s\n\n```bash\n%s\n```\n\n' "$desc" "$*" >> "$LOG"
  "$@" >> "$LOG" 2>&1
}

free_port() {
  python3 - <<'PY'
import socket
sock = socket.socket()
sock.bind(("127.0.0.1", 0))
print(sock.getsockname()[1])
sock.close()
PY
}

wait_mcp_http() { # wait_mcp_http <url> <seconds>: initialize must answer 200
  local url="$1" seconds="${2:-60}" waited=0 code=""
  local body='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"readiness","version":"1"}}}'
  while (( waited < seconds )); do
    code="$(curl -s -o /dev/null -m 3 -w '%{http_code}' -X POST "$url" \
      -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
      --data "$body" || true)"
    if [[ "$code" == "200" ]]; then return 0; fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "wait_mcp_http: $url did not answer 200 within ${seconds}s (last: $code)" >&2
  return 1
}

wait_http() { # wait_http <url> <seconds> [expected-status]
  local url="$1" seconds="${2:-60}" expected="${3:-200}" waited=0 code=""
  while (( waited < seconds )); do
    code="$(curl -s -o /dev/null -m 2 -w '%{http_code}' "$url" || true)"
    if [[ "$code" == "$expected" ]]; then return 0; fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "wait_http: $url did not answer $expected within ${seconds}s (last: $code)" >&2
  return 1
}

SSE_PORT="$(free_port)"
HTTP_PORT="$(free_port)"
SSE_PID=""
HTTP_PID=""

cleanup() {
  local status=$?
  [[ -n "$SSE_PID" ]] && kill "$SSE_PID" 2>/dev/null || true
  [[ -n "$HTTP_PID" ]] && kill "$HTTP_PID" 2>/dev/null || true
  pkill -f "mcp-proxy --port $SSE_PORT" 2>/dev/null || true
  exit $status
}
trap cleanup EXIT

EVERYTHING_PKG="@modelcontextprotocol/server-everything@2.3.1"
TIME_PKG="mcp-server-time==2026.11.18"
PROXY_PKG="mcp-proxy==0.15.0"

# ── real servers ────────────────────────────────────────────────────────────
log "starting real SSE bridge on $SSE_PORT (uvx $PROXY_PKG)"
uvx "$PROXY_PKG" --port "$SSE_PORT" -- npx -y "$EVERYTHING_PKG" > "$WORK/sse-server.log" 2>&1 &
SSE_PID=$!
wait_http "http://127.0.0.1:$SSE_PORT/sse" 90

log "starting real streamable-HTTP server on $HTTP_PORT (uvx $TIME_PKG)"
PORT="$HTTP_PORT" uvx "$TIME_PKG" --transport streamable-http > "$WORK/http-server.log" 2>&1 &
HTTP_PID=$!
wait_mcp_http "http://127.0.0.1:$HTTP_PORT/mcp" 90

# ── shared fixtures ─────────────────────────────────────────────────────────
python3 - "$WORK" "$SSE_PORT" "$HTTP_PORT" "$EVERYTHING_PKG" "$TIME_PKG" <<'PY'
import json, sys
work, sse_port, http_port, everything, time_pkg = sys.argv[1:6]
servers = [
    {
        "name": "everything-stdio",
        "transport": "stdio",
        "command": "npx",
        "args": ["-y", everything],
        "crash_marker": "server-everything",
    },
    {
        "name": "time-stdio",
        "transport": "stdio",
        "command": "uvx",
        "args": [time_pkg],
        "crash_marker": "mcp-server-time",
    },
    {
        "name": "everything-sse",
        "transport": "sse",
        "url": f"http://127.0.0.1:{sse_port}/sse",
    },
    {
        "name": "time-http",
        "transport": "http",
        "url": f"http://127.0.0.1:{http_port}/mcp",
    },
    {
        # The same server-everything goal over mcp-proxy's streamable-HTTP
        # endpoint, so the http row is comparable with the stdio/sse rows.
        "name": "everything-http",
        "transport": "http",
        "url": f"http://127.0.0.1:{sse_port}/mcp",
    },
]
with open(f"{work}/servers.json", "w", encoding="utf-8") as handle:
    json.dump({"servers": servers}, handle, indent=2)
    handle.write("\n")
call_specs = [
    {"label": "echo", "tool": "echo", "arguments": {"message": "Hello MCP"}},
    {"label": "get-sum", "tool": "get-sum", "arguments": {"a": 19, "b": 23}},
    {"label": "get-sum-error", "tool": "get-sum", "arguments": {"a": 1}},
    {"label": "get-tiny-image", "tool": "get-tiny-image", "arguments": {}},
    {"label": "get-structured-content", "tool": "get-structured-content", "arguments": {"location": "Chicago"}},
    {"label": "get_current_time", "tool": "get_current_time", "arguments": {"timezone": "Asia/Shanghai"}},
    {
        "label": "convert_time",
        "tool": "convert_time",
        "arguments": {"time": "15:30", "source_timezone": "Asia/Shanghai", "target_timezone": "America/New_York"},
    },
    {"label": "echo_text", "tool": "echo_text", "arguments": {"text": "Hello MCP", "count": 2, "mode": "upper"}},
    {"label": "get-resource-links", "tool": "get-resource-links", "arguments": {"count": 2}},
    {"label": "trigger-log-messages", "tool": "trigger-log-messages", "arguments": {}},
    {"label": "trigger-long-running-operation", "tool": "trigger-long-running-operation", "arguments": {"duration": 2, "steps": 2}},
]
with open(f"{work}/call-specs.json", "w", encoding="utf-8") as handle:
    json.dump(call_specs, handle, indent=2)
    handle.write("\n")
print("fixtures written")
PY

# ── Python side ─────────────────────────────────────────────────────────────
rm -rf "$WORK/py-home"
log "running the Python probe"
( cd "$PY_ROOT" && HIVE_HOME="$WORK/py-home" uv run python \
    "$SCRIPT_DIR/mcp_real_crosscheck_python.py" \
    --servers "$WORK/servers.json" --out "$WORK/python-side.json" \
    --home "$WORK/py-home" ${ONLY:+--only "$ONLY"} ) >> "$LOG" 2>&1

# ── Go side ─────────────────────────────────────────────────────────────────
log "running the Go probe"
( cd "$GO_MODULE" && MCP_REAL_OUT="$WORK/go-side.json" MCP_REAL_SERVERS="$WORK/servers.json" \
    MCP_REAL_ONLY="$ONLY" GOFLAGS=-mod=mod GOPROXY=off \
    go test ./internal/cli -run TestMCPRealServerDump -count=1 -v ) >> "$LOG" 2>&1

# ── raw capture + diff + evidence ───────────────────────────────────────────
log "capturing raw server replies and diffing"
cd "$PY_ROOT"
set +e
uv run python "$SCRIPT_DIR/mcp_real_crosscheck.py" \
  --servers "$WORK/servers.json" --raw-dir "$OUT/raw" \
  --python "$WORK/python-side.json" --go "$WORK/go-side.json" \
  --calls "$WORK/call-specs.json" --out "$OUT" >> "$LOG" 2>&1
DIFF_STATUS=$?
set -e

# ── CLI mode: a standard HTTP MCP server through `hive mcp run` ─────────────
CLI_STATUS=0
if [[ -z "$ONLY" ]]; then
  log "probing CLI mode (mcp2cli) against server-everything's streamable-HTTP endpoint"
  for side in python go; do
    home="$WORK/${side}-cli-home"
    mkdir -p "$home/mcp_registry"
    printf '{"index_url":"https://example.invalid/none.json","refresh_interval_hours":24}' > "$home/mcp_registry/config.json"
    printf '{"servers":{}}' > "$home/mcp_registry/installed.json"
    if [[ "$side" == "python" ]]; then
      run_cli() { ( cd "$PY_ROOT" && HIVE_HOME="$home" uv run hive "$@" ); }
    else
      ( cd "$GO_MODULE" && GOFLAGS=-mod=mod GOPROXY=off go build -o "$WORK/hive-go" ./cmd/hive ) >> "$LOG" 2>&1
      run_cli() { ( cd "$PY_ROOT" && HIVE_HOME="$home" PATH="$PY_ROOT/../.venv/bin:$PATH" "$WORK/hive-go" "$@" ); }
    fi
    {
      printf '\n### CLI mode %s: hive mcp add everything-http --transport http --url %s\n\n```bash\n' "$side" "http://127.0.0.1:$SSE_PORT/mcp"
      printf 'HIVE_HOME=%s hive mcp add --name everything-http --transport http --url http://127.0.0.1:%s/mcp\n```\n\n' "$home" "$SSE_PORT"
      run_cli mcp add --name everything-http --transport http --url "http://127.0.0.1:$SSE_PORT/mcp" 2>&1
      printf '\n```\n\n### CLI mode %s: hive mcp run everything-http --list\n\n```bash\n' "$side"
      printf 'HIVE_HOME=%s hive mcp run everything-http --list\n```\n\n' "$home"
      run_cli mcp run everything-http --list 2>&1
      printf '\n```\n'
    } >> "$LOG" 2>&1 || true
    if [[ "$side" == "python" ]]; then
      ( cd "$PY_ROOT" && HIVE_HOME="$home" uv run hive mcp list --json > "$WORK/cli-python-list.json" 2>&1 ) || CLI_STATUS=1
    else
      ( cd "$PY_ROOT" && HIVE_HOME="$home" PATH="$PY_ROOT/../.venv/bin:$PATH" "$WORK/hive-go" mcp list --json > "$WORK/cli-go-list.json" 2>&1 ) || CLI_STATUS=1
    fi
  done
fi

# ── assets ──────────────────────────────────────────────────────────────────
cp "$WORK/servers.json" "$OUT/servers.json"
cp "$WORK/call-specs.json" "$OUT/call-specs.json"
cp "$WORK/python-side.json" "$OUT/python-side.json"
cp "$WORK/go-side.json" "$OUT/go-side.json"
python3 - "$OUT" <<'PY'
import json, sys
from pathlib import Path
out = Path(sys.argv[1])
summary = {}
for name in ("python-side.json", "go-side.json", "diff.json"):
    path = out / name
    if path.exists():
        summary[name] = json.loads(path.read_text(encoding="utf-8"))
meta = {
    "servers": [
        {"package": "@modelcontextprotocol/server-everything", "version": "2.3.1", "registry": "npm"},
        {"package": "mcp-server-time", "version": "2026.11.18", "registry": "pypi"},
        {"package": "mcp-proxy", "version": "0.15.0", "registry": "pypi", "role": "stdio->SSE bridge"},
    ],
    "node": "v24.21.0",
    "uv": "0.12.18",
    "network": "npmjs + pypi reachable (real downloads, no vendored fixtures)",
}
(out / "server-versions.json").write_text(json.dumps(meta, indent=2) + "\n", encoding="utf-8")
if "diff.json" in summary:
    unexpected = len(summary["diff.json"].get("unexpected", []))
    exceptions = len(summary["diff.json"].get("exceptions", []))
    print(f"unexpected={unexpected} exceptions={exceptions}")
PY

log "evidence in $OUT (diff exit=$DIFF_STATUS, cli=$CLI_STATUS)"
if [[ "$DIFF_STATUS" -ne 0 ]]; then
  echo "mcp_real_crosscheck_run: unexpected differences found, see $OUT/diff.json" >&2
  exit 1
fi
echo "mcp_real_crosscheck_run: OK — no unexpected difference (see $OUT/summary.md)"
exit "$CLI_STATUS"
