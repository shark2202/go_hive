#!/usr/bin/env bash
# Thin wrapper -> ui_e2e_gate.py (the real gate). Usage:
#   ./ui-e2e-gate.sh <colony_disk_slug> [base_url]
set -uo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec /usr/bin/python3 "$DIR/ui_e2e_gate.py" "$@"
