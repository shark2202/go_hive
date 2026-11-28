#!/bin/sh
# launchd wrapper for go-hive-cloud (macOS).
#
# Install as /usr/local/bin/hive-cloud-launchd, mode 0755, root:wheel.
# launchd does not support EnvironmentFile, so this wrapper sources the same
# style env file used by the systemd unit and then execs the server.
#
# The env file must be readable by the service user (hivecloud). Keep it free
# of secret values when possible: hive-cloud creates <data-dir>/jwt_secret and
# <data-dir>/stream_secret itself (0600).
set -eu

ENV_FILE=${HIVE_CLOUD_ENV_FILE:-/usr/local/etc/hive-cloud/hive-cloud.env}
if [ ! -r "$ENV_FILE" ]; then
  echo "hive-cloud-launchd: env file not readable: $ENV_FILE" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

exec /usr/local/bin/hive-cloud serve
