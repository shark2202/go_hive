package builtin

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// terminalExecShellMetachars mirrors exec.py's _SHELL_METACHARS: tokens that
// only make sense to a shell. When shell=False, their presence silently routes
// the command through the platform shell instead of spawning the first token
// with the rest as junk argv.
var terminalExecShellMetachars = map[string]struct{}{
	"|":    {},
	"&&":   {},
	"||":   {},
	";":    {},
	">":    {},
	"<":    {},
	">>":   {},
	"<<":   {},
	"&":    {},
	"2>":   {},
	"2>&1": {},
	"|&":   {},
}

// terminalSplitCommand tokenizes a shell=False command with the POSIX quoting
// rules upstream's shlex.split(posix=True) applies: single quotes are literal,
// double quotes honour backslash escapes for \ " $ `, and an unquoted
// backslash escapes the next rune. Unbalanced quotes return an error so the
// caller can treat the string as shell syntax, like upstream.
func terminalSplitCommand(command string) ([]string, error) {
	var tokens []string
	var current strings.Builder
	inToken := false
	runes := []rune(command)
	flush := func() {
		if inToken {
			tokens = append(tokens, current.String())
			current.Reset()
			inToken = false
		}
	}
	for i := 0; i < len(runes); {
		switch ch := runes[i]; {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			flush()
			i++
		case ch == '\'':
			inToken = true
			i++
			for i < len(runes) && runes[i] != '\'' {
				current.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, errors.New("unbalanced single quote")
			}
			i++
		case ch == '"':
			inToken = true
			i++
			closed := false
			for i < len(runes) {
				if runes[i] == '\\' && i+1 < len(runes) {
					next := runes[i+1]
					if next == '"' || next == '\\' || next == '$' || next == '`' {
						current.WriteRune(next)
						i += 2
						continue
					}
					current.WriteRune(runes[i])
					i++
					continue
				}
				if runes[i] == '"' {
					closed = true
					i++
					break
				}
				current.WriteRune(runes[i])
				i++
			}
			if !closed {
				return nil, errors.New("unbalanced double quote")
			}
		case ch == '\\':
			inToken = true
			if i+1 < len(runes) {
				current.WriteRune(runes[i+1])
				i += 2
			} else {
				i++
			}
		default:
			inToken = true
			current.WriteRune(ch)
			i++
		}
	}
	flush()
	return tokens, nil
}

// terminalCommandNeedsShell reports whether the token list contains shell
// metacharacters or globs shlex left unexpanded.
//
// Upstream matches only standalone metachar tokens, which misses common forms
// like `pwd; echo hi` (shlex keeps the `;` attached to `pwd`). Detecting the
// metacharacters inside tokens too keeps those commands working instead of
// direct-exec'ing a program literally named `pwd;`. `&` is only matched
// standalone so an unquoted query string is not split into a background job.
func terminalCommandNeedsShell(tokens []string) bool {
	for i, token := range tokens {
		if _, ok := terminalExecShellMetachars[token]; ok {
			return true
		}
		if token != "[" && strings.ContainsAny(token, "*?[") {
			return true
		}
		if strings.ContainsAny(token, ";|<>`") || strings.Contains(token, "$(") {
			return true
		}
		if i == 0 && terminalLooksLikeEnvAssignment(token) {
			return true
		}
	}
	return false
}

// terminalLooksLikeEnvAssignment reports whether token starts a POSIX
// `NAME=value` command prefix, which only a shell understands.
func terminalLooksLikeEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	name := token[:eq]
	for i, ch := range name {
		if ch == '_' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

// terminalExecLimits coerces the limits argument the way upstream's
// coerce_limits does: only the four known keys survive, negative and
// non-integer values are dropped, and an invalid dict is a no-op rather than
// an error.
func terminalExecLimits(input map[string]any) map[string]int {
	raw, ok := input["limits"]
	if !ok || raw == nil {
		return nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		if typed, ok := raw.(map[string]int); ok {
			values = make(map[string]any, len(typed))
			for key, value := range typed {
				values[key] = value
			}
		} else {
			return nil
		}
	}
	out := map[string]int{}
	for _, key := range []string{"cpu_sec", "rss_mb", "fsize_mb", "nofile"} {
		value, ok := finiteNumberArg(values, key)
		if !ok {
			continue
		}
		if limit := int(value); limit > 0 {
			out[key] = limit
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// terminalUlimitPrefix renders best-effort POSIX ulimit statements. Unknown
// keys never reach this function. Each statement is independent: a kernel that
// refuses one cap (macOS rejects RLIMIT_AS, which upstream's setrlimit path
// also silently skips) must not stop the others.
func terminalUlimitPrefix(limits map[string]int) string {
	parts := make([]string, 0, 4)
	add := func(flag string, value int) {
		if value <= 0 {
			return
		}
		parts = append(parts, fmt.Sprintf("ulimit -%s %d 2>/dev/null || true", flag, value))
	}
	if value, ok := limits["cpu_sec"]; ok {
		add("t", value)
	}
	if value, ok := limits["rss_mb"]; ok {
		add("v", value*1024)
	}
	if value, ok := limits["fsize_mb"]; ok {
		add("f", value*1024)
	}
	if value, ok := limits["nofile"]; ok {
		add("n", value)
	}
	return strings.Join(parts, "; ")
}

// terminalExecLimitedCommand prefixes a bash command string with the ulimit
// preamble that enforces the accepted limits. Only POSIX bash has ulimit;
// Windows and PowerShell/cmd keep the command untouched (upstream's
// setrlimit path is a no-op there too).
func terminalExecLimitedCommand(command string, spec terminalShellSpec, limits map[string]int) string {
	if len(limits) == 0 || runtime.GOOS == "windows" || spec.executable == "" || spec.kind != "bash" {
		return command
	}
	prefix := terminalUlimitPrefix(limits)
	if prefix == "" {
		return command
	}
	return prefix + "; " + command
}

// terminalExecApplyLimits wraps a direct-exec argv in `bash -c 'ulimit ...;
// exec "$@"'` so each cap is applied before the program starts and argv
// survives byte for byte. Go's exec package has no pre-exec hook, so this is
// the only place the caps can be applied for the direct path.
func terminalExecApplyLimits(argv []string, limits map[string]int) []string {
	if len(limits) == 0 || len(argv) == 0 || runtime.GOOS == "windows" {
		return argv
	}
	prefix := terminalUlimitPrefix(limits)
	if prefix == "" {
		return argv
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return argv
	}
	wrapped := make([]string, 0, len(argv)+4)
	wrapped = append(wrapped, bash, "-c", prefix+`; exec "$@"`, "hive-exec")
	wrapped = append(wrapped, argv...)
	return wrapped
}
