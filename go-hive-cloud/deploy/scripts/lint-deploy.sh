#!/usr/bin/env bash
# Static checks for the go-hive-cloud deployment artifacts.
#
# Verifies shell syntax (bash -n and sh -n), systemd unit directives,
# launchd plist syntax/keys, and `docker compose config` when Docker is
# available. It does not start any service and does not need root.
#
# If Docker is unavailable the compose check prints BLOCKED and the script
# exits 2, so CI cannot mistake an unverified compose file for a pass.
set -u

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
DEPLOY=$(CDPATH= cd -- "$HERE/.." && pwd)
FAIL=0

ok() { printf 'lint-deploy: ok %s\n' "$*"; }
bad() {
  printf 'lint-deploy: FAIL %s\n' "$*" >&2
  FAIL=1
}
skip() { printf 'lint-deploy: SKIP %s\n' "$*"; }

# --- shell syntax ---------------------------------------------------------
for script in "$HERE"/*.sh "$DEPLOY/launchd/hive-cloud-run.example.sh"; do
  [ -f "$script" ] || continue
  if bash -n "$script"; then
    ok "bash -n $(basename "$script")"
  else
    bad "bash -n $(basename "$script")"
  fi
  if sh -n "$script"; then
    ok "sh -n $(basename "$script")"
  else
    bad "sh -n $(basename "$script")"
  fi
done

# --- systemd unit ---------------------------------------------------------
UNIT="$DEPLOY/systemd/hive-cloud.service"
if [ -f "$UNIT" ]; then
  for directive in \
    'User=hive-cloud' \
    'Group=hive-cloud' \
    'EnvironmentFile=/etc/hive-cloud/hive-cloud.env' \
    'ExecStart=/usr/local/bin/hive-cloud serve' \
    'Restart=on-failure' \
    'NoNewPrivileges=true' \
    'PrivateTmp=true' \
    'ProtectSystem=strict' \
    'ProtectHome=true' \
    'ReadWritePaths=/var/lib/hive-cloud' \
    'UMask=0077'; do
    if grep -Fq "$directive" "$UNIT"; then
      ok "systemd: $directive"
    else
      bad "systemd: missing $directive"
    fi
  done
  if [ -x /usr/bin/systemd-analyze ] || command -v systemd-analyze >/dev/null 2>&1; then
    systemd-analyze verify "$UNIT" && ok "systemd-analyze verify" || bad "systemd-analyze verify"
  else
    skip "systemd-analyze not available on this host (static directives checked only)"
  fi
else
  bad "missing systemd unit: $UNIT"
fi

# --- launchd plist --------------------------------------------------------
PLIST="$DEPLOY/launchd/com.hive.cloud.plist"
if [ -f "$PLIST" ]; then
  if command -v plutil >/dev/null 2>&1; then
    if plutil -lint "$PLIST" >/dev/null; then
      ok "plutil -lint com.hive.cloud.plist"
    else
      bad "plutil -lint com.hive.cloud.plist"
    fi
    label=$(plutil -extract Label raw -o - "$PLIST" 2>/dev/null || true)
    [ "$label" = com.hive.cloud ] && ok "launchd Label=com.hive.cloud" || bad "launchd Label mismatch: $label"
    user=$(plutil -extract UserName raw -o - "$PLIST" 2>/dev/null || true)
    [ "$user" = hivecloud ] && ok "launchd UserName=hivecloud" || bad "launchd UserName mismatch: $user"
    keepalive=$(plutil -extract KeepAlive raw -o - "$PLIST" 2>/dev/null || true)
    [ "$keepalive" = true ] && ok "launchd KeepAlive=true" || bad "launchd KeepAlive missing"
  else
    skip "plutil not available (non-macOS host)"
  fi
else
  bad "missing launchd plist: $PLIST"
fi

# --- docker compose -------------------------------------------------------
COMPOSE="$DEPLOY/docker/compose.yaml"
if [ -f "$COMPOSE" ]; then
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    if (cd "$DEPLOY/docker" && docker compose -f compose.yaml config -q); then
      ok "docker compose config"
    else
      bad "docker compose config"
    fi
  else
    skip "BLOCKED: docker compose not available; compose file not validated"
    FAIL=2
  fi
else
  bad "missing compose file: $COMPOSE"
fi

if [ "$FAIL" -eq 0 ]; then
  printf 'lint-deploy: PASS\n'
  exit 0
fi
printf 'lint-deploy: FAILED (status=%s)\n' "$FAIL" >&2
exit 2
