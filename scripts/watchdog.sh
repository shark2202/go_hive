#!/usr/bin/env bash
# Watchdog status snapshot for the Go port of Hive.
#
# Prints a compact, human-readable status block used by the periodic
# watchdog report (every 5 minutes) and by manual "proactive" checks.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MODULE="$ROOT/go_hive/go-hive"
GOVERNANCE="$ROOT/go_hive/.time_governance.conf"

echo "=== TIME ==="
date -u +"%Y-%m-%dT%H:%M:%SZ"

echo "=== TIME GOVERNANCE ==="
if [ -f "$GOVERNANCE" ]; then
  echo "governance: $GOVERNANCE"
  sed 's/^/  /' "$GOVERNANCE"
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$GOVERNANCE" <<'PY'
import sys
from datetime import datetime, timezone

vals = {}
for line in open(sys.argv[1], encoding="utf-8"):
    line = line.strip()
    if not line or line.startswith("#") or "=" not in line:
        continue
    key, value = line.split("=", 1)
    vals[key.strip()] = value.strip()

def parse(name):
    raw = vals.get(name, "")
    if not raw:
        return None
    try:
        return datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None

start = parse("started_at")
delivery = parse("expected_delivery")
now = datetime.now(timezone.utc)
if start and delivery:
    total = (delivery - start).total_seconds()
    elapsed = (now - start).total_seconds()
    remaining = (delivery - now).total_seconds()
    pct = (elapsed / total * 100.0) if total > 0 else 0.0
    if remaining < 0:
        status = "OVERDUE"
    elif total > 0 and remaining < total * 0.2:
        status = "AT_RISK"
    else:
        status = "ON_TRACK"
    print(f"  elapsed_hours={elapsed / 3600.0:.1f}")
    print(f"  remaining_hours={remaining / 3600.0:.1f}")
    print(f"  time_used_pct={pct:.1f}")
    print(f"  schedule={status}")
else:
    print("  schedule=UNBOUNDED (set started_at and expected_delivery)")
PY
  else
    echo "  schedule=UNKNOWN (python3 unavailable)"
  fi
else
  echo "  governance file missing: $GOVERNANCE"
  echo "  schedule=UNBOUNDED"
fi

echo "=== GIT ==="
git -C "$ROOT" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "no-git"
git -C "$ROOT" log --oneline -5 2>/dev/null || true
echo "--- dirty files ---"
git -C "$ROOT" status --porcelain 2>/dev/null | head -60

echo "=== GO MODULE ==="
if [ -d "$MODULE" ]; then
  ( cd "$MODULE" || exit 1
    echo "packages: $(go list ./... 2>/dev/null | wc -l | tr -d ' ')"
    echo "go files: $(find . -name '*.go' | wc -l | tr -d ' ')"
    echo "go lines: $(find . -name '*.go' -print0 | xargs -0 wc -l 2>/dev/null | tail -1 | awk '{print $1}')"
    echo "--- go vet ---"
    go vet ./... 2>&1 | head -20
    echo "vet-exit: $?"
    echo "--- go build (CGO off) ---"
    CGO_ENABLED=0 go build ./... 2>&1 | head -20
    echo "build-exit: $?"
    echo "--- go test ---"
    CGO_ENABLED=0 go test ./... 2>&1 | tail -40
    echo "test-exit: $?"
  )
else
  echo "module missing: $MODULE"
fi

echo "=== DOCS ==="
find "$ROOT/go_hive" -maxdepth 1 -name '*.md' -print 2>/dev/null | sort
echo "=== END ==="
