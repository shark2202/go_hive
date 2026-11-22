package cli

// Parity: framework/cli.py, framework/loader/cli.py, framework/skills/cli.py

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/arghelp"
)

type argKind int

const (
	kindBool argKind = iota
	kindString
	kindInt
	kindAppend
)

type argSpec struct {
	Dest    string
	Long    string
	Short   string
	Kind    argKind
	Default any
	Help    string
	Action  string
	Metavar string
	// Choices restricts the value to a fixed set, rendered argparse-style as
	// `--flag {a,b,c}` in the usage line and the options block. Upstream
	// declares them with add_argument(choices=...).
	Choices []string
	// Required marks a flag argparse renders without brackets in the usage
	// line (`--action {left_click,...}`). Behavior (the missing-flag error)
	// stays with the command's own validation, as upstream.
	Required bool
	// Hidden keeps the flag fully parseable but out of every rendered help
	// surface (usage line, options block). It mirrors argparse's
	// `help=argparse.SUPPRESS`: upstream hides `--profile` that way, and Go
	// hides its own internal extensions (`--bridge`) so the two help surfaces
	// match token for token.
	Hidden bool
}

type positionalSpec struct {
	Dest     string
	Required bool
	Help     string
	// Metavar overrides the rendered name (argparse's metavar=); an empty
	// value renders Dest.
	Metavar string
	// Choices renders an argparse positional choice set (`{accept,dismiss}`)
	// instead of a named slot.
	Choices []string
}

type commandSpec struct {
	Prog        string
	Help        string
	Description string
	Epilog      string
	// RawDescription mirrors argparse's RawDescriptionHelpFormatter: the
	// description and epilog keep their line breaks instead of wrapping.
	RawDescription bool
	Flags          []argSpec
	Positionals    []positionalSpec
	Subcommands    []*commandSpec
	Dest           string
	Run            func(*parsedArgs) int
	Passthrough    bool
	// Hidden keeps the command resolvable by name (parse, run, and error
	// handling all work as before) but out of the parent's usage choice set
	// and commands block. It is for internal extensions that have no upstream
	// counterpart (hive queen-mail-mcp, the legacy browser verbs).
	Hidden bool
}

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

func usageErr(prog, format string, args ...any) *usageError {
	return &usageError{msg: fmt.Sprintf(format, args...), prog: prog}
}

// parseState carries the tokens argparse defers to the top-level parse_args.
//
// An unknown option string, or a positional the command has no slot for, is
// not an error where it appears: argparse records it as an "extra", finishes
// parsing the command tree, and only then the outermost parse_args raises
// "unrecognized arguments: ..." — which is why those failures print the root
// usage and the root prog prefix. A missing required argument fires inside
// the parser that declares it (argparse checks its required actions before it
// returns), so it wins over any extra collected below it.
type parseState struct {
	extras []string
}

type helpRequest struct{ spec *commandSpec }

func (e *helpRequest) Error() string { return "help" }

type versionRequest struct{}

func (e *versionRequest) Error() string { return "version" }

// parseCommand parses one full command line. It owns the outermost state, so
// the deferred extras become the root's "unrecognized arguments" failure.
func parseCommand(spec *commandSpec, argv []string) (*parsedArgs, error) {
	state := &parseState{}
	args, err := parseInto(state, spec, argv)
	if err != nil {
		return nil, err
	}
	if len(state.extras) > 0 {
		return nil, &usageError{
			msg:       "unrecognized arguments: " + strings.Join(state.extras, " "),
			prog:      spec.Prog,
			rootOwned: true,
		}
	}
	return args, nil
}

// usageBlockFor returns the usage block of the parser that owns prog — the
// page an argparse failure prints before its "<prog>: error: <message>" line.
func usageBlockFor(root *commandSpec, prog string) string {
	spec := findSpecByProg(root, prog)
	if spec == nil {
		return ""
	}
	return spec.helpSpec().UsageText()
}

// usageErrorBlock picks the page a usage failure prints. argparse's deferred
// "unrecognized arguments" failure is raised by the ROOT parser after the
// whole command tree parsed, so it prints the root page even when the
// offending token sat on a leaf.
func usageErrorBlock(root *commandSpec, err *usageError) string {
	if err.rootOwned {
		return root.helpSpec().UsageText()
	}
	return usageBlockFor(root, err.prog)
}

func parseInto(state *parseState, spec *commandSpec, argv []string) (*parsedArgs, error) {
	args := newParsedArgs()
	for _, flag := range spec.Flags {
		switch {
		case flag.Default != nil:
			args.values[flag.Dest] = flag.Default
		case flag.Kind == kindAppend:
			args.values[flag.Dest] = []string{}
		case flag.Kind == kindBool:
			args.values[flag.Dest] = false
		case flag.Kind == kindString:
			args.values[flag.Dest] = ""
		}
	}

	if spec.Passthrough {
		args.rest = append([]string{}, argv...)
		args.cmd = spec
		return args, nil
	}
	if len(spec.Subcommands) > 0 {
		return parseGroup(state, spec, argv, args)
	}

	positionals := []string{}
	for i := 0; i < len(argv); {
		token := argv[i]
		if token == "--" {
			if len(positionals) >= len(spec.Positionals) {
				// No slot left to fill: argparse leaves the separator itself
				// unmatched, so it lands in the extras with everything after.
				state.extras = append(state.extras, argv[i:]...)
				break
			}
			// argparse stops option parsing at `--`: every later token is a
			// positional (or an extra when the command has no slot left).
			for _, tail := range argv[i+1:] {
				if len(positionals) < len(spec.Positionals) {
					positionals = append(positionals, tail)
				} else {
					state.extras = append(state.extras, tail)
				}
			}
			break
		}
		if isOptionToken(token) {
			next, err := consumeOption(state, spec, argv, i, args)
			if err != nil {
				return nil, err
			}
			i = next
			continue
		}
		if len(positionals) < len(spec.Positionals) {
			positional := spec.Positionals[len(positionals)]
			// argparse checks a positional choice while consuming it, before
			// the required-action check and before the root reports extras.
			if err := checkChoice(spec.Prog, argparseName(positional.Metavar, positional.Dest),
				token, positional.Choices); err != nil {
				return nil, err
			}
			positionals = append(positionals, token)
		} else {
			state.extras = append(state.extras, token)
		}
		i++
	}

	missing := make([]string, 0)
	for index, positional := range spec.Positionals {
		if index < len(positionals) {
			args.values[positional.Dest] = positionals[index]
			args.provided[positional.Dest] = true
			continue
		}
		if _, present := args.values[positional.Dest]; present {
			continue
		}
		if positional.Required {
			missing = append(missing, displayDest(positional.Dest))
			continue
		}
		args.values[positional.Dest] = ""
	}
	// Required flags are part of the same argparse check: argparse reports
	// every missing required action in declaration order (positionals before
	// options, matching the order the specs declare them).
	for index := range spec.Flags {
		flag := &spec.Flags[index]
		if !flag.Required || args.provided[flag.Dest] {
			continue
		}
		missing = append(missing, "--"+flag.Long)
	}
	if len(missing) > 0 {
		return nil, usageErr(spec.Prog, "the following arguments are required: %s",
			strings.Join(missing, ", "))
	}
	args.cmd = spec
	return args, nil
}

// checkChoice applies argparse's _check_value: a value outside the declared
// choice set is an immediate parse error on the page that declares it.
func checkChoice(prog, name, value string, choices []string) error {
	if len(choices) == 0 {
		return nil
	}
	for _, choice := range choices {
		if value == choice {
			return nil
		}
	}
	return usageErr(prog, "argument %s: invalid choice: '%s' (choose from %s)",
		name, value, quotedChoices(choices))
}

// quotedChoices renders argparse's `(choose from 'a', 'b')` tail.
func quotedChoices(choices []string) string {
	quoted := make([]string, 0, len(choices))
	for _, choice := range choices {
		quoted = append(quoted, "'"+choice+"'")
	}
	return strings.Join(quoted, ", ")
}

// argparseName is the name argparse uses for an action in its messages: the
// explicit metavar, else the destination.
func argparseName(metavar, dest string) string {
	if metavar != "" {
		return metavar
	}
	return dest
}

func parseGroup(state *parseState, spec *commandSpec, argv []string, args *parsedArgs) (*parsedArgs, error) {
	i := 0
	for i < len(argv) && isOptionToken(argv[i]) && argv[i] != "--" {
		next, err := consumeOption(state, spec, argv, i, args)
		if err != nil {
			return nil, err
		}
		i = next
	}
	if i < len(argv) && argv[i] == "--" {
		// argparse only swallows the separator when a positional value
		// follows; handed a value after `--`, the subcommand choice becomes
		// "--" itself and the choice check fails.
		if i+1 < len(argv) {
			return nil, usageErr(spec.Prog, "argument %s: invalid choice: '--' (choose from %s)",
				destOr(spec.Dest, "command"), quotedNames(spec.Subcommands))
		}
		i++
	}
	if i >= len(argv) {
		// argparse checks its required actions after consuming every token,
		// before the root reports extras: an unknown flag cannot hide the
		// missing required subcommand.
		return nil, usageErr(spec.Prog, "the following arguments are required: %s",
			destOr(spec.Dest, "command"))
	}
	name := argv[i]
	sub := findSubcommand(spec, name)
	if sub == nil {
		return nil, usageErr(spec.Prog, "argument %s: invalid choice: '%s' (choose from %s)",
			destOr(spec.Dest, "command"), name, quotedNames(spec.Subcommands))
	}
	subArgs, err := parseInto(state, sub, argv[i+1:])
	if err != nil {
		return nil, err
	}
	for key, value := range args.values {
		if _, present := subArgs.values[key]; !present {
			subArgs.values[key] = value
		}
	}
	return subArgs, nil
}

// isOptionToken reports whether the token occupies argparse's 'O' pattern
// slot: it starts with `-`, is longer than one character, and is not a
// negative number (argparse treats `-5` as a value, not an option). The same
// test decides whether a value-taking flag may consume the next token: a flag
// matched against the following pattern refuses an `O` token and reports
// "expected one argument" instead of swallowing it as a value.
func isOptionToken(token string) bool {
	return len(token) > 1 && token[0] == '-' && !isNegativeNumber(token)
}

var negativeNumberPattern = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

func isNegativeNumber(token string) bool {
	return negativeNumberPattern.MatchString(token)
}

func consumeOption(state *parseState, spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
	if strings.HasPrefix(argv[i], "--") {
		return consumeLong(state, spec, argv, i, args)
	}
	return consumeShort(state, spec, argv, i, args)
}

func consumeLong(state *parseState, spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
	token := argv[i]
	body := token[2:]
	value := ""
	hasValue := false
	if equal := strings.IndexByte(body, '='); equal >= 0 {
		value = body[equal+1:]
		body = body[:equal]
		hasValue = true
	}
	if body == "help" {
		return 0, &helpRequest{spec: spec}
	}
	flag, err := resolveLongFlag(spec, body)
	if err != nil {
		return 0, err
	}
	if flag == nil {
		// An option string this parser does not know is an extra: argparse
		// records the whole token (including any `=value` tail) and keeps
		// parsing so the root can report it once the tree is parsed.
		state.extras = append(state.extras, token)
		return i + 1, nil
	}
	if flag.Action == "version" {
		return 0, &versionRequest{}
	}
	if flag.Kind == kindBool {
		if hasValue {
			return 0, usageErr(spec.Prog, "argument --%s: ignored explicit argument '%s'", flag.Long, value)
		}
		args.values[flag.Dest] = true
		args.provided[flag.Dest] = true
		return i + 1, nil
	}
	next := i
	if !hasValue {
		if i+1 >= len(argv) || isOptionToken(argv[i+1]) {
			// The message names the resolved option: a prefix abbreviation
			// reports the full long name, as argparse's matched action does.
			return 0, usageErr(spec.Prog, "argument --%s: expected one argument", flag.Long)
		}
		value = argv[i+1]
		next = i + 1
	}
	if err := assignValue(spec.Prog, args, flag, value, "--"+flag.Long); err != nil {
		return 0, err
	}
	return next + 1, nil
}

// resolveLongFlag looks a long option up the way argparse's _parse_optional
// does: an exact match first, then (allow_abbrev, which is on by default) a
// unique prefix match. Several prefix matches are argparse's "ambiguous
// option" error, and no match at all returns nil so the caller can record an
// extra.
func resolveLongFlag(spec *commandSpec, body string) (*argSpec, error) {
	if body == "" {
		return nil, nil
	}
	if flag := findLongFlag(spec, body); flag != nil {
		return flag, nil
	}
	matches := make([]string, 0, 2)
	for index := range spec.Flags {
		if long := spec.Flags[index].Long; long != "" && strings.HasPrefix(long, body) {
			matches = append(matches, "--"+long)
		}
	}
	if strings.HasPrefix("help", body) {
		// -h/--help is argparse's own add_help action; it abbreviates too.
		matches = append(matches, "--help")
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		name := matches[0][2:]
		if name == "help" {
			return nil, nil
		}
		return findLongFlag(spec, name), nil
	default:
		return nil, usageErr(spec.Prog, "ambiguous option: --%s could match %s", body, strings.Join(matches, ", "))
	}
}

func consumeShort(state *parseState, spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
	token := argv[i]
	rest := token[1:]
	position := i
	for len(rest) > 0 {
		letter := rest[:1]
		rest = rest[1:]
		flag := findShortFlag(spec, letter)
		if flag == nil {
			if letter == "h" {
				return 0, &helpRequest{spec: spec}
			}
			// The remainder from the unknown letter on is one extra
			// (argparse: `-bc` for an unknown `b` in `-abc`).
			state.extras = append(state.extras, "-"+letter+rest)
			return position + 1, nil
		}
		if flag.Action == "version" {
			return 0, &versionRequest{}
		}
		if flag.Kind == kindBool {
			args.values[flag.Dest] = true
			args.provided[flag.Dest] = true
			continue
		}
		value := ""
		if len(rest) > 0 {
			if rest[0] == '=' {
				rest = rest[1:]
			}
			value = rest
			rest = ""
		} else {
			if position+1 >= len(argv) || isOptionToken(argv[position+1]) {
				return 0, usageErr(spec.Prog, "argument -%s: expected one argument", letter)
			}
			value = argv[position+1]
			position++
		}
		if err := assignValue(spec.Prog, args, flag, value, "-"+letter); err != nil {
			return 0, err
		}
	}
	return position + 1, nil
}

func assignValue(prog string, args *parsedArgs, flag *argSpec, value, rendered string) error {
	switch flag.Kind {
	case kindInt:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return usageErr(prog, "argument %s: invalid int value: %s", rendered, pyRepr(value))
		}
		args.values[flag.Dest] = parsed
	case kindAppend:
		existing := args.list(flag.Dest)
		args.values[flag.Dest] = append(append([]string{}, existing...), value)
	default:
		args.values[flag.Dest] = value
	}
	if err := checkChoice(prog, rendered, value, flag.Choices); err != nil {
		return err
	}
	args.provided[flag.Dest] = true
	return nil
}

func findLongFlag(spec *commandSpec, name string) *argSpec {
	for index := range spec.Flags {
		if spec.Flags[index].Long == name {
			return &spec.Flags[index]
		}
	}
	return nil
}

func findShortFlag(spec *commandSpec, letter string) *argSpec {
	for index := range spec.Flags {
		if spec.Flags[index].Short == letter {
			return &spec.Flags[index]
		}
	}
	return nil
}

func findSubcommand(spec *commandSpec, name string) *commandSpec {
	for _, sub := range spec.Subcommands {
		if sub.Dest == name {
			return sub
		}
		if tail := progTail(sub.Prog); tail == name {
			return sub
		}
	}
	return nil
}

func progTail(prog string) string {
	parts := strings.Fields(prog)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func quotedNames(specs []*commandSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			// Hidden commands stay out of the invalid-choice listing too: the
			// message is part of the observable help surface.
			continue
		}
		names = append(names, "'"+progTail(spec.Prog)+"'")
	}
	return strings.Join(names, ", ")
}

func destOr(dest, fallback string) string {
	if dest == "" {
		return fallback
	}
	return dest
}

func displayDest(dest string) string {
	return dest
}

// helpText renders the page the way argparse's HelpFormatter would: usage
// (wrapped at the terminal width), description, `positional arguments:` with
// subcommand choices, `options:` with a dynamic label column, then the epilog.
// It is the one help renderer for `hive`, `hive-browser` and `hive-cred`.
func (c *commandSpec) helpText() string {
	return c.helpSpec().Text()
}

// helpSpec projects the command tree onto arghelp's input model.
func (c *commandSpec) helpSpec() arghelp.Spec {
	spec := arghelp.Spec{
		Prog:           c.Prog,
		Description:    c.Description,
		Epilog:         c.Epilog,
		RawDescription: c.RawDescription,
	}
	// usage: [-h] then each visible optional, in declaration order (argparse
	// lists optionals before positionals and the subcommand choice set last).
	spec.UsageOptionals = append(spec.UsageOptionals, "[-h]")
	for _, flag := range c.Flags {
		if flag.Hidden {
			continue
		}
		spec.UsageOptionals = append(spec.UsageOptionals, flagUsageParts(flag)...)
	}
	if len(c.Subcommands) > 0 {
		spec.UsagePositionals = append(spec.UsagePositionals, "{"+subcommandNames(c.Subcommands)+"}", "...")
	}
	for _, positional := range c.Positionals {
		spec.UsagePositionals = append(spec.UsagePositionals, usagePositionalLabel(positional))
	}

	// options: -h first, then the declared flags.
	spec.Options = append(spec.Options, arghelp.Option{
		Label: "-h, --help",
		Help:  "show this help message and exit",
	})
	for _, flag := range c.Flags {
		if flag.Hidden {
			continue
		}
		spec.Options = append(spec.Options, arghelp.Option{Label: flagLabel(flag), Help: flag.Help})
	}

	// positional arguments: real argv slots, then the choice set.
	for _, positional := range c.Positionals {
		spec.Positionals = append(spec.Positionals, arghelp.Positional{
			Label: positionalLabel(positional),
			Help:  positional.Help,
		})
	}
	for _, sub := range c.Subcommands {
		if sub.Hidden {
			continue
		}
		spec.Choices = append(spec.Choices, arghelp.Choice{Name: progTail(sub.Prog), Help: sub.Help})
	}
	return spec
}

// flagUsageParts renders one flag's usage contribution the way argparse's
// _format_actions_usage does: a bracketed option is one part, while a required
// option taking a value splits into `--flag` and its metavar so the line can
// wrap between them.
func flagUsageParts(flag argSpec) []string {
	if flag.Kind == kindBool || flag.Action == "version" {
		if flag.Required {
			return []string{"-" + shortOrLong(flag)}
		}
		return []string{"[" + "-" + shortOrLong(flag) + "]"}
	}
	value := flagMetavar(flag)
	if flag.Required {
		return []string{"--" + flag.Long, value}
	}
	return []string{"[--" + flag.Long + " " + value + "]"}
}

// shortOrLong prefers the long name, matching the rendered label.
func shortOrLong(flag argSpec) string {
	if flag.Long != "" {
		return "-" + flag.Long
	}
	return flag.Short
}

func usagePositionalLabel(positional positionalSpec) string {
	label := positionalLabel(positional)
	if positional.Required {
		return label
	}
	return "[" + label + "]"
}

func positionalLabel(positional positionalSpec) string {
	if len(positional.Choices) > 0 {
		return "{" + strings.Join(positional.Choices, ",") + "}"
	}
	if positional.Metavar != "" {
		return positional.Metavar
	}
	return displayDest(positional.Dest)
}

// flagMetavar renders a flag's value placeholder: an explicit metavar, the
// choices set, or the upper-cased destination (argparse's default).
func flagMetavar(flag argSpec) string {
	if flag.Metavar != "" {
		return flag.Metavar
	}
	if len(flag.Choices) > 0 {
		return "{" + strings.Join(flag.Choices, ",") + "}"
	}
	return strings.ToUpper(strings.ReplaceAll(flag.Long, "-", "_"))
}

func subcommandNames(specs []*commandSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		names = append(names, progTail(spec.Prog))
	}
	return strings.Join(names, ",")
}

// flagLabel renders one options-block entry. argparse repeats the metavar for
// every alias and lists the option strings in declaration order; upstream
// declares `--long` before `-s` everywhere (`-h, --help` is argparse's own
// add_help action, which is why help is special-cased to short-first).
func flagLabel(flag argSpec) string {
	names := make([]string, 0, 2)
	if flag.Long != "" {
		names = append(names, "--"+flag.Long)
	}
	if flag.Short != "" {
		names = append(names, "-"+flag.Short)
	}
	if len(names) == 0 {
		return ""
	}
	if flag.Kind == kindBool || flag.Action == "version" {
		return strings.Join(names, ", ")
	}
	metavar := flagMetavar(flag)
	for index, name := range names {
		names[index] = name + " " + metavar
	}
	return strings.Join(names, ", ")
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
