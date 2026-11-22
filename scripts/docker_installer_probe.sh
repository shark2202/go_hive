#!/usr/bin/env bash
# Runs INSIDE a linux/arm64 container. Verifies the linux/arm64 release
# archive end to end with real execution:
#   C1 archive sha256 matches the release-wide SHA256SUMS
#   C2 extraction yields the four binaries
#   C3 `hive --version` exit 0 and prints the expected version
#   C4 `hive --help` exit 0
#   C5 fresh HIVE_HOME: `hive serve` comes up; GET /api/health -> 200 + JSON
#   C6 GET / -> 200 (embedded dashboard served from the binary)
#   C7 `hive-cred --help` exit 0
#   C8 (bonus) `hive-browser help` / `browserbridge -version` exit 0
#   C9 `hive serve` exits on SIGTERM
#   C10 (bonus) HIVE_BROWSER_BRIDGE=1: `hive serve` resolves the sibling
#       browserbridge, the bridge announces ready and answers /health, and it
#       is gone after serve exits
#
# Inputs (env):
#   ARCHIVE_NAME    archive basename under /release (required)
#   EXPECT_VERSION  version string that `hive --version` must contain
#   PORT            serve port (default 18787)
#   OUT             transcript path (default /evidence/docker-run.log)
#
# The transcript (commands, outputs, exit codes) is appended to $OUT; the
# script exits non-zero if any check failed.
set -uo pipefail

ARCHIVE_NAME="${ARCHIVE_NAME:?ARCHIVE_NAME required}"
EXPECT_VERSION="${EXPECT_VERSION:-}"
PORT="${PORT:-18787}"
BRIDGE_PORT="${BRIDGE_PORT:-14930}"
BRIDGE_WS_PORT="${BRIDGE_WS_PORT:-14929}"
OUT="${OUT:-/evidence/docker-run.log.txt}"
ARCHIVE="/release/$ARCHIVE_NAME"

PASS=0
FAIL=0
log() { printf '%s\n' "$*" | tee -a "$OUT"; }
ok() { PASS=$((PASS + 1)); log "  ok  : $*"; }
bad() { FAIL=$((FAIL + 1)); log "  FAIL: $*"; }
run() { # run a command; echo cmd, output, exit code into the transcript
	log "\$ $*"
	"$@" >>"$OUT" 2>&1
	rc=$?
	log "exit=$rc"
	return $rc
}

: >"$OUT"
log "container : $(uname -srm) $(grep -o 'PRETTY_NAME=.*' /etc/os-release 2>/dev/null | head -1)"
log "archive   : $ARCHIVE_NAME"
log "expect    : version='$EXPECT_VERSION' port=$PORT"
log ""

# C1 release-wide checksum (only the one archive; others are not mounted)
log "\$ (cd /release && sha256sum -c SHA256SUMS --ignore-missing) [filtered to $ARCHIVE_NAME]"
if (cd /release && awk -v want="$ARCHIVE_NAME" '{ f=$2; sub(/^\*/,"",f); if (f==want) print }' SHA256SUMS >/tmp/wanted.sums \
	&& sha256sum -c /tmp/wanted.sums >>"$OUT" 2>&1); then
	ok "C1 sha256 matches release SHA256SUMS"
else
	bad "C1 sha256 mismatch"
fi

# C2 extraction
mkdir -p /tmp/hive
if tar -xzf "$ARCHIVE" -C /tmp/hive >>"$OUT" 2>&1; then
	ok "C2 extracted $(ls /tmp/hive | tr '\n' ' ')"
else
	bad "C2 extraction failed"
fi
cd /tmp/hive || { bad "C2 cannot cd /tmp/hive"; log "RESULT: ABORT"; exit 2; }
for b in hive hive-cred hive-browser browserbridge; do
	[ -x "$b" ] && ok "C2 $b executable" || bad "C2 $b missing/not executable"
done

# C3 --version
version_out="$(./hive --version 2>&1)"
version_rc=$?
log "\$ ./hive --version"
log "$version_out"
log "exit=$version_rc"
if [ "$version_rc" = 0 ] && [ -n "$version_out" ]; then
	ok "C3 hive --version exit=0: $version_out"
	if [ -n "$EXPECT_VERSION" ]; then
		case "$version_out" in
			*"$EXPECT_VERSION"*) ok "C3 version matches expected '$EXPECT_VERSION'" ;;
			*) bad "C3 version does not contain '$EXPECT_VERSION'" ;;
		esac
	fi
else
	bad "C3 hive --version exit=$version_rc"
fi

# C4 --help
help_out="$(./hive --help 2>&1)"
help_rc=$?
log "\$ ./hive --help"
log "$(printf '%s\n' "$help_out" | head -n 12)"
log "... ($(printf '%s\n' "$help_out" | wc -l | tr -d ' ') lines total)"
log "exit=$help_rc"
if [ "$help_rc" = 0 ] && printf '%s' "$help_out" | grep -qi 'usage'; then
	ok "C4 hive --help exit=0, usage text present"
else
	bad "C4 hive --help exit=$help_rc (usage present: $(printf '%s' "$help_out" | grep -ci usage))"
fi

# C5/C6/C9: fresh HIVE_HOME + serve + HTTP probes + SIGTERM
HOME_DIR=/tmp/hivehome-fresh
log ""
log "fresh HIVE_HOME: $HOME_DIR exists before serve? $([ -e "$HOME_DIR" ] && echo yes || echo no)"
HIVE_HOME="$HOME_DIR" HIVE_BROWSER_BRIDGE=0 ./hive serve --port "$PORT" >/tmp/serve.log 2>&1 &
SERVE_PID=$!
log "\$ HIVE_HOME=$HOME_DIR HIVE_BROWSER_BRIDGE=0 ./hive serve --port $PORT &  (pid $SERVE_PID)"

ready=0
for _ in $(seq 1 120); do
	if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then
		exec 3<&- 3>&- 2>/dev/null || true
		ready=1
		break
	fi
	kill -0 "$SERVE_PID" 2>/dev/null || break
	sleep 0.5
done
http_get() { # $1 port, $2 path, $3 outfile
	exec 3<>"/dev/tcp/127.0.0.1/$1" || return 1
	printf 'GET %s HTTP/1.0\r\nHost: 127.0.0.1:%s\r\nConnection: close\r\n\r\n' "$2" "$1" >&3
	timeout 15 cat <&3 >"$3"
	exec 3<&- 3>&- 2>/dev/null || true
}
status_of() { head -n 1 "$1" | awk '{print $2}'; }
body_of() { awk 'body{print} { gsub(/\r$/,""); if ($0=="") body=1 }' "$1"; }

if [ "$ready" = 1 ]; then
	ok "C5 serve listening on 127.0.0.1:$PORT"
	if http_get "$PORT" /api/health /tmp/health.resp; then
		code="$(status_of /tmp/health.resp)"
		body="$(body_of /tmp/health.resp)"
		log "\$ GET /api/health -> $code"
		log "$body"
		if [ "$code" = 200 ] && printf '%s' "$body" | grep -q '"status":"ok"'; then
			ok "C5 GET /api/health -> 200 {\"status\":\"ok\"}"
		else
			bad "C5 /api/health code=$code body=$body"
		fi
	else
		bad "C5 /api/health request failed"
	fi
	if http_get "$PORT" / /tmp/root.resp; then
		code="$(status_of /tmp/root.resp)"
		marker="$(body_of /tmp/root.resp | head -c 120 | tr '\n' ' ')"
		log "\$ GET / -> $code (first 120 bytes: $marker)"
		if [ "$code" = 200 ]; then
			ok "C6 GET / -> 200 (embedded dashboard)"
		else
			bad "C6 GET / -> $code"
		fi
	else
		bad "C6 GET / failed"
	fi
else
	bad "C5 serve never listened (log tail:)"; tail -n 8 /tmp/serve.log | sed 's/^/        /'
fi

log ""
log "HIVE_HOME after serve (first 25 entries):"
find "$HOME_DIR" -maxdepth 2 2>/dev/null | head -n 25 | sed 's/^/        /'
if [ -d "$HOME_DIR" ]; then
	ok "C5 fresh HIVE_HOME created: $HOME_DIR"
else
	bad "C5 HIVE_HOME was not created"
fi

kill -TERM "$SERVE_PID" 2>/dev/null
for _ in $(seq 1 40); do
	kill -0 "$SERVE_PID" 2>/dev/null || break
	sleep 0.25
done
if kill -0 "$SERVE_PID" 2>/dev/null; then
	bad "C9 serve survived SIGTERM"
	kill -9 "$SERVE_PID" 2>/dev/null || true
else
	ok "C9 serve exited on SIGTERM"
fi

# C7 hive-cred --help
cred_out="$(./hive-cred --help 2>&1)"
cred_rc=$?
log ""
log "\$ ./hive-cred --help"
log "$(printf '%s\n' "$cred_out" | head -n 6)"
log "exit=$cred_rc"
if [ "$cred_rc" = 0 ]; then
	ok "C7 hive-cred --help exit=0"
else
	bad "C7 hive-cred --help exit=$cred_rc"
fi

# C8 bonus CLI surfaces
browse_rc=0; ./hive-browser help >/tmp/hb.log 2>&1 || browse_rc=$?
bridge_rc=0; ./browserbridge -version >/tmp/bb.log 2>&1 || bridge_rc=$?
log ""
log "\$ ./hive-browser help   exit=$browse_rc (first line: $(head -n 1 /tmp/hb.log))"
log "\$ ./browserbridge -version exit=$bridge_rc ($(head -n 1 /tmp/bb.log))"
if [ "$browse_rc" = 0 ]; then ok "C8 hive-browser help exit=0"; else bad "C8 hive-browser help exit=$browse_rc"; fi
if [ "$bridge_rc" = 0 ]; then ok "C8 browserbridge -version exit=0"; else bad "C8 browserbridge -version exit=$bridge_rc"; fi

# C10 bonus: `hive serve` resolves and supervises the sibling browserbridge
log ""
HIVE_HOME="$HOME_DIR" HIVE_BROWSER_BRIDGE=1 \
	HIVE_BROWSERBRIDGE_HTTP="127.0.0.1:$BRIDGE_PORT" \
	HIVE_BROWSERBRIDGE_ARGS="-ws 127.0.0.1:$BRIDGE_WS_PORT -no-legacy" \
	./hive serve --port "$PORT" >/tmp/serve-bridge.log 2>&1 &
SERVE2_PID=$!
log "\$ HIVE_BROWSER_BRIDGE=1 HIVE_BROWSERBRIDGE_HTTP=127.0.0.1:$BRIDGE_PORT ./hive serve --port $PORT & (pid $SERVE2_PID)"
bready=0
for _ in $(seq 1 120); do
	if grep -q "Browser bridge: ready" /tmp/serve-bridge.log 2>/dev/null; then bready=1; break; fi
	kill -0 "$SERVE2_PID" 2>/dev/null || break
	sleep 0.5
done
if [ "$bready" = 1 ]; then
	log "$(grep 'Browser bridge: ready' /tmp/serve-bridge.log | tail -1)"
	ok "C10 serve announced browserbridge ready (sibling resolved next to hive)"
	if http_get "$BRIDGE_PORT" /health /tmp/bridge.resp; then
		bcode="$(status_of /tmp/bridge.resp)"
		bbody="$(body_of /tmp/bridge.resp)"
		log "\$ GET http://127.0.0.1:$BRIDGE_PORT/health -> $bcode $bbody"
		if [ "$bcode" = 200 ]; then ok "C10 bridge /health -> 200"; else bad "C10 bridge /health -> $bcode"; fi
	else
		bad "C10 bridge /health request failed"
	fi
else
	bad "C10 browserbridge never announced ready"; tail -n 8 /tmp/serve-bridge.log | sed 's/^/        /'
fi
kill -TERM "$SERVE2_PID" 2>/dev/null
for _ in $(seq 1 40); do kill -0 "$SERVE2_PID" 2>/dev/null || break; sleep 0.25; done
kill -9 "$SERVE2_PID" 2>/dev/null || true
gone=0
for _ in $(seq 1 20); do
	if ! (exec 3<>"/dev/tcp/127.0.0.1/$BRIDGE_PORT") 2>/dev/null; then gone=1; break; fi
	exec 3<&- 3>&- 2>/dev/null || true
	sleep 0.25
done
if [ "$gone" = 1 ]; then
	ok "C10 bridge stopped with serve (port $BRIDGE_PORT closed)"
else
	bad "C10 port $BRIDGE_PORT still open after serve exit"
fi

log ""
if [ "$FAIL" = 0 ]; then
	log "RESULT: PASS ($PASS checks)"
else
	log "RESULT: FAIL ($FAIL of $((PASS + FAIL)) checks failed)"
fi
log "transcript: $OUT"
[ "$FAIL" = 0 ]
