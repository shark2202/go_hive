#!/usr/bin/env bash
# Static + installer acceptance checks for the six release archives.
#
# For every platform in the Makefile PLATFORMS list this script proves, without
# running target-OS binaries:
#   A1 archive exists and its sha256 matches the release-wide SHA256SUMS
#   A2 root members include hive[.exe] hive-cred[.exe] hive-browser[.exe]
#      browserbridge[.exe] LICENSE NOTICE README.md SHA256SUMS
#   A3 the archive-internal SHA256SUMS recomputes byte-for-byte
#   A4 install.sh accepts the archive (--from, HIVE_OS/HIVE_ARCH override),
#      installs all four binaries executable into a temp dir, and its own
#      --version / --help probes run (cross-OS binaries warn, exit 0)
#
# Running the extracted binaries is covered separately: darwin/arm64 by
# scripts/install_verify.sh on macOS, linux/arm64 by
# scripts/docker_installer_verify.sh inside a linux/arm64 container.
# windows/* and linux/amd64 cannot execute on an arm64 macOS host; A1-A4 still
# prove the artifact and installer path, and the report must say so.
#
# Usage:
#   scripts/verify_installer_archives.sh [release-dir]
#
# Environment:
#   VERSION   release version (default: internal/version/version.go)
#   PLATFORMS override the Makefile PLATFORMS list
set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go_hive_root="$(cd "$script_dir/.." && pwd)"
module_dir="$go_hive_root/go-hive"
release_dir="${1:-$module_dir/dist/release}"
install_sh="$go_hive_root/install.sh"

if [ -n "${VERSION:-}" ]; then
	version="$VERSION"
else
	version="$(sed -n 's/^const Version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' \
		"$module_dir/internal/version/version.go" | head -n 1)"
fi
[ -n "$version" ] || { echo "verify: cannot determine version" >&2; exit 2; }

if [ -n "${PLATFORMS:-}" ]; then
	platforms="$PLATFORMS"
else
	platforms="$(sed -n 's/^PLATFORMS[[:space:]]*=[[:space:]]*//p' "$module_dir/Makefile" | head -n 1)"
fi
[ -n "$platforms" ] || { echo "verify: cannot determine PLATFORMS" >&2; exit 2; }

[ -d "$release_dir" ] || { echo "verify: release dir not found: $release_dir (run scripts/release.sh first)" >&2; exit 2; }
[ -f "$release_dir/SHA256SUMS" ] || { echo "verify: $release_dir/SHA256SUMS missing" >&2; exit 2; }
[ -f "$install_sh" ] || { echo "verify: $install_sh missing" >&2; exit 2; }

PASS=0
FAIL=0
ok() { printf '  ok  : %s\n' "$*"; PASS=$((PASS + 1)); }
bad() { printf '  FAIL: %s\n' "$*"; FAIL=$((FAIL + 1)); }

sha256_file() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		sha256sum "$1" | awk '{print $1}'
	fi
}
sha256_check_dir() {
	if command -v shasum >/dev/null 2>&1; then
		(cd "$1" && shasum -a 256 -c SHA256SUMS)
	else
		(cd "$1" && sha256sum -c SHA256SUMS)
	fi
}

work="$(mktemp -d "${TMPDIR:-/tmp}/hive-installer-verify.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM

printf 'release dir : %s\n' "$release_dir"
printf 'version     : %s\n' "$version"
printf 'platforms   : %s\n' "$platforms"
printf 'host        : %s %s\n' "$(uname -s)" "$(uname -m)"
printf '\n'

for platform in $platforms; do
	os="${platform%/*}"
	arch="${platform#*/}"
	ext="tar.gz"
	suffix=""
	if [ "$os" = "windows" ]; then
		ext="zip"
		suffix=".exe"
	fi
	archive_name="hive-$version-$os-$arch.$ext"
	archive="$release_dir/$archive_name"

	printf '== %s/%s ==\n' "$os" "$arch"
	if [ ! -f "$archive" ]; then
		bad "A1 archive missing: $archive_name"
		printf '\n'
		continue
	fi
	bytes="$(wc -c <"$archive" | tr -d ' ')"
	ok "A1 archive present: $archive_name ($bytes bytes)"

	expected="$(awk -v want="$archive_name" '
		{ file = $2; sub(/^\*/, "", file); sub(/^\.\//, "", file)
		  if (file == want) { print $1; exit } }' "$release_dir/SHA256SUMS")"
	actual="$(sha256_file "$archive")"
	if [ -n "$expected" ] && [ "$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')" = "$actual" ]; then
		ok "A1 sha256 == SHA256SUMS: $actual"
	else
		bad "A1 sha256 mismatch: expected '${expected:-<no entry>}' got '$actual'"
	fi

	extract="$work/$os-$arch"
	mkdir -p "$extract"
	if [ "$ext" = "zip" ]; then
		command -v unzip >/dev/null 2>&1 || { bad "A2 unzip unavailable on host"; printf '\n'; continue; }
		members="$(unzip -Z1 "$archive")"
		unzip -q -o "$archive" -d "$extract" || bad "A2 unzip failed"
	else
		members="$(tar -tzf "$archive")"
		tar -xzf "$archive" -C "$extract" || bad "A2 tar extraction failed"
	fi
	missing=""
	for name in "hive$suffix" "hive-cred$suffix" "hive-browser$suffix" "browserbridge$suffix" LICENSE NOTICE README.md SHA256SUMS; do
		printf '%s\n' "$members" | grep -qx "$name" || missing="$missing $name"
	done
	if [ -z "$missing" ]; then
		ok "A2 members: hive$suffix hive-cred$suffix hive-browser$suffix browserbridge$suffix LICENSE NOTICE README.md SHA256SUMS"
	else
		bad "A2 missing members:$missing"
	fi
	for name in "hive$suffix" "hive-cred$suffix" "hive-browser$suffix" "browserbridge$suffix"; do
		if [ -x "$extract/$name" ]; then
			printf '        %-16s %10s bytes sha256=%s\n' "$name" \
				"$(wc -c <"$extract/$name" | tr -d ' ')" "$(sha256_file "$extract/$name")"
		else
			bad "A2 extracted $name missing or not executable"
		fi
	done

	if inner="$(sha256_check_dir "$extract" 2>&1)"; then
		ok "A3 inner SHA256SUMS: $(printf '%s\n' "$inner" | wc -l | tr -d ' ') files OK"
	else
		bad "A3 inner SHA256SUMS failed: $inner"
	fi

	inst="$work/install-$os-$arch"
	mkdir -p "$inst"
	if out="$(HIVE_VERSION="$version" HIVE_OS="$os" HIVE_ARCH="$arch" \
		HIVE_INSTALL_DIR="$inst" bash "$install_sh" --from "$release_dir" 2>&1)"; then
		ok "A4 install.sh exit=0 (SHA256 verified, four binaries installed)"
		printf '%s\n' "$out" | sed 's/^/        /'
	else
		bad "A4 install.sh failed:"
		printf '%s\n' "$out" | sed 's/^/        /'
	fi
	installed_missing=""
	for name in "hive$suffix" "hive-cred$suffix" "hive-browser$suffix" "browserbridge$suffix"; do
		[ -x "$inst/$name" ] || installed_missing="$installed_missing $name"
	done
	if [ -z "$installed_missing" ]; then
		ok "A4 four binaries executable under $inst"
	else
		bad "A4 not executable after install:$installed_missing"
	fi
	printf '\n'
done

if [ "$FAIL" = 0 ]; then
	printf 'RESULT: PASS (%s checks)\n' "$PASS"
else
	printf 'RESULT: FAIL (%s of %s checks failed)\n' "$FAIL" "$((PASS + FAIL))"
fi
[ "$FAIL" = 0 ]
