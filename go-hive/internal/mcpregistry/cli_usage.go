package mcpregistry

import (
	"fmt"
	"strings"
)

// UsageError is an `hive mcp` parse failure in argparse's shape: the caller
// prints the responsible page's usage block, then "<prog>: error: <message>",
// and exits 2.
//
// Root marks argparse's deferred "unrecognized arguments" check: the extras
// are reported by the top-level parse_args after the whole command tree
// parsed, so the page is the ROOT's (`hive`), never the verb's — the caller
// supplies that page through CLI.RootUsage.
type UsageError struct {
	Prog string
	Msg  string
	Root bool
}

func (e *UsageError) Error() string { return e.Msg }

func usageAt(prog, format string, args ...any) *UsageError {
	return &UsageError{Prog: prog, Msg: fmt.Sprintf(format, args...)}
}

// unrecognizedArgs is argparse's extras failure for one command line.
func unrecognizedArgs(extras []string) *UsageError {
	return &UsageError{Msg: "unrecognized arguments: " + strings.Join(extras, " "), Root: true}
}

type MCPCommand struct {
	Kind string
	// HelpTopic is the verb whose help page was requested (`run`, `install`,
	// ...); empty means the group page.
	HelpTopic string

	Install InstallArgs
	Add     AddArgs
	Mode    ModeArgs
	Run     RunArgs
	List    ListArgs
	Info    InfoArgs
	Config  ConfigArgs
	Search  SearchArgs
	Health  HealthArgs
	Update  UpdateArgs

	Name string
	// HelpVerb names the verb whose per-command help page to print; empty
	// means the group page (a bare --help, or the Go-only `hive mcp help`
	// alias).
	HelpVerb string
}

var mcpSubcommands = map[string]bool{
	"install": true, "add": true, "mode": true, "run": true, "remove": true,
	"enable": true, "disable": true, "list": true, "info": true, "config": true,
	"search": true, "health": true, "init": true, "update": true,
}

// mcpParse collects argparse's deferred extras for one `hive mcp` command
// line. An unknown option string, or a positional the verb has no slot for,
// is not an error where it appears: the verb finishes parsing, and only then
// the top-level parse_args reports the leftovers as "unrecognized arguments"
// against the root page. A missing required argument fires inside the verb
// first (argparse checks its required actions before returning), so it wins.
type mcpParse struct{ extras []string }

func (s *mcpParse) extra(token string) { s.extras = append(s.extras, token) }

func splitFlag(token string) (string, string, bool) {
	if idx := strings.Index(token, "="); idx >= 0 && strings.HasPrefix(token, "--") {
		return token[:idx], token[idx+1:], true
	}
	return token, "", false
}

// looksLikeOption reports whether a token occupies argparse's 'O' pattern
// slot, so a value-taking flag refuses it ("expected one argument") instead
// of swallowing it as a value.
func looksLikeOption(token string) bool {
	return len(token) > 1 && strings.HasPrefix(token, "-")
}

func takeValue(rest []string, index int, inline string, hasInline bool, flag string) (string, int, error) {
	if hasInline {
		return inline, index + 1, nil
	}
	if index+1 >= len(rest) || looksLikeOption(rest[index+1]) {
		return "", index, fmt.Errorf("argument %s: expected one argument", flag)
	}
	return rest[index+1], index + 2, nil
}

func takeMany(rest []string, index int, inline string, hasInline bool) ([]string, int) {
	values := []string{}
	if hasInline {
		values = append(values, inline)
	}
	index++
	for index < len(rest) && !strings.HasPrefix(rest[index], "-") {
		values = append(values, rest[index])
		index++
	}
	return values, index
}
