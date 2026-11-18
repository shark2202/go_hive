#!/usr/bin/env bash
# Audit // Parity: annotations in the Go tree against the Python reference.
#
# Scans BOTH test files (*_test.go) and production files (*.go that are not
# tests), so a production annotation cannot point at a symbol that no longer
# exists (and vice versa).
#
# Test files — hard failures:
#   * the annotation names a .py file that does not exist
#   * the annotation names a test function that is not defined in that file
# Soft warnings (not failures):
#   * annotation has no .py file (e.g. a class-style shorthand, or a prose note)
#   * annotation was split across "::" with an empty test name
#
# Production files — hard failures:
#   * the annotation names a .py file that does not exist
#   * the annotation names a symbol that is not defined in that file
#     (top-level def/class/name, or Class.member for a dotted reference)
# Soft warnings (not failures):
#   * annotation has no .py file (module-qualified shorthand or prose)
#   * annotation names a file but no resolvable identifier after it
#
# Multi-line annotations are supported: a "// Parity:" line whose payload ends
# with a comma continues on the following "//"-comment line.
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

prod_checked=0
prod_missing_file=0
prod_missing_sym=0
prod_warn=0

resolve_py() { # echo resolved path or nothing
  local py="$1"
  if [ -f "$PY_ROOT/$py" ]; then echo "$PY_ROOT/$py"; return; fi
  if [ -f "$(dirname "$PY_ROOT")/$py" ]; then echo "$(dirname "$PY_ROOT")/$py"; return; fi
  if [ -f "$PY_ROOT/${py#core/}" ]; then echo "$PY_ROOT/${py#core/}"; return; fi
  echo ""
}

# Emit "file<TAB>lineno<TAB>payload" for every // Parity: annotation, joining
# comma-continued lines onto the payload.
collect_payloads() { # $1 = test | prod
  local mode="$1" list
  if [ "$mode" = test ]; then
    list="$(grep -rl '// Parity: ' "$GO_ROOT" --include='*_test.go' || true)"
  else
    list="$(grep -rl '// Parity: ' "$GO_ROOT" --include='*.go' | grep -v '_test\.go$' || true)"
  fi
  [ -z "$list" ] && return 0
  printf '%s\n' "$list" | while IFS= read -r f; do
    [ -f "$f" ] || continue
    awk -v FILE="$f" '
      function flush() {
        if (pend != "") { printf "%s\t%d\t%s\n", FILE, pend_ln, pend; pend = "" }
      }
      {
        if (pend != "" && pend ~ /,[[:space:]]*$/ \
            && $0 ~ /^[[:space:]]*\/\/[[:space:]]/ && $0 !~ /Parity:/ && $0 ~ /\.py/) {
          body = $0
          sub(/^[[:space:]]*\/\/[[:space:]]?/, "", body)
          pend = pend " " body
          if (body !~ /,[[:space:]]*$/) flush()
          next
        }
        flush()
        if (index($0, "// Parity: ")) {
          p = $0
          sub(/^.*\/\/ Parity: /, "", p)
          pend = p
          pend_ln = FNR
        }
      }
      END { flush() }
    ' "$f"
  done
}

# True when the python file defines the (possibly dotted) symbol.
py_has_symbol() { # $1 = resolved python file, $2 = dotted symbol
  local f="$1" dotted="$2" head member
  case "$dotted" in
    *.*) head="${dotted%%.*}"; member="${dotted#*.}" ;;
    *)   head="$dotted"; member="" ;;
  esac
  if [ -z "$member" ]; then
    grep -qE "^[[:space:]]*(async[[:space:]]+)?def[[:space:]]+${head}[[:space:]]*\(|^[[:space:]]*class[[:space:]]+${head}\b|^[[:space:]]*${head}[[:space:]]*[:=]" "$f"
    return
  fi
  member="${member%%.*}"
  grep -qE "^[[:space:]]*class[[:space:]]+${head}\b" "$f" || return 1
  # Method, class-level attribute, or an attribute assigned on self
  # (plain or annotated: ``self.budget_pinned: bool = ...``).
  grep -qE "^[[:space:]]*(async[[:space:]]+)?def[[:space:]]+${member}[[:space:]]*\(|^[[:space:]]*${member}[[:space:]]*[:=]|self\.${member}[[:space:]]*[:=]" "$f"
}

# ---------------------------------------------------------------- test files
while IFS=$'\t' read -r file lineno payload; do
  [ -z "${file:-}" ] && continue
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
      echo "MISSING FILE  $file:$lineno -> $py_file"
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
          echo "MISSING TEST  $file:$lineno -> $py_file::$test_name"
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
done < <(collect_payloads test)

# ---------------------------------------------------------- production files
while IFS=$'\t' read -r file lineno payload; do
  [ -z "${file:-}" ] && continue
  old_ifs="$IFS"; IFS=','
  for ref in $payload; do
    IFS="$old_ifs"
    ref="$(printf '%s' "$ref" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [ -z "$ref" ] && continue
    case "$ref" in
      *://*|*http*) continue ;;
    esac
    # Drop a sentence period after the extension: "foo.py." -> "foo.py".
    case "$ref" in
      *.py.) ref="${ref%.}" ;;
    esac
    prod_checked=$((prod_checked + 1))
    py_file=""
    sym=""
    case "$ref" in
      *.py::*) py_file="${ref%%::*}"; sym="${ref#*::}"; sym="${sym##*::}" ;;
      *.py)    py_file="$ref" ;;
      *.py[[:space:]]*)
        py_file="${ref%%[[:space:]]*}"
        sym="${ref#"$py_file"}"
        ;;
      *) prod_warn=$((prod_warn + 1)); continue ;;
    esac
    case "$py_file" in
      *.py) ;;
      *) prod_warn=$((prod_warn + 1)); continue ;;
    esac
    resolved="$(resolve_py "$py_file")"
    if [ -z "$resolved" ]; then
      echo "MISSING FILE  $file:$lineno -> $py_file"
      prod_missing_file=$((prod_missing_file + 1))
      continue
    fi
    [ -z "$sym" ] && continue
    # Leading dotted identifier only; trailing prose in parentheses is ignored.
    ident="$(printf '%s\n' "$sym" | sed 's/^[[:space:]]*//' \
      | grep -oE '^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*' || true)"
    if [ -z "$ident" ]; then
      prod_warn=$((prod_warn + 1)); continue
    fi
    if ! py_has_symbol "$resolved" "$ident"; then
      echo "MISSING SYMBOL  $file:$lineno -> $py_file::$ident"
      prod_missing_sym=$((prod_missing_sym + 1))
    fi
  done
done < <(collect_payloads prod)

echo "----------------------------------------"
echo "test parity refs: $checked checked, $missing_file missing files, $missing_test missing tests, $warn_format shorthand/prose"
echo "prod parity refs: $prod_checked checked, $prod_missing_file missing files, $prod_missing_sym missing symbols, $prod_warn shorthand/prose"
total_missing=$((missing_file + missing_test + prod_missing_file + prod_missing_sym))
echo "parity refs checked: $((checked + prod_checked))  missing: $total_missing"
if [ "$total_missing" -ne 0 ]; then
  echo "check_parity_targets: FAIL"
  exit 1
fi
echo "check_parity_targets: OK"
