#!/usr/bin/env bash
# Verify a go-hive-cloud backup without touching any live data directory.
#
# Checks:
#   1. the archive .sha256 sidecar (when present) or a printed archive hash
#   2. safe archive paths (no absolute paths, no "..", no symlinks)
#   3. BACKUP-META.txt identity and layout (teams/, SHA256SUMS)
#   4. SHA256SUMS for every archived file
#   5. PRAGMA integrity_check for every SQLite database (when sqlite3 exists)
#
# No secret values are printed. Exit codes: 0 verified, 1 usage, 2 failure.
set -eu

usage() {
  cat <<'EOF'
usage: verify-backup.sh --archive FILE

Runs the restore-time checks on a backup archive in a throwaway directory.
Use this on a schedule to prove backups are restorable, not just present.
EOF
}

die() {
  printf 'verify-backup: ERROR: %s\n' "$1" >&2
  exit "${2:-2}"
}

have() { command -v "$1" >/dev/null 2>&1; }

sha256_file() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "need sha256sum or shasum" 2
  fi
}

ARCHIVE=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --archive)
      [ "$#" -ge 2 ] || die "--archive requires a value" 1
      ARCHIVE=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1" 1
      ;;
  esac
done
[ -n "$ARCHIVE" ] || die "--archive is required" 1
[ -f "$ARCHIVE" ] || die "archive not found: $ARCHIVE" 1
[ -r "$ARCHIVE" ] || die "archive not readable: $ARCHIVE" 1
case "$ARCHIVE" in
  /*) ;;
  *) ARCHIVE=$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE") ;;
esac

umask 077
ACTUAL_SHA=$(sha256_file "$ARCHIVE")
if [ -f "$ARCHIVE.sha256" ]; then
  EXPECTED_SHA=$(awk 'NR==1 {print $1}' "$ARCHIVE.sha256")
  [ -n "$EXPECTED_SHA" ] || die "empty checksum sidecar" 2
  [ "$EXPECTED_SHA" = "$ACTUAL_SHA" ] || die "archive checksum mismatch" 2
else
  printf 'verify-backup: WARN: no .sha256 sidecar; archive sha256 is %s\n' "$ACTUAL_SHA" >&2
fi

STAGE=$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-verify.XXXXXX") || die "cannot create staging dir" 2
cleanup() {
  if [ -n "${STAGE:-}" ]; then
    rm -rf "$STAGE"
  fi
  if [ -n "${LIST:-}" ]; then
    rm -f "$LIST"
  fi
  return 0
}
trap cleanup EXIT HUP INT TERM

LIST="$STAGE.listing"
tar -tzf "$ARCHIVE" > "$LIST" 2>/dev/null || die "not a readable tar.gz archive" 2
if ! awk '
  /^\// { bad = 1 }
  {
    n = split($0, parts, "/")
    for (i = 1; i <= n; i++) if (parts[i] == "..") bad = 1
  }
  END { exit bad ? 1 : 0 }
' "$LIST"; then
  rm -f "$LIST"
  die "unsafe archive path (absolute or contains '..')" 2
fi
rm -f "$LIST"
tar -xzf "$ARCHIVE" -C "$STAGE" --no-same-owner 2>/dev/null || die "archive extraction failed" 2
[ -z "$(find "$STAGE" -type l -print -quit)" ] || die "archive contains symlinks" 2
[ -f "$STAGE/BACKUP-META.txt" ] || die "missing BACKUP-META.txt" 2
grep -q '^kind=hive-cloud$' "$STAGE/BACKUP-META.txt" || die "not a hive-cloud backup" 2
[ -d "$STAGE/teams" ] || die "missing teams/ in the archive" 2
[ -f "$STAGE/SHA256SUMS" ] || die "missing SHA256SUMS" 2

if have sha256sum; then
  (cd "$STAGE" && sha256sum -c SHA256SUMS >/dev/null 2>&1) || die "SHA256SUMS verification failed" 2
elif have shasum; then
  (cd "$STAGE" && shasum -a 256 -c SHA256SUMS >/dev/null 2>&1) || die "SHA256SUMS verification failed" 2
else
  die "need sha256sum or shasum" 2
fi

DB_COUNT=0
INTEGRITY=skipped
if have sqlite3; then
  INTEGRITY=ok
fi
for db in "$STAGE"/teams/*.db; do
  [ -f "$db" ] || continue
  DB_COUNT=$((DB_COUNT + 1))
  if have sqlite3; then
    RESULT=$(sqlite3 "$db" 'PRAGMA integrity_check;' 2>/dev/null | head -1) || true
    [ "$RESULT" = ok ] || die "integrity_check failed for $(basename "$db"): $RESULT" 2
  fi
done
[ "$INTEGRITY" = ok ] || printf 'verify-backup: WARN: sqlite3 not found; integrity_check skipped\n' >&2

printf 'verify-backup: ok archive=%s\n' "$ARCHIVE"
printf 'verify-backup: sha256=%s databases=%s integrity=%s\n' "$ACTUAL_SHA" "$DB_COUNT" "$INTEGRITY"
exit 0
