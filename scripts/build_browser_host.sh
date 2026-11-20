#!/usr/bin/env bash
#
# Build the browser-host binaries — the `make browserbridge` equivalent.
#
# The Makefile is intentionally NOT modified for this lane (its targets are the
# frozen CI contract), so this script is the supported way to produce the three
# binaries that make up the browser host:
#
#   hive            (go_hive/go-hive)          supervise + serve the bridge
#   hive-browser    (go_hive/go-hive)          CLI: one POST /command per verb
#   browserbridge   (go_hive/browserbridge)    WS 14829 / HTTP 14830 / legacy 9229
#
# Same toolchain discipline as the Makefile: CGO_ENABLED=0, GOPROXY=off, six
# platforms. Output: <go-hive>/dist/browser-host/<os>-<arch>/{hive,hive-browser,browserbridge}[.exe]
#
# Usage:
#   scripts/build_browser_host.sh [-h] [os/arch ...]
#     (no arguments = all six Makefile platforms)
#
# Environment:
#   DIST=...       output root (default: <go-hive>/dist)
#   GO=...         go binary (default: go)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODULE_DIR="$GO_HIVE_ROOT/go-hive"
BRIDGE_MODULE_DIR="$GO_HIVE_ROOT/browserbridge"

GO="${GO:-go}"
DIST="${DIST:-$MODULE_DIR/dist}"
OFFLINE=(env CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod)

usage() {
	cat <<'EOF'
Build hive, hive-browser and browserbridge for the six release platforms.

Usage:
  scripts/build_browser_host.sh [os/arch ...]

Arguments:
  os/arch            e.g. darwin/arm64 (default: the Makefile PLATFORMS list)

Environment:
  DIST, GO
EOF
}

case "${1:-}" in
	-h|--help) usage; exit 0 ;;
esac

platforms=("$@")
if [ "${#platforms[@]}" -eq 0 ]; then
	read -r -a platforms <<< "$(sed -n 's/^PLATFORMS[[:space:]]*=[[:space:]]*//p' "$MODULE_DIR/Makefile" | head -n 1)"
fi
[ "${#platforms[@]}" -gt 0 ] || { echo "build_browser_host: no platforms" >&2; exit 2; }

command -v "$GO" >/dev/null 2>&1 || { echo "build_browser_host: go binary '$GO' not found" >&2; exit 2; }

for platform in "${platforms[@]}"; do
	os="${platform%/*}"
	arch="${platform#*/}"
	if [ "$os" = "$platform" ] || [ -z "$arch" ]; then
		echo "build_browser_host: '$platform' is not os/arch" >&2
		exit 2
	fi
	ext=""
	[ "$os" = "windows" ] && ext=".exe"
	out_dir="$DIST/browser-host/$os-$arch"
	mkdir -p "$out_dir"
	printf '== %s ==\n' "$platform"
	( cd "$MODULE_DIR" && "${OFFLINE[@]}" GOOS="$os" GOARCH="$arch" \
		"$GO" build -trimpath -ldflags "-s -w" -o "$out_dir/hive$ext" ./cmd/hive )
	( cd "$MODULE_DIR" && "${OFFLINE[@]}" GOOS="$os" GOARCH="$arch" \
		"$GO" build -trimpath -ldflags "-s -w" -o "$out_dir/hive-browser$ext" ./cmd/hive-browser )
	( cd "$BRIDGE_MODULE_DIR" && "${OFFLINE[@]}" GOOS="$os" GOARCH="$arch" \
		"$GO" build -trimpath -ldflags "-s -w" -o "$out_dir/browserbridge$ext" ./cmd/browserbridge )
	printf '  -> %s\n' "$out_dir"
done

cat <<EOF

Built into $DIST/browser-host/.
Install layout: keep hive, hive-browser and browserbridge in ONE directory —
the serve-side supervisor resolves browserbridge next to the hive binary (or
via \$HIVE_BROWSERBRIDGE_BIN / \$PATH). See go_hive/docs/browser-host.md.
EOF
