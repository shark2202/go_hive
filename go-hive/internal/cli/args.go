package cli

// Compatibility layer for the argparse-compatible parser extracted to
// internal/cli/cliargs. Command specs keep their historical unexported type
// and field names so the command tree does not need to change; the parser and
// help renderer live in the subpackage, which must not import package cli.

import (
	"fmt"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/cli/cliargs"
)

type argKind = cliargs.ArgKind

const (
	kindBool   = cliargs.KindBool
	kindString = cliargs.KindString
	kindInt    = cliargs.KindInt
	kindAppend = cliargs.KindAppend
)

type argSpec = cliargs.ArgSpec

type positionalSpec = cliargs.PositionalSpec

// commandSpec is the command tree node. The type parameter binds Run's argument
// back to package cli's parsedArgs view, so every existing Run callback keeps
// its func(*parsedArgs) int signature.
type commandSpec = cliargs.CommandSpec[parsedArgs]

// parsedArgs is the package-cli view of a parse result. It mirrors the four
// fields command implementations read (values/provided/cmd/rest) and exposes
// the historical unexported accessors; cliargs owns the parser's own instance.
type parsedArgs struct {
	values   map[string]any
	provided map[string]bool
	cmd      *commandSpec
	rest     []string
}

func newParsedArgs() *parsedArgs {
	return &parsedArgs{values: map[string]any{}, provided: map[string]bool{}}
}

func (p *parsedArgs) wasProvided(key string) bool {
	return p.provided[key]
}

func newArgs(values map[string]any) *parsedArgs {
	out := newParsedArgs()
	for key, value := range values {
		out.values[key] = value
	}
	return out
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

func (p *parsedArgs) rawArgs() []string {
	return p.rest
}

type usageError struct {
	msg  string
	prog string
	// rootOwned marks argparse's deferred "unrecognized" failure: the tokens
	// were collected as extras and the top-level parse_args raised the error
	// after the whole command tree parsed, so the page (and prog prefix) are
	// the ROOT's, not the page of the command that carried the token.
	rootOwned bool
}

func (e *usageError) Error() string { return e.msg }

// helpRequest and versionRequest keep the package-cli error identities that
// callers match with errors.As; parseCommand translates the cliargs signals.
type helpRequest struct{ spec *commandSpec }

func (e *helpRequest) Error() string { return "help" }

type versionRequest struct{}

func (e *versionRequest) Error() string { return "version" }

// parseCommand parses one full command line and translates the parser's result
// and sentinel errors back into package cli's historical shapes.
func parseCommand(spec *commandSpec, argv []string) (*parsedArgs, error) {
	parsed, err := cliargs.ParseCommand(spec, argv)
	if err != nil {
		switch typed := err.(type) {
		case *cliargs.HelpRequest[parsedArgs]:
			return nil, &helpRequest{spec: typed.Spec}
		case *cliargs.VersionRequest:
			return nil, &versionRequest{}
		case *cliargs.UsageError:
			return nil, &usageError{msg: typed.Msg, prog: typed.Prog, rootOwned: typed.RootOwned}
		}
		return nil, err
	}
	out := &parsedArgs{
		values:   parsed.Values(),
		provided: parsed.Provided(),
		rest:     parsed.Rest(),
	}
	if cmd, ok := parsed.Cmd().(*commandSpec); ok {
		out.cmd = cmd
	}
	return out, nil
}

// usageErrorBlock picks the page a usage failure prints.
func usageErrorBlock(root *commandSpec, err *usageError) string {
	return cliargs.UsageErrorBlock(root, &cliargs.UsageError{
		Msg:       err.msg,
		Prog:      err.prog,
		RootOwned: err.rootOwned,
	})
}

func findLongFlag(spec *commandSpec, name string) *argSpec {
	return cliargs.FindLongFlag(spec, name)
}

func findSubcommand(spec *commandSpec, name string) *commandSpec {
	return cliargs.FindSubcommand(spec, name)
}

func padRight(text string, width int) string {
	padding := width - len([]rune(text))
	if padding < 1 {
		// Always keep at least one separator space. A label at or over the pad
		// width used to run straight into its help text
		// (`--limit-files LIMIT_FILESMaximum number ...`), which the contract
		// capture parser reads as a mangled flag name.
		padding = 1
	}
	return text + strings.Repeat(" ", padding)
}
