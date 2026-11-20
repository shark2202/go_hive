package nativecli

import (
	"fmt"
	"strconv"
	"strings"
)

const progName = "mcp2cli"

// argparseWidth is argparse's default help width (80 columns minus 2).
const argparseWidth = 78

// maxHelpPosition is argparse's default cap on the help column.
const maxHelpPosition = 24

// helpRow is one option entry in a rendered help screen.
type helpRow struct {
	invocation string
	help       string
}

// renderHelp lays out an argparse-style help screen: usage, optional
// description, then the options section with its two-column layout.
func renderHelp(prefix string, items []string, description string, rows []helpRow) string {
	var builder strings.Builder
	builder.WriteString("usage: ")
	builder.WriteString(prefix)
	for index, item := range items {
		if index == 0 {
			builder.WriteString(" ")
		} else {
			builder.WriteString(" ")
		}
		builder.WriteString(item)
	}
	usage := wrapUsage(builder.String(), len("usage: ")+len(prefix))
	if description != "" {
		// argparse wraps the description block at the full width, unindented.
		usage += "\n\n" + strings.TrimSuffix(wrapHelp(description, 0), "\n")
	}
	usage += "\n\noptions:\n"

	helpPosition := 0
	for _, row := range rows {
		if len(row.invocation) > helpPosition {
			helpPosition = len(row.invocation)
		}
	}
	helpPosition += 4
	if helpPosition > maxHelpPosition {
		helpPosition = maxHelpPosition
	}

	for _, row := range rows {
		invocation := "  " + row.invocation
		if row.help == "" {
			usage += invocation + "\n"
			continue
		}
		if len(invocation) <= helpPosition-2 {
			usage += invocation + strings.Repeat(" ", helpPosition-2-len(invocation)) + "  "
			usage += wrapHelp(row.help, helpPosition)
			continue
		}
		usage += invocation + "\n" + strings.Repeat(" ", helpPosition)
		usage += wrapHelp(row.help, helpPosition)
	}
	return usage
}

// wrapUsage wraps a usage line at argparse's width, aligning continuation
// lines under the first item.
func wrapUsage(line string, indent int) string {
	if len(line) <= argparseWidth {
		return line
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return line
	}
	var builder strings.Builder
	builder.WriteString(words[0])
	column := len(words[0])
	pad := strings.Repeat(" ", indent)
	for _, word := range words[1:] {
		if column+1+len(word) > argparseWidth {
			builder.WriteString("\n" + pad)
			column = indent
		} else {
			builder.WriteString(" ")
			column++
		}
		builder.WriteString(word)
		column += len(word)
	}
	return builder.String()
}

// wrapHelp wraps help text at argparse's width with continuation lines
// indented to the help column.
func wrapHelp(text string, indent int) string {
	width := argparseWidth - indent
	if width < 11 {
		width = 11
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return "\n"
	}
	var builder strings.Builder
	column := 0
	for index, word := range words {
		if index == 0 {
			builder.WriteString(word)
			column = len(word)
			continue
		}
		if column+1+len(word) > width {
			builder.WriteString("\n" + strings.Repeat(" ", indent))
			builder.WriteString(word)
			column = len(word)
			continue
		}
		builder.WriteString(" ")
		builder.WriteString(word)
		column += 1 + len(word)
	}
	builder.WriteString("\n")
	return builder.String()
}

// toolUsage builds the "mcp2cli <tool> ..." usage prefix and items.
func toolUsage(command *Command) (string, []string) {
	items := []string{"[-h]"}
	if command.HasBody {
		items = append(items, "[--stdin]")
	}
	for _, param := range command.Params {
		name := param.cliName()
		if param.Kind == kindBool {
			items = append(items, "[--"+name+"]")
			continue
		}
		items = append(items, "[--"+name+" "+strings.ToUpper(paramDest(param))+"]")
	}
	return progName + " " + command.Name, items
}

// renderToolHelp renders `mcp2cli <tool> --help`.
func renderToolHelp(command *Command) string {
	prefix, items := toolUsage(command)
	rows := []helpRow{{invocation: "-h, --help", help: "show this help message and exit"}}
	if command.HasBody {
		rows = append(rows, helpRow{
			invocation: "--stdin",
			help:       "Read JSON body/arguments from stdin; parameter flags are merged in (a conflicting value is an error)",
		})
	}
	for _, param := range command.Params {
		invocation := "--" + param.cliName()
		if param.Kind != kindBool {
			invocation += " " + strings.ToUpper(paramDest(param))
		}
		rows = append(rows, helpRow{invocation: invocation, help: param.Description})
	}
	return renderHelp(prefix, items, command.Description, rows)
}

// mainUsage renders the top-level usage line for the global parser.
func mainUsage(commands []Command) string {
	items := []string{}
	if len(commands) == 0 {
		items = append(items, "[-h]", "[--version]", "[--mcp MCP]", "[--mcp-stdio MCP_STDIO]", "...")
	} else {
		names := make([]string, 0, len(commands))
		for _, command := range commands {
			names = append(names, command.Name)
		}
		items = append(items, "[-h]", "{"+strings.Join(names, ",")+"}", "...")
	}
	return wrapUsage("usage: "+progName+" "+strings.Join(items, " "), len("usage: "+progName+" "))
}

// renderCommandHelp renders the parser help for `mcp2cli <server> --help`:
// the usage line, the description, the tool list and the built-in options.
func renderCommandHelp(commands []Command) string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	choices := "{" + strings.Join(names, ",") + "}"
	var builder strings.Builder
	builder.WriteString(wrapUsage("usage: "+progName+" [-h] "+choices+" ...", len("usage: "+progName+" ")))
	builder.WriteString("\n\nTurn any MCP server or OpenAPI spec into a CLI\n\n")
	builder.WriteString("positional arguments:\n  " + choices + "\n")
	for _, command := range commands {
		line := "    " + command.Name
		if len(line) < 24 {
			line += strings.Repeat(" ", 24-len(line))
		} else {
			line += "\n" + strings.Repeat(" ", 24)
		}
		builder.WriteString(line)
		builder.WriteString(wrapHelp(command.Description, 24))
	}
	builder.WriteString("\noptions:\n")
	builder.WriteString("  -h, --help            show this help message and exit\n")
	return builder.String()
}

// renderMainHelp renders the help shown when no tool subcommand is given.
func renderMainHelp(commands []Command, globalRows []helpRow) string {
	items := []string{"[-h]"}
	if len(commands) > 0 {
		names := make([]string, 0, len(commands))
		for _, command := range commands {
			names = append(names, command.Name)
		}
		items = append(items, "{"+strings.Join(names, ",")+"}", "...")
	} else {
		items = append(items, "[--version]", "[--mcp MCP]", "[--mcp-stdio MCP_STDIO]", "...")
	}
	return renderHelp(progName, items, "Turn any MCP server or OpenAPI spec into a CLI",
		append(globalRows, helpRow{invocation: "--version", help: "show program's version number and exit"}))
}

// defaultGlobalRows describes the options the native CLI accepts, for the
// top-level help screen.
func defaultGlobalRows() []helpRow {
	return []helpRow{
		{invocation: "--mcp MCP", help: "MCP server URL (HTTP/SSE)"},
		{invocation: "--mcp-stdio MCP_STDIO", help: "MCP server command (stdio)"},
		{invocation: "--auth-header AUTH_HEADER", help: "HTTP header as Name:Value (repeatable). Value supports env:VAR and file:/path prefixes"},
		{invocation: "--cache-key CACHE_KEY", help: "Custom cache key"},
		{invocation: "--cache-ttl CACHE_TTL", help: "Cache TTL in seconds"},
		{invocation: "--refresh", help: "Force re-fetch spec"},
		{invocation: "--list", help: "List available subcommands"},
		{invocation: "--search PATTERN", help: "Search tools by name or description (case-insensitive substring match)"},
		{invocation: "--verbose", help: "Show full tool descriptions in --list output, wrapped to terminal width (default: truncated with ...)"},
		{invocation: "--sort {usage,recent,alpha,default}", help: "Sort order for --list output."},
		{invocation: "--top N", help: "Show only the top N tools in --list output (useful for LLM agents)"},
		{invocation: "--compact", help: "Space-separated tool names only, no descriptions (~2 tokens/tool)"},
		{invocation: "--pretty", help: "Pretty-print JSON output"},
		{invocation: "--raw", help: "Print raw response body"},
		{invocation: "--json", help: "Force valid JSON output for every command."},
		{invocation: "--head N", help: "Limit output to first N records (arrays) or N lines (text)"},
		{invocation: "--transport {auto,sse,streamable}", help: "MCP HTTP transport."},
		{invocation: "--env ENV", help: "Environment variable KEY=VALUE for MCP stdio (repeatable)"},
		{invocation: "--root PATH|FILE_URI", help: "Expose a filesystem path or file:// URI to the server (repeatable)."},
		{invocation: "--list-resources", help: "List available resources"},
		{invocation: "--list-resource-templates", help: "List resource templates"},
		{invocation: "--read-resource URI", help: "Read a resource by URI"},
		{invocation: "--list-prompts", help: "List available prompts"},
		{invocation: "--get-prompt NAME", help: "Get a prompt by name"},
		{invocation: "--prompt-arg KEY=VALUE", help: "Argument for --get-prompt (repeatable)"},
	}
}

// usageFailure builds the argparse-style two-line error for the tool parser.
func toolUsageFailure(command *Command, message string) *usageError {
	prefix, items := toolUsage(command)
	return &usageError{
		prog:    progName + " " + command.Name,
		message: message,
		usage:   strings.Join(append([]string{prefix}, items...), " "),
	}
}

func invalidValueMessage(flag string, kind paramKind, value string) string {
	switch kind {
	case kindInt, kindUnion:
		return fmt.Sprintf("argument %s: invalid %s value: %s", flag, "int", pyRepr(value))
	case kindFloat:
		return fmt.Sprintf("argument %s: invalid %s value: %s", flag, "float", pyRepr(value))
	default:
		return fmt.Sprintf("argument %s: invalid value: %s", flag, pyRepr(value))
	}
}

// parseToolArgs parses the tool subcommand's arguments, mirroring
// build_argparse plus argparse's conversion errors.
func parseToolArgs(command *Command, argv []string) (map[string]any, bool, bool, []string, *usageError) {
	values := map[string]any{}
	stdin := false
	extras := []string{}
	paramsByFlag := map[string]Param{}
	for _, param := range command.Params {
		paramsByFlag[param.cliName()] = param
	}

	for index := 0; index < len(argv); index++ {
		arg := argv[index]
		if arg == "--" {
			extras = append(extras, argv[index+1:]...)
			break
		}
		if arg == "-h" || arg == "--help" {
			return values, stdin, true, extras, nil
		}
		if arg == "--stdin" && command.HasBody {
			stdin = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			extras = append(extras, arg)
			continue
		}
		name, inline, hasInline := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if before, after, found := strings.Cut(arg, "="); found {
				name, inline, hasInline = before, after, true
			}
		}
		param, known := paramsByFlag[strings.TrimPrefix(name, "--")]
		if param.Name == "" || !known || !strings.HasPrefix(name, "--") {
			extras = append(extras, arg)
			continue
		}
		flag := "--" + param.cliName()
		if param.Kind == kindBool {
			if hasInline {
				return nil, false, false, nil, toolUsageFailure(command,
					fmt.Sprintf("argument %s: ignored explicit argument %s", flag, pyRepr(inline)))
			}
			values[paramDest(param)] = true
			continue
		}
		value := inline
		if !hasInline {
			if index+1 >= len(argv) {
				return nil, false, false, nil, toolUsageFailure(command,
					fmt.Sprintf("argument %s: expected one argument", flag))
			}
			index++
			value = argv[index]
		}
		converted, err := convertArg(value, param.Kind)
		if err != nil {
			return nil, false, false, nil, toolUsageFailure(command, invalidValueMessage(flag, param.Kind, value))
		}
		values[paramDest(param)] = converted
	}
	return values, stdin, false, extras, nil
}

// convertArg applies the argparse type function for a parameter.
func convertArg(value string, kind paramKind) (any, error) {
	switch kind {
	case kindInt:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	case kindFloat:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	case kindUnion:
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return parsed, nil
		}
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return parsed, nil
		}
		return value, nil
	default:
		return value, nil
	}
}
