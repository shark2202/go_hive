#!/usr/bin/env bash
# go-hive-cloud M3 smoke (L2): streamToken rotation + `hive` LLM credential
# validation, against two real local servers.
#
#   1. server A accepts a current AND a previous streamToken secret (rotation
#      window); server B accepts only the current secret (window closed)
#   2. harness/streamtoken-smoke drives the REAL Python runtime client
#      (framework.server.streamtoken_refresh.post_refresh / _refresh_loop /
#      try_refresh_now) and records the stored `hive` credential rotation
#   3. harness/llm-credential-smoke drives the REAL go_hive keycheck
#      (llm.ValidateProviderKey("hive", …)) and credential store
#   4. the two reports merge into evidence/m3-smoke.json
#
# No external cloud is contacted. Toolchain: source the lane goenv (go1.26.11 +
# module cache) before running, or point GOENV at it; Python runs through uv.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
GOENV="${GOENV:-/Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh}"
if [[ -f "$GOENV" ]]; then
  # shellcheck disable=SC1090
  source "$GOENV"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "m3-smoke: go toolchain not found (source $GOENV)" >&2
  exit 1
fi
if ! command -v uv >/dev/null 2>&1; then
  echo "m3-smoke: uv not found (the Python runtime client needs the repo env)" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-m3-smoke.XXXXXX")"
PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORK"
}
trap cleanup EXIT

JWT_SECRET="m3-jwt-secret-0123456789abcdef0123456789"
CURRENT_SECRET="m3-current-stream-secret-0123456789abcdef"
OLD_SECRET="m3-previous-stream-secret-0123456789abcdef"
WRONG_SECRET="m3-wrong-stream-secret-0123456789abcdef"

echo "m3-smoke: building hive-cloud ($(go version))"
(cd "$ROOT" && go build -o "$WORK/hive-cloud" ./cmd/hive-cloud)

start_server() {
  local name="$1"; shift
  "$WORK/hive-cloud" serve --listen 127.0.0.1:0 --data-dir "$WORK/data-$name" "$@" >"$WORK/$name.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  for _ in $(seq 1 100); do
    if grep -q "listening on" "$WORK/$name.log"; then
      local url
      url="$(sed -n 's/^hive-cloud: listening on \(http[^ ]*\).*/\1/p' "$WORK/$name.log" | head -1)"
      if [[ -n "$url" ]]; then
        echo "$url"
        return 0
      fi
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "m3-smoke: server $name exited before listening" >&2
      cat "$WORK/$name.log" >&2
      return 1
    fi
    sleep 0.1
  done
  echo "m3-smoke: no listen address for server $name" >&2
  cat "$WORK/$name.log" >&2
  return 1
}

echo "m3-smoke: serving rotation window (current + previous) and closed window (current only)"
URL="$(start_server window --jwt-secret "$JWT_SECRET" --stream-secret "$CURRENT_SECRET" --stream-secret-previous "$OLD_SECRET")"
URL_NO_PREV="$(start_server closed --jwt-secret "$JWT_SECRET" --stream-secret "$CURRENT_SECRET")"
curl -fsS "$URL/health" >/dev/null
curl -fsS "$URL_NO_PREV/health" >/dev/null
echo "m3-smoke: window=$URL closed=$URL_NO_PREV"

mint() {
  "$WORK/hive-cloud" token --data-dir "$WORK/token-data" "$@"
}
JWT="$(mint --jwt-secret "$JWT_SECRET" --user-id u_m3 --team-id team_m3 --ttl 2h)"
OLD_TOKEN="$(mint --stream-secret "$OLD_SECRET" --type stream --user-id u_m3 --team-id team_m3 --ttl 45m)"
CURRENT_TOKEN="$(mint --stream-secret "$CURRENT_SECRET" --type stream --user-id u_m3 --team-id team_m3 --ttl 10m)"
EXPIRED_TOKEN="$(mint --stream-secret "$CURRENT_SECRET" --type stream --user-id u_m3 --team-id team_m3 --ttl 1s)"
WRONG_TOKEN="$(mint --stream-secret "$WRONG_SECRET" --type stream --user-id u_m3 --team-id team_m3 --ttl 45m)"
if [[ -z "$JWT" || -z "$OLD_TOKEN" || -z "$CURRENT_TOKEN" || -z "$EXPIRED_TOKEN" || -z "$WRONG_TOKEN" ]]; then
  echo "m3-smoke: token minting failed" >&2
  exit 1
fi
sleep 1.2 # let the 1s token expire

export HIVE_CLOUD_BASE="$URL"
export M3_BASE_NO_PREVIOUS="$URL_NO_PREV"
export M3_STREAM_SECRET="$CURRENT_SECRET"
export M3_OLD_TOKEN="$OLD_TOKEN"
export M3_CURRENT_TOKEN="$CURRENT_TOKEN"
export M3_NEAR_EXPIRY_TOKEN="$CURRENT_TOKEN"
export M3_EXPIRED_TOKEN="$EXPIRED_TOKEN"
export M3_WRONG_TOKEN="$WRONG_TOKEN"
export M3_USER_JWT="$JWT"
export M3_LLM_BASE="$URL"

echo "m3-smoke: driving the REAL Python runtime refresh client (post_refresh/_refresh_loop/try_refresh_now)"
(cd "$REPO_ROOT" && EVIDENCE_OUT="$WORK/rotation.json" uv run python "$ROOT/harness/streamtoken-smoke/main.py")

echo "m3-smoke: driving the REAL go_hive keycheck + credential store"
(cd "$ROOT/harness/llm-credential-smoke" && EVIDENCE_OUT="$WORK/llm.json" go run .)

echo "m3-smoke: merging evidence"
mkdir -p "$ROOT/evidence"
python3 "$ROOT/scripts/merge_m3_evidence.py" "$WORK/rotation.json" "$WORK/llm.json" "$ROOT/evidence/m3-smoke.json"

for check in \
  rotation_refresh_rotates \
  rotation_refresh_loop_saves_hive_credential \
  rotation_try_refresh_now \
  old_token_rejected_without_previous_secret \
  llm_valid_stream_token_keycheck \
  llm_expired_token_keycheck \
  hive_credential_store_roundtrip; do
  if ! grep -q "\"name\": \"$check\"" "$ROOT/evidence/m3-smoke.json"; then
    echo "m3-smoke: evidence is missing check $check" >&2
    exit 1
  fi
done
if ! grep -q '"ok": true' "$ROOT/evidence/m3-smoke.json"; then
  echo "m3-smoke: evidence reports a failed check" >&2
  cat "$ROOT/evidence/m3-smoke.json" >&2
  exit 1
fi
echo "m3-smoke: OK — evidence at evidence/m3-smoke.json"
