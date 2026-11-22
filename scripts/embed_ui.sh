#!/usr/bin/env bash
# Build a single Go binary that carries the dashboard.
#
# go:embed cannot reference files outside its package directory, so this
# script stages the frontend build at
# go_hive/go-hive/internal/server/uiembed/dist and then compiles ./cmd/hive
# with -tags embed_ui. Both the staged tree and the frontend dist/ are
# gitignored build outputs.
#
# Freshness: a prebuilt dist/ is reused only when no frontend source is newer
# than dist/index.html. A stale dist is rebuilt; with SKIP_FRONTEND_BUILD=1 a
# stale dist is refused instead of embedded, because shipping it would hide
# shipped UI behind an older dashboard (the slot binaries before 20261122-16
# embedded a dashboard from before the 1.0.15 memory surface).
#
# Usage (from go_hive/go-hive or the repository root):
#   go_hive/scripts/embed_ui.sh          build frontend if needed, stage, build
#   go_hive/scripts/embed_ui.sh --print-mode
#                                        print needs-build | up-to-date, no build
#   make -C go_hive/go-hive embed-ui     same, through the Makefile
#
# Environment:
#   FRONTEND_DIR          frontend source dir (default: <go_hive>/frontend
#                         when it has package.json, else <repo>/core/frontend)
#   FRONTEND_DIST         prebuilt dashboard dir (default: $FRONTEND_DIR/dist)
#   FORCE_FRONTEND_BUILD=1 always run npm build, even when dist looks current
#   GO_BUILD_FLAGS        extra `go build` flags, e.g. -trimpath
#   SKIP_FRONTEND_BUILD=1 never run npm; fail when dist is missing or stale
#   OUT                   output binary (default: go_hive/go-hive/dist/hive-embed)
#
# Plain `go build ./...` (no tag) is unchanged: it never reads the staged tree
# and the server keeps discovering the dashboard from disk via FrontendDistDir.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go_hive_dir="$(cd "$script_dir/../go-hive" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

print_mode=0
while [ $# -gt 0 ]; do
	case "$1" in
	--print-mode) print_mode=1 ;;
	*)
		echo "embed-ui: unknown argument: $1" >&2
		exit 2
		;;
	esac
	shift
done

# Prefer the vendored frontend so a standalone go_hive checkout can build the
# UI; fall back to the monorepo copy when the vendored tree is absent.
default_frontend_dir="$go_hive_dir/frontend"
if [ ! -f "$default_frontend_dir/package.json" ]; then
	default_frontend_dir="$repo_root/core/frontend"
fi
frontend_dir="${FRONTEND_DIR:-$default_frontend_dir}"
frontend_dist="${FRONTEND_DIST:-$frontend_dir/dist}"
stage_dir="$go_hive_dir/internal/server/uiembed/dist"
out="${OUT:-$go_hive_dir/dist/hive-embed}"
go_bin="${GO:-go}"
npm_bin="${NPM:-npm}"

# Why a frontend build is (not) needed: forced > dist-missing > sources-newer
# > up-to-date. Prints exactly one of those words; exits 0 either way.
frontend_build_reason() {
	if [ "${FORCE_FRONTEND_BUILD:-0}" = "1" ]; then
		echo "forced"
		return 0
	fi
	if [ ! -f "$frontend_dist/index.html" ]; then
		echo "dist-missing"
		return 0
	fi
	local newer
	newer="$(find \
		"$frontend_dir/src" \
		"$frontend_dir/public" \
		"$frontend_dir/index.html" \
		"$frontend_dir/package.json" \
		"$frontend_dir/package-lock.json" \
		"$frontend_dir/vite.config.ts" \
		"$frontend_dir/tsconfig.json" \
		-type f -newer "$frontend_dist/index.html" -print -quit 2>/dev/null || true)"
	if [ -n "$newer" ]; then
		echo "sources-newer"
		return 0
	fi
	echo "up-to-date"
}

build_reason="$(frontend_build_reason)"

if [ "$print_mode" = "1" ]; then
	if [ "$build_reason" = "up-to-date" ]; then
		echo "up-to-date"
	else
		echo "needs-build"
	fi
	exit 0
fi

if [ "$build_reason" = "up-to-date" ]; then
	echo "embed-ui: frontend dist is up to date ($frontend_dist)"
else
	if [ "${SKIP_FRONTEND_BUILD:-0}" = "1" ]; then
		echo "embed-ui: frontend dist is $build_reason under $frontend_dist (SKIP_FRONTEND_BUILD=1)" >&2
		echo "embed-ui: a stale dashboard would be embedded; rebuild the frontend or pass a fresh FRONTEND_DIST" >&2
		exit 1
	fi
	if [ ! -f "$frontend_dir/package.json" ]; then
		echo "embed-ui: frontend sources not found at $frontend_dir" >&2
		echo "embed-ui: set FRONTEND_DIST to a built dashboard or run from a full checkout" >&2
		exit 1
	fi
	echo "embed-ui: building frontend in $frontend_dir ($build_reason)"
	if [ ! -f "$frontend_dir/node_modules/.bin/vite" ] || [ ! -f "$frontend_dir/node_modules/.bin/tsc" ]; then
		if [ -f "$frontend_dir/package-lock.json" ]; then
			(cd "$frontend_dir" && "$npm_bin" ci --include=dev --no-fund --no-audit)
		else
			(cd "$frontend_dir" && "$npm_bin" install --include=dev --no-fund --no-audit)
		fi
	fi
	(cd "$frontend_dir" && "$npm_bin" run build)
fi

if [ ! -f "$frontend_dist/index.html" ]; then
	echo "embed-ui: frontend build produced no index.html under $frontend_dist" >&2
	exit 1
fi

echo "embed-ui: staging $frontend_dist -> $stage_dir"
rm -rf "$stage_dir"
mkdir -p "$stage_dir"
cp -R "$frontend_dist/." "$stage_dir/"

# The staged tree is what the binary embeds: prove it is byte-for-byte the
# dist that was just built (or declared fresh), so a partial copy can't ship.
if ! diff -r "$frontend_dist" "$stage_dir" >/dev/null 2>&1; then
	echo "embed-ui: staged tree does not match $frontend_dist" >&2
	exit 1
fi

mkdir -p "$(dirname "$out")"
echo "embed-ui: building $out (-tags embed_ui ${GO_BUILD_FLAGS:-})"
(
	cd "$go_hive_dir"
	if [ -n "${GO_BUILD_FLAGS:-}" ]; then
		# shellcheck disable=SC2086 — GO_BUILD_FLAGS is a deliberate flag list.
		CGO_ENABLED=0 GOPROXY="${GOPROXY:-off}" GOSUMDB="${GOSUMDB:-off}" GOFLAGS="${GOFLAGS:--mod=mod}" \
			"$go_bin" build $GO_BUILD_FLAGS -tags embed_ui -o "$out" ./cmd/hive
	else
		CGO_ENABLED=0 GOPROXY="${GOPROXY:-off}" GOSUMDB="${GOSUMDB:-off}" GOFLAGS="${GOFLAGS:--mod=mod}" \
			"$go_bin" build -tags embed_ui -o "$out" ./cmd/hive
	fi
)
echo "embed-ui: wrote $out"
