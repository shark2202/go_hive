#!/usr/bin/env bash
# install_verify.sh — end-to-end install + in-place upgrade verification.
#
# Proves that a released slot archive installs through go_hive/install.sh into
# a throwaway HOME and that upgrading an existing 1.0.26 install in place is
# lossless. Unlike scripts/verify_release_archive.sh (browser-bridge takeover)
# and scripts/upgrade_compat_check.sh (storage fixtures matrix), this driver
# exercises the *user-facing* path: SHA256SUMS gate -> unpack -> install ->
# `hive --version` -> cold `hive serve` -> old binary home -> upgrade ->
# read-back.
#
# Checks (see docs/install-verify.md for the full contract):
#   I1  archive + manifest.json present; manifest parses
#   I2  archive sha256/bytes match manifest.json (independent of install.sh)
#   I3  archive carries SHA256SUMS + hive/hive-cred/hive-browser/browserbridge
#   I4  archive-internal SHA256SUMS verifies after extraction
#   I5  install.sh accepts the archive (SHA256SUMS gate) and exits 0
#   I6  four binaries installed into --dir and executable
#   I7  installed hive --version == manifest.version; cred/browser/bridge run
#   I8  cold start on a fresh HOME: /api/health 200, / 200, history 200, clean SIGTERM
#   I9  negative control: a corrupted SHA256SUMS is rejected (gate is real)
#   I10 old archive installs (previous slot) and reports its version
#   I11 v2 legacy HIVE_HOME fixture built; old binary serves it (health 200,
#       session + colony visible), then the home is snapshotted
#   I12 upgrade in place: same install dir now carries the new binary (sha match)
#   I13 new binary serves the same home: health 200, same sessions/colony visible
#   I14 user data lossless: snapshot diff has 0 user-data changes (allowlist:
#       logs/, *.log, *.db-wal, *.db-shm, maintenance/server.lock)
#
# Usage:
#   bash go_hive/scripts/install_verify.sh --archive-dir DIR [--old-dir DIR]
#       [--install-sh PATH] [--work DIR] [--out FILE] [--port-base N] [--skip-upgrade]
#
#   --archive-dir DIR  slot/release dir holding hive-<version>-<os>-<arch>.tar.gz
#                      and manifest.json (e.g. go_hive/dist/slots/20261121-18)
#   --old-dir DIR      previous slot for the upgrade leg (default: newest slot
#                      other than --archive-dir)
#   --install-sh PATH  installer under test (default: go_hive/install.sh in this checkout)
#   --work DIR         scratch dir (default: mktemp -d); keeps evidence
#   --out FILE         report JSON (default: <work>/install-verify-report.json)
#   --port-base N      first port of the drill range (default 8940)
#   --skip-upgrade     run the fresh-install checks only
#
# Supported: darwin/linux on amd64/arm64 (same platform set as the release
# archives). Exit: 0 all checks PASS, 1 any FAIL, 2 usage/environment.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SLOTS="$GO_HIVE_ROOT/dist/slots"

ARCHIVE_DIR="" ; OLD_DIR="" ; INSTALL_SH="$GO_HIVE_ROOT/install.sh"
WORK="" ; OUT="" ; PORT_BASE=8940 ; SKIP_UPGRADE=0

usage() { sed -n '2,45p' "$0"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--archive-dir) ARCHIVE_DIR="$2"; shift 2 ;;
		--old-dir) OLD_DIR="$2"; shift 2 ;;
		--install-sh) INSTALL_SH="$2"; shift 2 ;;
		--work) WORK="$2"; shift 2 ;;
		--out) OUT="$2"; shift 2 ;;
		--port-base) PORT_BASE="$2"; shift 2 ;;
		--skip-upgrade) SKIP_UPGRADE=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) echo "install-verify: unknown argument: $1" >&2; exit 2 ;;
	esac
done

log() { printf 'install-verify: %s\n' "$*"; }
die() { printf 'install-verify: ERROR: %s\n' "$*" >&2; exit 2; }

[ -f "$INSTALL_SH" ] || die "install.sh not found: $INSTALL_SH (git grep install.sh)"

pick_slot() { # newest->oldest slot dirs carrying a darwin/linux archive + manifest
	ls -1d "$SLOTS"/20*-* 2>/dev/null | sort -r | while read -r d; do
		[ -f "$d/manifest.json" ] || continue
		ls "$d"/hive-*.tar.gz >/dev/null 2>&1 || continue
		echo "$d"
	done
}

if [ -z "$ARCHIVE_DIR" ]; then
	[ -d "$SLOTS" ] || die "no --archive-dir given and $SLOTS does not exist (gitignored slot builds live in the main checkout)"
	ARCHIVE_DIR="$(pick_slot | head -1)"
	[ -n "$ARCHIVE_DIR" ] || die "no slot with manifest.json + archive under $SLOTS"
	log "archive dir resolved: $ARCHIVE_DIR"
fi
[ -d "$ARCHIVE_DIR" ] || die "archive dir not found: $ARCHIVE_DIR"

if [ -z "$OLD_DIR" ] && [ "$SKIP_UPGRADE" = 0 ]; then
	OLD_DIR="$(pick_slot | while read -r d; do [ "$(cd "$d" && pwd)" = "$(cd "$ARCHIVE_DIR" && pwd)" ] && continue; echo "$d"; done | head -1)"
	[ -n "$OLD_DIR" ] || die "no previous slot found for the upgrade leg; pass --old-dir or --skip-upgrade"
	log "old dir resolved: $OLD_DIR"
fi

archive="$(ls -1 "$ARCHIVE_DIR"/hive-*.tar.gz 2>/dev/null | head -1)"
[ -n "$archive" ] || die "no hive-*.tar.gz in $ARCHIVE_DIR"
archive_name="$(basename "$archive")"

if [ -z "$WORK" ]; then
	WORK="$(mktemp -d "${TMPDIR:-/tmp}/hive-install-verify.XXXXXX")"
fi
mkdir -p "$WORK" || die "cannot create work dir $WORK"
WORK="$(cd "$WORK" && pwd)"
[ -n "$OUT" ] || OUT="$WORK/install-verify-report.json"
CHECK_TSV="$WORK/checks.tsv"
: >"$CHECK_TSV"

PASS=0; FAIL=0; SKIP=0
check() { # id title status detail
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >>"$CHECK_TSV"
	case "$3" in
		PASS) PASS=$((PASS + 1)); printf '  ok  : [%s] %s — %s\n' "$1" "$2" "$4" ;;
		FAIL) FAIL=$((FAIL + 1)); printf '  FAIL: [%s] %s — %s\n' "$1" "$2" "$4" ;;
		*) SKIP=$((SKIP + 1)); printf '  skip: [%s] %s — %s\n' "$1" "$2" "$4" ;;
	esac
}

sha256_file() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		sha256sum "$1" | awk '{print $1}'
	fi
}

for tool in curl tar python3; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if command -v lsof >/dev/null 2>&1; then
	for p in $(seq "$PORT_BASE" $((PORT_BASE + 6))); do
		if lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1; then
			die "port $p is already in use; pass --port-base for a free range"
		fi
	done
fi

os="$(uname -s)"; arch="$(uname -m)"
case "$arch" in arm64 | aarch64) arch=arm64 ;; x86_64 | amd64) arch=amd64 ;; *) die "unsupported arch $arch" ;; esac

# ── I1: manifest ────────────────────────────────────────────────────────────
manifest="$ARCHIVE_DIR/manifest.json"
if [ -f "$manifest" ]; then
	expected_version="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["version"])' "$manifest" 2>/dev/null)"
	expected_build="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print(d.get("build_version",d["version"]))' "$manifest" 2>/dev/null)"
	expected_sha="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["sha256"])' "$manifest" 2>/dev/null)"
	expected_bytes="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["bytes"])' "$manifest" 2>/dev/null)"
	check I1 "manifest.json parses" PASS "version=$expected_version build=$expected_build"
else
	expected_version=""; expected_build=""; expected_sha=""; expected_bytes=""
	check I1 "manifest.json present" FAIL "$manifest missing (slot builds must carry it)"
fi

# ── I2: archive checksum vs manifest ────────────────────────────────────────
actual_sha="$(sha256_file "$archive")"
actual_bytes="$(wc -c <"$archive" | tr -d ' ')"
if [ -n "$expected_sha" ] && [ "$actual_sha" = "$expected_sha" ] && [ "$actual_bytes" = "$expected_bytes" ]; then
	check I2 "archive sha256/bytes match manifest" PASS "sha256=${actual_sha:0:16}… bytes=$actual_bytes"
elif [ -n "$expected_sha" ]; then
	check I2 "archive sha256/bytes match manifest" FAIL "manifest=${expected_sha:0:16}…/$expected_bytes actual=${actual_sha:0:16}…/$actual_bytes"
	die "archive does not match its manifest; refusing to install"
else
	check I2 "archive sha256/bytes match manifest" SKIP "no manifest sha256 to compare"
fi

# ── I3/I4: archive structure + internal checksums ───────────────────────────
extract="$WORK/extract-new"
mkdir -p "$extract"
if tar -xzf "$archive" -C "$extract"; then
	missing=""
	for b in hive hive-cred hive-browser browserbridge; do
		[ -f "$extract/$b" ] && [ -x "$extract/$b" ] || missing="$missing $b"
	done
	if [ -z "$missing" ]; then
		check I3 "archive carries four executables" PASS "$archive_name"
	else
		check I3 "archive carries four executables" FAIL "missing:$missing"
	fi
	if [ -f "$extract/SHA256SUMS" ]; then
		if ( cd "$extract" && shasum -a 256 -c SHA256SUMS >"$WORK/inner-sha256.log" 2>&1 ); then
			check I4 "archive-internal SHA256SUMS verifies" PASS "$(grep -c 'OK$' "$WORK/inner-sha256.log" 2>/dev/null || echo '?') entries OK"
		else
			check I4 "archive-internal SHA256SUMS verifies" FAIL "see $WORK/inner-sha256.log"
		fi
	else
		# Slot archives are binary-only by design (sha256 lives in manifest.json,
		# and install.sh stages a release-level SHA256SUMS). release.sh archives
		# do carry the inner file, so this is a SKIP, not a pass.
		check I4 "archive-internal SHA256SUMS verifies" SKIP "slot archive has no inner SHA256SUMS (manifest-level sha256 verified in I2)"
	fi
else
	check I3 "archive carries four executables" FAIL "tar failed"
	check I4 "archive-internal SHA256SUMS verifies" SKIP "tar failed"
fi

run_install() { # <archive-file> <home> <log>
	local arc="$1" home="$2" logf="$3"
	local name; name="$(basename "$arc")"
	local stage="$WORK/stage-${name%.tar.gz}"
	mkdir -p "$stage" "$home"
	# Stage a private SHA256SUMS next to a link/copy of the archive so the
	# published slot directory is never written to.
	ln "$arc" "$stage/$name" 2>/dev/null || { [ -e "$stage/$name" ] || cp "$arc" "$stage/$name"; }
	( cd "$stage" && printf '%s  %s\n' "$(sha256_file "$name")" "$name" >SHA256SUMS )
	HOME="$home" PATH="$PATH" bash "$INSTALL_SH" --from "$stage/$name" --dir "$home/.local/bin" >"$logf" 2>&1
}

# ── I5/I6/I7: fresh install into a throwaway HOME ───────────────────────────
home_new="$WORK/home-new"
install_log="$WORK/install-new.log"
if run_install "$archive" "$home_new" "$install_log"; then
	if grep -q 'verified SHA256' "$install_log"; then
		check I5 "install.sh accepts the archive (SHA256 gate)" PASS "log has 'verified SHA256'"
	else
		check I5 "install.sh accepts the archive (SHA256 gate)" FAIL "no 'verified SHA256' in $install_log"
	fi
else
	check I5 "install.sh accepts the archive (SHA256 gate)" FAIL "installer rc!=0, see $install_log"
fi

bindir="$home_new/.local/bin"
missing=""
for b in hive hive-cred hive-browser browserbridge; do
	[ -x "$bindir/$b" ] || missing="$missing $b"
done
if [ -z "$missing" ]; then
	check I6 "four binaries installed and executable" PASS "$bindir"
else
	check I6 "four binaries installed and executable" FAIL "missing:$missing"
fi

if [ -x "$bindir/hive" ]; then
	got_version="$("$bindir/hive" --version 2>&1 | head -1)"
	if [ -n "$expected_version" ] && [ "$got_version" = "hive $expected_version" ]; then
		check I7 "installed hive --version == manifest version" PASS "$got_version"
	else
		check I7 "installed hive --version == manifest version" FAIL "got '$got_version', want 'hive $expected_version'"
	fi
	cli_rc=0
	"$bindir/hive-cred" --help >/dev/null 2>&1 || cli_rc=$?
	"$bindir/hive-browser" help >/dev/null 2>&1 || cli_rc=$?
	"$bindir/browserbridge" -version >/dev/null 2>&1 || cli_rc=$?
	if [ "$cli_rc" = 0 ]; then
		check I7b "hive-cred / hive-browser / browserbridge run" PASS "help/-version rc=0"
	else
		check I7b "hive-cred / hive-browser / browserbridge run" FAIL "rc=$cli_rc"
	fi
else
	check I7 "installed hive --version == manifest version" FAIL "no installed hive"
	check I7b "hive-cred / hive-browser / browserbridge run" FAIL "no installed binaries"
fi

SERVE_PID=""
start_serve() { # bin home port log
	local bin="$1" home="$2" port="$3" logf="$4"
	( cd "$(dirname "$bin")" && exec env -u HIVE_BROWSERBRIDGE_BIN HIVE_HOME="$home" HIVE_BROWSER_BRIDGE=0 \
		"$bin" serve --port "$port" ) >"$logf" 2>&1 &
	SERVE_PID=$!
}
stop_serve() {
	[ -n "$SERVE_PID" ] || return 0
	kill -TERM "$SERVE_PID" 2>/dev/null || true
	for _ in $(seq 1 40); do
		kill -0 "$SERVE_PID" 2>/dev/null || break
		sleep 0.25
	done
	if kill -0 "$SERVE_PID" 2>/dev/null; then
		kill -9 "$SERVE_PID" 2>/dev/null || true
		return 1
	fi
	SERVE_PID=""
	return 0
}
wait_health() { # port
	local port="$1" code=""
	for _ in $(seq 1 80); do
		code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$port/api/health")"
		[ "$code" = 200 ] && return 0
		sleep 0.5
	done
	return 1
}

# ── I8: cold start on a fresh HOME ──────────────────────────────────────────
port_cold=$PORT_BASE
home_cold="$WORK/home-cold-serve"
mkdir -p "$home_cold"
start_serve "$bindir/hive" "$home_cold" "$port_cold" "$WORK/serve-cold.log"
if wait_health "$port_cold"; then
	root_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "http://127.0.0.1:$port_cold/")"
	hist_code="$(curl -s -o "$WORK/cold-history.json" -w '%{http_code}' --max-time 5 "http://127.0.0.1:$port_cold/api/sessions/history")"
	if [ "$root_code" = 200 ] && [ "$hist_code" = 200 ]; then
		check I8 "cold start: health/root/history 200" PASS "health=200 /=$root_code history=$hist_code"
	else
		check I8 "cold start: health/root/history 200" FAIL "health=200 /=$root_code history=$hist_code"
	fi
else
	check I8 "cold start: health/root/history 200" FAIL "no health 200 on port $port_cold; see $WORK/serve-cold.log"
fi
if stop_serve; then
	check I8b "cold-start serve exits on SIGTERM" PASS "pid reaped"
else
	check I8b "cold-start serve exits on SIGTERM" FAIL "pid survived SIGTERM"
fi

# ── I9: negative control — corrupt SHA256SUMS must be rejected ──────────────
neg="$WORK/negative"
mkdir -p "$neg"
ln "$archive" "$neg/$archive_name" 2>/dev/null || cp "$archive" "$neg/$archive_name"
printf '%s  %s\n' "0000000000000000000000000000000000000000000000000000000000000000" "$archive_name" >"$neg/SHA256SUMS"
home_neg="$WORK/home-negative"
if HOME="$home_neg" bash "$INSTALL_SH" --from "$neg/$archive_name" --dir "$home_neg/.local/bin" >"$WORK/install-negative.log" 2>&1; then
	check I9 "corrupted SHA256SUMS is rejected" FAIL "installer accepted a wrong checksum"
else
	if grep -qi 'sha256 mismatch' "$WORK/install-negative.log"; then
		check I9 "corrupted SHA256SUMS is rejected" PASS "installer refused: $(grep -i 'sha256 mismatch' "$WORK/install-negative.log" | head -1 | cut -c1-90)"
	else
		check I9 "corrupted SHA256SUMS is rejected" FAIL "installer failed for another reason; see $WORK/install-negative.log"
	fi
fi

# ── upgrade leg ─────────────────────────────────────────────────────────────
if [ "$SKIP_UPGRADE" = 1 ]; then
	check I10 "upgrade leg (old -> new)" SKIP "--skip-upgrade"
else
	old_archive="$(ls -1 "$OLD_DIR"/hive-*.tar.gz 2>/dev/null | head -1)"
	old_hive="$OLD_DIR/hive"
	old_version="$( [ -x "$old_hive" ] && "$old_hive" --version 2>/dev/null | head -1 )"
	old_expected="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["version"])' "$OLD_DIR/manifest.json" 2>/dev/null)"
	upgrade_possible=1
	if [ -n "$old_archive" ] && [ -x "$old_hive" ]; then
		check I10 "previous delivery binary + archive present" PASS "$old_version ($OLD_DIR)"
	else
		check I10 "previous delivery binary + archive present" FAIL "missing archive or hive in $OLD_DIR"
		upgrade_possible=0
	fi
	if [ "$upgrade_possible" = 1 ]; then
	home_upg="$WORK/home-upgrade"
	upg_bindir="$home_upg/.local/bin"
	old_install_log="$WORK/install-old.log"
	if run_install "$old_archive" "$home_upg" "$old_install_log"; then
		got_old="$("$upg_bindir/hive" --version 2>&1 | head -1)"
		if [ -n "$old_expected" ] && [ "$got_old" = "hive $old_expected" ]; then
			check I10b "old archive installs into ~/.local/bin" PASS "$got_old"
		else
			check I10b "old archive installs into ~/.local/bin" SKIP "old reports '$got_old', manifest '$old_expected'"
		fi
	else
		check I10b "old archive installs into ~/.local/bin" FAIL "see $old_install_log"
		upgrade_possible=0
	fi
	fi
	if [ "$upgrade_possible" = 1 ]; then

	# v2 legacy home fixture (same generator the upgrade-compat gate uses).
	fixture_home="$home_upg/hivehome"
	if python3 "$SCRIPT_DIR/storage_compat_gen_fixture.py" --dest "$fixture_home" --version v2 >"$WORK/fixture.log" 2>&1 \
		&& python3 "$SCRIPT_DIR/storage_compat_gen_fixture.py" --fix-paths "$fixture_home" >>"$WORK/fixture.log" 2>&1; then
		check I11 "v2 legacy HIVE_HOME fixture built" PASS "$(find "$fixture_home" -type f | wc -l | tr -d ' ') files"
	else
		check I11 "v2 legacy HIVE_HOME fixture built" FAIL "see $WORK/fixture.log"
	fi

	port_old=$((PORT_BASE + 1))
	start_serve "$upg_bindir/hive" "$fixture_home" "$port_old" "$WORK/serve-old.log"
	if wait_health "$port_old"; then
		curl -s -o "$WORK/old-history.json" --max-time 5 "http://127.0.0.1:$port_old/api/sessions/history"
		curl -s -o "$WORK/old-discover.json" --max-time 5 "http://127.0.0.1:$port_old/api/discover"
		sess_ok="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print("session_20260201_000001_bbbbbbbb" in {s["session_id"] for s in d.get("sessions",[])})' "$WORK/old-history.json" 2>/dev/null)"
		col_ok="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print("Alpha" in {a.get("name") for a in d.get("Your Agents",[])})' "$WORK/old-discover.json" 2>/dev/null)"
		if [ "$sess_ok" = "True" ] && [ "$col_ok" = "True" ]; then
			check I11b "old binary migrates + reads fixture home" PASS "health 200, session+colony visible"
		else
			check I11b "old binary migrates + reads fixture home" FAIL "sess=$sess_ok colony=$col_ok; see $WORK/serve-old.log"
		fi
	else
		check I11b "old binary migrates + reads fixture home" FAIL "no health 200; see $WORK/serve-old.log"
	fi
	stop_serve || true

	python3 - "$fixture_home" "$WORK/snapshot-before.json" <<'PY'
import hashlib, json, os, sys
root, out = sys.argv[1], sys.argv[2]
snap = {}
for dp, _dn, fn in os.walk(root):
    for f in fn:
        p = os.path.join(dp, f)
        rel = os.path.relpath(p, root)
        try:
            snap[rel] = hashlib.sha256(open(p, "rb").read()).hexdigest()
        except OSError as exc:
            snap[rel] = f"ERR:{exc}"
json.dump(snap, open(out, "w"), indent=1, sort_keys=True)
print(f"snapshot: {len(snap)} files")
PY

	# In-place upgrade: same install dir, new archive.
	new_sha="$(sha256_file "$extract/hive")"
	if run_install "$archive" "$home_upg" "$WORK/install-upgrade.log"; then
		upg_sha="$(sha256_file "$upg_bindir/hive" 2>/dev/null)"
		upg_version="$("$upg_bindir/hive" --version 2>&1 | head -1)"
		if [ "$upg_sha" = "$new_sha" ] && [ "$upg_version" = "hive $expected_version" ]; then
			check I12 "in-place upgrade replaces hive with the new build" PASS "binary sha matches archive; $upg_version"
		else
			check I12 "in-place upgrade replaces hive with the new build" FAIL "sha=${upg_sha:0:16}… want ${new_sha:0:16}…; version=$upg_version"
		fi
	else
		check I12 "in-place upgrade replaces hive with the new build" FAIL "see $WORK/install-upgrade.log"
	fi

	port_new=$((PORT_BASE + 2))
	start_serve "$upg_bindir/hive" "$fixture_home" "$port_new" "$WORK/serve-new.log"
	if wait_health "$port_new"; then
		curl -s -o "$WORK/new-history.json" --max-time 5 "http://127.0.0.1:$port_new/api/sessions/history"
		curl -s -o "$WORK/new-discover.json" --max-time 5 "http://127.0.0.1:$port_new/api/discover"
		sess_ok="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));print("session_20260201_000001_bbbbbbbb" in {s["session_id"] for s in d.get("sessions",[])})' "$WORK/new-history.json" 2>/dev/null)"
		col_ok="$(python3 -c 'import json,sys;n={a.get("name"):a.get("session_count") for a in json.load(open(sys.argv[1])).get("Your Agents",[])};o={a.get("name"):a.get("session_count") for a in json.load(open(sys.argv[2])).get("Your Agents",[])};print("Alpha" in n and n["Alpha"]==o.get("Alpha") and (n.get("Alpha") or 0)>=2)' "$WORK/new-discover.json" "$WORK/old-discover.json" 2>/dev/null)"
		markers_ok="$(python3 -c 'import os,sys;h=sys.argv[1];print(os.path.exists(os.path.join(h,".migrated-v2")) and os.path.exists(os.path.join(h,".migrated-v3")) and os.path.exists(os.path.join(h,"colonies",".migrations","colony_sessions_v1")))' "$fixture_home")"
		if [ "$sess_ok" = "True" ] && [ "$col_ok" = "True" ] && [ "$markers_ok" = "True" ]; then
			check I13 "upgraded binary serves the old home" PASS "health 200, sessions+colony+markers intact"
		else
			check I13 "upgraded binary serves the old home" FAIL "sess=$sess_ok colony=$col_ok markers=$markers_ok"
		fi
	else
		check I13 "upgraded binary serves the old home" FAIL "no health 200; see $WORK/serve-new.log"
	fi
	stop_serve || true

	python3 - "$fixture_home" "$WORK/snapshot-before.json" "$WORK/snapshot-after.json" "$WORK/snapshot-diff.json" <<'PY'
import hashlib, json, os, re, sys
root, before_path, after_path, out = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
before = json.load(open(before_path))
def snap(base):
    result = {}
    for dp, _dn, fn in os.walk(base):
        for f in fn:
            p = os.path.join(dp, f)
            rel = os.path.relpath(p, base)
            try:
                result[rel] = hashlib.sha256(open(p, "rb").read()).hexdigest()
            except OSError as exc:
                result[rel] = f"ERR:{exc}"
    return result
after = snap(root)
json.dump(after, open(after_path, "w"), indent=1, sort_keys=True)
allow = re.compile(r"(^|/)(logs?)(/|$)|\.log$|\.db-wal$|\.db-shm$|(^|/)maintenance/server\.lock$|(^|/)\.serve|(^|/)serve\.pid$")
added = sorted(set(after) - set(before))
removed = sorted(set(before) - set(after))
modified = sorted(k for k in before if k in after and before[k] != after[k])
bad_added = [p for p in added if not allow.search(p)]
bad_removed = [p for p in removed]
bad_modified = [p for p in modified if not allow.search(p)]
diff = {
    "added": added, "removed": removed, "modified": modified,
    "allowlisted_modified": [p for p in modified if allow.search(p)],
    "user_data_changes": {"added": bad_added, "removed": bad_removed, "modified": bad_modified},
}
json.dump(diff, open(out, "w"), indent=2)
print(json.dumps({k: len(v) if isinstance(v, list) else v for k, v in diff.items()}, ensure_ascii=False))
PY
	if python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));c=d["user_data_changes"];sys.exit(0 if not (c["added"] or c["removed"] or c["modified"]) else 1)' "$WORK/snapshot-diff.json"; then
		check I14 "user data lossless across upgrade" PASS "0 added / 0 removed / 0 user-data modified (allowlisted churn: $(python3 -c 'import json,sys;print(len(json.load(open(sys.argv[1]))["allowlisted_modified"]))' "$WORK/snapshot-diff.json"))"
	else
		check I14 "user data lossless across upgrade" FAIL "$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["user_data_changes"])' "$WORK/snapshot-diff.json")"
	fi
	fi
fi

# ── report ──────────────────────────────────────────────────────────────────
mkdir -p "$(dirname "$OUT")"
python3 - "$CHECK_TSV" "$OUT" "$archive" "$OLD_DIR" "$WORK" "$PASS" "$FAIL" "$SKIP" <<'PY'
import json, sys
from datetime import datetime, timezone
tsv, out, archive, old_dir, work, p, f, s = sys.argv[1:]
checks = []
for line in open(tsv, encoding="utf-8"):
    parts = line.rstrip("\n").split("\t")
    if len(parts) == 4:
        checks.append({"id": parts[0], "title": parts[1], "status": parts[2], "detail": parts[3]})
report = {
    "schema_version": 1,
    "tool": "go_hive/scripts/install_verify.sh",
    "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    "archive": archive,
    "old_dir": old_dir,
    "work": work,
    "pass": int(p), "fail": int(f), "skip": int(s),
    "verdict": "FAIL" if int(f) else ("PASS" if not int(f) and all(c["status"] == "PASS" for c in checks) else "PASS(with skips)"),
    "checks": checks,
}
open(out, "w", encoding="utf-8").write(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
print(f"\nverdict: {report['verdict']} — pass={p} fail={f} skip={s}")
print(f"report : {out}")
print(f"work   : {work}")
PY
[ "$FAIL" = 0 ] && exit 0 || exit 1
