package builtin

// Semantic exit-code classification and the advisory destructive-command
// warning, both carried on the terminal_exec envelope. Ported verbatim from
// tools/src/terminal_tools/common/semantic_exit.py and
// tools/src/terminal_tools/common/destructive_warning.py so the model-visible
// JSON matches the Python reference.

import (
	"regexp"
	"strconv"
	"strings"
)

// terminalSemantic describes one (status, message) pair. A nil message is
// serialized as null, like the Python None.
type terminalSemantic struct {
	status  string
	message *string
}

func terminalSemanticOK(message string) terminalSemantic {
	return terminalSemantic{status: "ok", message: &message}
}

// terminalCommandSemantics mirrors the per-command catalog: exit codes that
// convey information other than success/failure.
var terminalCommandSemantics = map[string]map[int]terminalSemantic{
	"grep":    {0: {status: "ok"}, 1: terminalSemanticOK("No matches found")},
	"rg":      {0: {status: "ok"}, 1: terminalSemanticOK("No matches found")},
	"ripgrep": {0: {status: "ok"}, 1: terminalSemanticOK("No matches found")},
	"find":    {0: {status: "ok"}, 1: terminalSemanticOK("Some directories were inaccessible")},
	"diff":    {0: {status: "ok"}, 1: terminalSemanticOK("Files differ")},
	"test":    {0: {status: "ok"}, 1: terminalSemanticOK("Condition is false")},
	"[":       {0: {status: "ok"}, 1: terminalSemanticOK("Condition is false")},
}

// terminalBaseCommand extracts the command whose exit code the shell
// propagates: the segment after the last unquoted pipe/&&/||/; for a shell
// string (best effort, exactly like the upstream heuristic), or the first
// element of an argv list.
func terminalBaseCommand(command any) string {
	switch typed := command.(type) {
	case []string:
		if len(typed) > 0 {
			return typed[0]
		}
		return ""
	case string:
		text := typed
		for _, separator := range []string{"||", "&&", "|", ";"} {
			if strings.Contains(text, separator) {
				parts := strings.Split(text, separator)
				text = parts[len(parts)-1]
			}
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		first := strings.Fields(text)[0]
		if index := strings.LastIndexByte(first, '/'); index >= 0 {
			return first[index+1:]
		}
		return first
	}
	return ""
}

// terminalClassifyExit ports semantic_exit.classify: (status, message).
func terminalClassifyExit(command any, exitCode *int, timedOut, signaled bool) (string, *string) {
	if timedOut {
		message := "Command timed out"
		return "error", &message
	}
	if signaled {
		if exitCode != nil {
			message := "Killed by signal (exit " + strconv.Itoa(*exitCode) + ")"
			return "signal", &message
		}
		message := "Killed by signal (exit <nil>)"
		return "signal", &message
	}
	if exitCode == nil {
		message := "Still running"
		return "ok", &message
	}
	base := terminalBaseCommand(command)
	if table, ok := terminalCommandSemantics[base]; ok {
		if semantic, ok := table[*exitCode]; ok {
			return semantic.status, semantic.message
		}
		message := "Command failed with exit code " + strconv.Itoa(*exitCode)
		return "error", &message
	}
	if *exitCode == 0 {
		return "ok", nil
	}
	message := "Command failed with exit code " + strconv.Itoa(*exitCode)
	return "error", &message
}

// terminalDestructivePatterns is the advisory catalog, in the upstream order so
// the first matching message wins.
var terminalDestructivePatterns = []struct {
	pattern *regexp.Regexp
	message string
}{
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "may discard uncommitted changes"},
	{regexp.MustCompile(`\bgit\s+push\b[^;&|\n]*[ \t](--force|--force-with-lease|-f)\b`), "may overwrite remote history"},
	// git clean with a force flag but no dry-run flag; the negative lookahead in
	// the Python pattern is handled by terminalGitCleanWarning below.
	{regexp.MustCompile(`\bgit\s+checkout\s+(--\s+)?\.[ \t]*($|[;&|\n])`), "may discard all working tree changes"},
	{regexp.MustCompile(`\bgit\s+restore\s+(--\s+)?\.[ \t]*($|[;&|\n])`), "may discard all working tree changes"},
	{regexp.MustCompile(`\bgit\s+stash[ \t]+(drop|clear)\b`), "may permanently remove stashed changes"},
	{regexp.MustCompile(`\bgit\s+branch\s+(-D[ \t]|--delete\s+--force|--force\s+--delete)\b`), "may force-delete a branch"},
	{regexp.MustCompile(`\bgit\s+(commit|push|merge)\b[^;&|\n]*--no-verify\b`), "may skip safety hooks"},
	{regexp.MustCompile(`\bgit\s+commit\b[^;&|\n]*--amend\b`), "may rewrite the last commit"},
	{regexp.MustCompile(`(^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR][a-zA-Z]*f|(^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f[a-zA-Z]*[rR]`), "may recursively force-remove files"},
	{regexp.MustCompile(`(^|[;&|\n]\s*)rm\s+-[a-zA-Z]*[rR]`), "may recursively remove files"},
	{regexp.MustCompile(`(^|[;&|\n]\s*)rm\s+-[a-zA-Z]*f`), "may force-remove files"},
	{regexp.MustCompile(`(?i)\b(DROP|TRUNCATE)\s+(TABLE|DATABASE|SCHEMA)\b`), "may drop or truncate database objects"},
	{regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+\w+[ \t]*(;|"|'|\n|$)`), "may delete rows from a database table"},
	{regexp.MustCompile(`\bkubectl\s+delete\b`), "may delete Kubernetes resources"},
	{regexp.MustCompile(`\bterraform\s+destroy\b`), "may destroy Terraform infrastructure"},
}

var (
	terminalGitCleanPrefix = regexp.MustCompile(`\bgit\s+clean\b`)
	terminalGitCleanForce  = regexp.MustCompile(`^[^;&|\n]*-[a-zA-Z]*f`)
	terminalGitCleanDry    = regexp.MustCompile(`-[a-zA-Z]*n|--dry-run`)
)

// terminalGitCleanWarning applies the upstream `git clean` rule (force flag,
// no dry-run switch in the same segment) without needing regex lookahead.
func terminalGitCleanWarning(command string) string {
	for _, location := range terminalGitCleanPrefix.FindAllStringIndex(command, -1) {
		rest := command[location[1]:]
		if end := strings.IndexAny(rest, ";&|\n"); end >= 0 {
			rest = rest[:end]
		}
		if terminalGitCleanDry.MatchString(rest) {
			continue
		}
		if terminalGitCleanForce.MatchString(rest) {
			return "may permanently delete untracked files"
		}
	}
	return ""
}

// terminalDestructiveWarning returns the advisory warning for command, or nil.
func terminalDestructiveWarning(command any) *string {
	if warning := terminalGitCleanWarning(stringifyCommand(command)); warning != "" {
		return &warning
	}
	text := stringifyCommand(command)
	for _, entry := range terminalDestructivePatterns {
		if entry.pattern.MatchString(text) {
			message := entry.message
			return &message
		}
	}
	return nil
}

func stringifyCommand(command any) string {
	switch typed := command.(type) {
	case []string:
		return strings.Join(typed, " ")
	case string:
		return typed
	}
	return ""
}
