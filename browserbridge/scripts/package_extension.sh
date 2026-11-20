#!/usr/bin/env bash
# Package the Chrome extension with manifest.json at the zip root.
#
# Usage:
#   scripts/package_extension.sh          build dist/hive-browser-bridge-<version>.zip
#   scripts/package_extension.sh --check  validate an existing archive
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXT="$ROOT/extension"
DIST="$ROOT/dist"
MANIFEST="$EXT/manifest.json"
VERSION="$(sed -n 's/^[[:space:]]*"version":[[:space:]]*"\([^"]*\)".*/\1/p' "$MANIFEST" | head -1)"
if [ -z "$VERSION" ]; then
  echo "error: could not read version from $MANIFEST" >&2
  exit 1
fi
OUT="$DIST/hive-browser-bridge-$VERSION.zip"
LIST=""

fail() {
  echo "ZIP CHECK FAILED: $*" >&2
  exit 1
}

require_entry() {
  local entry="$1"
  grep -qx "$entry" "$LIST" || fail "missing zip entry: $entry"
}

check_zip() {
  [ -f "$OUT" ] || fail "archive not found: $OUT (run without --check first)"
  command -v unzip >/dev/null 2>&1 || fail "unzip is required"

  LIST="$(mktemp "${TMPDIR:-/tmp}/browserbridge-zip.XXXXXX")"
  unzip -Z1 "$OUT" > "$LIST"

  # manifest.json must be at the archive root, not inside a wrapper folder.
  grep -qx "manifest.json" "$LIST" || fail "manifest.json is not at the zip root"
  if grep -q '^[^/]*/manifest.json$' "$LIST"; then
    fail "manifest.json is nested inside a directory"
  fi

  # Required runtime files.
  for entry in \
    manifest.json background.js offscreen.html offscreen.js \
    popup.html popup.js sidepanel.html sidepanel.js wordlist.js \
    README.md SOURCE.md LICENSE \
    icons/icon-16.png icons/icon-32.png icons/icon-48.png icons/icon-128.png; do
    require_entry "$entry"
  done

  # Every path the manifest references must be present in the archive.
  local refs
  refs="$(unzip -p "$OUT" manifest.json | sed -n \
    -e 's/.*"service_worker":[[:space:]]*"\([^"]*\)".*/\1/p' \
    -e 's/.*"default_path":[[:space:]]*"\([^"]*\)".*/\1/p' \
    -e 's/.*"[0-9][0-9]*":[[:space:]]*"\([^"]*\)".*/\1/p' | sort -u)"
  while IFS= read -r ref; do
    [ -z "$ref" ] && continue
    # Ignore non-file JSON string values that happen to match the icon shape.
    case "$ref" in
      *.js|*.html|*.json|*.png|*.svg) require_entry "$ref" ;;
    esac
  done <<< "$refs"

  local count
  count="$(grep -vc '/$' "$LIST" || true)"
  echo "zip OK: $OUT ($count files, version $VERSION)"
}

if [ "${1:-}" = "--check" ]; then
  check_zip
  rm -f "$LIST"
  exit 0
fi
if [ $# -gt 0 ]; then
  echo "usage: $0 [--check]" >&2
  exit 2
fi

[ -f "$MANIFEST" ] || { echo "error: missing $MANIFEST" >&2; exit 1; }
command -v zip >/dev/null 2>&1 || { echo "error: zip is required" >&2; exit 1; }
mkdir -p "$DIST"
rm -f "$OUT"
(
  cd "$EXT"
  zip -qr -X "$OUT" . -x '*.DS_Store'
)
echo "packaged: $OUT"
check_zip
rm -f "$LIST"
