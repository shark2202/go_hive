#!/usr/bin/env bash
# worker_stream_network_e2e.sh — real loopback HTTP/SSE stream-stall E2E.
#
# Drives the production colony worker and overseer loops through the real
# llm.Provider OpenAI-compatible SSE transport against an httptest server on
# loopback TCP. No external model or network is contacted: the server injects
# (a) a withheld first byte (TTFT stall) or (b) silence after the first SSE
# event (inter-event stall), then serves a complete stream to the retried
# call. The test asserts the watchdog verdict, the STREAM_STALLED continue
# nudge, the same-inner-turn re-stream and the recovered turn.
#
# Evidence (JSON results, verbose log, package test log, exact commands and
# SHA256 sums) lands in go_hive/docs/e2e-assets/worker-stream-network/.
#
# Usage:
#   go_hive/scripts/worker_stream_network_e2e.sh
#   WORKER_STREAM_NETWORK_OUT=/tmp/evidence go_hive/scripts/worker_stream_network_e2e.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE_DIR="$GO_HIVE_DIR/go-hive"
OUT="${WORKER_STREAM_NETWORK_OUT:-$GO_HIVE_DIR/docs/e2e-assets/worker-stream-network}"

mkdir -p "$OUT"
export HIVE_WORKER_STREAM_E2E_OUT="$OUT"

cd "$MODULE_DIR" || exit 2

env_cmd=(env CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod)
packages_cmd=(go test ./internal/colony ./internal/agentloop -run 'Stream|Stall' -count=1)
e2e_cmd=(go test ./internal/colony -run 'TestWorkerOverseerStreamStallRealNetwork' -count=1 -v)

echo "=== package stream/stall tests ==="
"${env_cmd[@]}" "${packages_cmd[@]}" 2>&1 | tee "$OUT/stream-stall-packages.log.txt"
packages_status=${PIPESTATUS[0]}

echo "=== real-network stream-stall E2E ==="
"${env_cmd[@]}" "${e2e_cmd[@]}" 2>&1 | tee "$OUT/test.log.txt"
e2e_status=${PIPESTATUS[0]}

status=$packages_status
if [ "$e2e_status" -ne 0 ]; then
  status=$e2e_status
fi

display_out="$OUT"

{
  echo "# generated $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "# workdir: go_hive/go-hive"
  echo "# evidence dir: $display_out"
  echo "HIVE_WORKER_STREAM_E2E_OUT=$display_out ${env_cmd[*]} go test ./internal/colony ./internal/agentloop -run 'Stream|Stall' -count=1"
  echo "exit_code: $packages_status"
  echo "HIVE_WORKER_STREAM_E2E_OUT=$display_out ${env_cmd[*]} ${e2e_cmd[*]}"
  echo "exit_code: $e2e_status"
} > "$OUT/commands.txt"

if [ -f "$OUT/results.json" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$OUT" && sha256sum results.json test.log.txt stream-stall-packages.log.txt commands.txt > SHA256SUMS )
  else
    ( cd "$OUT" && shasum -a 256 results.json test.log.txt stream-stall-packages.log.txt commands.txt > SHA256SUMS )
  fi
fi

echo "worker-stream-network e2e: packages_exit=$packages_status e2e_exit=$e2e_status evidence=$OUT"
exit "$status"
