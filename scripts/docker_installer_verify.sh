#!/usr/bin/env bash
# Host-side driver for the linux/arm64 real-run verification.
#
# Picks the linux/<arch> archive from a release directory and runs
# scripts/docker_installer_probe.sh inside a linux container with the
# release directory mounted read-only. The container transcript (commands,
# outputs, exit codes) is written to <out-dir>/docker-linux-arm64.log on the
# host via the /evidence mount; stdout is the same transcript.
#
# Requires docker on the host. On an Apple-silicon Mac linux/arm64 is native;
# on an amd64 host docker emulates it (slower, still a real execution).
#
# Usage:
#   scripts/docker_installer_verify.sh [release-dir]
#
# Environment:
#   IMAGE     container image (default: debian:stable-slim)
#   ARCH      target linux arch (default: arm64)
#   OUT_DIR   evidence dir (default: <go_hive>/docs/e2e-assets/installer-verify/<yyyymmdd>)
#   PLATFORM  docker platform (default: linux/$ARCH)
#   PORT      serve port inside the container (default: 18787)
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go_hive_root="$(cd "$script_dir/.." && pwd)"
module_dir="$go_hive_root/go-hive"
release_dir="${1:-${RELEASE_DIR:-$module_dir/dist/release}}"
image="${IMAGE:-debian:stable-slim}"
arch="${ARCH:-arm64}"
platform="${PLATFORM:-linux/$arch}"
port="${PORT:-18787}"
out_dir="${OUT_DIR:-$go_hive_root/docs/e2e-assets/installer-verify/$(date +%Y%m%d)}"

command -v docker >/dev/null 2>&1 || { echo "docker not available" >&2; exit 2; }

version="$(sed -n 's/^const Version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' \
	"$module_dir/internal/version/version.go" | head -n 1)"
[ -n "$version" ] || { echo "cannot determine version" >&2; exit 2; }

archive="$(ls -1 "$release_dir"/hive-*-linux-"$arch".tar.gz 2>/dev/null | tail -1)"
[ -n "$archive" ] || { echo "no hive-*-linux-$arch.tar.gz under $release_dir (run scripts/release.sh)" >&2; exit 2; }
archive_name="$(basename "$archive")"

mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
log_path="$out_dir/docker-linux-$arch.log.txt"

echo "== docker installer verify =="
echo "image    : $image ($platform)"
echo "archive  : $archive"
echo "evidence : $log_path"
echo ""

set +e
docker run --rm --platform "$platform" \
	-v "$release_dir:/release:ro" \
	-v "$script_dir/docker_installer_probe.sh:/probe.sh:ro" \
	-v "$out_dir:/evidence" \
	-e "ARCHIVE_NAME=$archive_name" \
	-e "EXPECT_VERSION=$version" \
	-e "PORT=$port" \
	-e "OUT=/evidence/docker-linux-$arch.log.txt" \
	"$image" bash /probe.sh
rc=$?
set -e

echo ""
echo "docker exit=$rc"
echo "transcript: $log_path"
exit "$rc"
