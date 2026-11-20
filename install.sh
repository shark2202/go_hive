#!/usr/bin/env bash
# Install a Hive release archive into ~/.local/bin.
#
# The archive is either found locally (release.sh output, a release asset
# directory, or --from PATH) or downloaded from a release base URL. In both
# cases SHA256 verification against the release's SHA256SUMS is mandatory,
# and the archive's own SHA256SUMS (hive, hive-cred, hive-browser,
# browserbridge + docs) is checked after extraction. All four binaries are
# installed side by side: `hive serve` resolves browserbridge next to the hive
# executable, so an archive missing any of them is a build error and aborts
# the install instead of leaving a half-working setup.
#
# Usage:
#   install.sh [options]
#
# Options:
#   --version <v>     release version (default: read internal/version, or the
#                     only matching local archive)
#   --dir <path>      install directory (default: $HOME/.local/bin)
#   --from <path>     archive file or directory containing archives and
#                     SHA256SUMS; defaults to auto-detecting dist/release
#   --base-url <url>  release download base (default:
#                     https://github.com/aden-hive/hive/releases/download/v<version>)
#   --remote          skip local archives and always download
#   -h, --help        show this help
#
# Environment: HIVE_VERSION, HIVE_INSTALL_DIR, HIVE_ARCHIVE, HIVE_BASE_URL,
# HIVE_OS, HIVE_ARCH (the last two are for testing cross-OS archives).
#
# Supported targets: darwin/linux/windows on amd64/arm64.
# Only system tools are used: tar, unzip, shasum/sha256sum, curl/wget.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

version="${HIVE_VERSION:-}"
install_dir="${HIVE_INSTALL_DIR:-$HOME/.local/bin}"
from="${HIVE_ARCHIVE:-}"
base_url="${HIVE_BASE_URL:-}"
force_remote=0

usage() {
	cat <<'EOF'
Install a Hive release archive into ~/.local/bin.

Options:
  --version <v>     release version (default: read internal/version, or the
                    only matching local archive)
  --dir <path>      install directory (default: $HOME/.local/bin)
  --from <path>     archive file or directory containing archives and
                    SHA256SUMS; defaults to auto-detecting dist/release
  --base-url <url>  release download base
                    (default: https://github.com/aden-hive/hive/releases/download/v<version>)
  --remote          skip local archives and always download
  -h, --help        show this help
EOF
}

log() { printf 'install: %s\n' "$*"; }
warn() { printf 'install: WARNING: %s\n' "$*" >&2; }
die() { printf 'install: ERROR: %s\n' "$*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			[ "$#" -ge 2 ] || die "--version needs a value"
			version="$2"; shift 2 ;;
		--version=*) version="${1#*=}"; shift ;;
		--dir)
			[ "$#" -ge 2 ] || die "--dir needs a value"
			install_dir="$2"; shift 2 ;;
		--dir=*) install_dir="${1#*=}"; shift ;;
		--from)
			[ "$#" -ge 2 ] || die "--from needs a value"
			from="$2"; shift 2 ;;
		--from=*) from="${1#*=}"; shift ;;
		--base-url)
			[ "$#" -ge 2 ] || die "--base-url needs a value"
			base_url="$2"; shift 2 ;;
		--base-url=*) base_url="${1#*=}"; shift ;;
		--remote) force_remote=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1 (try --help)" ;;
	esac
done

case "$install_dir" in
	"~") install_dir="$HOME" ;;
	"~/"*) install_dir="$HOME/${install_dir#\~/}" ;;
esac

os="${HIVE_OS:-}"
if [ -z "$os" ]; then
	case "$(uname -s)" in
		Darwin) os=darwin ;;
		Linux) os=linux ;;
		MINGW*|MSYS*|CYGWIN*|Windows_NT) os=windows ;;
		*) die "unsupported OS: $(uname -s) (supported: darwin, linux, windows)" ;;
	esac
fi
arch="${HIVE_ARCH:-}"
if [ -z "$arch" ]; then
	case "$(uname -m)" in
		x86_64|amd64) arch=amd64 ;;
		arm64|aarch64) arch=arm64 ;;
		*) die "unsupported architecture: $(uname -m) (supported: amd64, arm64)" ;;
	esac
fi
case "$os" in darwin|linux|windows) ;; *) die "unsupported OS: $os" ;; esac
case "$arch" in amd64|arm64) ;; *) die "unsupported architecture: $arch" ;; esac

archive_ext="tar.gz"
bin_name="hive"
cred_bin_name="hive-cred"
browser_bin_name="hive-browser"
bridge_bin_name="browserbridge"
if [ "$os" = "windows" ]; then
	archive_ext="zip"
	bin_name="hive.exe"
	cred_bin_name="hive-cred.exe"
	browser_bin_name="hive-browser.exe"
	bridge_bin_name="browserbridge.exe"
fi

if [ -z "$version" ]; then
	for candidate in \
		"$script_dir/go-hive/internal/version/version.go" \
		"$script_dir/../go-hive/internal/version/version.go" \
		"$script_dir/internal/version/version.go"
	do
		if [ -f "$candidate" ]; then
			version="$(sed -n 's/^const Version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' "$candidate" | head -n 1)"
			[ -n "$version" ] && break
		fi
	done
fi
version="${version#v}"

tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/hive-install.XXXXXX")"
cleanup() { rm -rf "$tmp_root"; }
trap cleanup EXIT INT TERM

sha256_file() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		sha256sum "$1" | awk '{print $1}'
	fi
}

sha256_check() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$1"
	else
		sha256sum -c "$1"
	fi
}

download() {
	url="$1"
	dest="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 2 --connect-timeout 15 -o "$dest" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$dest" "$url"
	else
		die "neither curl nor wget is available for downloading $url"
	fi
}

# Print the best matching local archive in $1 for the detected target.
# Exact version wins; otherwise the newest timestamped match is used.
find_local() {
	dir="$1"
	if [ -n "$version" ]; then
		exact="$dir/hive-$version-$os-$arch.$archive_ext"
		if [ -f "$exact" ]; then
			printf '%s\n' "$exact"
			return 0
		fi
		return 1
	fi
	matches="$(ls -1t "$dir"/hive-*-"$os"-"$arch".$archive_ext 2>/dev/null || true)"
	[ -n "$matches" ] || return 1
	count="$(printf '%s\n' "$matches" | wc -l | tr -d ' ')"
	chosen="$(printf '%s\n' "$matches" | head -n 1)"
	if [ "$count" -gt 1 ]; then
		warn "multiple local archives match $os/$arch; using newest: $(basename "$chosen")"
	fi
	printf '%s\n' "$chosen"
}

archive=""
if [ "$force_remote" = 0 ]; then
	if [ -n "$from" ]; then
		if [ -f "$from" ]; then
			archive="$from"
		elif [ -d "$from" ]; then
			archive="$(find_local "$from" || true)"
			[ -n "$archive" ] || die "no hive-*-$os-$arch.$archive_ext in $from"
		else
			die "--from path not found: $from"
		fi
	else
		for dir in \
			"$script_dir/dist/release" \
			"$script_dir/go-hive/dist/release" \
			"$script_dir/../go-hive/dist/release" \
			"$script_dir" \
			"$PWD/dist/release"
		do
			[ -d "$dir" ] || continue
			archive="$(find_local "$dir" || true)"
			[ -n "$archive" ] && break
		done
	fi
fi

downloaded=0
if [ -z "$archive" ]; then
	[ -n "$version" ] || die "no local archive for $os/$arch; pass --version or --from"
	if [ -z "$base_url" ]; then
		base_url="https://github.com/aden-hive/hive/releases/download/v$version"
	fi
	base_url="${base_url%/}"
	archive_name="hive-$version-$os-$arch.$archive_ext"
	log "downloading $base_url/$archive_name"
	if ! download "$base_url/$archive_name" "$tmp_root/$archive_name"; then
		die "failed to download $base_url/$archive_name"
	fi
	if ! download "$base_url/SHA256SUMS" "$tmp_root/SHA256SUMS"; then
		die "failed to download $base_url/SHA256SUMS"
	fi
	archive="$tmp_root/$archive_name"
	downloaded=1
fi

archive_dir="$(cd "$(dirname "$archive")" && pwd)"
archive="$archive_dir/$(basename "$archive")"
archive_name="$(basename "$archive")"
[ "$downloaded" = 1 ] || log "using local archive: $archive"

sums="$archive_dir/SHA256SUMS"
[ -f "$sums" ] || die "SHA256SUMS not found next to $archive_name; refusing unverified install"

expected="$(awk -v want="$archive_name" '
	{
		file = $2
		sub(/^\*/, "", file)
		sub(/^\.\//, "", file)
		if (file == want) { print $1; exit }
	}' "$sums")"
[ -n "$expected" ] || die "no SHA256 entry for $archive_name in $sums"
expected="$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')"
actual="$(sha256_file "$archive")"
if [ "$actual" != "$expected" ]; then
	die "SHA256 mismatch for $archive_name (expected $expected, got $actual)"
fi
log "verified SHA256: $archive_name"

extract_dir="$tmp_root/extract"
mkdir -p "$extract_dir"
case "$archive_ext" in
	tar.gz)
		tar -xzf "$archive" -C "$extract_dir"
		;;
	zip)
		command -v unzip >/dev/null 2>&1 || die "unzip is required to install windows archives"
		unzip -q "$archive" -d "$extract_dir"
		;;
esac

if [ -f "$extract_dir/SHA256SUMS" ]; then
	if ! ( cd "$extract_dir" && sha256_check SHA256SUMS >/dev/null ); then
		die "archive content checksum verification failed"
	fi
	log "verified archive contents"
fi

locate_binary() {
	name="$1"
	found="$extract_dir/$name"
	if [ ! -f "$found" ]; then
		found="$(find "$extract_dir" -maxdepth 2 -type f -name "$name" | head -n 1)"
	fi
	[ -n "$found" ] && [ -f "$found" ] || return 1
	printf '%s\n' "$found"
}

bin_path="$(locate_binary "$bin_name" || true)"
[ -n "$bin_path" ] || die "archive does not contain $bin_name"
cred_path="$(locate_binary "$cred_bin_name" || true)"
[ -n "$cred_path" ] || die "archive does not contain $cred_bin_name (release archives must carry hive and hive-cred; rebuild with scripts/release.sh)"
browser_path="$(locate_binary "$browser_bin_name" || true)"
[ -n "$browser_path" ] || die "archive does not contain $browser_bin_name (release archives must carry hive, hive-cred, hive-browser and browserbridge; rebuild with scripts/release.sh)"
bridge_path="$(locate_binary "$bridge_bin_name" || true)"
[ -n "$bridge_path" ] || die "archive does not contain $bridge_bin_name (release archives must carry hive, hive-cred, hive-browser and browserbridge; rebuild with scripts/release.sh)"

mkdir -p "$install_dir"
install_binary() {
	src="$1"
	name="$2"
	dest="$install_dir/$name"
	tmp_dest="$install_dir/.$name.tmp.$$"
	cp "$src" "$tmp_dest"
	chmod 0755 "$tmp_dest"
	mv -f "$tmp_dest" "$dest"
	log "installed $name -> $dest"
}
install_binary "$bin_path" "$bin_name"
install_binary "$cred_path" "$cred_bin_name"
install_binary "$browser_path" "$browser_bin_name"
install_binary "$bridge_path" "$bridge_bin_name"

if version_output="$("$install_dir/$bin_name" --version 2>&1)"; then
	printf '%s\n' "$version_output"
else
	warn "installed binary did not run --version (cross-OS archive?)"
fi

if "$install_dir/$cred_bin_name" --help >/dev/null 2>&1; then
	log "verified $cred_bin_name --help"
else
	warn "installed $cred_bin_name did not run --help (cross-OS archive?)"
fi

if "$install_dir/$browser_bin_name" help >/dev/null 2>&1; then
	log "verified $browser_bin_name help"
else
	warn "installed $browser_bin_name did not run help (cross-OS archive?)"
fi

if "$install_dir/$bridge_bin_name" -version >/dev/null 2>&1; then
	log "verified $bridge_bin_name -version"
else
	warn "installed $bridge_bin_name did not run -version (cross-OS archive?)"
fi

case ":$PATH:" in
	*":$install_dir:"*) ;;
	*)
		printf '\nAdd the install directory to PATH:\n  export PATH="%s:$PATH"\n' "$install_dir"
		;;
esac
