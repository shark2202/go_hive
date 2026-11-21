#!/usr/bin/env bash
# upgrade_compat_check.sh — standing upgrade-compatibility gate.
#
# Proves, per delivery, that a *candidate* hive binary upgrades homes written
# by the *previous* delivery losslessly, and that the previous binary can
# still read the upgraded home back without writing to it.
#
# For each legacy fixture (v1 pre-v2 layout, v2 pre-v3 layout, v3 already
# migrated) the driver:
#
#   1. builds the fixture (storage_compat_gen_fixture.py) and seeds one
#      Fernet credential through the Python reference (SKIP-able, see below)
#   2. candidate `hive serve` #1  -> health + read APIs (sessions/history,
#      discover, credentials, colony sessions) -> snapshot diff
#   3. candidate `hive serve` #2  -> idempotency (no new migration)
#   4. previous binary CLI reads  -> snapshot diff must be 0 added/0 removed/
#      0 modified (the old user can roll back and read; nothing gets written)
#   5. previous binary `serve` on the migrated home -> health 200, no user-data
#      writes (rollback scenario)
#
# Verdicts are aggregated by upgrade_compat_probe.py into a JSON report; any
# FAIL exits non-zero. Churn that is *not* user data (serve logs, the serve
# marker, SQLite WAL sidecars, the credential index, the per-session summary
# cache) is allow-listed and listed in the report.
#
# Usage:
#   bash go_hive/scripts/upgrade_compat_check.sh --old-dir <prev binaries dir> \
#       [--new-dir <candidate binaries dir>] [--fixtures v1,v2,v3] \
#       [--work DIR] [--out FILE] [--label TAG] [--port-base N]
#
#   --old-dir DIR      directory holding the previous delivery's `hive`
#                      (a release-archive extraction or a dist/slots/<slot>)
#   --old-binary PATH  same, but a direct binary path
#   --new-dir DIR      directory holding the candidate `hive`; when omitted the
#                      driver builds ./cmd/hive from this checkout into
#                      <work>/build-new (offline, CGO off)
#   --new-binary PATH  same, but a direct binary path
#   --prev-slot        resolve --old-dir from go_hive/dist/slots: the newest
#                      slot that is not the --new-dir. Convenience for the
#                      slot cadence (release_slot.sh) and manual runs.
#   --self-test        hermetically prove the gate FAILs on synthetic
#                      regressions (no binaries, no servers) and exit
#   --fixtures LIST    comma-separated subset of v1,v2,v3 (default all)
#   --work DIR         scratch directory (default: mktemp -d)
#   --out FILE         report JSON (default <work>/upgrade-compat-report.json)
#   --port-base N      first port of the drill range (default 8840)
#
# The work directory always keeps its evidence (fixtures, snapshots, diffs,
# probe captures, serve logs, report JSON): it is what the report cites.
#
# Exit: 0 all cases PASS (SKIPs are listed), 1 any FAIL, 2 usage/environment.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REPO="$(cd "$GO_HIVE_ROOT/.." && pwd)"
GO_MODULE="$REPO/go_hive/go-hive"
FIXTURE="$SCRIPT_DIR/storage_compat_gen_fixture.py"
PROBE="$SCRIPT_DIR/g2_rollback_probe.py"
CHECKER="$SCRIPT_DIR/upgrade_compat_probe.py"
SLOTS="$GO_HIVE_ROOT/dist/slots"

OLD_DIR="" ; OLD_BINARY=""
NEW_DIR="" ; NEW_BINARY=""
WORK="" ; OUT="" ; LABEL="" ; PORT_BASE=8840 ; PREV_SLOT=0 ; SELF_TEST=0
FIXTURES="v1,v2,v3"

while [ $# -gt 0 ]; do
	case "$1" in
		--old-dir)    OLD_DIR="$2"; shift 2 ;;
		--old-binary) OLD_BINARY="$2"; shift 2 ;;
		--new-dir)    NEW_DIR="$2"; shift 2 ;;
		--new-binary) NEW_BINARY="$2"; shift 2 ;;
		--prev-slot)  PREV_SLOT=1; shift ;;
		--self-test)  SELF_TEST=1; shift ;;
		--fixtures)   FIXTURES="$2"; shift 2 ;;
		--work)       WORK="$2"; shift 2 ;;
		--out)        OUT="$2"; shift 2 ;;
		--label)      LABEL="$2"; shift 2 ;;
		--port-base)  PORT_BASE="$2"; shift 2 ;;
		-h|--help)    sed -n '2,58p' "$0"; exit 0 ;;
		*) echo "upgrade-compat: unknown argument: $1" >&2; exit 2 ;;
	esac
done

if [ "$SELF_TEST" = 1 ]; then
	[ -n "$WORK" ] || WORK="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-compat-selftest.XXXXXX")"
	mkdir -p "$WORK"
	python3 "$SCRIPT_DIR/upgrade_compat_probe.py" self-test --work "$WORK"
	exit $?
fi

log() { printf '\n=== %s\n' "$*"; }
die() { echo "upgrade-compat: $*" >&2; exit 2; }

command -v curl >/dev/null 2>&1 || die "missing curl"

# The manifest/diff helper reads SQLite through `file:...?mode=ro` URIs, which
# macOS' system python3 (3.9, no URI support) cannot do. Prefer a capable
# python3 (Linux CI), else the project venv's python through uv.
PY_MODE=""
_py_uri_capable() {
	# A WAL-mode DB is the discriminator: macOS' system SQLite (3.32, shipped
	# with python3.9) cannot open one through `mode=ro` without the -shm
	# sidecar; the uv-provided Python (SQLite 3.4x) can. The g2 probe copies
	# and opens every *.db this way, so the driver must pick a python that
	# handles WAL homes.
	"$1" - <<'PY' >/dev/null 2>&1
import pathlib, shutil, sqlite3, tempfile
d = pathlib.Path(tempfile.mkdtemp())
p = d / "probe.db"
con = sqlite3.connect(str(p))
con.execute("PRAGMA journal_mode=WAL")
con.execute("create table t(x)")
con.execute("insert into t values (1)")
con.commit()
con.close()
q = d / "copy.db"
shutil.copy2(p, q)
con = sqlite3.connect(f"file:{q}?mode=ro", uri=True)
assert list(con.execute("select x from t")) == [(1,)]
PY
}
if command -v python3 >/dev/null 2>&1 && _py_uri_capable python3; then
	PY_MODE="python3"
elif command -v uv >/dev/null 2>&1; then
	PY_MODE="uv"
else
	die "need python3 with SQLite URI support, or uv"
fi
pyrun() {
	if [ "$PY_MODE" = "python3" ]; then
		python3 "$@"
	else
		( cd "$REPO/core" && uv run --no-sync python "$@" )
	fi
}
json_field() { # <json> <key>
	pyrun -c 'import json,sys;print(json.loads(sys.argv[1]).get(sys.argv[2],""))' "$1" "$2" 2>/dev/null || echo ""
}

# --- resolve binaries ------------------------------------------------------
if [ "$PREV_SLOT" = 1 ] && [ -z "$OLD_DIR" ]; then
	[ -d "$SLOTS" ] || die "--prev-slot: no slot directory at $SLOTS"
	new_real=""
	[ -n "$NEW_DIR" ] && new_real="$(cd "$NEW_DIR" && pwd)"
	OLD_DIR="$(ls -1d "$SLOTS"/20*-* 2>/dev/null | sort | while read -r d; do
		[ -n "$new_real" ] && [ "$(cd "$d" && pwd)" = "$new_real" ] && continue
		echo "$d"
	done | tail -1)"
	[ -n "$OLD_DIR" ] || die "--prev-slot: no previous slot found under $SLOTS"
	log "prev-slot resolved: $OLD_DIR"
fi

[ -n "$OLD_BINARY" ] || OLD_BINARY="$OLD_DIR/hive"
[ -x "$OLD_BINARY" ] || die "previous binary not executable: $OLD_BINARY"

if [ -z "$NEW_DIR" ] && [ -z "$NEW_BINARY" ]; then
	[ -n "$WORK" ] || WORK="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-compat.XXXXXX")"
	mkdir -p "$WORK"
	NEW_DIR="$WORK/build-new"
	mkdir -p "$NEW_DIR"
	log "building candidate binary ($NEW_DIR/hive)"
	( cd "$GO_MODULE" && CGO_ENABLED=0 GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off \
		go build -trimpath -o "$NEW_DIR/hive" ./cmd/hive ) || die "candidate build failed"
fi

[ -n "$NEW_BINARY" ] || NEW_BINARY="$NEW_DIR/hive"
[ -x "$NEW_BINARY" ] || die "candidate binary not executable: $NEW_BINARY"
[ -n "$WORK" ] || WORK="$(mktemp -d "${TMPDIR:-/tmp}/upgrade-compat.XXXXXX")"
mkdir -p "$WORK"
WORK="$(cd "$WORK" && pwd)"
mkdir -p "$WORK/cases"
[ -n "$OUT" ] || OUT="$WORK/upgrade-compat-report.json"

OLD_VERSION="$("$OLD_BINARY" --version 2>/dev/null | head -1 || true)"
NEW_VERSION="$("$NEW_BINARY" --version 2>/dev/null | head -1 || true)"
OLD_SHA="$(shasum -a 256 "$OLD_BINARY" | awk '{print $1}')"
NEW_SHA="$(shasum -a 256 "$NEW_BINARY" | awk '{print $1}')"
log "previous: $OLD_BINARY ($OLD_VERSION, sha256 ${OLD_SHA:0:12})"
log "candidate: $NEW_BINARY ($NEW_VERSION, sha256 ${NEW_SHA:0:12})"

# A stray listener on a drill port would answer our health probes.
if command -v lsof >/dev/null 2>&1; then
	for p in $(seq "$PORT_BASE" $((PORT_BASE + 24))); do
		if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
			die "port $p is already in use; free it or pick another --port-base"
		fi
	done
fi

uvpy() { pyrun "$@"; }

# capture_api <port> <cap-dir> <slug:path>...
capture_api() {
	local port="$1" cap="$2"; shift 2
	local pair slug path
	for pair in "$@"; do
		slug="${pair%%:*}"; path="${pair#*:}"
		{
			printf 'status=%s\n' "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port$path" || true)"
			curl -s "http://127.0.0.1:$port$path" || true
		} >"$cap/$slug.http"
	done
}

# api_probe <bindir> <home> <port> <cap-dir> <log> <slug:path>...
api_probe() {
	local bindir="$1" home="$2" port="$3" cap="$4" logf="$5"; shift 5
	mkdir -p "$cap" "$(dirname "$logf")"
	(
		export HIVE_HOME="$home" HIVE_BROWSER_BRIDGE=0
		"$bindir/hive" serve --port "$port" >"$logf" 2>&1 &
		local pid=$! code=""
		for _ in $(seq 1 200); do
			code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/health" || true)
			[ "$code" = "200" ] && break
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.2
		done
		printf 'status=%s\n' "$code" >"$cap/health.http"
		capture_api "$port" "$cap" "$@"
		kill -TERM "$pid" 2>/dev/null || true
		for _ in $(seq 1 80); do kill -0 "$pid" 2>/dev/null || break; sleep 0.2; done
		if kill -0 "$pid" 2>/dev/null; then kill -9 "$pid" 2>/dev/null || true; printf 'KILLED\n' >>"$logf"; fi
	)
}

# cli_read <binary> <home> <out-file> -> exit code echoed
cli_read() {
	local bin="$1" home="$2" out="$3"; shift 3
	( export HIVE_HOME="$home"; "$bin" "$@" >"$out" 2>"$out.err" )
}

manifest() { uvpy "$PROBE" manifest --home "$1" --out "$2" >/dev/null || return 1; }
diffy() { uvpy "$PROBE" diff --before "$1" --after "$2" --out "$3" >/dev/null || return 1; }

# --- one case ---------------------------------------------------------------
# run_case <case> <fixture-kind> <port-base-for-case>
run_case() {
	local case="$1" fixture="$2" pbase="$3"
	local cdir="$WORK/cases/$case"
	local home="$cdir/home"
	rm -rf "$cdir"; mkdir -p "$cdir/probes"
	log "case $case (fixture $fixture) -> $home"

	local gen="$fixture"
	[ "$fixture" = "v3" ] && gen="v2"
	uvpy "$FIXTURE" --dest "$home" --version "$gen" || die "fixture generation failed ($case)"
	uvpy "$FIXTURE" --fix-paths "$home" || die "fixture fix-paths failed ($case)"

	# Credential seeding runs under the project venv (framework imports). A
	# missing/broken venv downgrades M5 to SKIP with the reason — never a pass.
	local seed_out seed_ok="false" seed_reason=""
	if command -v uv >/dev/null 2>&1; then
		if seed_out="$( cd "$REPO/core" && uv run python "$CHECKER" seed-credentials --home "$home" 2>"$cdir/probes/seed.err" )"; then
			seed_ok="$(json_field "$seed_out" seeded)"
			case "$seed_ok" in
				True|true) seed_ok="true" ;;
				*) seed_ok="false"; seed_reason="$(json_field "$seed_out" reason)"; [ -n "$seed_reason" ] || seed_reason="seeding failed" ;;
			esac
		else
			seed_ok="false"
			seed_reason="python reference venv unavailable: $(head -c 160 "$cdir/probes/seed.err" | tr '\n' ' ')"
		fi
	else
		seed_ok="false"
		seed_reason="uv unavailable: cannot run the Python reference seed"
	fi
	echo "credential seed: seeded=$seed_ok ${seed_reason:+reason=$seed_reason}"

	# v3 case: the home has already been migrated by the previous delivery.
	local v3_prepared_by=""
	if [ "$fixture" = "v3" ]; then
		api_probe "$(dirname "$OLD_BINARY")" "$home" "$pbase" "$cdir/probes/v3prep-old" "$cdir/logs/v3prep-old.log"
		v3_prepared_by="old"
		if [ ! -f "$home/.migrated-v3" ]; then
			# The previous binary predates startup migrations: complete the
			# home with the candidate so the case still tests "candidate on an
			# already-v3 home", and record the provenance in the report.
			api_probe "$(dirname "$NEW_BINARY")" "$home" "$pbase" "$cdir/probes/v3prep-new" "$cdir/logs/v3prep-new.log"
			v3_prepared_by="new (previous binary has no startup migration)"
		fi
		cli_read "$OLD_BINARY" "$home" "$cdir/v3prep-old-cold.json" session list --cold --json || true
	fi

	# --- snapshots and steps -------------------------------------------------
	manifest "$home" "$cdir/s0.json" || die "manifest s0 failed ($case)"

	log "case $case: candidate serve #1"
	api_probe "$(dirname "$NEW_BINARY")" "$home" "$((pbase + 1))" "$cdir/probes/new1" "$cdir/logs/new1.log" \
		"sessions_history:/api/sessions/history" "discover:/api/discover" "credentials:/api/credentials" \
		"colony_sessions:/api/colonies/$([ "$fixture" = "v1" ] && echo legacy_colony || echo alpha)/sessions"
	manifest "$home" "$cdir/s1.json" || die "manifest s1 failed ($case)"
	diffy "$cdir/s0.json" "$cdir/s1.json" "$cdir/d_new1.json" || die "diff new1 failed ($case)"

	log "case $case: candidate serve #2 (idempotency)"
	api_probe "$(dirname "$NEW_BINARY")" "$home" "$((pbase + 2))" "$cdir/probes/new2" "$cdir/logs/new2.log"
	manifest "$home" "$cdir/s2.json" || die "manifest s2 failed ($case)"
	diffy "$cdir/s1.json" "$cdir/s2.json" "$cdir/d_new2.json" || die "diff new2 failed ($case)"

	# Warm the read caches with the candidate so the previous binary's reads
	# are a pure read of an already-warm home (summary.json rebuilds are a
	# documented cache write belonging to whichever side gets there first).
	cli_read "$NEW_BINARY" "$home" "$cdir/warm-cold.json" session list --cold --json || true
	cli_read "$NEW_BINARY" "$home" "$cdir/warm-colony.json" colony list --json || true
	manifest "$home" "$cdir/s3.json" || die "manifest s3 failed ($case)"

	log "case $case: previous binary CLI reads"
	local rc_session rc_colony rc_queen
	cli_read "$OLD_BINARY" "$home" "$cdir/old-cold.json" session list --cold --json; rc_session=$?
	cli_read "$OLD_BINARY" "$home" "$cdir/old-colony.json" colony list --json; rc_colony=$?
	cli_read "$OLD_BINARY" "$home" "$cdir/old-queen.json" queen list --json; rc_queen=$?
	manifest "$home" "$cdir/s4.json" || die "manifest s4 failed ($case)"
	diffy "$cdir/s3.json" "$cdir/s4.json" "$cdir/d_old_read.json" || die "diff old_read failed ($case)"

	log "case $case: previous binary serve (rollback)"
	api_probe "$(dirname "$OLD_BINARY")" "$home" "$((pbase + 3))" "$cdir/probes/old_serve" "$cdir/logs/old-serve.log"
	manifest "$home" "$cdir/s5.json" || die "manifest s5 failed ($case)"
	diffy "$cdir/s4.json" "$cdir/s5.json" "$cdir/d_old_serve.json" || die "diff old_serve failed ($case)"

	python3 "$CHECKER" record-case \
		--case "$case" --fixture "$fixture" --work "$WORK" --home "$home" \
		--old-version "$OLD_VERSION" --new-version "$NEW_VERSION" \
		--old-sha256 "$OLD_SHA" --new-sha256 "$NEW_SHA" \
		--old-binary "$OLD_BINARY" --new-binary "$NEW_BINARY" \
		--seeded "$seed_ok" --seed-reason "$seed_reason" \
		--v3-prepared-by "$v3_prepared_by" \
		--rc-session "$rc_session" --rc-colony "$rc_colony" --rc-queen "$rc_queen" \
		--old-serve-ran 1 || die "record-case failed ($case)"
}

# --- run --------------------------------------------------------------------
IFS=',' read -r -a CASE_LIST <<<"$FIXTURES"
idx=0
for case in "${CASE_LIST[@]}"; do
	case "$case" in
		v1|v2|v3) ;;
		*) die "unknown fixture: $case (expected v1,v2,v3)" ;;
	esac
	run_case "$case" "$case" "$((PORT_BASE + idx * 4))"
	idx=$((idx + 1))
done

log "verdict"
pyrun "$CHECKER" verify --work "$WORK" --out "$OUT" --label "$LABEL" \
	--old "$OLD_BINARY" --new "$NEW_BINARY"
rc=$?
log "report: $OUT"
log "work dir: $WORK"
exit $rc
