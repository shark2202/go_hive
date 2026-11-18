package cli

// Parity: framework/cli.py, framework/loader/cli.py, framework/skills/cli.py

import (
	"fmt"
	"strconv"
	"strings"
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
}

type positionalSpec struct {
	Dest     string
	Required bool
	Help     string
}

type commandSpec struct {
	Prog        string
	Help        string
	Description string
	Flags       []argSpec
	Positionals []positionalSpec
	Subcommands []*commandSpec
	Dest        string
	Run         func(*parsedArgs) int
	Passthrough bool
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
}

func (e *usageError) Error() string { return e.msg }

func usageErr(prog, format string, args ...any) *usageError {
	return &usageError{msg: fmt.Sprintf(format, args...), prog: prog}
}

type helpRequest struct{ spec *commandSpec }

func (e *helpRequest) Error() string { return "help" }

type versionRequest struct{}

func (e *versionRequest) Error() string { return "version" }

func parseCommand(spec *commandSpec, argv []string) (*parsedArgs, error) {
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
		return parseGroup(spec, argv, args)
	}

	positionals := []string{}
	for i := 0; i < len(argv); {
		token := argv[i]
		if token == "--" {
			positionals = append(positionals, argv[i+1:]...)
			break
		}
		if isOptionToken(token) {
			next, err := consumeOption(spec, argv, i, args)
			if err != nil {
				return nil, err
			}
			i = next
			continue
		}
		positionals = append(positionals, token)
		i++
	}

	if len(positionals) > len(spec.Positionals) {
		return nil, usageErr(spec.Prog, "unrecognized arguments: %s",
			strings.Join(positionals[len(spec.Positionals):], " "))
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
	if len(missing) > 0 {
		return nil, usageErr(spec.Prog, "the following arguments are required: %s",
			strings.Join(missing, ", "))
	}
	args.cmd = spec
	return args, nil
}

func parseGroup(spec *commandSpec, argv []string, args *parsedArgs) (*parsedArgs, error) {
	i := 0
	for i < len(argv) && isOptionToken(argv[i]) {
		next, err := consumeOption(spec, argv, i, args)
		if err != nil {
			return nil, err
		}
		i = next
	}
	if i >= len(argv) {
		return nil, usageErr(spec.Prog, "the following arguments are required: %s",
			destOr(spec.Dest, "command"))
	}
	name := argv[i]
	sub := findSubcommand(spec, name)
	if sub == nil {
		return nil, usageErr(spec.Prog, "argument %s: invalid choice: %q (choose from %s)",
			destOr(spec.Dest, "command"), name, quotedNames(spec.Subcommands))
	}
	subArgs, err := parseCommand(sub, argv[i+1:])
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

func isOptionToken(token string) bool {
	return len(token) > 1 && token[0] == '-'
}

func consumeOption(spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
	if strings.HasPrefix(argv[i], "--") {
		return consumeLong(spec, argv, i, args)
	}
	return consumeShort(spec, argv, i, args)
}

func consumeLong(spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
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
	flag := findLongFlag(spec, body)
	if flag == nil {
		return 0, usageErr(spec.Prog, "unrecognized arguments: %s", token)
	}
	if flag.Action == "version" {
		return 0, &versionRequest{}
	}
	if flag.Kind == kindBool {
		if hasValue {
			return 0, usageErr(spec.Prog, "argument --%s: ignored explicit argument %q", body, value)
		}
		args.values[flag.Dest] = true
		args.provided[flag.Dest] = true
		return i + 1, nil
	}
	next := i
	if !hasValue {
		if i+1 >= len(argv) {
			return 0, usageErr(spec.Prog, "argument --%s: expected one argument", body)
		}
		value = argv[i+1]
		next = i + 1
	}
	if err := assignValue(spec.Prog, args, flag, value, "--"+body); err != nil {
		return 0, err
	}
	return next + 1, nil
}

func consumeShort(spec *commandSpec, argv []string, i int, args *parsedArgs) (int, error) {
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
			return 0, usageErr(spec.Prog, "unrecognized arguments: %s", token)
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
			if position+1 >= len(argv) {
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

func (c *commandSpec) helpText() string {
	var builder strings.Builder
	builder.WriteString("usage: ")
	if len(c.Subcommands) > 0 {
		fmt.Fprintf(&builder, "%s [-h] [options] {%s} ...\n", c.Prog, subcommandNames(c.Subcommands))
	} else {
		builder.WriteString(c.Prog)
		if len(c.Flags) > 0 || len(c.Positionals) > 0 {
			builder.WriteString(" [-h]")
		}
		for _, positional := range c.Positionals {
			if positional.Required {
				fmt.Fprintf(&builder, " %s", displayDest(positional.Dest))
			} else {
				fmt.Fprintf(&builder, " [%s]", displayDest(positional.Dest))
			}
		}
		builder.WriteString("\n")
	}
	if c.Description != "" {
		fmt.Fprintf(&builder, "\n%s\n", c.Description)
	}
	if c.Help != "" {
		fmt.Fprintf(&builder, "\n%s\n", c.Help)
	}
	if len(c.Subcommands) > 0 {
		builder.WriteString("\ncommands:\n")
		for _, sub := range c.Subcommands {
			fmt.Fprintf(&builder, "  %s\n", padRight(progTail(sub.Prog), 16)+sub.Help)
		}
	}
	if len(c.Positionals) > 0 {
		builder.WriteString("\npositional arguments:\n")
		for _, positional := range c.Positionals {
			fmt.Fprintf(&builder, "  %s\n", padRight(displayDest(positional.Dest), 22)+positional.Help)
		}
	}
	builder.WriteString("\noptions:\n")
	builder.WriteString("  -h, --help            show this help message and exit\n")
	for _, flag := range c.Flags {
		fmt.Fprintf(&builder, "  %s\n", padRight(flagLabel(flag), 22)+flag.Help)
	}
	return builder.String()
}

func subcommandNames(specs []*commandSpec) string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, progTail(spec.Prog))
	}
	return strings.Join(names, ",")
}

func flagLabel(flag argSpec) string {
	if flag.Long == "" {
		return "-" + flag.Short
	}
	label := "--" + flag.Long
	if flag.Short != "" {
		label += ", -" + flag.Short
	}
	switch flag.Kind {
	case kindString:
		label += " " + strings.ToUpper(strings.ReplaceAll(flag.Long, "-", "_"))
	case kindInt:
		label += " " + strings.ToUpper(strings.ReplaceAll(flag.Long, "-", "_"))
	case kindAppend:
		label += " " + strings.ToUpper(strings.ReplaceAll(flag.Long, "-", "_"))
	}
	return label
}

func padRight(text string, width int) string {
	padding := width - len([]rune(text))
	if padding <= 0 {
		return text
	}
	return text + strings.Repeat(" ", padding)
}
