#!/usr/bin/env bash
# Audit // Parity: annotations in the Go tree against the Python reference.
#
# Hard failures:
#   * the annotation names a .py file that does not exist
#   * the annotation names a test function that is not defined in that file
# Soft warnings (not failures):
#   * annotation has no .py file (e.g. a class-style shorthand, or a prose note)
#   * annotation was split across "::" with an empty test name
#
# Usage: check_parity_targets.sh [go_hive_root] [python_root]
set -u

GO_ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PY_ROOT="${2:-/Users/mac/hive/core}"

if [ ! -d "$PY_ROOT/tests" ]; then
  echo "check_parity_targets: SKIP — python root not found at $PY_ROOT"
  exit 0
fi

checked=0
missing_file=0
missing_test=0
warn_format=0

resolve_py() { # echo resolved path or nothing
  local py="$1"
  if [ -f "$PY_ROOT/$py" ]; then echo "$PY_ROOT/$py"; return; fi
  if [ -f "$(dirname "$PY_ROOT")/$py" ]; then echo "$(dirname "$PY_ROOT")/$py"; return; fi
  if [ -f "$PY_ROOT/${py#core/}" ]; then echo "$PY_ROOT/${py#core/}"; return; fi
  echo ""
}

while IFS= read -r hit; do
  file="${hit%%:*}"
  payload="${hit#*:*:}"            # strips file + lineno
  payload="${payload#*// Parity: }"
  # An annotation can list several tests separated by commas.
  old_ifs="$IFS"; IFS=','
  for ref in $payload; do
    IFS="$old_ifs"
    # Trim surrounding whitespace without xargs: prose refs may contain quotes.
    ref="$(printf '%s' "$ref" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [ -z "$ref" ] && continue
    case "$ref" in
      *://*|*http*) continue ;;
    esac
    checked=$((checked + 1))
    py_file=""
    test_path=""
    case "$ref" in
      *.py::*) py_file="${ref%%::*}"; test_path="${ref#*::}" ;;
      *.py)    py_file="$ref"; test_path="" ;;
      *)       warn_format=$((warn_format + 1)); continue ;;
    esac
    resolved="$(resolve_py "$py_file")"
    if [ -z "$resolved" ]; then
      echo "MISSING FILE  $file -> $py_file"
      missing_file=$((missing_file + 1))
      continue
    fi
    if [ -z "$test_path" ]; then
      warn_format=$((warn_format + 1)); continue
    fi
    test_name="${test_path##*::}"
    if [ -z "$test_name" ]; then
      warn_format=$((warn_format + 1)); continue
    fi
    case "$test_name" in
      test_*)
        # Anything that is not a bare identifier (e.g. prose accidentally
        # captured after "::") is a format warning, not a missing test.
        if ! printf '%s\n' "$test_name" | grep -qE '^test_[A-Za-z0-9_]+$'; then
          warn_format=$((warn_format + 1)); continue
        fi
        if ! grep -qE "^[[:space:]]*(async[[:space:]]+)?def[[:space:]]+${test_name}\(" "$resolved"; then
          echo "MISSING TEST  $file -> $py_file::$test_name"
          missing_test=$((missing_test + 1))
        fi
        ;;
      [A-Z]*)
        # Class-style or constant-style reference: a class is a fine anchor,
        # a module-level constant is a data reference rather than a test.
        if ! printf '%s\n' "$test_name" | grep -qE '^[A-Z][A-Za-z0-9_]*$'; then
          warn_format=$((warn_format + 1)); continue
        fi
        if ! grep -qE "^[[:space:]]*class[[:space:]]+${test_name}\b" "$resolved"; then
          warn_format=$((warn_format + 1))
        fi
        ;;
      *)
        warn_format=$((warn_format + 1))
        ;;
    esac
  done
done < <(grep -rn '// Parity: ' "$GO_ROOT" --include='*_test.go' || true)

echo "----------------------------------------"
echo "parity refs checked: $checked  missing files: $missing_file  missing tests: $missing_test  shorthand/prose: $warn_format"
if [ "$missing_file" -ne 0 ] || [ "$missing_test" -ne 0 ]; then
  echo "check_parity_targets: FAIL"
  exit 1
fi
echo "check_parity_targets: OK"
