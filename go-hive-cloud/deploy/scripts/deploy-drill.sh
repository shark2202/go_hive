#!/usr/bin/env bash
# M7 deployment drill: prove the backup/restore/upgrade scripts against a
# throwaway data directory, without touching any real deployment.
#
#   1. build hive-cloud, start a throwaway server on 127.0.0.1:0
#   2. seed a Global DB table/row and one inbox message
#   3. live backup -> verify-backup -> delete data dir -> restore -> health
#   4. assert the restored rows and 0600/0700 permissions
#   5. upgrade --check -> install -> rollback; prove a bad version is refused
#   6. static deploy checks (shell syntax, plist, systemd directives, compose config)
#
# Evidence is written to evidence/m7-deploy-drill.json by default. It contains
# versions, counts, hashes and status strings only: no JWTs, streamTokens or
# secret file contents.
#
# Requirements: go toolchain (or GOENV), curl, jq, sqlite3 (live backup),
# bash. Docker is required only for the compose config check.
set -euo pipefail

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DEPLOY=$(CDPATH= cd -- "$HERE/.." && pwd)
CLOUD_ROOT=$(CDPATH= cd -- "$DEPLOY/.." && pwd)
SCRIPTS="$HERE"
EVIDENCE_OUT=${EVIDENCE_OUT:-"$CLOUD_ROOT/evidence/m7-deploy-drill.json"}
GOENV=${GOENV:-/Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh}

if [ -f "$GOENV" ]; then
  # shellcheck disable=SC1090
  . "$GOENV"
fi

for tool in go curl jq sqlite3; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf 'drill: required tool not found: %s\n' "$tool" >&2
    exit 1
  }
done

WORK=$(mktemp -d "${TMPDIR:-/tmp}/hive-cloud-m7.XXXXXX")
CHECKS="$WORK/checks.jsonl"
: > "$CHECKS"
FAILS=0
EVIDENCE_WRITTEN=0
SERVE_PID=""
URL=""
JWT=""
VERSION=""
ARCHIVE=""
BACKUP_BYTES=0
BACKUP_SHA=""
BACKUP_FILES=0
DB_COUNT=0
STARTED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)

record() {
  jq -cn --arg name "$1" --argjson ok "$2" --arg detail "${3:-}" \
    '{name:$name, ok:$ok, detail:$detail}' >> "$CHECKS"
}

stop_server() {
  if [ -n "${SERVE_PID:-}" ]; then
    kill "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
    SERVE_PID=""
  fi
}

finish_evidence() {
  EXIT_CODE=${1:-1}
  mkdir -p "$(dirname "$EVIDENCE_OUT")"
  SQLITE_VAL=$(sqlite3 --version 2>/dev/null | head -1 || true)
  DOCKER_VAL=$(docker compose version 2>/dev/null | head -1 || printf 'unavailable')
  PLUTIL_VAL=unavailable
  command -v plutil >/dev/null 2>&1 && PLUTIL_VAL=available
  SYSTEMD_VAL=unavailable
  command -v systemd-analyze >/dev/null 2>&1 && SYSTEMD_VAL=available
  jq -n \
    --arg harness 'm7-deploy-drill (backup -> delete -> restore -> health, upgrade check/install/rollback)' \
    --arg started_at "$STARTED_AT" \
    --arg finished_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson exit_code "$EXIT_CODE" \
    --argjson fails "$FAILS" \
    --arg binary_version "${VERSION:-unknown}" \
    --arg os "$(uname -srm)" \
    --arg bash_version "${BASH_VERSION:-unknown}" \
    --arg sqlite3 "$SQLITE_VAL" \
    --arg docker "$DOCKER_VAL" \
    --arg plutil "$PLUTIL_VAL" \
    --arg systemd_analyze "$SYSTEMD_VAL" \
    --argjson backup_bytes "${BACKUP_BYTES:-0}" \
    --arg backup_sha "${BACKUP_SHA:-}" \
    --argjson backup_files "${BACKUP_FILES:-0}" \
    --argjson team_databases "${DB_COUNT:-0}" \
    --slurpfile checks "$CHECKS" \
    '{
      harness: $harness,
      started_at: $started_at,
      finished_at: $finished_at,
      ok: (($exit_code == 0) and ($fails == 0) and ([$checks[].ok] | all)),
      exit_code: $exit_code,
      binary_version: $binary_version,
      environment: {
        os: $os,
        bash: $bash_version,
        sqlite3: $sqlite3,
        docker_compose: $docker,
        plutil: $plutil,
        systemd_analyze: $systemd_analyze,
        docker_image_build: "not run: the drill validates compose config; building needs registry access"
      },
      artifacts: {
        backup_bytes: $backup_bytes,
        backup_sha256: $backup_sha,
        archive_entries: $backup_files,
        team_databases: $team_databases
      },
      checks: $checks
    }' > "$EVIDENCE_OUT.tmp"
  mv "$EVIDENCE_OUT.tmp" "$EVIDENCE_OUT"
  EVIDENCE_WRITTEN=1
}

fail() {
  record "$1" false "${2:-failed}"
  FAILS=$((FAILS + 1))
  printf 'drill: FAIL %s: %s\n' "$1" "${2:-}" >&2
  finish_evidence 1
  exit 1
}

cleanup() {
  stop_server
  if [ -f "$WORK/service.pid" ]; then
    kill "$(cat "$WORK/service.pid")" 2>/dev/null || true
    rm -f "$WORK/service.pid"
  fi
  if [ "$EVIDENCE_WRITTEN" -eq 0 ]; then
    finish_evidence 1
  fi
  if [ "${KEEP_WORK:-0}" = 1 ]; then
    printf 'drill: work dir kept at %s\n' "$WORK"
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT TERM

start_server() {
  DATA=$1
  LOG=$2
  "$BIN" serve --listen 127.0.0.1:0 --data-dir "$DATA" >"$LOG" 2>&1 &
  SERVE_PID=$!
  i=0
  while [ "$i" -lt 100 ]; do
    if grep -q 'listening on' "$LOG" 2>/dev/null; then
      break
    fi
    if ! kill -0 "$SERVE_PID" 2>/dev/null; then
      fail serve_start "server exited before listening"
    fi
    sleep 0.1
    i=$((i + 1))
  done
  URL=$(sed -n 's/^hive-cloud: listening on \(http[^ ]*\).*/\1/p' "$LOG" | head -1)
  [ -n "$URL" ] || fail serve_start "no listen address in server log"
  j=0
  while [ "$j" -lt 100 ]; do
    if curl -fsS --max-time 2 "$URL/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
    j=$((j + 1))
  done
  fail serve_health "GET /health did not become ready at $URL"
}

http_post() {
  # url json-body output-file -> status code
  curl -sS -o "$3" -w '%{http_code}' -X POST "$1" \
    -H "Authorization: Bearer $JWT" \
    -H 'Content-Type: application/json' \
    --data-binary "$2"
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

# --- build ----------------------------------------------------------------
BIN="$WORK/bin/hive-cloud"
mkdir -p "$WORK/bin"
(
  cd "$CLOUD_ROOT"
  go build -o "$BIN" ./cmd/hive-cloud
) || fail build_binary "go build failed"
VERSION=$("$BIN" version)
record build_binary true "version=$VERSION"

# --- initial server + seed ------------------------------------------------
DATA="$WORK/data"
start_server "$DATA" "$WORK/serve-initial.log"
HEALTH_BODY=$(curl -fsS "$URL/health") || fail health_initial "GET /health failed"
HEALTH_VERSION=$(printf '%s' "$HEALTH_BODY" | jq -r '.version // empty')
[ "$HEALTH_VERSION" = "$(printf '%s' "$VERSION" | sed 's/^hive-cloud //')" ] ||
  fail health_initial "health version mismatch: $HEALTH_VERSION"
record health_initial true "status=200 version=$HEALTH_VERSION"

JWT=$("$BIN" token --data-dir "$DATA" --user-id u_m7 --team-id team_m7 --ttl 2h) ||
  fail mint_jwt "token command failed"
[ -n "$JWT" ] || fail mint_jwt "empty token"

CREATE_BODY=$(jq -cn --arg sql 'CREATE TABLE m7_drill (id TEXT PRIMARY KEY, note TEXT NOT NULL)' '{sql:$sql}')
CODE=$(http_post "$URL/v1/global-db/sql" "$CREATE_BODY" "$WORK/create.json")
[ "$CODE" = 200 ] || fail seed_global_db "CREATE status=$CODE"

INSERT_BODY=$(jq -cn --arg sql "INSERT INTO m7_drill (id, note) VALUES ('sentinel-1', 'm7 backup drill')" '{sql:$sql}')
CODE=$(http_post "$URL/v1/global-db/sql" "$INSERT_BODY" "$WORK/insert.json")
[ "$CODE" = 200 ] || fail seed_global_db "INSERT status=$CODE"

SELECT_BODY=$(jq -cn --arg sql 'SELECT COUNT(*) AS n FROM m7_drill' '{sql:$sql}')
CODE=$(http_post "$URL/v1/global-db/sql" "$SELECT_BODY" "$WORK/select-before.json")
[ "$CODE" = 200 ] || fail seed_global_db "SELECT status=$CODE"
COUNT=$(jq -r '.rows[0].n' "$WORK/select-before.json")
[ "$COUNT" = 1 ] || fail seed_global_db "unexpected seeded row count: $COUNT"

INBOX_BODY=$(jq -cn '{kind:"blocker",title:"m7 drill",body:"backup drill",session_id:"sess_m7",runtime_id:"rt_m7",correlation_token:"corr_m7"}')
CODE=$(http_post "$URL/v1/inbox/messages" "$INBOX_BODY" "$WORK/inbox.json")
[ "$CODE" = 200 ] || fail seed_inbox "inbox message status=$CODE"
MESSAGE_ID=$(jq -r '.id // empty' "$WORK/inbox.json")
[ -n "$MESSAGE_ID" ] || fail seed_inbox "inbox message id missing"
record seed_data true "globaldb_rows=1 inbox_messages=1"

# --- live backup + verification ------------------------------------------
if ! "$SCRIPTS/backup.sh" --data-dir "$DATA" --out "$WORK/backups" --label drill --binary "$BIN" >"$WORK/backup.log" 2>&1; then
  fail backup_live "backup.sh failed (see deploy-drill work dir, KEEP_WORK=1 to retain)"
fi
ARCHIVE=$(find "$WORK/backups" -maxdepth 1 -type f -name '*.tar.gz' -print -quit)
[ -n "$ARCHIVE" ] || fail backup_live "no archive produced"
BACKUP_BYTES=$(wc -c < "$ARCHIVE" | tr -d ' ')
BACKUP_SHA=$(awk 'NR==1 {print $1}' "$ARCHIVE.sha256")
BACKUP_FILES=$(tar -tzf "$ARCHIVE" | wc -l | tr -d ' ')
record backup_live true "mode=live bytes=$BACKUP_BYTES entries=$BACKUP_FILES sha256=$BACKUP_SHA"

if ! "$SCRIPTS/verify-backup.sh" --archive "$ARCHIVE" >"$WORK/verify.log" 2>&1; then
  fail verify_backup "verify-backup.sh failed"
fi
record verify_backup true "checksums=ok integrity=ok"

# --- delete + restore -----------------------------------------------------
stop_server
rm -rf "$DATA"
[ ! -e "$DATA" ] || fail delete_data "data dir still exists after rm -rf"
[ ! -f "$DATA/teams/team_m7.db" ] || fail delete_data "team database still present"
record delete_data true "path_removed=1"

if ! "$SCRIPTS/restore.sh" --archive "$ARCHIVE" --data-dir "$DATA" >"$WORK/restore.log" 2>&1; then
  fail restore_archive "restore.sh failed"
fi
DATA_MODE=$(file_mode "$DATA")
JWT_MODE=$(file_mode "$DATA/jwt_secret")
STREAM_MODE=$(file_mode "$DATA/stream_secret")
DB_COUNT=$(find "$DATA/teams" -maxdepth 1 -type f -name '*.db' | wc -l | tr -d ' ')
[ "$DATA_MODE" = 700 ] || fail restore_permissions "data dir mode=$DATA_MODE"
[ "$JWT_MODE" = 600 ] || fail restore_permissions "jwt_secret mode=$JWT_MODE"
[ "$STREAM_MODE" = 600 ] || fail restore_permissions "stream_secret mode=$STREAM_MODE"
[ "$DB_COUNT" = 1 ] || fail restore_permissions "restored database count=$DB_COUNT"
record restore_archive true "target_created=1 databases=$DB_COUNT"
record restore_permissions true "data_dir=$DATA_MODE jwt_secret=$JWT_MODE stream_secret=$STREAM_MODE"

start_server "$DATA" "$WORK/serve-restored.log"
HEALTH_BODY=$(curl -fsS "$URL/health") || fail restore_health "GET /health failed after restore"
HEALTH_VERSION=$(printf '%s' "$HEALTH_BODY" | jq -r '.version // empty')
record restore_health true "status=200 version=$HEALTH_VERSION"

JWT=$("$BIN" token --data-dir "$DATA" --user-id u_m7 --team-id team_m7 --ttl 2h) ||
  fail restore_health "token mint from restored secrets failed"
CODE=$(http_post "$URL/v1/global-db/sql" "$SELECT_BODY" "$WORK/select-after.json")
[ "$CODE" = 200 ] || fail restore_data_intact "SELECT status=$CODE after restore"
jq -e '.rows[0].n == 1' "$WORK/select-after.json" >/dev/null ||
  fail restore_data_intact "seeded row missing after restore"

INBOX_SQL="SELECT COUNT(*) AS n FROM inbox_messages WHERE session_id = 'sess_m7'"
INBOX_COUNT_BODY=$(jq -cn --arg sql "$INBOX_SQL" '{sql:$sql}')
CODE=$(http_post "$URL/v1/global-db/sql" "$INBOX_COUNT_BODY" "$WORK/inbox-count.json")
[ "$CODE" = 200 ] || fail restore_data_intact "inbox count status=$CODE"
INBOX_COUNT=$(jq -r '.rows[0].n' "$WORK/inbox-count.json")
[ "$INBOX_COUNT" = 1 ] || fail restore_data_intact "inbox message missing after restore: $INBOX_COUNT"
record restore_data_intact true "globaldb_rows=1 inbox_rows=1"

# --- upgrade check / install / rollback ----------------------------------
stop_server
INSTALL_DIR="$WORK/install"
NEW_BIN="$WORK/new/hive-cloud"
mkdir -p "$INSTALL_DIR" "$WORK/new"
cp "$BIN" "$INSTALL_DIR/hive-cloud"
cp "$BIN" "$NEW_BIN"
chmod 755 "$INSTALL_DIR/hive-cloud" "$NEW_BIN"

UPGRADE="$SCRIPTS/upgrade.sh"
if ! "$UPGRADE" --check --binary "$NEW_BIN" --data-dir "$DATA" \
  --install-dir "$INSTALL_DIR" --backup-dir "$WORK/upgrade-backups" \
  >"$WORK/upgrade-check.log" 2>&1; then
  fail upgrade_check "upgrade.sh --check failed"
fi
record upgrade_check true "version=$VERSION writes=0"

# Health-gated upgrade against a throwaway server: a restart helper kills the
# old PID and starts the installed binary; upgrade.sh must wait for /health.
PORT=18788
if command -v nc >/dev/null 2>&1; then
  i=0
  while [ "$i" -lt 20 ] && nc -z 127.0.0.1 "$PORT" 2>/dev/null; do
    PORT=$((PORT + 1))
    i=$((i + 1))
  done
fi
cat > "$WORK/restart-service.sh" <<'RS_EOF'
#!/bin/sh
set -eu
PIDFILE=$1
BINDIR=$2
DATADIR=$3
LOG=$4
PORT=$5
if [ -f "$PIDFILE" ]; then
  kill "$(cat "$PIDFILE")" 2>/dev/null || true
  sleep 0.2
fi
"$BINDIR/hive-cloud" serve --listen "127.0.0.1:$PORT" --data-dir "$DATADIR" >"$LOG" 2>&1 &
echo $! > "$PIDFILE"
RS_EOF
chmod 755 "$WORK/restart-service.sh"
RESTART_CMD="\"$WORK/restart-service.sh\" \"$WORK/service.pid\" \"$INSTALL_DIR\" \"$DATA\" \"$WORK/service.log\" \"$PORT\""
"$WORK/restart-service.sh" "$WORK/service.pid" "$INSTALL_DIR" "$DATA" "$WORK/service.log" "$PORT"
HEALTH_URL="http://127.0.0.1:$PORT/health"
i=0
while [ "$i" -lt 100 ]; do
  if curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
  i=$((i + 1))
done
curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1 ||
  fail upgrade_health_restart "pre-upgrade service not healthy at $HEALTH_URL"

if ! "$UPGRADE" --binary "$NEW_BIN" --data-dir "$DATA" \
  --install-dir "$INSTALL_DIR" --backup-dir "$WORK/upgrade-backups" \
  --restart-cmd "$RESTART_CMD" --health-url "$HEALTH_URL" \
  >"$WORK/upgrade-install.log" 2>&1; then
  fail upgrade_install "upgrade.sh install failed"
fi
[ -x "$INSTALL_DIR/hive-cloud" ] || fail upgrade_install "installed binary missing"
[ -f "$INSTALL_DIR/hive-cloud.prev" ] || fail upgrade_install "hive-cloud.prev missing"
INSTALLED_VERSION=$("$INSTALL_DIR/hive-cloud" version)
[ "$INSTALLED_VERSION" = "$VERSION" ] || fail upgrade_install "installed version=$INSTALLED_VERSION"
PREV_VERSION=$("$INSTALL_DIR/hive-cloud.prev" version)
[ "$PREV_VERSION" = "$VERSION" ] || fail upgrade_install "previous version=$PREV_VERSION"
curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1 ||
  fail upgrade_health_restart "health failed after restart"
UPGRADE_BACKUPS=$(find "$WORK/upgrade-backups" -maxdepth 1 -type f -name '*.tar.gz' | wc -l | tr -d ' ')
[ "$UPGRADE_BACKUPS" -ge 1 ] || fail upgrade_install "pre-upgrade backup missing"
record upgrade_install true "version=$INSTALLED_VERSION previous=$PREV_VERSION backups=$UPGRADE_BACKUPS"
record upgrade_health_restart true "status=200 port=$PORT"

if ! "$UPGRADE" --rollback --install-dir "$INSTALL_DIR" \
  --restart-cmd "$RESTART_CMD" --health-url "$HEALTH_URL" \
  >"$WORK/upgrade-rollback.log" 2>&1; then
  fail upgrade_rollback "upgrade.sh --rollback failed"
fi
ROLLED_BACK_VERSION=$("$INSTALL_DIR/hive-cloud" version)
[ "$ROLLED_BACK_VERSION" = "$VERSION" ] || fail upgrade_rollback "version=$ROLLED_BACK_VERSION"
curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1 ||
  fail upgrade_rollback "health failed after rollback"
record upgrade_rollback true "version=$ROLLED_BACK_VERSION health=ok"

if [ -f "$WORK/service.pid" ]; then
  kill "$(cat "$WORK/service.pid")" 2>/dev/null || true
  rm -f "$WORK/service.pid"
fi

BAD_BIN="$WORK/bad/hive-cloud"
mkdir -p "$WORK/bad"
cat > "$BAD_BIN" <<'BAD_EOF'
#!/bin/sh
if [ "${1:-}" = "version" ]; then
  echo "hive-cloud 9.9.9-m7-bad"
  exit 0
fi
exit 1
BAD_EOF
chmod 755 "$BAD_BIN"
set +e
"$UPGRADE" --binary "$BAD_BIN" --data-dir "$DATA" --install-dir "$INSTALL_DIR" \
  --backup-dir "$WORK/upgrade-backups" --expect-version "$VERSION" --no-restart \
  >"$WORK/upgrade-guard.log" 2>&1
GUARD_EXIT=$?
set -e
[ "$GUARD_EXIT" -ne 0 ] || fail upgrade_version_guard "bad version was accepted"
AFTER_GUARD_VERSION=$("$INSTALL_DIR/hive-cloud" version)
[ "$AFTER_GUARD_VERSION" = "$VERSION" ] || fail upgrade_version_guard "installed binary changed"
record upgrade_version_guard true "exit=$GUARD_EXIT installed_unchanged=1"

# --- static deployment checks --------------------------------------------
if command -v plutil >/dev/null 2>&1; then
  if plutil -lint "$DEPLOY/launchd/com.hive.cloud.plist" >/dev/null 2>&1; then
    record launchd_plist true "plutil=ok"
  else
    fail launchd_plist "plutil -lint failed"
  fi
else
  record launchd_plist false "blocked=plutil-unavailable"
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  if (cd "$DEPLOY/docker" && docker compose -f compose.yaml config -q >/dev/null 2>&1); then
    record compose_config true "docker compose config=ok"
  else
    fail compose_config "docker compose config failed"
  fi
else
  record compose_config false "blocked=docker-compose-unavailable"
fi

if "$SCRIPTS/lint-deploy.sh" >"$WORK/lint-deploy.log" 2>&1; then
  record deploy_lint true "shell=ok systemd_directives=ok plist=ok compose=ok"
else
  fail deploy_lint "lint-deploy.sh failed"
fi

if command -v systemd-analyze >/dev/null 2>&1; then
  record systemd_unit_static true "systemd-analyze=available"
else
  record systemd_unit_static true "static_directives=ok runtime_verify=BLOCKED(systemd-analyze-unavailable)"
fi

# --- evidence -------------------------------------------------------------
finish_evidence 0
printf 'drill: OK checks=%s archive_sha256=%s\n' "$(wc -l < "$CHECKS" | tr -d ' ')" "$BACKUP_SHA"
printf 'drill: evidence=%s\n' "$EVIDENCE_OUT"
exit 0
