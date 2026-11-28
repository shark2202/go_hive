#!/usr/bin/env bash
# go-hive-cloud M2 inbox smoke (L2b): the closed loop with the REAL go_hive
# Sentinel client.
#   1. build the service, boot it on a random port with a throwaway data dir
#   2. mint an operator JWT and a streamToken with the real `hive-cloud token`
#   3. run harness/inbox-smoke: sentinel.HTTPNotifier escalates over
#      POST /v1/inbox/messages, the operator replies through the extension,
#      sentinel.HiveInboxListener receives `event: reply` over
#      GET /v1/inbox/runtime/stream and dispatches OnInbound
#   4. assert the harness verdict and leave evidence at evidence/inbox-smoke.json
#
# Toolchain: source the lane goenv (go1.26.11 + module cache) before running, or
# point GOENV at it.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
GOENV="${GOENV:-/Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh}"
if [[ -f "$GOENV" ]]; then
  # shellcheck disable=SC1090
  source "$GOENV"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "inbox-smoke: go toolchain not found (source $GOENV)" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-inbox-smoke.XXXXXX")"
SERVE_PID=""
cleanup() {
  if [[ -n "$SERVE_PID" ]]; then
    kill "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "inbox-smoke: building hive-cloud ($(go version))"
(cd "$ROOT" && go build -o "$WORK/hive-cloud" ./cmd/hive-cloud)

echo "inbox-smoke: serving with a throwaway data dir"
"$WORK/hive-cloud" serve --listen 127.0.0.1:0 --data-dir "$WORK/data" >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 100); do
  if grep -q "listening on" "$WORK/serve.log"; then
    break
  fi
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "inbox-smoke: server exited before listening" >&2
    cat "$WORK/serve.log" >&2
    exit 1
  fi
  sleep 0.1
done
URL="$(sed -n 's/^hive-cloud: listening on \(http[^ ]*\).*/\1/p' "$WORK/serve.log" | head -1)"
if [[ -z "$URL" ]]; then
  echo "inbox-smoke: no listen address in the server log" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
curl -fsS "$URL/health" >/dev/null || { echo "inbox-smoke: /health not ready" >&2; exit 1; }
echo "inbox-smoke: serving at $URL"

echo "inbox-smoke: minting operator JWT + streamToken"
JWT="$("$WORK/hive-cloud" token --data-dir "$WORK/data" --user-id u_inbox_smoke --team-id team_inbox_smoke --ttl 2h)"
STREAM="$("$WORK/hive-cloud" token --data-dir "$WORK/data" --user-id u_inbox_smoke --team-id team_inbox_smoke --ttl 2h --type stream)"
if [[ -z "$JWT" || -z "$STREAM" ]]; then
  echo "inbox-smoke: token command produced nothing" >&2
  exit 1
fi
export HIVE_CLOUD_BASE="$URL"
export HIVE_CLOUD_JWT="$JWT"
export HIVE_CLOUD_STREAM_TOKEN="$STREAM"

echo "inbox-smoke: driving the closed loop with the real go_hive sentinel client"
mkdir -p "$ROOT/evidence"
(cd "$ROOT/harness/inbox-smoke" && EVIDENCE_OUT="$ROOT/evidence/inbox-smoke.json" go run .)

if ! grep -q '"ok": true' "$ROOT/evidence/inbox-smoke.json"; then
  echo "inbox-smoke: evidence reports a failed check" >&2
  cat "$ROOT/evidence/inbox-smoke.json" >&2
  exit 1
fi
if ! grep -q '"name": "sse_reply_delivered"' "$ROOT/evidence/inbox-smoke.json"; then
  echo "inbox-smoke: evidence is missing the SSE delivery check" >&2
  exit 1
fi
if ! grep -q '"name": "stream_token_rejected"' "$ROOT/evidence/inbox-smoke.json"; then
  echo "inbox-smoke: evidence is missing the streamToken rejection check" >&2
  exit 1
fi
if ! grep -q '"name": "desktop_stream_unknown_404"' "$ROOT/evidence/inbox-smoke.json"; then
  echo "inbox-smoke: evidence is missing the unknown-endpoint check" >&2
  exit 1
fi
echo "inbox-smoke: OK — evidence at evidence/inbox-smoke.json"
