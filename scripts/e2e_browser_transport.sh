#!/usr/bin/env bash
# e2e_browser_transport.sh — transport/lifecycle parity probe.
#
# Builds the real browserbridge + hive-browser binaries, then runs
# scripts/e2e_browser_transport.py, which asserts the five收口 items:
#
#   1  WS-layer keepalive (200ms/200ms via -ws-ping/-ws-pong): a peer that
#      never pongs is closed with 1011 "keepalive ping timeout"; a ponging
#      peer stays connected.
#   2  legacy 9229/9230 pair: the status endpoint answers the primary payload
#      (listening.legacy_status true) and the legacy WS accepts a hello.
#   3  CLI auto-ensure: `hive-browser status` with no --bridge spawns the
#      bridge detached, waits for /health, and succeeds.
#   4  exit codes: local precheck 6; bridge bad_request text decides (4 for a
#      "File not found" round trip; 1 for a fragment-less message).
#   5  parent death: the default -parent-check is upstream's 5s × 2, measured
#      exit window 5–10s.
#   6  legacy self-collision guard (no duplicate bind of the primary address).
#
# Ports are all ephemeral; the developer's resident bridge (14829–14831) is
# never touched. Everything is torn down on exit.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
go_hive=$(cd "$here/.." && pwd)
root=$(cd "$go_hive/.." && pwd)

tmp=$(mktemp -d "${TMPDIR:-/tmp}/browser-transport-e2e.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

echo "== build =="
(cd "$go_hive/browserbridge" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go build -o "$tmp/browserbridge" ./cmd/browserbridge)
(cd "$go_hive/go-hive" && CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go build -o "$tmp/hive-browser" ./cmd/hive-browser)
echo "browserbridge: $("$tmp/browserbridge" -version)"
echo "hive-browser built"

echo "== probe =="
uv run --offline --project "$root/tools" python "$here/e2e_browser_transport.py" \
  --bridge "$tmp/browserbridge" --cli "$tmp/hive-browser"
