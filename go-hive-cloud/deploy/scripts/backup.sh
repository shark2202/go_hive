#!/usr/bin/env bash
# Back up a go-hive-cloud data directory.
#
# Live mode (default) snapshots every per-team SQLite database with
# `sqlite3 .backup` (WAL-safe) and copies the data-dir root files (config.json
# and the auto-generated JWT/streamToken secret files). Offline mode copies the
# database files and their -wal/-shm sidecars as-is; use it only with the
# service stopped (or when sqlite3 is unavailable).
#
# The archive is self-describing:
#   BACKUP-META.txt  key=value metadata (no secret values)
#   SHA256SUMS       checksums for every archived file
#   teams/*.db       per-team SQLite databases
#   *.json / secret files from the data-dir root
#
# Secrets stay inside the 0600 archive; they are never printed by this script.
set -eu

usage() {
  cat <<'EOF'
usage: backup.sh --data-dir DIR --out DIR [--label NAME] [--binary PATH] [--offline]

  --data-dir DIR   hive-cloud data directory (contains teams/, jwt_secret, ...)
  --out DIR        directory that receives the .tar.gz archive and .sha256
  --label NAME     optional label suffix (letters, digits, '-', '_', '.')
  --binary PATH    optional hive-cloud binary used to record its version
  --offline        copy SQLite files instead of taking an online .backup
                   (requires the service to be stopped)

Exit codes: 0 success, 1 usage/validation error, 2 backup failure.
EOF
}

die() {
  printf 'backup: ERROR: %s\n' "$1" >&2
  exit "${2:-2}"
}

have() { command -v "$1" >/dev/null 2>&1; }

sha256_file() {
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "need sha256sum or shasum to checksum the backup" 1
  fi
}

# Quote a filesystem path as a SQL string literal for sqlite3's .backup command.
sql_literal() {
  printf '%s' "$1" | sed "s/'/''/g"
}

DATA_DIR=""
OUT_DIR=""
LABEL=""
BINARY=""
OFFLINE=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --data-dir)
      [ "$#" -ge 2 ] || die "--data-dir requires a value" 1
      DATA_DIR=$2
      shift 2
      ;;
    --out)
      [ "$#" -ge 2 ] || die "--out requires a value" 1
      OUT_DIR=$2
      shift 2
      ;;
    --label)
      [ "$#" -ge 2 ] || die "--label requires a value" 1
      LABEL=$2
      shift 2
      ;;
    --binary)
      [ "$#" -ge 2 ] || die "--binary requires a value" 1
      BINARY=$2
      shift 2
      ;;
    --offline)
      OFFLINE=1
      shift
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

[ -n "$DATA_DIR" ] || die "--data-dir is required" 1
[ -n "$OUT_DIR" ] || die "--out is required" 1
[ -d "$DATA_DIR" ] || die "data dir not found: $DATA_DIR" 1
DATA_DIR=$(cd "$DATA_DIR" && pwd)
mkdir -p "$OUT_DIR" || die "cannot create out dir: $OUT_DIR"
OUT_DIR=$(cd "$OUT_DIR" && pwd)

case "$LABEL" in
  *[!A-Za-z0-9._-]*)
    die "invalid --label (allowed: letters, digits, '-', '_', '.')" 1
    ;;
esac

if [ "$OFFLINE" -eq 0 ] && ! have sqlite3; then
  die "sqlite3 not found; stop the service and re-run with --offline" 2
fi

umask 077
NOW=$(date -u +%Y%m%dT%H%M%SZ)
STAGE=$(mktemp -d "$OUT_DIR/.hive-cloud-backup.XXXXXX") || die "cannot create staging dir" 2
TMP_ARCHIVE=""
ARCHIVE=""
cleanup() {
  rm -rf "$STAGE" 2>/dev/null || true
  if [ -n "${TMP_ARCHIVE:-}" ]; then
    rm -f "$TMP_ARCHIVE"
  fi
  return 0
}
trap cleanup EXIT HUP INT TERM

BIN_VERSION=""
if [ -n "$BINARY" ]; then
  BIN_VERSION=$("$BINARY" version 2>/dev/null | head -1) || die "cannot read version from $BINARY" 2
  case "$BIN_VERSION" in
    hive-cloud*) ;;
    *) die "unexpected version output from $BINARY: $BIN_VERSION" 2 ;;
  esac
fi

MODE=live
if [ "$OFFLINE" -eq 1 ]; then
  MODE=offline
fi

mkdir -p "$STAGE/teams"

TEAM_DBS=0
if [ -d "$DATA_DIR/teams" ]; then
  for db in "$DATA_DIR"/teams/*.db; do
    [ -f "$db" ] || continue
    base=$(basename "$db")
    dest="$STAGE/teams/$base"
    if [ "$OFFLINE" -eq 1 ]; then
      cp -p "$db" "$dest" || die "copy failed: $db" 2
      for suffix in -wal -shm; do
        if [ -f "$db$suffix" ]; then
          cp -p "$db$suffix" "$dest$suffix" || die "copy failed: $db$suffix" 2
        fi
      done
    else
      sqlite3 "$db" ".backup '$(sql_literal "$dest")'" || die "sqlite3 .backup failed: $db" 2
    fi
    TEAM_DBS=$((TEAM_DBS + 1))
  done
fi

# Data-dir root files: config.json, secret files and anything else small that
# belongs to the deployment. Subdirectories other than teams/ are not cloud
# layout and are skipped with a warning.
for entry in "$DATA_DIR"/*; do
  [ -e "$entry" ] || continue
  if [ -d "$entry" ]; then
    [ "$(basename "$entry")" = teams ] && continue
    printf 'backup: WARN: ignoring unknown directory %s\n' "$entry" >&2
    continue
  fi
  name=$(basename "$entry")
  cp -p "$entry" "$STAGE/$name" || die "copy failed: $entry" 2
done

{
  printf 'backup_format=1\n'
  printf 'kind=hive-cloud\n'
  printf 'created_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'host=%s\n' "$(hostname 2>/dev/null || printf unknown)"
  printf 'source_data_dir=%s\n' "$DATA_DIR"
  printf 'mode=%s\n' "$MODE"
  printf 'team_databases=%s\n' "$TEAM_DBS"
  printf 'binary_version=%s\n' "$BIN_VERSION"
} > "$STAGE/BACKUP-META.txt"

(
  cd "$STAGE" || exit 1
  find . -type f ! -name SHA256SUMS ! -name BACKUP-META.txt -print | LC_ALL=C sort |
    while IFS= read -r rel; do
      printf '%s  %s\n' "$(sha256_file "$rel")" "$rel"
    done
) > "$STAGE/SHA256SUMS" || die "cannot build SHA256SUMS" 2

FILE_COUNT=$(find "$STAGE" -type f | wc -l | tr -d ' ')

ARCHIVE="$OUT_DIR/hive-cloud-backup-$NOW${LABEL:+-$LABEL}.tar.gz"
if [ -e "$ARCHIVE" ]; then
  ARCHIVE="$OUT_DIR/hive-cloud-backup-$NOW${LABEL:+-$LABEL}-$$.tar.gz"
fi
TMP_ARCHIVE="$ARCHIVE.tmp.$$"
COPYFILE_DISABLE=1 tar -C "$STAGE" -czf "$TMP_ARCHIVE" . || die "tar failed" 2
chmod 600 "$TMP_ARCHIVE" || die "chmod failed: $TMP_ARCHIVE" 2
mv "$TMP_ARCHIVE" "$ARCHIVE" || die "cannot move archive into place" 2
TMP_ARCHIVE=""
ARCHIVE_SHA=$(sha256_file "$ARCHIVE") || die "cannot hash archive" 2
printf '%s  %s\n' "$ARCHIVE_SHA" "$(basename "$ARCHIVE")" > "$ARCHIVE.sha256"
chmod 600 "$ARCHIVE.sha256"

printf 'backup: ok archive=%s\n' "$ARCHIVE"
printf 'backup: sha256=%s\n' "$ARCHIVE_SHA"
printf 'backup: bytes=%s files=%s team_databases=%s mode=%s\n' \
  "$(wc -c < "$ARCHIVE" | tr -d ' ')" "$FILE_COUNT" "$TEAM_DBS" "$MODE"
exit 0
