#!/usr/bin/env bash
# Verify a release archive on the local platform: unpack it, run `./hive serve`
# from the extracted directory with NO HIVE_BROWSERBRIDGE_BIN, and assert the
# sibling `browserbridge` is resolved, started, healthy, drivable through
# `hive-browser`, and reaped on SIGTERM.
#
# This is the release-layout counterpart of scripts/e2e_browser_host.sh:
# e2e_browser_host.sh builds from source, this one proves the *archive* that
# users download actually carries a working browser host.
#
# Usage:
#   scripts/verify_release_archive.sh [release-dir]
#
# Arguments:
#   release-dir    directory holding hive-<version>-<os>-<arch>.tar.gz
#                  (default: <go-hive>/dist/release)
#
# Environment:
#   BRIDGE_HTTP_PORT  isolated bridge HTTP port (default 14930)
#   BRIDGE_WS_PORT    isolated bridge WebSocket port (default 14929)
#   SERVE_PORT        isolated `hive serve` HTTP port (default 8797)
#
# The three ports are deliberately off the product defaults (14829/14830/
# 9229/8787) so the check can run while a dev `hive serve` + bridge is up; the
# bridge is moved with HIVE_BROWSERBRIDGE_HTTP / HIVE_BROWSERBRIDGE_ARGS.
# Supported: darwin/linux on amd64/arm64.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE_DIR="$GO_HIVE_ROOT/go-hive"
RELEASE_DIR="${1:-$MODULE_DIR/dist/release}"
BRIDGE_PORT="${BRIDGE_HTTP_PORT:-14930}"
BRIDGE_WS_PORT="${BRIDGE_WS_PORT:-14929}"
SERVE_PORT="${SERVE_PORT:-8797}"

PASS=0
FAIL=0
OUT=""
SERVE_PID=""
BRIDGE_PID=""
ok() { printf '  ok  : %s\n' "$*"; PASS=$((PASS + 1)); }
bad() { printf '  FAIL: %s\n' "$*"; FAIL=$((FAIL + 1)); }
log() { printf '%s\n' "$*"; }
die() { printf 'verify: ERROR: %s\n' "$*" >&2; exit 2; }

cleanup() {
	if [ -n "$SERVE_PID" ] && kill -0 "$SERVE_PID" 2>/dev/null; then
		kill -TERM "$SERVE_PID" 2>/dev/null || true
		for _ in $(seq 1 20); do
			kill -0 "$SERVE_PID" 2>/dev/null || break
			sleep 0.25
		done
		kill -9 "$SERVE_PID" 2>/dev/null || true
	fi
	if [ -n "$BRIDGE_PID" ] && ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1; then
		kill -9 "$BRIDGE_PID" 2>/dev/null || true
	fi
	if [ -n "$OUT" ]; then
		log "evidence: $OUT"
	fi
}
trap cleanup EXIT INT TERM

case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "unsupported OS: $(uname -s) (use scripts/e2e_browser_host.sh on Windows)" ;;
esac
arch="$(uname -m)"
case "$arch" in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) die "unsupported architecture: $arch" ;;
esac

for tool in tar curl ps; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

archive="$(ls -1 "$RELEASE_DIR"/hive-*-"$os"-"$arch".tar.gz 2>/dev/null | tail -1)"
[ -n "$archive" ] || die "no hive-*-$os-$arch.tar.gz under $RELEASE_DIR (run scripts/release.sh first)"

OUT="$(mktemp -d "${TMPDIR:-/tmp}/hive-release-verify.XXXXXX")"
OUT="$(cd "$OUT" && pwd)"

if command -v lsof >/dev/null 2>&1; then
	for p in "$BRIDGE_PORT" "$BRIDGE_WS_PORT" "$SERVE_PORT"; do
		if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
			die "port $p is already in use; set BRIDGE_HTTP_PORT/BRIDGE_WS_PORT/SERVE_PORT"
		fi
	done
else
	log "verify: lsof not found; skipping the port preflight"
fi

mkdir -p "$OUT/extract" "$OUT/home"
tar -xzf "$archive" -C "$OUT/extract"
log "archive : $archive"
log "workdir : $OUT"
log "members : $(cd "$OUT/extract" && printf '%s ' *)"
log "target  : $os/$arch; bridge=127.0.0.1:$BRIDGE_PORT ws=127.0.0.1:$BRIDGE_WS_PORT serve=127.0.0.1:$SERVE_PORT"
log "env     : HIVE_BROWSERBRIDGE_BIN unset (sibling resolution is under test)"
log ""

for b in hive hive-cred hive-browser browserbridge; do
	if [ -x "$OUT/extract/$b" ]; then ok "extracted $b is executable"; else bad "extracted $b missing or not executable"; fi
done

cd "$OUT/extract" || die "cannot cd $OUT/extract"
serve_log="$OUT/serve.log"
env -u HIVE_BROWSERBRIDGE_BIN \
	HIVE_HOME="$OUT/home" HIVE_BROWSER_BRIDGE=1 \
	HIVE_BROWSERBRIDGE_HTTP="127.0.0.1:$BRIDGE_PORT" \
	HIVE_BROWSERBRIDGE_ARGS="-ws 127.0.0.1:$BRIDGE_WS_PORT -no-legacy" \
	./hive serve --port "$SERVE_PORT" >"$serve_log" 2>&1 &
SERVE_PID=$!

ready=0
for _ in $(seq 1 60); do
	if grep -q "Browser bridge: ready on 127.0.0.1:$BRIDGE_PORT" "$serve_log"; then ready=1; break; fi
	kill -0 "$SERVE_PID" 2>/dev/null || break
	sleep 0.5
done
if [ "$ready" = 1 ]; then
	ok "serve announced: $(grep 'Browser bridge: ready' "$serve_log" | tail -1)"
else
	bad "serve never announced a ready bridge; log tail:"
	tail -5 "$serve_log"
fi

BRIDGE_PID="$(sed -n "s/.*Browser bridge: ready on 127.0.0.1:$BRIDGE_PORT (pid \([0-9]*\)).*/\1/p" "$serve_log" | tail -1)"
if [ -n "$BRIDGE_PID" ] && ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1; then
	ppid="$(ps -p "$BRIDGE_PID" -o ppid= | tr -d ' ')"
	cmdline="$(ps -p "$BRIDGE_PID" -o command= | tr -d '\n')"
	log "  bridge child: $cmdline"
	if [ "$ppid" = "$SERVE_PID" ]; then ok "bridge (pid $BRIDGE_PID) is a child of serve (pid $SERVE_PID)"; else bad "bridge ppid=$ppid, want serve pid $SERVE_PID"; fi
	case "$cmdline" in
		"$OUT/extract/browserbridge"*) ok "child command resolves to the extracted archive binary" ;;
		*) bad "child command is not the extracted archive binary" ;;
	esac
else
	bad "no live bridge child process"
fi

health_code="$(curl -s -o "$OUT/health.json" -w '%{http_code}' --max-time 5 "http://127.0.0.1:$BRIDGE_PORT/health")"
if [ "$health_code" = 200 ]; then
	ok "GET http://127.0.0.1:$BRIDGE_PORT/health -> 200 $(cat "$OUT/health.json")"
else
	bad "GET http://127.0.0.1:$BRIDGE_PORT/health -> $health_code"
fi

# The CLI ships in the same archive; with a bridge but no Chrome the contract
# is exit 2 (not_connected) and a JSON body with connected=false.
env -u HIVE_BROWSERBRIDGE_BIN HIVE_BROWSERBRIDGE_HTTP="127.0.0.1:$BRIDGE_PORT" \
	./hive-browser status --json >"$OUT/cli-status.json" 2>"$OUT/cli-status.err"
cli_code=$?
if [ "$cli_code" = 2 ]; then
	ok "hive-browser status --json exit=2 (not_connected)"
else
	bad "hive-browser status exit=$cli_code want 2 ($(head -c 120 "$OUT/cli-status.json" 2>/dev/null))"
fi
if grep -q '"connected": *false' "$OUT/cli-status.json"; then ok "status --json reports connected=false"; else bad "status --json did not report connected=false"; fi

kill -TERM "$SERVE_PID" 2>/dev/null
for _ in $(seq 1 40); do
	kill -0 "$SERVE_PID" 2>/dev/null || break
	sleep 0.25
done
if kill -0 "$SERVE_PID" 2>/dev/null; then bad "serve did not exit on SIGTERM"; else ok "serve exited on SIGTERM"; fi
SERVE_PID=""
if [ -n "$BRIDGE_PID" ]; then
	for _ in $(seq 1 40); do
		ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1 || break
		sleep 0.25
	done
	if ps -p "$BRIDGE_PID" -o pid= >/dev/null 2>&1; then bad "bridge child $BRIDGE_PID survived serve shutdown"; else ok "bridge child $BRIDGE_PID reaped with serve (SIGTERM)"; fi
fi
if curl -sf --max-time 3 "http://127.0.0.1:$BRIDGE_PORT/health" >/dev/null 2>&1; then
	bad "something still answers http://127.0.0.1:$BRIDGE_PORT/health"
else
	ok "nothing answers http://127.0.0.1:$BRIDGE_PORT/health after shutdown"
fi

log ""
if [ "$FAIL" = 0 ]; then
	log "PASS: release archive brings up and reaps the browser bridge ($PASS checks)"
else
	log "FAILED: $FAIL of $((PASS + FAIL)) checks"
fi
exit "$FAIL"