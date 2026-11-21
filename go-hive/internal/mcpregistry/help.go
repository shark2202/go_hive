package mcpregistry

// Parity: framework/loader/mcp_registry_cli.py::register_mcp_commands
//
// Per-verb help pages. Upstream registers all 14 verbs as argparse subparsers,
// so `hive mcp <verb> --help` renders that verb's own usage/positionals/options
// page. The Go CLI used to fold every verb's help into the group page; these
// specs restore the per-verb pages through the same arghelp formatter the rest
// of the CLI uses, so the layout (wrapping, label column, section names) matches
// argparse at any COLUMNS.

import "github.com/aden-hive/hive-go/internal/arghelp"

// MCPHelpText renders the `hive mcp` group page: usage + the choice set +
// options. Upstream's group parser declares no description or flags of its own.
func MCPHelpText() string { return mcpGroupSpec().Text() }

// MCPGroupUsageText is the usage block alone: a bare `hive mcp` prints it on
// stderr before argparse's missing-argument error (exit 2).
func MCPGroupUsageText() string { return mcpGroupSpec().UsageText() }

// MCPVerbHelpText renders the page for one verb; unknown topics fall back to
// the group page (`hive mcp help` with no topic).
func MCPVerbHelpText(topic string) string {
	if spec, ok := mcpVerbSpecs()[topic]; ok {
		return spec.Text()
	}
	return MCPHelpText()
}

var mcpVerbOrder = []string{
	"install", "add", "mode", "run", "remove", "enable", "disable",
	"list", "info", "config", "search", "health", "init", "update",
}

var mcpVerbSummary = map[string]string{
	"install": "Install a server from the registry",
	"add":     "Register a local/running MCP server",
	"mode":    "Reach a server from the terminal (cli) or as tools",
	"run":     "Run a server's tools as commands",
	"remove":  "Remove an installed server",
	"enable":  "Enable a disabled server",
	"disable": "Disable a server without removing it",
	"list":    "List servers",
	"info":    "Show server details",
	"config":  "Set server configuration overrides",
	"search":  "Search the registry",
	"health":  "Check server health",
	"init":    "Initialize the local MCP registry and seed built-in servers",
	"update":  "Update installed servers or refresh the registry index",
}

func mcpGroupSpec() arghelp.Spec {
	spec := arghelp.Spec{
		Prog:             "hive mcp",
		UsageOptionals:   []string{"[-h]"},
		UsagePositionals: []string{"{" + joinNames(mcpVerbOrder) + "}", "..."},
		Options:          []arghelp.Option{{Label: "-h, --help", Help: "show this help message and exit"}},
	}
	for _, name := range mcpVerbOrder {
		spec.Choices = append(spec.Choices, arghelp.Choice{Name: name, Help: mcpVerbSummary[name]})
	}
	return spec
}

func joinNames(names []string) string {
	out := ""
	for index, name := range names {
		if index > 0 {
			out += ","
		}
		out += name
	}
	return out
}

func mcpVerbSpecs() map[string]arghelp.Spec {
	helpFlag := arghelp.Option{Label: "-h, --help", Help: "show this help message and exit"}
	specs := map[string]arghelp.Spec{}

	specs["install"] = arghelp.Spec{
		Prog:             "hive mcp install",
		UsageOptionals:   []string{"[-h]", "[--version VERSION]", "[--transport TRANSPORT]"},
		UsagePositionals: []string{"name"},
		Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name in the registry"}},
		Options: []arghelp.Option{
			helpFlag,
			{Label: "--version VERSION", Help: "Pin to a specific version"},
			{Label: "--transport TRANSPORT", Help: "Override default transport (stdio, http, unix, sse)"},
		},
	}

	specs["add"] = arghelp.Spec{
		Prog: "hive mcp add",
		UsageOptionals: []string{
			"[-h]", "[--name NAME]", "[--transport {stdio,http,unix,sse}]",
			"[--url URL]", "[--command COMMAND]", "[--args [ARGS ...]]",
			"[--socket-path SOCKET_PATH]", "[--description DESCRIPTION]",
			"[--env KEY=VAL [KEY=VAL ...]]", "[--cwd CWD]", "[--as-cli]",
			"[--from FROM_MANIFEST]",
		},
		UsagePositionals: []string{"[-- COMMAND [ARG ...] ...]"},
		Positionals: []arghelp.Positional{{
			Label: "-- COMMAND [ARG ...]",
			Help:  "The stdio command and its arguments, after --; arguments may start with -",
		}},
		Options: []arghelp.Option{
			helpFlag,
			{Label: "--name NAME", Help: "Server name"},
			{Label: "--transport {stdio,http,unix,sse}", Help: "Transport type"},
			{Label: "--url URL", Help: "Server URL (http, unix, sse)"},
			{Label: "--command COMMAND", Help: "Command to run (stdio)"},
			{Label: "--args [ARGS ...]", Help: "Command arguments (stdio)"},
			{Label: "--socket-path SOCKET_PATH", Help: "Unix socket path"},
			{Label: "--description DESCRIPTION", Help: "Server description"},
			{Label: "--env KEY=VAL [KEY=VAL ...]", Help: "Environment variables (stdio)"},
			{Label: "--cwd CWD", Help: "Working directory (stdio)"},
			{Label: "--as-cli", Help: "Agents run it from the terminal (hive mcp run) instead of loading its tools"},
			{Label: "--from FROM_MANIFEST", Help: "Path to manifest.json"},
		},
	}

	specs["mode"] = arghelp.Spec{
		Prog:             "hive mcp mode",
		UsageOptionals:   []string{"[-h]"},
		UsagePositionals: []string{"name", "{cli,tools}"},
		Positionals: []arghelp.Positional{
			{Label: "name", Help: "Server name"},
			{Label: "{cli,tools}", Help: "cli: agents run `hive mcp run`; tools: load its tools"},
		},
		Options: []arghelp.Option{helpFlag},
	}

	specs["run"] = arghelp.Spec{
		Prog:             "hive mcp run",
		UsageOptionals:   []string{"[-h]"},
		UsagePositionals: []string{"name", "..."},
		Positionals: []arghelp.Positional{
			{Label: "name", Help: "Server name"},
			{Label: "mcp2cli_args", Help: "mcp2cli arguments (default: --list)"},
		},
		Options: []arghelp.Option{helpFlag},
		Description: "Run an installed MCP server's tools as commands, through mcp2cli. Examples: " +
			"hive mcp run NAME --list; hive mcp run NAME TOOL --help; hive mcp run NAME TOOL --flag value",
	}

	for _, name := range []string{"remove", "enable", "disable"} {
		specs[name] = arghelp.Spec{
			Prog:             "hive mcp " + name,
			UsageOptionals:   []string{"[-h]"},
			UsagePositionals: []string{"name"},
			Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name"}},
			Options:          []arghelp.Option{helpFlag},
		}
	}

	specs["list"] = arghelp.Spec{
		Prog:           "hive mcp list",
		UsageOptionals: []string{"[-h]", "[--available]", "[--json]"},
		Options: []arghelp.Option{
			helpFlag,
			{Label: "--available", Help: "Show available servers from registry"},
			{Label: "--json", Help: "Output as JSON"},
		},
	}

	specs["info"] = arghelp.Spec{
		Prog:             "hive mcp info",
		UsageOptionals:   []string{"[-h]", "[--json]"},
		UsagePositionals: []string{"name"},
		Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name"}},
		Options:          []arghelp.Option{helpFlag, {Label: "--json", Help: "Output as JSON"}},
	}

	specs["config"] = arghelp.Spec{
		Prog: "hive mcp config",
		UsageOptionals: []string{
			"[-h]", "[--set KEY=VAL [KEY=VAL ...]]", "[--set-header KEY=VAL [KEY=VAL ...]]",
		},
		UsagePositionals: []string{"name"},
		Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name"}},
		Options: []arghelp.Option{
			helpFlag,
			{Label: "--set KEY=VAL [KEY=VAL ...]", Help: "Set environment variable overrides"},
			{Label: "--set-header KEY=VAL [KEY=VAL ...]", Help: "Set header overrides"},
		},
	}

	specs["search"] = arghelp.Spec{
		Prog:             "hive mcp search",
		UsageOptionals:   []string{"[-h]", "[--json]"},
		UsagePositionals: []string{"query"},
		Positionals:      []arghelp.Positional{{Label: "query", Help: "Search term (name, tag, description, tool name)"}},
		Options:          []arghelp.Option{helpFlag, {Label: "--json", Help: "Output as JSON"}},
	}

	specs["health"] = arghelp.Spec{
		Prog:             "hive mcp health",
		UsageOptionals:   []string{"[-h]", "[--json]"},
		UsagePositionals: []string{"[name]"},
		Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name (all if omitted)"}},
		Options:          []arghelp.Option{helpFlag, {Label: "--json", Help: "Output as JSON"}},
	}

	specs["init"] = arghelp.Spec{
		Prog:           "hive mcp init",
		UsageOptionals: []string{"[-h]"},
		Options:        []arghelp.Option{helpFlag},
	}

	specs["update"] = arghelp.Spec{
		Prog:             "hive mcp update",
		UsageOptionals:   []string{"[-h]"},
		UsagePositionals: []string{"[name]"},
		Positionals:      []arghelp.Positional{{Label: "name", Help: "Server name to update (omit to update all registry servers)"}},
		Options:          []arghelp.Option{helpFlag},
	}

	return specs
}
