#!/usr/bin/env bash
# Build a single Go binary that carries the dashboard.
#
# go:embed cannot reference files outside its package directory, so this
# script stages the frontend build at
# go_hive/go-hive/internal/server/uiembed/dist and then compiles ./cmd/hive
# with -tags embed_ui. Both the staged tree and core/frontend/dist are
# gitignored build outputs.
#
# Usage (from go_hive/go-hive or the repository root):
#   go_hive/scripts/embed_ui.sh          build frontend if needed, stage, build
#   make -C go_hive/go-hive embed-ui     same, through the Makefile
#
# Environment:
#   FRONTEND_DIR          frontend source dir (default: <repo>/core/frontend)
#   FRONTEND_DIST         prebuilt dashboard dir (default: $FRONTEND_DIR/dist)
#   SKIP_FRONTEND_BUILD=1 fail instead of running npm when dist/index.html is missing
#   OUT                   output binary (default: go_hive/go-hive/dist/hive-embed)
#
# Plain `go build ./...` (no tag) is unchanged: it never reads the staged tree
# and the server keeps discovering the dashboard from disk via FrontendDistDir.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go_hive_dir="$(cd "$script_dir/../go-hive" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

frontend_dir="${FRONTEND_DIR:-$repo_root/core/frontend}"
frontend_dist="${FRONTEND_DIST:-$frontend_dir/dist}"
stage_dir="$go_hive_dir/internal/server/uiembed/dist"
out="${OUT:-$go_hive_dir/dist/hive-embed}"
go_bin="${GO:-go}"
npm_bin="${NPM:-npm}"

if [ ! -f "$frontend_dist/index.html" ]; then
	if [ "${SKIP_FRONTEND_BUILD:-0}" = "1" ]; then
		echo "embed-ui: no index.html under $frontend_dist (SKIP_FRONTEND_BUILD=1)" >&2
		exit 1
	fi
	if [ ! -f "$frontend_dir/package.json" ]; then
		echo "embed-ui: frontend sources not found at $frontend_dir" >&2
		echo "embed-ui: set FRONTEND_DIST to a built dashboard or run from a full checkout" >&2
		exit 1
	fi
	echo "embed-ui: building frontend in $frontend_dir"
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

mkdir -p "$(dirname "$out")"
echo "embed-ui: building $out (-tags embed_ui)"
(
	cd "$go_hive_dir"
	CGO_ENABLED=0 GOPROXY="${GOPROXY:-off}" GOSUMDB="${GOSUMDB:-off}" GOFLAGS="${GOFLAGS:--mod=mod}" \
		"$go_bin" build -tags embed_ui -o "$out" ./cmd/hive
)
echo "embed-ui: wrote $out"
