#!/usr/bin/env bash
# Restore a go-hive-cloud data directory from a backup.sh archive.
#
# Safety properties:
#   - refuses to extract absolute paths or paths containing ".."
#   - verifies the archive .sha256 sidecar when present
#   - verifies SHA256SUMS for every archived file
#   - runs PRAGMA integrity_check on every restored SQLite database (when
#     sqlite3 is available)
#   - refuses a non-empty target unless --force is given; with --force the old
#     data directory is moved aside, not deleted
#   - enforces 0700 directories and 0600 files
#
# The service is NOT started or stopped by this script. Stop it before
# restoring, start it afterwards and verify /health.
set -eu

usage() {
  cat <<'EOF'
usage: restore.sh --archive FILE --data-dir DIR [--force] [--owner USER[:GROUP]]

  --archive FILE        backup archive produced by backup.sh
  --data-dir DIR        target data directory (created when absent)
  --force               move an existing non-empty data directory aside
                        (<dir>.pre-restore-<UTC timestamp>) before restoring
  --owner USER[:GROUP]  optional chown -R for the restored tree (systemd
                        deployments usually need the service user)

Exit codes: 0 success, 1 usage/validation error, 2 verification/extract error.
EOF
}

die() {
  printf 'restore: ERROR: %s\n' "$1" >&2
  exit "${2:-1}"
}

have() { command -v "$1" >/dev/null 2>&1; }

sha256_file() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "need sha256sum or shasum to verify the archive" 2
  fi
}

ARCHIVE=""
DATA_DIR=""
FORCE=0
OWNER=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --archive)
      [ "$#" -ge 2 ] || die "--archive requires a value" 1
      ARCHIVE=$2
      shift 2
      ;;
    --data-dir)
      [ "$#" -ge 2 ] || die "--data-dir requires a value" 1
      DATA_DIR=$2
      shift 2
      ;;
    --force)
      FORCE=1
      shift
      ;;
    --owner)
      [ "$#" -ge 2 ] || die "--owner requires a value" 1
      OWNER=$2
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
[ -n "$DATA_DIR" ] || die "--data-dir is required" 1
[ -f "$ARCHIVE" ] || die "archive not found: $ARCHIVE" 1
[ -r "$ARCHIVE" ] || die "archive not readable: $ARCHIVE" 1
case "$ARCHIVE" in
  /*) ;;
  *) ARCHIVE=$(cd "$(dirname "$ARCHIVE")" && pwd)/$(basename "$ARCHIVE") ;;
esac

if [ -n "$OWNER" ]; then
  case "$OWNER" in
    *[!A-Za-z0-9_.:-]*)
      die "invalid --owner value: $OWNER" 1
      ;;
  esac
fi

umask 077

# --- verify the outer checksum -------------------------------------------
ACTUAL_SHA=$(sha256_file "$ARCHIVE")
if [ -f "$ARCHIVE.sha256" ]; then
  EXPECTED_SHA=$(awk 'NR==1 {print $1}' "$ARCHIVE.sha256")
  [ -n "$EXPECTED_SHA" ] || die "empty checksum sidecar: $ARCHIVE.sha256" 2
  if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
    die "archive checksum mismatch (expected $EXPECTED_SHA, got $ACTUAL_SHA)" 2
  fi
else
  printf 'restore: WARN: no .sha256 sidecar; archive sha256 is %s\n' "$ACTUAL_SHA" >&2
fi

STAGE=$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-restore.XXXXXX") || die "cannot create staging dir" 2
TARGET=""
OLD=""
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

LIST="$STAGE.list"
tar -tzf "$ARCHIVE" > "$LIST" 2>/dev/null || die "not a readable tar.gz archive: $ARCHIVE" 2
if ! awk '
  /^\// { bad = 1 }
  {
    n = split($0, parts, "/")
    for (i = 1; i <= n; i++) {
      if (parts[i] == "..") bad = 1
    }
  }
  END { exit bad ? 1 : 0 }
' "$LIST"; then
  rm -f "$LIST"
  die "unsafe archive path (absolute or contains '..')" 2
fi
rm -f "$LIST"
tar -xzf "$ARCHIVE" -C "$STAGE" --no-same-owner 2>/dev/null || die "archive extraction failed" 2

if [ -n "$(find "$STAGE" -type l -print -quit)" ]; then
  die "archive contains symlinks; refusing to restore" 2
fi
[ -f "$STAGE/BACKUP-META.txt" ] || die "missing BACKUP-META.txt: not a hive-cloud backup" 2
grep -q '^kind=hive-cloud$' "$STAGE/BACKUP-META.txt" || die "BACKUP-META.txt is not a hive-cloud backup" 2
[ -d "$STAGE/teams" ] || die "missing teams/ in the archive" 2
[ -f "$STAGE/SHA256SUMS" ] || die "missing SHA256SUMS in the archive" 2

# --- verify every archived file ------------------------------------------
if have sha256sum; then
  (cd "$STAGE" && sha256sum -c SHA256SUMS >/dev/null 2>&1) || die "SHA256SUMS verification failed" 2
elif have shasum; then
  (cd "$STAGE" && shasum -a 256 -c SHA256SUMS >/dev/null 2>&1) || die "SHA256SUMS verification failed" 2
else
  die "need sha256sum or shasum to verify SHA256SUMS" 2
fi

# --- verify SQLite databases ---------------------------------------------
DB_COUNT=0
for db in "$STAGE"/teams/*.db; do
  [ -f "$db" ] || continue
  DB_COUNT=$((DB_COUNT + 1))
  if have sqlite3; then
    RESULT=$(sqlite3 "$db" 'PRAGMA integrity_check;' 2>/dev/null | head -1) || true
    [ "$RESULT" = ok ] || die "integrity_check failed for $(basename "$db"): $RESULT" 2
  else
    printf 'restore: WARN: sqlite3 not found; skipping integrity_check for %s\n' "$(basename "$db")" >&2
  fi
done

[ -n "$DATA_DIR" ] || die "--data-dir is required" 1
TARGET=$DATA_DIR
case "$TARGET" in
  /*) ;;
  *) TARGET=$(pwd)/$TARGET ;;
esac

# --- move the current data aside / install the restored tree ------------
if [ -e "$TARGET" ]; then
  [ -d "$TARGET" ] || die "target exists and is not a directory: $TARGET" 1
  if [ -n "$(ls -A "$TARGET" 2>/dev/null)" ]; then
    [ "$FORCE" -eq 1 ] || die "target data dir is not empty: $TARGET (use --force; existing data is moved aside)" 1
    NOW=$(date -u +%Y%m%dT%H%M%SZ)
    OLD="$TARGET.pre-restore-$NOW"
    [ -e "$OLD" ] && die "pre-restore path already exists: $OLD" 1
    mv "$TARGET" "$OLD" || die "cannot move existing data aside (mount point?): $TARGET" 2
    printf 'restore: previous data moved to %s\n' "$OLD"
  else
    rmdir "$TARGET" 2>/dev/null || die "cannot remove empty target dir: $TARGET" 2
  fi
fi
mkdir -p "$(dirname "$TARGET")" || die "cannot create parent of $TARGET" 2
mv "$STAGE" "$TARGET" || die "cannot install restored data at $TARGET" 2
STAGE=""

# --- permissions ----------------------------------------------------------
chmod 700 "$TARGET" || die "chmod failed: $TARGET" 2
if [ -d "$TARGET/teams" ]; then
  chmod 700 "$TARGET/teams" 2>/dev/null || true
fi
find "$TARGET" -type d -exec chmod 700 {} + 2>/dev/null || true
find "$TARGET" -type f -exec chmod 600 {} + 2>/dev/null || true
if [ -n "$OWNER" ]; then
  chown -R "$OWNER" "$TARGET" || die "chown -R $OWNER failed" 2
fi

printf 'restore: ok data_dir=%s\n' "$TARGET"
printf 'restore: source=%s sha256=%s databases=%s\n' "$ARCHIVE" "$ACTUAL_SHA" "$DB_COUNT"
if [ -n "$OLD" ]; then
  printf 'restore: rollback_data=%s\n' "$OLD"
fi
printf 'restore: next: start the service, then GET /health before opening traffic\n'
exit 0
