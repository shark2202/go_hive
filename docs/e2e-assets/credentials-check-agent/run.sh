#!/usr/bin/env bash
# Recomputable evidence for /api/credentials/check-agent + /api/credentials/resync.
#
# Usage:
#   bash run.sh [PORT]      # default 8798; expects a running `hive serve` whose
#                           # HIVE_HOME contains colonies/cred_demo (worker.json
#                           # declares ["github_create_issue"]) and GITHUB_TOKEN
#                           # set to a fake value in the server's environment.
#
# Each case prints "==> <name> <status>" and writes <name>.body.json next to
# this script. Cases mirror the CredentialsModal's real call
# (frontend/src/api/credentials.ts checkAgent: POST {agent_path}) plus the
# resync call (ToolsEditor / credentials page: POST {}).
set -uo pipefail

PORT="${1:-8798}"
BASE="http://127.0.0.1:${PORT}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOME_DIR="${HIVE_HOME_E2E:?set the HIVE_HOME_E2E env var to the server home}"
AGENT_PATH="$HOME_DIR/colonies/cred_demo"
GHOST_PATH="$HOME_DIR/colonies/ghost"

post() {
  local name="$1" path="$2" payload="$3"
  local out
  out=$(curl -sS -o "$HERE/$name.body.json" -w '%{http_code}' \
    -X POST "$BASE$path" -H 'Content-Type: application/json' -d "$payload")
  echo "==> $name $out"
}

post modal_default          /api/credentials/check-agent "{\"agent_path\":\"$AGENT_PATH\"}"
post modal_verify_false     /api/credentials/check-agent "{\"agent_path\":\"$AGENT_PATH\",\"verify\":false}"
post unknown_agent          /api/credentials/check-agent "{\"agent_path\":\"$GHOST_PATH\",\"verify\":false}"
post missing_agent_path     /api/credentials/check-agent "{}"
post traversal_rejected     /api/credentials/check-agent "{\"agent_path\":\"exports/../../etc/passwd\"}"
post malformed_body         /api/credentials/check-agent "not json"
post resync_without_aden    /api/credentials/resync "{}"

# Save an Aden key through the API, then resync: the store-backed key makes the
# handler take the success path with the fresh account snapshot.
post save_aden_key          /api/credentials "{\"credential_id\":\"aden_api_key\",\"keys\":{\"api_key\":\"aden-fake-e2e\"}}"
post resync_with_aden       /api/credentials/resync "{}"
