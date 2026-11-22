#!/usr/bin/env bash
# go-hive-cloud L2a smoke (M1 acceptance):
#   1. build the service, boot it on a random port with a throwaway data dir
#   2. mint an operator JWT with the real `hive-cloud token` command
#   3. drive every global-db method with the REAL go_hive client
#      (harness/globaldb-smoke → github.com/shark2202/go_hive/go-hive/internal/globaldb)
#   4. assert the harness verdict and leave evidence at evidence/globaldb-smoke.json
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
