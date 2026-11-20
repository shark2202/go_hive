package nativecli

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is the mcp2cli release this implementation reproduces.
const Version = "3.8.0"

// DefaultCacheTTL mirrors mcp2cli's one-hour tool-list cache.
const DefaultCacheTTL = 3600

// Options holds the parsed global (pre-subcommand) arguments.
type Options struct {
	MCPServerURL string
	MCPStdio     string

	AuthHeaders []string
	CacheKey    string
	CacheTTL    int
	Refresh     bool

	List          bool
	Search        string
	Verbose       bool
	Sort          string
	Top           int
	Compact       bool
	Pretty        bool
	Raw           bool
	JSON          bool
	Toon          bool
	Head          int
	Transport     string
	Env           []string
	Roots         []string
	ListResources bool
	ListTemplates bool
	ReadResource  string
	ListPrompts   bool
	GetPrompt     string
	PromptArgs    []string

	// Unsupported mcp2cli modes and flags, tracked so callers can fall back
	// to the reference binary instead of silently changing behaviour.
	Spec         string
	GraphQL      string
	BaseURL      string
	Fields       string
	Complete     string
	OAuth        bool
	OAuthOther   bool
	SessionMode  bool
	SessionStart string
	Version      bool
}

// usageError is an argparse-style failure: two lines on stderr and exit 2.
type usageError struct {
	prog    string
	message string
	usage   string
}

func (e *usageError) Error() string { return e.message }

func (e *usageError) render() string {
	if e.usage != "" {
		return "usage: " + e.usage + "\n" + e.prog + ": error: " + e.message + "\n"
	}
	return e.prog + ": error: " + e.message + "\n"
}

// optionSpec describes one global option for the tiny argparse engine below.
type optionSpec struct {
	names   []string
	dest    string
	value   bool // consumes a following token
	append  bool
	integer bool
	choice  []string
	flag    bool
}

var globalOptionSpecs = []optionSpec{
	{names: []string{"--spec"}, dest: "spec", value: true},
	{names: []string{"--mcp"}, dest: "mcp", value: true},
	{names: []string{"--mcp-stdio"}, dest: "mcp_stdio", value: true},
	{names: []string{"--graphql"}, dest: "graphql", value: true},
	{names: []string{"--auth-header"}, dest: "auth_header", value: true, append: true},
	{names: []string{"--base-url"}, dest: "base_url", value: true},
	{names: []string{"--cache-key"}, dest: "cache_key", value: true},
	{names: []string{"--cache-ttl"}, dest: "cache_ttl", value: true, integer: true},
	{names: []string{"--refresh"}, dest: "refresh", flag: true},
	{names: []string{"--list"}, dest: "list", flag: true},
	{names: []string{"--search"}, dest: "search", value: true},
	{names: []string{"--verbose"}, dest: "verbose", flag: true},
	{names: []string{"--sort"}, dest: "sort", value: true, choice: []string{"usage", "recent", "alpha", "default"}},
	{names: []string{"--top"}, dest: "top", value: true, integer: true},
	{names: []string{"--compact"}, dest: "compact", flag: true},
	{names: []string{"--pretty"}, dest: "pretty", flag: true},
	{names: []string{"--raw"}, dest: "raw", flag: true},
	{names: []string{"--json"}, dest: "json", flag: true},
	{names: []string{"--toon"}, dest: "toon", flag: true},
	{names: []string{"--head"}, dest: "head", value: true, integer: true},
	{names: []string{"--fields"}, dest: "fields", value: true},
	{names: []string{"--transport"}, dest: "transport", value: true, choice: []string{"auto", "sse", "streamable"}},
	{names: []string{"--env"}, dest: "env", value: true, append: true},
	{names: []string{"--root"}, dest: "root", value: true, append: true},
	{names: []string{"--complete"}, dest: "complete", value: true},
	{names: []string{"--oauth"}, dest: "oauth", flag: true},
	{names: []string{"--oauth-client-id"}, dest: "oauth_client_id", value: true},
	{names: []string{"--oauth-client-secret"}, dest: "oauth_client_secret", value: true},
	{names: []string{"--oauth-client-name"}, dest: "oauth_client_name", value: true},
	{names: []string{"--oauth-scope"}, dest: "oauth_scope", value: true},
	{names: []string{"--oauth-redirect-uri"}, dest: "oauth_redirect_uri", value: true},
	{names: []string{"--oauth-flow"}, dest: "oauth_flow", value: true, choice: []string{"auto", "authorization_code", "client_credentials"}},
	{names: []string{"--oauth-manual-callback"}, dest: "oauth_manual_callback", flag: true},
	{names: []string{"--list-resources"}, dest: "list_resources", flag: true},
	{names: []string{"--list-resource-templates"}, dest: "list_resource_templates", flag: true},
	{names: []string{"--read-resource"}, dest: "read_resource", value: true},
	{names: []string{"--list-prompts"}, dest: "list_prompts", flag: true},
	{names: []string{"--get-prompt"}, dest: "get_prompt", value: true},
	{names: []string{"--prompt-arg"}, dest: "prompt_arg", value: true, append: true},
	{names: []string{"--session-start"}, dest: "session_start", value: true},
	{names: []string{"--session-stop"}, dest: "session_stop", value: true},
	{names: []string{"--session-list"}, dest: "session_list", flag: true},
	{names: []string{"--session"}, dest: "session", value: true},
	// -h/--help are deliberately absent: mcp2cli's pre-parser has
	// add_help=False, so they fall through to the tool parser (or the
	// no-source help screen) rather than being consumed as global options.
	{names: []string{"--version"}, dest: "version", flag: true},
}

func findGlobalSpec(name string) *optionSpec {
	for index := range globalOptionSpecs {
		for _, candidate := range globalOptionSpecs[index].names {
			if candidate == name {
				return &globalOptionSpecs[index]
			}
		}
	}
	return nil
}

// splitAtSubcommand walks argv consuming only global options and their
// values, returning the global portion and everything from the first
// positional token onward (mcp2cli's _split_at_subcommand). This is what
// keeps a tool parameter that shares a name with a global flag (--env) from
// being swallowed by the global parser.
func splitAtSubcommand(argv []string) ([]string, []string) {
	index := 0
	for index < len(argv) {
		arg := argv[index]
		if arg == "--" {
			return argv[:index], argv[index+1:]
		}
		if strings.HasPrefix(arg, "-") {
			if strings.HasPrefix(arg, "--") && strings.Contains(arg, "=") {
				index++
				continue
			}
			spec := findGlobalSpec(arg)
			switch {
			case spec == nil:
				index++
			case spec.value:
				index += 2
			default:
				index++
			}
			continue
		}
		return argv[:index], argv[index:]
	}
	return argv, nil
}

// parseGlobal parses the global portion of argv strictly, returning the
// unconsumed tokens (unknown options are left for the caller to report, just
// like argparse's parse_known_args plus a later parse_args).
func parseGlobal(argv []string) (*Options, []string, error) {
	options := &Options{CacheTTL: DefaultCacheTTL, Top: -1, Head: -1, Transport: "auto"}
	leftover := []string{}

	for index := 0; index < len(argv); index++ {
		arg := argv[index]
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			leftover = append(leftover, arg)
			continue
		}
		name := arg
		inline := ""
		hasInline := false
		if strings.HasPrefix(arg, "--") {
			if before, after, found := strings.Cut(arg, "="); found {
				name, inline, hasInline = before, after, true
			}
		}
		spec := findGlobalSpec(name)
		if spec == nil {
			leftover = append(leftover, arg)
			continue
		}
		if !spec.value {
			if hasInline {
				return nil, nil, &usageError{
					prog:    progName,
					message: fmt.Sprintf("argument %s: ignored explicit argument '%s'", name, inline),
					usage:   mainUsage(nil),
				}
			}
			applyFlag(options, spec.dest)
			continue
		}
		value := inline
		if !hasInline {
			if index+1 >= len(argv) {
				return nil, nil, &usageError{
					prog:    progName,
					message: fmt.Sprintf("argument %s: expected one argument", name),
					usage:   mainUsage(nil),
				}
			}
			index++
			value = argv[index]
		}
		if err := applyValue(options, spec, name, value); err != nil {
			return nil, nil, err
		}
	}
	return options, leftover, nil
}

func applyFlag(options *Options, dest string) {
	switch dest {
	case "refresh":
		options.Refresh = true
	case "list":
		options.List = true
	case "verbose":
		options.Verbose = true
	case "compact":
		options.Compact = true
	case "pretty":
		options.Pretty = true
	case "raw":
		options.Raw = true
	case "json":
		options.JSON = true
	case "toon":
		options.Toon = true
	case "oauth":
		options.OAuth = true
	case "oauth_manual_callback":
		options.OAuthOther = true
	case "list_resources":
		options.ListResources = true
	case "list_resource_templates":
		options.ListTemplates = true
	case "list_prompts":
		options.ListPrompts = true
	case "session_list", "session_stop", "session_start", "session":
		options.SessionMode = true
	case "version":
		options.Version = true
	}
}

func applyValue(options *Options, spec *optionSpec, name, value string) error {
	if spec.choice != nil {
		valid := false
		for _, choice := range spec.choice {
			if value == choice {
				valid = true
				break
			}
		}
		if !valid {
			quoted := make([]string, 0, len(spec.choice))
			for _, choice := range spec.choice {
				quoted = append(quoted, "'"+choice+"'")
			}
			return &usageError{
				prog:    progName,
				message: fmt.Sprintf("argument %s: invalid choice: '%s' (choose from %s)", name, value, strings.Join(quoted, ", ")),
				usage:   mainUsage(nil),
			}
		}
	}
	number := 0
	if spec.integer {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return &usageError{
				prog:    progName,
				message: fmt.Sprintf("argument %s: invalid int value: '%s'", name, value),
				usage:   mainUsage(nil),
			}
		}
		number = parsed
	}
	switch spec.dest {
	case "mcp":
		options.MCPServerURL = value
	case "mcp_stdio":
		options.MCPStdio = value
	case "auth_header":
		options.AuthHeaders = append(options.AuthHeaders, value)
	case "cache_key":
		options.CacheKey = value
	case "cache_ttl":
		options.CacheTTL = number
	case "search":
		options.Search = value
	case "sort":
		options.Sort = value
	case "top":
		options.Top = number
	case "head":
		options.Head = number
	case "transport":
		options.Transport = value
	case "env":
		options.Env = append(options.Env, value)
	case "root":
		options.Roots = append(options.Roots, value)
	case "list_resources":
		options.ListResources = true
	case "read_resource":
		options.ReadResource = value
	case "get_prompt":
		options.GetPrompt = value
	case "prompt_arg":
		options.PromptArgs = append(options.PromptArgs, value)
	case "spec":
		options.Spec = value
	case "graphql":
		options.GraphQL = value
	case "base_url":
		options.BaseURL = value
	case "fields":
		options.Fields = value
	case "complete":
		options.Complete = value
	case "session_start":
		options.SessionMode = true
		options.SessionStart = value
	case "session_stop", "session":
		options.SessionMode = true
	default:
		// OAuth client options and other accepted-but-unsupported switches.
		options.OAuthOther = true
	}
	return nil
}

// fatalError is a plain "Error: ..." line on stderr with exit code 1.
type fatalError struct {
	message string
}

func (e *fatalError) Error() string { return e.message }

// pyRepr renders a string the way Python's repr does for the error messages
// that quote a user-supplied value.
func pyRepr(value string) string {
	quote := "'"
	if strings.Contains(value, "'") && !strings.Contains(value, `"`) {
		quote = `"`
	}
	var builder strings.Builder
	builder.WriteString(quote)
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if string(r) == quote {
				builder.WriteString(`\` + quote)
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteString(quote)
	return builder.String()
}
