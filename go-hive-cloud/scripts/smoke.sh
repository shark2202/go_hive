#!/usr/bin/env bash
# go-hive-cloud smoke entrypoint:
#   1. L2a: global-db with the REAL go_hive client (evidence/globaldb-smoke.json)
#   2. L2b: inbox closed loop with the REAL go_hive sentinel client
#      (evidence/inbox-smoke.json)
#   3. L2 M3: streamToken rotation with the REAL Python runtime client +
#      `hive` LLM credential validation with the REAL go_hive keycheck
#      (evidence/m3-smoke.json)
#   4. L2 M5: Messages JSON/SSE pass-through with the REAL go_hive Anthropic
#      client + real hive-cloud binary + local fake upstream
#      (evidence/m5-smoke.json)
#   5. L2 M4: grid_query-generated Postgres SQL through the REAL go_hive
#      global-db client + the /v1/crm/* generic grid subset
#      (evidence/m4-smoke.json)
#
# Each stage boots its own throwaway server on a random port.
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
  echo "smoke: go toolchain not found (source $GOENV)" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-smoke.XXXXXX")"
SERVE_PID=""
cleanup() {
  if [[ -n "$SERVE_PID" ]]; then
    kill "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "smoke: building hive-cloud ($(go version))"
(cd "$ROOT" && go build -o "$WORK/hive-cloud" ./cmd/hive-cloud)

echo "smoke: serving with a throwaway data dir"
"$WORK/hive-cloud" serve --listen 127.0.0.1:0 --data-dir "$WORK/data" >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 100); do
  if grep -q "listening on" "$WORK/serve.log"; then
    break
  fi
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "smoke: server exited before listening" >&2
    cat "$WORK/serve.log" >&2
    exit 1
  fi
  sleep 0.1
done
URL="$(sed -n 's/^hive-cloud: listening on \(http[^ ]*\).*/\1/p' "$WORK/serve.log" | head -1)"
if [[ -z "$URL" ]]; then
  echo "smoke: no listen address in the server log" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
curl -fsS "$URL/health" >/dev/null || { echo "smoke: /health not ready" >&2; exit 1; }
echo "smoke: serving at $URL"

echo "smoke: minting operator JWT"
JWT="$("$WORK/hive-cloud" token --data-dir "$WORK/data" --user-id u_smoke --team-id team_smoke --ttl 2h)"
if [[ -z "$JWT" ]]; then
  echo "smoke: token command produced nothing" >&2
  exit 1
fi
export HIVE_CLOUD_BASE="$URL"
export HIVE_CLOUD_JWT="$JWT"

echo "smoke: driving the service with the real go_hive globaldb client"
mkdir -p "$ROOT/evidence"
(cd "$ROOT/harness/globaldb-smoke" && EVIDENCE_OUT="$ROOT/evidence/globaldb-smoke.json" go run .)

if ! grep -q '"ok": true' "$ROOT/evidence/globaldb-smoke.json"; then
  echo "smoke: evidence reports a failed check" >&2
  exit 1
fi
if ! grep -q '"name": "list_rows_introspect"' "$ROOT/evidence/globaldb-smoke.json"; then
  echo "smoke: evidence is missing the introspection check" >&2
  exit 1
fi
echo "smoke: OK — evidence at evidence/globaldb-smoke.json"

echo "smoke: running the M2 inbox closed-loop stage"
"$HERE/inbox-smoke.sh"
echo "smoke: running the M3 streamToken rotation + hive LLM credential stage"
"$HERE/m3-smoke.sh"
echo "smoke: running the M5 Messages/images pass-through stage"
"$HERE/m5-smoke.sh"
echo "smoke: running the M4 CRM/grid_query stage"
"$HERE/m4-smoke.sh"
echo "smoke: OK — evidence at evidence/globaldb-smoke.json, evidence/inbox-smoke.json, evidence/m3-smoke.json, evidence/m5-smoke.json and evidence/m4-smoke.json"
