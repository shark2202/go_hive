#!/usr/bin/env bash
# Build the R2 distribution: six cross-compiled archives + checksums.
#
# Default product is the UI-embedded binary (hive-embed): the dashboard is
# staged by scripts/embed_ui.sh and compiled into ./cmd/hive with
# -tags embed_ui. When that build is impossible (no frontend dist, no npm,
# npm/tsc failure) the script falls back to plain binaries and says so
# loudly: those binaries can run the API/CLI but serve no built-in dashboard.
#
# Archives (dist/release/):
#   hive-<version>-<os>-<arch>.tar.gz   darwin/linux
#   hive-<version>-<os>-<arch>.zip      windows
#   SHA256SUMS                          hashes of the archives above
# Each archive contains, at its root:
#   hive[.exe] LICENSE NOTICE README.md SHA256SUMS
#
# Only system tools are used: tar, zip, shasum/sha256sum, go.
#
# Usage:
#   scripts/release.sh [--no-embed]
#
# Environment:
#   VERSION=...            release version (default: internal/version.Version)
#   DIST=...               dist root (default: <go-hive>/dist)
#   RELEASE_DIR=...        output directory (default: $DIST/release)
#   PLATFORMS="..."        override the Makefile PLATFORMS list
#   EMBED=0                same as --no-embed
#   GO=...                 go binary (default: go)
#   FRONTEND_DIST=...      prebuilt dashboard directory (passed to embed_ui.sh)
#   SKIP_FRONTEND_BUILD=1  never run npm; require FRONTEND_DIST to exist
set -euo pipefail

export COPYFILE_DISABLE=1

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go_hive_root="$(cd "$script_dir/.." && pwd)"
module_dir="$go_hive_root/go-hive"
repo_root="$(cd "$go_hive_root/.." && pwd)"

log() { printf 'release: %s\n' "$*"; }
warn() { printf 'release: WARNING: %s\n' "$*" >&2; }
die() { printf 'release: ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
	cat <<'EOF'
Build Hive release archives for the six Makefile PLATFORMS.

Usage:
  scripts/release.sh [--no-embed]

Archives go to <dist>/release/:
  hive-<version>-<os>-<arch>.tar.gz   darwin/linux
  hive-<version>-<os>-<arch>.zip      windows
  SHA256SUMS

Each archive contains hive[.exe], LICENSE, NOTICE, README.md and SHA256SUMS.
The default binary embeds the dashboard (hive-embed); if the frontend build
is unavailable the script falls back to plain binaries with a warning.

Environment:
  VERSION, DIST, RELEASE_DIR, PLATFORMS, EMBED=0, GO,
  FRONTEND_DIST, SKIP_FRONTEND_BUILD=1
EOF
}

no_embed=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		--no-embed) no_embed=1; shift ;;
		--help|-h) usage; exit 0 ;;
		*) echo "release: unknown argument: $1" >&2; exit 2 ;;
	esac
done

GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || die "go binary '$GO' not found"

version="${VERSION:-}"
if [ -z "$version" ]; then
	version_file="$module_dir/internal/version/version.go"
	[ -f "$version_file" ] || die "version file not found: $version_file"
	version="$(sed -n 's/^const Version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' "$version_file" | head -n 1)"
fi
version="${version#v}"
[ -n "$version" ] || die "could not determine release version"

if [ -n "${PLATFORMS:-}" ]; then
	platforms="$PLATFORMS"
else
	platforms="$(sed -n 's/^PLATFORMS[[:space:]]*=[[:space:]]*//p' "$module_dir/Makefile" | head -n 1)"
fi
[ -n "$platforms" ] || die "PLATFORMS not found in $module_dir/Makefile"

dist_dir="${DIST:-$module_dir/dist}"
release_dir="${RELEASE_DIR:-$dist_dir/release}"

case "$release_dir" in
	/*) ;;
	*) release_dir="$PWD/$release_dir" ;;
esac
case "$dist_dir" in
	/*) ;;
	*) dist_dir="$PWD/$dist_dir" ;;
esac
case "$release_dir" in
	""|"/"|"$HOME"|"$HOME/") die "refusing to wipe unsafe RELEASE_DIR: '$release_dir'" ;;
esac

# License and README may live in the module directory (once W5-b lands) or at
# the repository root. NOTICE is optional here: when absent we generate a
# minimal one so every archive still carries the attribution required by
# Apache-2.0. The licensing lane can replace it with a fuller file any time.
license=""
for candidate in "$module_dir/LICENSE" "$go_hive_root/LICENSE" "$repo_root/LICENSE"; do
	if [ -f "$candidate" ]; then license="$candidate"; break; fi
done
[ -n "$license" ] || die "LICENSE not found (looked in $module_dir, $go_hive_root, $repo_root)"

readme=""
for candidate in "$module_dir/README.md" "$go_hive_root/README.md" "$repo_root/README.md"; do
	if [ -f "$candidate" ]; then readme="$candidate"; break; fi
done
[ -n "$readme" ] || die "README.md not found"

notice=""
for candidate in "$module_dir/NOTICE" "$go_hive_root/NOTICE" "$repo_root/NOTICE"; do
	if [ -f "$candidate" ]; then notice="$candidate"; break; fi
done

need_zip=0
for platform in $platforms; do
	case "$platform" in windows/*) need_zip=1 ;; esac
done
if [ "$need_zip" = 1 ]; then
	command -v zip >/dev/null 2>&1 || die "zip is required to package windows archives"
fi

sha256_write() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$@"
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		die "neither shasum nor sha256sum is available"
	fi
}

sha256_check() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$1"
	else
		sha256sum -c "$1"
	fi
}

mkdir -p "$dist_dir"
rm -rf "$release_dir"
mkdir -p "$release_dir"

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/hive-release.XXXXXX")"
trap 'rm -rf "$work_dir"' EXIT INT TERM

log "version: $version"
log "platforms: $platforms"
log "output: $release_dir"

# --- default product: UI-embedded binary -----------------------------------
embed=1
if [ "$no_embed" = 1 ] || [ "${EMBED:-1}" = "0" ]; then
	embed=0
	warn "embedded UI disabled (--no-embed/EMBED=0); packaging plain binaries"
else
	log "building hive-embed (dashboard embedded; npm build may run once)"
	if OUT="$dist_dir/hive-embed" GOPROXY=off GOSUMDB=off bash "$script_dir/embed_ui.sh"; then
		[ -f "$dist_dir/hive-embed" ] || die "embed_ui.sh succeeded but $dist_dir/hive-embed is missing"
		log "hive-embed ready: $dist_dir/hive-embed"
	else
		embed=0
		warn "could not build the embedded-UI binary (missing frontend dist or npm/tsc failure)"
		warn "FALLING BACK to plain binaries: the dashboard is NOT embedded and"
		warn "must be served from disk (FrontendDistDir); set FRONTEND_DIST to a"
		warn "prebuilt dashboard to produce embed builds in offline environments."
	fi
fi

# --- cross compile ----------------------------------------------------------
build_platform() {
	os="$1"
	arch="$2"
	bin_name="hive"
	[ "$os" = "windows" ] && bin_name="hive.exe"
	out_dir="$work_dir/build-$os-$arch"
	mkdir -p "$out_dir"

	if [ "$embed" = 1 ]; then
		log "building $os/$arch (embed_ui)"
		(
			cd "$module_dir"
			CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
				"$GO" build -trimpath -ldflags "-s -w" -tags embed_ui -o "$out_dir/$bin_name" ./cmd/hive
		)
	else
		log "building $os/$arch (plain)"
		(
			cd "$module_dir"
			CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
				"$GO" build -trimpath -ldflags "-s -w" -o "$out_dir/$bin_name" ./cmd/hive
		)
	fi
}

# --- package ----------------------------------------------------------------
package_platform() {
	os="$1"
	arch="$2"
	bin_name="hive"
	[ "$os" = "windows" ] && bin_name="hive.exe"
	build_dir="$work_dir/build-$os-$arch"
	pkg_dir="$work_dir/pkg-$os-$arch"
	mkdir -p "$pkg_dir"

	cp "$build_dir/$bin_name" "$pkg_dir/$bin_name"
	cp "$license" "$pkg_dir/LICENSE"
	cp "$readme" "$pkg_dir/README.md"
	if [ -n "$notice" ]; then
		cp "$notice" "$pkg_dir/NOTICE"
	else
		cat > "$pkg_dir/NOTICE" <<'EOF'
Hive
Copyright 2024 Aden

This product includes software developed by Aden
(https://github.com/aden-hive/hive).

Hive is licensed under the Apache License, Version 2.0. See LICENSE for the
full license text.
EOF
	fi

	(
		cd "$pkg_dir"
		sha256_write "$bin_name" LICENSE NOTICE README.md > SHA256SUMS
	)
	if ! ( cd "$pkg_dir" && sha256_check SHA256SUMS >/dev/null ); then
		die "internal SHA256SUMS verification failed for $os/$arch"
	fi

	if [ "$os" = "windows" ]; then
		archive_name="hive-$version-$os-$arch.zip"
		( cd "$pkg_dir" && zip -q -X "$release_dir/$archive_name" "$bin_name" LICENSE NOTICE README.md SHA256SUMS )
	else
		archive_name="hive-$version-$os-$arch.tar.gz"
		tar -czf "$release_dir/$archive_name" -C "$pkg_dir" "$bin_name" LICENSE NOTICE README.md SHA256SUMS
	fi
	[ -s "$release_dir/$archive_name" ] || die "archive not created: $archive_name"
	printf '%s\n' "$archive_name" >> "$work_dir/archives.txt"
	log "packaged $archive_name"
}

: > "$work_dir/archives.txt"
for platform in $platforms; do
	case "$platform" in
		*/*) ;;
		*) die "bad PLATFORMS entry: $platform (want os/arch)" ;;
	esac
	os="${platform%/*}"
	arch="${platform#*/}"
	build_platform "$os" "$arch"
	package_platform "$os" "$arch"
done

# --- release-wide checksums -------------------------------------------------
(
	cd "$release_dir"
	while IFS= read -r archive_name; do
		sha256_write "$archive_name"
	done < "$work_dir/archives.txt"
) | LC_ALL=C sort -k2 > "$release_dir/SHA256SUMS"

if ! ( cd "$release_dir" && sha256_check SHA256SUMS >/dev/null ); then
	die "SHA256SUMS verification failed"
fi

log "verified SHA256SUMS"
log "release artifacts:"
while IFS= read -r archive_name; do
	size="$(wc -c < "$release_dir/$archive_name" | tr -d ' ')"
	printf '  %-52s %12s bytes\n' "$archive_name" "$size"
done < "$work_dir/archives.txt"
printf '  %-52s\n' "SHA256SUMS"
printf '\n'
log "install locally with: $go_hive_root/install.sh --from \"$release_dir\""
