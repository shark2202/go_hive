#!/usr/bin/env bash
# Host-side runner for the Linux UI E2E gate.
#
# Builds an embedded-UI Linux binary for the container's architecture, then runs
# go_hive/scripts/ui_e2e_linux.py inside the already-present Linux QA image.
# No network is used: the Go build is GOPROXY=off, and the frontend dist must
# already exist (it is a gitignored build output).
#
# Usage:
#   go_hive/scripts/ui_e2e_linux.sh
#   go_hive/scripts/ui_e2e_linux.sh --fault dom-empty \
#       --evidence-dir go_hive/docs/e2e-assets/linux-ui/fault \
#       --json-out go_hive/docs/e2e-assets/linux-ui/fault/ui-e2e-linux-fault.json
#
# Wrapper options:
#   --image NAME     container image (default: $HIVE_LINUX_QA_IMAGE or hive-linux-qa:ui)
#   --no-build       reuse go_hive/go-hive/dist/hive-embed-linux-<arch>
#   --help           this text
# Everything else is passed to ui_e2e_linux.py.
#
# Exit: whatever the Python gate returns (0 pass, 1 assertion/public-gate fail,
# 2 environment error), or 2 for a host-side prerequisite failure.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GO_HIVE="$REPO_ROOT/go_hive/go-hive"
IMAGE="${HIVE_LINUX_QA_IMAGE:-hive-linux-qa:ui}"
DO_BUILD=1
PASSTHRU=()

usage() {
  sed -n '4,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --image)
      [ $# -ge 2 ] || { echo "ui_e2e_linux.sh: --image needs a value" >&2; exit 2; }
      IMAGE="$2"; shift 2 ;;
    --no-build)
      DO_BUILD=0; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      PASSTHRU+=("$1"); shift ;;
  esac
done

for tool in docker go; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "ui_e2e_linux.sh: missing host tool: $tool" >&2
    exit 2
  }
done

docker image inspect "$IMAGE" >/dev/null 2>&1 || {
  echo "ui_e2e_linux.sh: docker image '$IMAGE' is not present; load/build it first" >&2
  exit 2
}

IMAGE_ARCH="$(docker image inspect --format '{{.Architecture}}' "$IMAGE")"
case "$IMAGE_ARCH" in
  arm64|aarch64) GOARCH=arm64 ;;
  amd64|x86_64)  GOARCH=amd64 ;;
  *)
    echo "ui_e2e_linux.sh: unsupported image architecture '$IMAGE_ARCH'" >&2
    exit 2
    ;;
esac
BINARY="$GO_HIVE/dist/hive-embed-linux-$GOARCH"

if [ "$DO_BUILD" = "1" ]; then
  STAGE="$GO_HIVE/internal/server/uiembed/dist"
  SRC_DIST="$GO_HIVE/frontend/dist"
  if [ ! -f "$STAGE/index.html" ]; then
    if [ ! -f "$SRC_DIST/index.html" ]; then
      echo "ui_e2e_linux.sh: no frontend build at $SRC_DIST/index.html" >&2
      echo "  build the dashboard offline or point FRONTEND_DIST at a built dist;" >&2
      echo "  this wrapper never downloads npm packages." >&2
      exit 2
    fi
    rm -rf "$STAGE"
    mkdir -p "$STAGE"
    cp -R "$SRC_DIST/." "$STAGE/"
  fi
  echo "ui_e2e_linux.sh: building $GOARCH Linux binary (GOPROXY=off)"
  (
    cd "$GO_HIVE"
    CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" GOPROXY=off GOSUMDB=off \
      go build -tags embed_ui -o "$BINARY" ./cmd/hive
  )
fi

[ -f "$BINARY" ] || {
  echo "ui_e2e_linux.sh: no Linux binary at $BINARY (rerun without --no-build)" >&2
  exit 2
}
if command -v file >/dev/null 2>&1; then
  file "$BINARY" | grep -q 'ELF' || {
    echo "ui_e2e_linux.sh: $BINARY is not an ELF binary" >&2
    exit 2
  }
fi

# Default evidence locations, unless the caller chose their own.
has_json=0
has_log=0
for arg in "${PASSTHRU[@]:-}"; do
  [ "$arg" = "--json-out" ] && has_json=1
  [ "$arg" = "--serve-log-out" ] && has_log=1
done
[ "$has_json" = "1" ] || PASSTHRU+=(--json-out go_hive/docs/e2e-assets/linux-ui/ui-e2e-linux.json)
[ "$has_log" = "1" ] || PASSTHRU+=(--serve-log-out go_hive/docs/e2e-assets/linux-ui/serve.log)

echo "ui_e2e_linux.sh: image=$IMAGE arch=$IMAGE_ARCH binary=$BINARY"
docker run --rm --platform "linux/$GOARCH" \
  -e "HIVE_LINUX_QA_IMAGE=$IMAGE" \
  -v "$REPO_ROOT":/repo -w /repo \
  "$IMAGE" \
  python3 go_hive/scripts/ui_e2e_linux.py --binary "go_hive/go-hive/dist/hive-embed-linux-$GOARCH" \
  "${PASSTHRU[@]}"
