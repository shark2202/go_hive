#!/usr/bin/env bash
# go-hive-cloud M5 smoke (L2): real go_hive Anthropic client + real hive-cloud
# binary + local fake upstream (Messages JSON/SSE, 4xx forwarding, cancellation,
# images pass-through). No external network is contacted.
#
# Evidence: evidence/m5-smoke.json (statuses/counters/key fingerprints only;
# no token or key plaintext).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
GOENV="${GOENV:-/Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh}"
if [[ -f "$GOENV" ]]; then
  # shellcheck disable=SC1090
  source "$GOENV"
fi
if ! command -v go >/dev/null 2>&1; then
  echo "m5-smoke: go toolchain not found (source $GOENV)" >&2
  exit 1
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-m5-smoke.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

echo "m5-smoke: building hive-cloud ($(go version))"
(cd "$ROOT" && go build -o "$WORK/hive-cloud" ./cmd/hive-cloud)

mkdir -p "$ROOT/evidence"
echo "m5-smoke: driving the proxy with the real go_hive Anthropic client"
(cd "$ROOT/harness/llm-proxy-smoke" && HIVE_CLOUD_BIN="$WORK/hive-cloud" EVIDENCE_OUT="$ROOT/evidence/m5-smoke.json" go run .)

if ! grep -q '"ok": true' "$ROOT/evidence/m5-smoke.json"; then
  echo "m5-smoke: evidence reports a failed check" >&2
  cat "$ROOT/evidence/m5-smoke.json" >&2
  exit 1
fi

for check in \
  messages_nonstream_real_client \
  messages_stream_real_client \
  messages_stream_incremental \
  messages_upstream_429_forwarded \
  messages_invalid_token_401 \
  messages_operator_jwt_accepted \
  messages_cancel_propagates \
  unconfigured_answers_explicit \
  images_pass_through \
  images_upstream_402_forwarded \
  upstream_credential_isolation; do
  if ! grep -q "\"name\": \"$check\"" "$ROOT/evidence/m5-smoke.json"; then
    echo "m5-smoke: evidence is missing check $check" >&2
    exit 1
  fi
done
echo "m5-smoke: OK — evidence at evidence/m5-smoke.json"
