#!/usr/bin/env bash
# g2_rollback_drill.sh — G2 rollback + install/upgrade drill (evidence generator).
#
# Stage 1 (rollback, Go -> Python):
#   v2 fixture home -> Go `hive serve` (startup migration) -> Go storagecompat
#   write-probes (session + credentials) -> snapshot ->
#   Python CLI reads (session list --cold / queen sessions / colony list) ->
#   Python API read-back (history / credentials / tracker via framework.*) ->
#   Python `hive serve` on the same home -> Go CLI cold read -> Go serve #2.
#   Every step is bracketed by whole-home manifests (sha256 + SQLite row
#   digests) so the diffs prove what each side wrote.
#
# Stage 2 (install/upgrade):
#   build the previous release's hive/hive-cred from OLD_TAG, create an "old
#   user" home (v2 layout + old serve artifacts) -> build the release archive
#   with go_hive/scripts/release.sh -> extract it -> new `hive serve` migrates
#   the old home -> sessions become readable -> previous binary also runs on
#   the migrated home -> install.sh upgrades an existing install in place ->
#   install-dir `serve` health -> verify_release_archive.sh.
#
# Usage:
#   bash go_hive/scripts/g2_rollback_drill.sh [--work DIR] [--skip-upgrade]
#        [--no-embed] [--keep]
#
#   --work DIR      scratch directory (default: mktemp -d)
#   --skip-upgrade  run stage 1 only
#   --no-embed      build the release archive without the embedded dashboard
#   --keep          keep going past a failing check (default: set -e)
#
# Env:
#   OLD_TAG         previous release tag to build the "old" binaries from
#                   (default: v1.0.25-go.1)
#   FRONTEND_DIST   prebuilt dashboard dir for the embedded-UI archive; when
#                   unset the script looks for <repo>/go_hive/go-hive/frontend/dist
#                   and falls back to --no-embed when neither exists.
#
# Only system tools plus go/uv are used. Outputs land under <work>/:
#   logs/  snap/  bin/  bin-old/  dist/release/  install-bin/  upgrade/
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_MODULE="$REPO/go_hive/go-hive"
BRIDGE_MODULE="$REPO/go_hive/browserbridge"
PROBE="$REPO/go_hive/scripts/g2_rollback_probe.py"
FIXTURE="$REPO/go_hive/scripts/storage_compat_gen_fixture.py"
OLD_TAG="${OLD_TAG:-v1.0.25-go.1}"

WORK=""
SKIP_UPGRADE=0
NO_EMBED=0
while [ $# -gt 0 ]; do
	case "$1" in
		--work) WORK="$2"; shift 2 ;;
		--skip-upgrade) SKIP_UPGRADE=1; shift ;;
		--no-embed) NO_EMBED=1; shift ;;
		-h|--help) sed -n '2,50p' "$0"; exit 0 ;;
		*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done
[ -n "$WORK" ] || WORK="$(mktemp -d "${TMPDIR:-/tmp}/g2-rollback.XXXXXX")"
WORK="$(cd "$WORK" && pwd)"
LOGS="$WORK/logs"; SNAP="$WORK/snap"
mkdir -p "$LOGS" "$SNAP" "$WORK/bin" "$WORK/bin-old"

log() { printf '\n=== %s\n' "$*"; }
pyrepo() { (cd "$REPO/core" && uv run --no-sync python "$PROBE" "$@"); }
pycli() { (cd "$REPO/core" && HIVE_HOME="$1" uv run --no-sync hive "${@:2}"); }
manifest() { pyrepo manifest --home "$1" --out "$SNAP/$2"; }
diffy() { pyrepo diff --before "$SNAP/$1" --after "$SNAP/$2" --out "$SNAP/$3"; }
summary() { (cd "$REPO/core" && uv run --no-sync python - "$SNAP/$1" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print(f"{sys.argv[1].split('/')[-1]:32s} {d['summary']}")
PY
); }

for tool in go git uv curl; do
	command -v "$tool" >/dev/null 2>&1 || { echo "g2-rollback: missing $tool" >&2; exit 2; }
done

# Refuse to start when one of the drill ports already has a listener: a stray
# server from a previous run would otherwise answer our health probes.
if command -v lsof >/dev/null 2>&1; then
	for drill_port in 8798 8799 8800 8801 8802 8803 8804 8805 8806 8810 14939 14940; do
		if lsof -nP -iTCP:"$drill_port" -sTCP:LISTEN >/dev/null 2>&1; then
			echo "g2-rollback: port $drill_port is already in use; free it or override the drill" >&2
			exit 2
		fi
	done
fi

# ---------------------------------------------------------------------------
# probe helpers (start serve, wait for health, read, stop)
# ---------------------------------------------------------------------------
serve_probe() { # bindir home port log tag
	local bindir="$1" home="$2" port="$3" logf="$4" tag="$5"
	(
		export HIVE_HOME="$home" HIVE_BROWSER_BRIDGE=0
		"$bindir/hive" serve --port "$port" >"$logf" 2>&1 &
		local pid=$! code=""
		for _ in $(seq 1 120); do
			code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/health" || true)
			[ "$code" = "200" ] && break
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		printf '%s health=%s\n' "$tag" "$code"
		root_code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" || true)
		printf '%s root=%s\n' "$tag" "$root_code"
		curl -s "http://127.0.0.1:$port/api/sessions" | head -c 500; echo
		if [ -f "$home/maintenance/server.lock" ]; then
			printf '%s marker=present %s\n' "$tag" "$(cat "$home/maintenance/server.lock")"
		else
			printf '%s marker=absent\n' "$tag"
		fi
		kill -TERM "$pid" 2>/dev/null || true
		for _ in $(seq 1 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done
		if kill -0 "$pid" 2>/dev/null; then kill -9 "$pid" 2>/dev/null || true; printf '%s KILLED\n' "$tag"; fi
		if [ -f "$home/maintenance/server.lock" ]; then printf '%s marker-after=present\n' "$tag"; else printf '%s marker-after=absent\n' "$tag"; fi
	) | tee "$logf.probe.txt"
}

py_serve_probe() { # home port log tag
	local home="$1" port="$2" logf="$3" tag="$4"
	(
		export HIVE_HOME="$home"
		(cd "$REPO/core" && uv run --no-sync hive serve --port "$port") >"$logf" 2>&1 &
		local pid=$! code=""
		for _ in $(seq 1 160); do
			code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/health" || true)
			[ "$code" = "200" ] && break
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		printf '%s health=%s\n' "$tag" "$code"
		root_code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" || true)
		printf '%s root=%s\n' "$tag" "$root_code"
		curl -s "http://127.0.0.1:$port/api/sessions" | head -c 500; echo
		if [ -f "$home/maintenance/server.lock" ]; then
			printf '%s marker=present %s\n' "$tag" "$(cat "$home/maintenance/server.lock")"
		else
			printf '%s marker=absent\n' "$tag"
		fi
		# `uv run` spawns the venv python as a child and does not forward
		# SIGTERM to it, so TERM both the wrapper and the real listener.
		local listener=""
		if command -v lsof >/dev/null 2>&1; then
			listener="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -1)"
		fi
		kill -TERM "$pid" 2>/dev/null || true
		if [ -n "$listener" ]; then kill -TERM "$listener" 2>/dev/null || true; fi
		for _ in $(seq 1 80); do
			alive=0
			if kill -0 "$pid" 2>/dev/null; then alive=1; fi
			if [ -n "$listener" ] && kill -0 "$listener" 2>/dev/null; then alive=1; fi
			if [ "$alive" = 0 ]; then break; fi
			sleep 0.25
		done
		if kill -0 "$pid" 2>/dev/null; then kill -9 "$pid" 2>/dev/null || true; printf '%s KILLED\n' "$tag"; fi
		if [ -n "$listener" ] && kill -0 "$listener" 2>/dev/null; then
			kill -9 "$listener" 2>/dev/null || true
			printf '%s listener-killed\n' "$tag"
		fi
		sleep 1
		if [ -f "$home/maintenance/server.lock" ]; then printf '%s marker-after=present\n' "$tag"; else printf '%s marker-after=absent\n' "$tag"; fi
	) | tee "$logf.probe.txt"
}

# ---------------------------------------------------------------------------
# stage 1: rollback drill
# ---------------------------------------------------------------------------
log "stage 1: build current binaries"
(
	cd "$GO_MODULE"
	CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go build -o "$WORK/bin/hive" ./cmd/hive
	CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go build -o "$WORK/bin/hive-cred" ./cmd/hive-cred
	CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go build -o "$WORK/bin/hive-browser" ./cmd/hive-browser
	CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go build -o "$WORK/bin/storagecompat" ./e2e/cmd/storagecompat
)
(
	cd "$BRIDGE_MODULE"
	CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go build -o "$WORK/bin/browserbridge" ./cmd/browserbridge
)
"$WORK/bin/hive" --version | tee "$LOGS/new-binaries-version.txt"

HOME_DIR="$WORK/rollback/home"
rm -rf "$HOME_DIR"
log "stage 1: v2 fixture -> $HOME_DIR"
(cd "$REPO/core" && uv run --no-sync python "$FIXTURE" --dest "$HOME_DIR" --version v2)
(cd "$REPO/core" && uv run --no-sync python "$FIXTURE" --fix-paths "$HOME_DIR")
manifest "$HOME_DIR" S0-fixture.json

log "stage 1: Go serve #1 (startup migration)"
serve_probe "$WORK/bin" "$HOME_DIR" 8798 "$LOGS/go-serve-1.log" "go-serve-1"
log "stage 1: Go write-probes (session + credentials + probe colony)"
"$WORK/bin/storagecompat" write-probes --home "$HOME_DIR" | tee "$LOGS/go-write-probes.json"
manifest "$HOME_DIR" S1-after-go1.json
diffy S0-fixture.json S1-after-go1.json D0-go1-migration.json

log "stage 1: Python CLI reads"
pycli "$HOME_DIR" session list --cold --json > "$LOGS/py-session-list.json"
pycli "$HOME_DIR" queen list --json > "$LOGS/py-queen-list.json"
pycli "$HOME_DIR" queen sessions queen_growth --json > "$LOGS/py-queen-sessions.json"
pycli "$HOME_DIR" colony list --json > "$LOGS/py-colony-list.json"
manifest "$HOME_DIR" S2a-after-py-cli.json
diffy S1-after-go1.json S2a-after-py-cli.json D1a-py-cli.json

log "stage 1: Python API read-back (history / credentials / tracker)"
pyrepo read --home "$HOME_DIR" --out "$LOGS/py-readback.json"
manifest "$HOME_DIR" S2b-after-py-read.json
diffy S2a-after-py-cli.json S2b-after-py-read.json D1b-py-read.json

log "stage 1: Python serve on the Go home"
py_serve_probe "$HOME_DIR" 8799 "$LOGS/py-serve.log" "py-serve"
manifest "$HOME_DIR" S2c-after-py-serve.json
diffy S2b-after-py-read.json S2c-after-py-serve.json D2-py-serve.json

log "stage 1: Go CLI cold read (of Python-written summaries)"
( export HIVE_HOME="$HOME_DIR"; "$WORK/bin/hive" session list --cold --json ) | tee "$LOGS/go-session-list-after-py.json"
manifest "$HOME_DIR" S2d-after-go-cli.json
diffy S2c-after-py-serve.json S2d-after-go-cli.json D3-go-cli.json

log "stage 1: Go serve #2 (idempotency)"
serve_probe "$WORK/bin" "$HOME_DIR" 8800 "$LOGS/go-serve-2.log" "go-serve-2"
manifest "$HOME_DIR" S3-after-go2.json
diffy S2d-after-go-cli.json S3-after-go2.json D4-go2.json

# The stage-1 binaries are plain `go build` (no -tags embed_ui). `/` 404s
# only because neither the UI is embedded nor a frontend/dist sits under the
# CWD; the dashboard served from disk flips it to 200 without touching data.
log "stage 1: plain binary + on-disk dashboard (root check)"
ui_src="${FRONTEND_DIST:-$GO_MODULE/frontend/dist}"
if [ -f "$ui_src/index.html" ]; then
	rm -rf "$WORK/uihost" && mkdir -p "$WORK/uihost/frontend"
	cp -R "$ui_src" "$WORK/uihost/frontend/dist"
	( cd "$WORK/uihost" && serve_probe "$WORK/bin" "$HOME_DIR" 8801 "$LOGS/go-serve-ui.log" "go-serve-ui" )
	manifest "$HOME_DIR" S4-after-go-ui.json
	diffy S3-after-go2.json S4-after-go-ui.json D5-go-ui.json
else
	echo "skip: no frontend dist at $ui_src (root stays 404 by design)" | tee "$LOGS/go-serve-ui.log"
fi

if [ "$SKIP_UPGRADE" = 1 ]; then
	log "rollback diffs"
	for f in D0-go1-migration D1a-py-cli D1b-py-read D2-py-serve D3-go-cli D4-go2 D5-go-ui; do
		[ -f "$SNAP/$f.json" ] && summary "$f.json" || true
	done
	log "work dir: $WORK"
	exit 0
fi

# ---------------------------------------------------------------------------
# stage 2: upgrade + install drill
# ---------------------------------------------------------------------------
log "stage 2: build $OLD_TAG binaries (old user)"
rm -rf "$WORK/old-tree" && mkdir -p "$WORK/old-tree"
git -C "$REPO" archive "$OLD_TAG" | tar -x -C "$WORK/old-tree"
(
	cd "$WORK/old-tree/go_hive/go-hive"
	export CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off
	go build -o "$WORK/bin-old/hive" ./cmd/hive
	go build -o "$WORK/bin-old/hive-cred" ./cmd/hive-cred
)
"$WORK/bin-old/hive" --version | tee "$LOGS/old-binaries-version.txt"

log "stage 2: build release archive (release.sh)"
embed_flag=""
frontend_dist="${FRONTEND_DIST:-$GO_MODULE/frontend/dist}"
if [ "$NO_EMBED" = 1 ]; then
	embed_flag="--no-embed"
elif [ ! -f "$frontend_dist/index.html" ]; then
	embed_flag="--no-embed"
	echo "g2-rollback: no FRONTEND_DIST index.html; building without the embedded dashboard" >&2
fi
host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(uname -m)"; case "$host_arch" in arm64|aarch64) host_arch=arm64 ;; x86_64|amd64) host_arch=amd64 ;; esac
(
	cd "$REPO"
	VERSION=1.0.26-go.1 DIST="$WORK/dist" RELEASE_DIR="$WORK/dist/release" \
		PLATFORMS="$host_os/$host_arch" FRONTEND_DIST="$frontend_dist" \
		bash go_hive/scripts/release.sh $embed_flag
) | tee "$LOGS/release-build.log"
( cd "$WORK/dist/release" && shasum -a 256 -c SHA256SUMS ) | tee "$LOGS/release-archive-sha.txt"

rm -rf "$WORK/upgrade/new-extract" && mkdir -p "$WORK/upgrade/new-extract"
tar -xzf "$WORK/dist/release/hive-1.0.26-go.1-$host_os-$host_arch.tar.gz" -C "$WORK/upgrade/new-extract"
( cd "$WORK/upgrade/new-extract" && shasum -a 256 -c SHA256SUMS ) | tee -a "$LOGS/release-archive-sha.txt"
"$WORK/upgrade/new-extract/hive" --version | tee "$LOGS/new-archive-version.txt"

OLDHOME="$WORK/upgrade/oldhome"
rm -rf "$OLDHOME"
log "stage 2: old user home (v2 fixture) + $OLD_TAG serve"
(cd "$REPO/core" && uv run --no-sync python "$FIXTURE" --dest "$OLDHOME" --version v2)
(cd "$REPO/core" && uv run --no-sync python "$FIXTURE" --fix-paths "$OLDHOME")
serve_probe "$WORK/bin-old" "$OLDHOME" 8802 "$LOGS/old-serve-1.log" "old-serve-1"
( export HIVE_HOME="$OLDHOME"; "$WORK/bin-old/hive" session list --cold --json ) > "$LOGS/old-session-list.json"
( export HIVE_HOME="$OLDHOME"; "$WORK/bin-old/hive" colony list --json ) > "$LOGS/old-colony-list.json"
manifest "$OLDHOME" U0-oldhome-before-new.json

log "stage 2: new binary serve on the old home (startup migration)"
serve_probe "$WORK/upgrade/new-extract" "$OLDHOME" 8803 "$LOGS/upgrade-new-serve.log" "new-serve-1"
( export HIVE_HOME="$OLDHOME"; "$WORK/upgrade/new-extract/hive" session list --cold --json ) > "$LOGS/new-session-list-after-upgrade.json"
( export HIVE_HOME="$OLDHOME"; "$WORK/upgrade/new-extract/hive" queen sessions queen_growth --json ) > "$LOGS/new-queen-sessions-after-upgrade.json"
manifest "$OLDHOME" U1-after-new-serve.json
diffy U0-oldhome-before-new.json U1-after-new-serve.json DU-upgrade-migration.json

log "stage 2: previous binary reads the migrated home"
( export HIVE_HOME="$OLDHOME"; "$WORK/bin-old/hive" session list --cold --json ) > "$LOGS/old-session-list-after-migration.json"
serve_probe "$WORK/bin-old" "$OLDHOME" 8805 "$LOGS/old-serve-on-migrated.log" "old-serve-2"
manifest "$OLDHOME" U2-after-old-binary.json
diffy U1-after-new-serve.json U2-after-old-binary.json DU2-old-binary-reads.json

log "stage 2: new binary serve #2 (idempotency)"
serve_probe "$WORK/upgrade/new-extract" "$OLDHOME" 8806 "$LOGS/upgrade-new-serve-2.log" "new-serve-2"
manifest "$OLDHOME" U3-after-new-serve2.json
diffy U2-after-old-binary.json U3-after-new-serve2.json DU3-new-serve2.json

log "stage 2: install.sh upgrade in place"
INSTALL_DIR="$WORK/install-bin"
rm -rf "$INSTALL_DIR" && mkdir -p "$INSTALL_DIR"
cp "$WORK/bin-old/hive" "$WORK/bin-old/hive-cred" "$INSTALL_DIR/"
"$INSTALL_DIR/hive" --version | tee "$LOGS/install-old-version.txt"
bash "$REPO/go_hive/install.sh" --from "$WORK/dist/release" --version 1.0.26-go.1 --dir "$INSTALL_DIR" \
	| tee "$LOGS/install-sh.log"
( cd "$INSTALL_DIR" && ls -la ) | tee -a "$LOGS/install-sh.log"
"$INSTALL_DIR/hive" --version | tee "$LOGS/install-new-version.txt"

log "stage 2: installed binaries serve health"
mkdir -p "$WORK/upgrade/installhome"
serve_probe "$INSTALL_DIR" "$WORK/upgrade/installhome" 8804 "$LOGS/install-serve.log" "install-serve"

log "stage 2: verify_release_archive.sh"
( cd "$REPO/go_hive" && BRIDGE_HTTP_PORT=14940 BRIDGE_WS_PORT=14939 SERVE_PORT=8810 \
	bash scripts/verify_release_archive.sh "$WORK/dist/release" ) | tee "$LOGS/verify-release-archive.txt"

log "all diffs"
for f in D0-go1-migration D1a-py-cli D1b-py-read D2-py-serve D3-go-cli D4-go2 D5-go-ui \
	DU-upgrade-migration DU2-old-binary-reads DU3-new-serve2; do
	[ -f "$SNAP/$f.json" ] && summary "$f.json" || true
done
log "work dir: $WORK"
