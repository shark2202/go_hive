#!/usr/bin/env bash
# M4 smoke stage: grid_query-generated Postgres SQL through the real go_hive
# global-db client + the /v1/crm/* generic grid subset over operator JWT.
# Boots its own throwaway server on a random port; evidence/m4-smoke.json.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
GOENV="${GOENV:-/Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh}"
if [[ -f "$GOENV" ]]; then
  # shellcheck disable=SC1090
  source "$GOENV"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "m4-smoke: go toolchain not found (source $GOENV)" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-m4.XXXXXX")"
SERVE_PID=""
cleanup() {
  if [[ -n "$SERVE_PID" ]]; then
    kill "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "m4-smoke: building hive-cloud"
(cd "$ROOT" && go build -o "$WORK/hive-cloud" ./cmd/hive-cloud)

echo "m4-smoke: serving with a throwaway data dir"
"$WORK/hive-cloud" serve --listen 127.0.0.1:0 --data-dir "$WORK/data" >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 100); do
  if grep -q "listening on" "$WORK/serve.log"; then
    break
  fi
  if ! kill -0 "$SERVE_PID" 2>/dev/null; then
    echo "m4-smoke: server exited before listening" >&2
    cat "$WORK/serve.log" >&2
    exit 1
  fi
  sleep 0.1
done
URL="$(sed -n 's/^hive-cloud: listening on \(http[^ ]*\).*/\1/p' "$WORK/serve.log" | head -1)"
if [[ -z "$URL" ]]; then
  echo "m4-smoke: no listen address in the server log" >&2
  cat "$WORK/serve.log" >&2
  exit 1
fi
curl -fsS "$URL/health" >/dev/null || { echo "m4-smoke: /health not ready" >&2; exit 1; }

JWT="$("$WORK/hive-cloud" token --data-dir "$WORK/data" --user-id u_m4 --team-id team_m4 --ttl 2h)"
if [[ -z "$JWT" ]]; then
  echo "m4-smoke: token command produced nothing" >&2
  exit 1
fi
export HIVE_CLOUD_BASE="$URL"
export HIVE_CLOUD_JWT="$JWT"

mkdir -p "$ROOT/evidence"
echo "m4-smoke: running the M4 harness"
(cd "$ROOT/harness/m4-smoke" && EVIDENCE_OUT="$ROOT/evidence/m4-smoke.json" go run .)

if ! grep -q '"ok": true' "$ROOT/evidence/m4-smoke.json"; then
  echo "m4-smoke: evidence reports a failed check" >&2
  exit 1
fi
echo "m4-smoke: OK — evidence at evidence/m4-smoke.json"
