// Package pyenv parses environment overrides with the *Python* semantics the
// upstream runtime uses, so a Go reader resolves the same value the reference
// implementation would for the same environment.
//
// Upstream's helpers are thin wrappers around the Python builtins:
//
//	def _env_int(name: str, default: int) -> int:
//	    try:
//	        return int(os.environ.get(name, str(default)))
//	    except ValueError:
//	        return default
//
// Python's int()/float() are more permissive than strconv.Atoi: they strip
// surrounding whitespace, accept a leading sign, allow single underscores
// between digits ("1_000"), and a float accepts exponents, "inf" and "nan".
// These helpers mirror that accept/settle behaviour exactly:
//
//   - unset            -> def
//   - empty/whitespace -> def (int("") raises ValueError upstream)
//   - unparsable       -> def (the upstream except ValueError branch)
//   - parsed           -> the parsed value
//
// They are deliberately per-call readers (no package-level caching): upstream
// resolves many of these at import, but a live override must be visible to a
// long-running Go process, and the resolved *values* stay identical.
package pyenv

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

// Int mirrors Python int(os.environ.get(name, str(def))).
func Int(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	value, ok := parseInt(raw)
	if !ok {
		return def
	}
	return value
}

// ParseInt exposes the Python-int accept rule for callers that mirror a raise
// (upstream `int(os.environ[...])` with no except) instead of a fallback.
func ParseInt(raw string) (int, bool) { return parseInt(raw) }

// IntOrError mirrors a bare upstream `int(os.environ.get(name, str(def)))` that
// has no ValueError handler: unset is the default, a present value must parse.
// present reports whether the variable was set at all.
func IntOrError(name string, def int) (value int, present bool, err error) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def, false, nil
	}
	parsed, ok := parseInt(raw)
	if !ok {
		return 0, true, fmt.Errorf("invalid %s %q: expected an integer", name, raw)
	}
	return parsed, true, nil
}

// Float mirrors Python float(os.environ.get(name, str(def))).
func Float(name string, def float64) float64 {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	value, ok := parseFloat(raw)
	if !ok {
		return def
	}
	return value
}

// NonEmpty mirrors upstream's `if not os.environ.get(name)` gate: a variable
// counts as set only when its value is not the empty string.
func NonEmpty(name string) bool {
	return os.Getenv(name) != ""
}

// Is1OrTrue mirrors upstream's `.strip().lower() in ("1", "true")` opt-in gate
// (llm_debug_logger). Unset, empty and every other spelling are false.
func Is1OrTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true":
		return true
	}
	return false
}

// DenyListBool mirrors upstream's falsey deny-list
// (`.strip().lower() not in ("0", "false", "no", "off")` for a non-empty
// value): true unless the lower-cased trimmed value is a deny word.
func DenyListBool(name string, def bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	if strings.TrimSpace(raw) == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// parseInt reproduces the accepted subset of Python int(): optional sign,
// ASCII digits, and single underscores between digits. Leading/trailing
// whitespace is ignored. Anything else is not parsable.
func parseInt(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	negative := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		negative = true
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var value int64
	prevUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			// Python only allows underscores between digits.
			if prevUnderscore || i == 0 || i == len(s)-1 {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		prevUnderscore = false
		digit := int64(c - '0')
		if value > (math.MaxInt64-digit)/10 {
			// Python ints are unbounded; Go's are not. A value this large is
			// never a sane env override, so upstream could only ever see it
			// reach an error path — treat it as unparsable.
			return 0, false
		}
		value = value*10 + digit
	}
	if negative {
		value = -value
	}
	return int(value), true
}

// parseFloat reproduces the accepted subset of Python float(): ints, decimals,
// exponents, "inf"/"nan" with an optional sign, and underscores between digits.
func parseFloat(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	if strings.Contains(s, "_") {
		if !underscoresBetweenDigits(s) {
			return 0, false
		}
		s = strings.ReplaceAll(s, "_", "")
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// underscoresBetweenDigits reports whether every '_' in s has a digit on both
// sides, matching Python's numeric-literal rule for float("1_0.5").
func underscoresBetweenDigits(s string) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	for i := 0; i < len(s); i++ {
		if s[i] != '_' {
			continue
		}
		if i == 0 || i == len(s)-1 || !isDigit(s[i-1]) || !isDigit(s[i+1]) {
			return false
		}
	}
	return true
}
