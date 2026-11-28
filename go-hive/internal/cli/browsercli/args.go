package browsercli

// Compatibility layer for the argparse-compatible parser extracted to
// internal/cli/cliargs. The command tree keeps the historical unexported type
// and field names; the parser and help renderer live in the shared subpackage,
// and browsercli must not import internal/cli.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/cli/cliargs"
)

const (
	kindBool   = cliargs.KindBool
	kindString = cliargs.KindString
	kindInt    = cliargs.KindInt
	kindAppend = cliargs.KindAppend
)

type argSpec = cliargs.ArgSpec

type positionalSpec = cliargs.PositionalSpec

// commandSpec is the command tree node. The type parameter binds Run's argument
// to this package's parsedArgs view, so every Run callback keeps its
// historical func(*parsedArgs) int signature.
type commandSpec = cliargs.CommandSpec[parsedArgs]

// parsedArgs is the browsercli view of a parse result. It mirrors the four
// fields command implementations read (values/provided/cmd/rest) and exposes
// the historical unexported accessors.
type parsedArgs struct {
	values   map[string]any
	provided map[string]bool
	cmd      *commandSpec
}

func (p *parsedArgs) wasProvided(key string) bool {
	return p.provided[key]
}

func (p *parsedArgs) str(key string) string {
	value, ok := p.values[key]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Sprintf("%v", value)
	}
	return text
}

func (p *parsedArgs) boolean(key string) bool {
	value, _ := p.values[key].(bool)
	return value
}

func (p *parsedArgs) intValue(key string) int {
	switch typed := p.values[key].(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	}
	return 0
}

func (p *parsedArgs) list(key string) []string {
	values, _ := p.values[key].([]string)
	return values
}

// parseCommand parses one full command line through the shared parser and
// adapts the result to this package's view.
func parseCommand(spec *commandSpec, argv []string) (*parsedArgs, error) {
	parsed, err := cliargs.ParseCommand(spec, argv)
	if err != nil {
		return nil, err
	}
	out := &parsedArgs{
		values:   parsed.Values(),
		provided: parsed.Provided(),
	}
	if cmd, ok := parsed.Cmd().(*commandSpec); ok {
		out.cmd = cmd
	}
	return out, nil
}

// writeJSONOut writes one JSON value the way the Python CLI does: no HTML
// escaping (the agent contract keeps `<`, `>` and `&` verbatim) and an
// optional two-space indent.
func writeJSONOut(w io.Writer, value any, indented bool) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if indented {
		encoder.SetIndent("", "  ")
	}
	_ = encoder.Encode(value)
}

// expandUser expands a leading `~` the way the Python CLI's os.path
// .expanduser does; every other path is returned verbatim.
func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
		}
	}
	return path
}
