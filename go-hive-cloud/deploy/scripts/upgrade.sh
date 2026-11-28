#!/usr/bin/env bash
# Upgrade the hive-cloud binary with a pre-upgrade backup, version checks,
# optional service restart + health gate and an explicit rollback path.
#
# There are no schema migrations in the SQLite implementation: the per-team
# schema is created lazily by the binary, so an upgrade is a binary swap plus
# a restart. Backups are taken before the swap anyway.
#
# This script never prints secret values and never modifies the data directory
# (backup.sh only reads it). The new binary is installed atomically
# (<install-dir>/hive-cloud -> .hive-cloud.new.$$ -> mv).
#
# Exit codes: 0 success, 1 usage/safety refusal, 2 version/backup/install
# failure, 3 health check failure (rollback instructions printed).
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() {
  cat <<'EOF'
usage:
  upgrade.sh --binary NEW --data-dir DIR --install-dir DIR [options]

  --binary PATH        new hive-cloud binary to install
  --data-dir DIR       live data directory (backed up before the swap)
  --install-dir DIR    directory that holds the hive-cloud binary
  --backup-dir DIR     backup output directory (default: <data-dir>/../hive-cloud-backups)
  --expect-version V   require the new binary to report this version
  --service NAME       systemd service to restart (systemctl restart NAME)
  --restart-cmd CMD    custom restart command (used for launchd/docker)
  --health-url URL     wait for GET URL to succeed after restart
  --health-timeout S   health wait budget in seconds (default 30)
  --no-restart         install the binary but do not restart anything
  --rollback-on-failure  restore the previous binary when the health gate fails
  --check              validate versions and print the plan; no writes
  --rollback           swap <install-dir>/hive-cloud.prev back into place

Examples:
  ./upgrade.sh --check --binary ./hive-cloud-0.2.0 \
      --data-dir /var/lib/hive-cloud --install-dir /usr/local/bin
  ./upgrade.sh --binary ./hive-cloud-0.2.0 \
      --data-dir /var/lib/hive-cloud --install-dir /usr/local/bin \
      --service hive-cloud --health-url http://127.0.0.1:8788/health
  ./upgrade.sh --rollback --install-dir /usr/local/bin --service hive-cloud \
      --health-url http://127.0.0.1:8788/health
EOF
}

die() {
  printf 'upgrade: ERROR: %s\n' "$1" >&2
  exit "${2:-1}"
}

have() { command -v "$1" >/dev/null 2>&1; }

version_of() {
  OUT=$("$1" version 2>/dev/null | head -1) || die "not an executable hive-cloud binary: $1" 2
  case "$OUT" in
    hive-cloud\ *) printf '%s\n' "$OUT" ;;
    *) die "unexpected version output from $1: $OUT" 2 ;;
  esac
}

normalize_version() {
  printf '%s' "$1" | sed 's/^hive-cloud //'
}

BINARY=""
DATA_DIR=""
INSTALL_DIR=""
BACKUP_DIR=""
EXPECT_VERSION=""
SERVICE=""
RESTART_CMD=""
HEALTH_URL=""
HEALTH_TIMEOUT=30
NO_RESTART=0
ROLLBACK_ON_FAILURE=0
CHECK_ONLY=0
DO_ROLLBACK=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary)
      [ "$#" -ge 2 ] || die "--binary requires a value" 1
      BINARY=$2
      shift 2
      ;;
    --data-dir)
      [ "$#" -ge 2 ] || die "--data-dir requires a value" 1
      DATA_DIR=$2
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || die "--install-dir requires a value" 1
      INSTALL_DIR=$2
      shift 2
      ;;
    --backup-dir)
      [ "$#" -ge 2 ] || die "--backup-dir requires a value" 1
      BACKUP_DIR=$2
      shift 2
      ;;
    --expect-version)
      [ "$#" -ge 2 ] || die "--expect-version requires a value" 1
      EXPECT_VERSION=$2
      shift 2
      ;;
    --service)
      [ "$#" -ge 2 ] || die "--service requires a value" 1
      SERVICE=$2
      shift 2
      ;;
    --restart-cmd)
      [ "$#" -ge 2 ] || die "--restart-cmd requires a value" 1
      RESTART_CMD=$2
      shift 2
      ;;
    --health-url)
      [ "$#" -ge 2 ] || die "--health-url requires a value" 1
      HEALTH_URL=$2
      shift 2
      ;;
    --health-timeout)
      [ "$#" -ge 2 ] || die "--health-timeout requires a value" 1
      HEALTH_TIMEOUT=$2
      shift 2
      ;;
    --no-restart)
      NO_RESTART=1
      shift
      ;;
    --rollback-on-failure)
      ROLLBACK_ON_FAILURE=1
      shift
      ;;
    --check)
      CHECK_ONLY=1
      shift
      ;;
    --rollback)
      DO_ROLLBACK=1
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

case "$HEALTH_TIMEOUT" in
  ''|*[!0-9]*) die "--health-timeout must be a positive integer" 1 ;;
esac
[ "$HEALTH_TIMEOUT" -gt 0 ] || die "--health-timeout must be positive" 1

if [ -n "$HEALTH_URL" ] && [ "$NO_RESTART" -eq 0 ] && [ -z "$SERVICE" ] && [ -z "$RESTART_CMD" ]; then
  die "--health-url requires --service or --restart-cmd (otherwise an old process can answer the probe)" 1
fi

[ -n "$INSTALL_DIR" ] || die "--install-dir is required" 1
CURRENT="$INSTALL_DIR/hive-cloud"
PREV="$INSTALL_DIR/hive-cloud.prev"

restart_service() {
  if [ "$NO_RESTART" -eq 1 ]; then
    printf 'upgrade: --no-restart set; service not restarted\n'
    return 0
  fi
  if [ -n "$RESTART_CMD" ]; then
    sh -c "$RESTART_CMD" || die "restart command failed: $RESTART_CMD" 2
    return 0
  fi
  if [ -n "$SERVICE" ]; then
    have systemctl || die "systemctl not found; pass --restart-cmd for launchd/docker" 2
    systemctl restart "$SERVICE" || die "systemctl restart $SERVICE failed" 2
    return 0
  fi
  printf 'upgrade: WARN: no --service/--restart-cmd; restart the service manually\n'
  return 0
}

wait_health() {
  [ -n "$HEALTH_URL" ] || return 0
  [ "$NO_RESTART" -eq 1 ] && return 0
  have curl || die "curl is required for --health-url" 2
  i=0
  while [ "$i" -lt "$HEALTH_TIMEOUT" ]; do
    if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}

install_binary() {
  mkdir -p "$INSTALL_DIR" || die "cannot create install dir: $INSTALL_DIR" 2
  STAGED="$INSTALL_DIR/.hive-cloud.new.$$"
  cp "$BINARY" "$STAGED" || die "cannot copy new binary" 2
  chmod 755 "$STAGED" || die "cannot chmod staged binary" 2
  if ! "$STAGED" version >/dev/null 2>&1; then
    rm -f "$STAGED"
    die "new binary failed its smoke check (version)" 2
  fi
  mv "$STAGED" "$CURRENT" || die "cannot atomically replace $CURRENT" 2
}

# --- rollback action ------------------------------------------------------
if [ "$DO_ROLLBACK" -eq 1 ]; then
  [ -f "$PREV" ] || die "no previous binary at $PREV" 1
  NEW_VERSION=$(version_of "$CURRENT")
  OLD_VERSION=$(version_of "$PREV")
  STAGED="$INSTALL_DIR/.hive-cloud.rollback.$$"
  cp "$PREV" "$STAGED" || die "cannot copy previous binary" 2
  chmod 755 "$STAGED"
  mv "$STAGED" "$CURRENT" || die "cannot atomically restore $CURRENT" 2
  printf 'upgrade: rolled back %s -> %s\n' "$NEW_VERSION" "$OLD_VERSION"
  restart_service
  if ! wait_health; then
    die "health check failed after rollback ($HEALTH_URL); inspect the service logs" 3
  fi
  if [ -n "$HEALTH_URL" ] && [ "$NO_RESTART" -eq 0 ]; then
    printf 'upgrade: health ok %s\n' "$HEALTH_URL"
  fi
  printf 'upgrade: rollback complete (previous binary kept at %s)\n' "$PREV"
  exit 0
fi

# --- upgrade / check ------------------------------------------------------
[ -n "$BINARY" ] || die "--binary is required (or use --rollback)" 1
[ -n "$DATA_DIR" ] || die "--data-dir is required" 1
[ -f "$BINARY" ] || die "new binary not found: $BINARY" 1
[ -r "$BINARY" ] || die "new binary not readable: $BINARY" 1
[ -d "$DATA_DIR" ] || die "data dir not found: $DATA_DIR" 1
DATA_DIR=$(cd "$DATA_DIR" && pwd)
case "$BINARY" in
  /*) ;;
  *) BINARY=$(cd "$(dirname "$BINARY")" && pwd)/$(basename "$BINARY") ;;
esac

NEW_VERSION=$(version_of "$BINARY")
if [ -n "$EXPECT_VERSION" ]; then
  if [ "$(normalize_version "$NEW_VERSION")" != "$(normalize_version "$EXPECT_VERSION")" ]; then
    die "version check failed: want $(normalize_version "$EXPECT_VERSION"), got $(normalize_version "$NEW_VERSION"); refusing to install" 2
  fi
fi

CURRENT_VERSION=none
if [ -f "$CURRENT" ]; then
  CURRENT_VERSION=$(version_of "$CURRENT")
fi

if [ -z "$BACKUP_DIR" ]; then
  BACKUP_DIR=$(dirname "$DATA_DIR")/hive-cloud-backups
fi

printf 'upgrade: current=%s\n' "$CURRENT_VERSION"
printf 'upgrade: new=%s\n' "$NEW_VERSION"
printf 'upgrade: target=%s\n' "$CURRENT"
printf 'upgrade: backup_dir=%s\n' "$BACKUP_DIR"

if [ "$CHECK_ONLY" -eq 1 ]; then
  printf 'upgrade: check ok (no writes performed)\n'
  exit 0
fi
mkdir -p "$BACKUP_DIR" || die "cannot create backup dir: $BACKUP_DIR" 2

# --- backup first, then atomic swap --------------------------------------
if ! "$HERE/backup.sh" --data-dir "$DATA_DIR" --out "$BACKUP_DIR" --label pre-upgrade --binary "$BINARY"; then
  die "pre-upgrade backup failed; refusing to touch the installed binary" 2
fi

if [ -f "$CURRENT" ]; then
  cp -p "$CURRENT" "$PREV.tmp.$$" || die "cannot copy current binary to $PREV" 2
  mv "$PREV.tmp.$$" "$PREV" || die "cannot move previous binary into place" 2
fi
install_binary

INSTALLED_VERSION=$(version_of "$CURRENT")
[ "$(normalize_version "$INSTALLED_VERSION")" = "$(normalize_version "$NEW_VERSION")" ] ||
  die "post-install version mismatch (want $NEW_VERSION, got $INSTALLED_VERSION)" 2

printf 'upgrade: installed %s\n' "$INSTALLED_VERSION"
restart_service

if ! wait_health; then
  printf 'upgrade: ERROR: health check failed: %s\n' "$HEALTH_URL" >&2
  if [ "$ROLLBACK_ON_FAILURE" -eq 1 ] && [ -f "$PREV" ]; then
    printf 'upgrade: restoring previous binary %s\n' "$PREV" >&2
    STAGED="$INSTALL_DIR/.hive-cloud.rollback.$$"
    cp "$PREV" "$STAGED" && chmod 755 "$STAGED" && mv "$STAGED" "$CURRENT" ||
      die "automatic rollback could not restore $CURRENT" 2
    restart_service
    if wait_health; then
      die "health failed after upgrade; rolled back to $(version_of "$CURRENT")" 3
    fi
    die "health failed after upgrade AND rollback; inspect logs and data dir" 3
  fi
  printf 'upgrade: rollback with:\n  %s --rollback --install-dir %s' "$HERE/upgrade.sh" "$INSTALL_DIR" >&2
  [ -n "$SERVICE" ] && printf ' --service %s' "$SERVICE" >&2
  [ -n "$RESTART_CMD" ] && printf ' --restart-cmd %s' "$RESTART_CMD" >&2
  [ -n "$HEALTH_URL" ] && printf ' --health-url %s' "$HEALTH_URL" >&2
  printf '\n' >&2
  exit 3
fi
if [ -n "$HEALTH_URL" ] && [ "$NO_RESTART" -eq 0 ]; then
  printf 'upgrade: health ok %s\n' "$HEALTH_URL"
fi
printf 'upgrade: ok previous_binary=%s\n' "$PREV"
exit 0
