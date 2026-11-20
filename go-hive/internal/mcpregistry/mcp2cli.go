package mcpregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/mcpregistry/nativecli"
)

func MCP2CLIInvocation(config *ServerConfig) ([]string, map[string]string, *string, error) {
	env := map[string]string{}

	switch config.Transport {
	case "stdio":
		command := []string{""}
		if config.Command != nil {
			command[0] = *config.Command
		}
		command = append(command, config.Args...)
		argv := []string{"--mcp-stdio", ShellJoin(command)}
		for key, value := range config.Env {
			env[key] = value
		}
		return append(argv, cacheKey(argv, env)...), env, config.Cwd, nil

	case "http", "sse":
		url := ""
		if config.URL != nil {
			url = *config.URL
		}
		argv := []string{"--mcp", url}
		if config.Transport == "sse" {
			argv = append(argv, "--transport", "sse")
		}
		for index, header := range sortedStringKeys(config.Headers) {
			variable := fmt.Sprintf("HIVE_MCP_HEADER_%d", index)
			env[variable] = config.Headers[header]
			argv = append(argv, "--auth-header", header+":env:"+variable)
		}
		return append(argv, cacheKey(argv, env)...), env, nil, nil
	}

	return nil, nil, nil, &Error{
		Code: CodeProtocolMismatch,
		What: "Cannot run '" + config.Name + "' from the terminal",
		Why:  "mcp2cli can't reach a server over the " + config.Transport + " transport.",
		Fix:  "Run: hive mcp mode " + config.Name + " tools  — to load its tools instead.",
	}
}

func cacheKey(argv []string, env map[string]string) []string {
	quoted := make([]string, 0, len(argv))
	for _, item := range argv {
		quoted = append(quoted, pyJSONString(item))
	}
	argvJSON := "[" + strings.Join(quoted, ", ") + "]"

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, "["+pyJSONString(key)+", "+pyJSONString(env[key])+"]")
	}
	envJSON := "[" + strings.Join(pairs, ", ") + "]"

	digest := sha256.Sum256([]byte("[" + argvJSON + ", " + envJSON + "]"))
	return []string{"--cache-key", "hive-" + hex.EncodeToString(digest[:])[:16]}
}

func pyJSONString(s string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || r > 0x7e:
				if r <= 0xffff {
					fmt.Fprintf(&builder, `\u%04x`, r)
				} else {
					r -= 0x10000
					high := 0xd800 + (r >> 10)
					low := 0xdc00 + (r & 0x3ff)
					fmt.Fprintf(&builder, `\u%04x\u%04x`, high, low)
				}
			default:
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

func shellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@%+=:,./-_", r):
		default:
			return false
		}
	}
	return true
}

func ShellQuote(s string) string {
	if shellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func ShellJoin(words []string) string {
	quoted := make([]string, 0, len(words))
	for _, word := range words {
		quoted = append(quoted, ShellQuote(word))
	}
	return strings.Join(quoted, " ")
}

func ShellSplit(line string) ([]string, error) {
	words := []string{}
	var current strings.Builder
	haveToken := false
	inSingle, inDouble := false, false

	for index := 0; index < len(line); {
		ch := line[index]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				current.WriteByte(ch)
			}
			index++
		case inDouble:
			switch {
			case ch == '"':
				inDouble = false
				index++
			case ch == '\\' && index+1 < len(line) && strings.ContainsRune(`"\\$`+"`", rune(line[index+1])):
				current.WriteByte(line[index+1])
				index += 2
			default:
				current.WriteByte(ch)
				index++
			}
		default:
			switch ch {
			case '\'':
				inSingle = true
				haveToken = true
				index++
			case '"':
				inDouble = true
				haveToken = true
				index++
			case '\\':
				if index+1 < len(line) {
					current.WriteByte(line[index+1])
					haveToken = true
					index += 2
				} else {
					index++
				}
			case ' ', '\t', '\n':
				if haveToken {
					words = append(words, current.String())
					current.Reset()
					haveToken = false
				}
				index++
			default:
				current.WriteByte(ch)
				haveToken = true
				index++
			}
		}
	}

	if inSingle || inDouble {
		return nil, errors.New("no closing quotation")
	}
	if haveToken {
		words = append(words, current.String())
	}
	return words, nil
}

// MCP2CLIBackend names the implementation `hive mcp run` should use.
type MCP2CLIBackend string

const (
	// BackendAuto prefers the in-process implementation and falls back to the
	// external binary only for features the native subset does not cover.
	BackendAuto MCP2CLIBackend = "auto"
	// BackendNative forces the in-process implementation.
	BackendNative MCP2CLIBackend = "native"
	// BackendExternal forces the external mcp2cli binary.
	BackendExternal MCP2CLIBackend = "external"
)

// MCP2CLIBackendFromEnv reads HIVE_MCP2CLI, defaulting to auto.
func MCP2CLIBackendFromEnv() MCP2CLIBackend {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HIVE_MCP2CLI"))) {
	case "native":
		return BackendNative
	case "external":
		return BackendExternal
	default:
		return BackendAuto
	}
}

func RunMCP2CLI(config *ServerConfig, args []string) (int, error) {
	source, env, cwd, err := MCP2CLIInvocation(config)
	if err != nil {
		return 1, err
	}
	if len(args) == 0 {
		args = []string{"--list"}
	}

	execArgs := append(append([]string{}, source...), args...)

	switch MCP2CLIBackendFromEnv() {
	case BackendNative:
		return runNativeMCP2CLI(execArgs, env, cwd)
	case BackendExternal:
		return runExternalMCP2CLI(execArgs, env, cwd)
	default:
		supportErr := nativecli.Supports(execArgs)
		if supportErr == nil {
			return runNativeMCP2CLI(execArgs, env, cwd)
		}
		// The native subset cannot serve this invocation: use the installed
		// binary when there is one, and explain the gap when there is not.
		if _, lookErr := exec.LookPath("mcp2cli"); lookErr == nil {
			return runExternalMCP2CLI(execArgs, env, cwd)
		}
		return 1, fmt.Errorf("%v; install mcp2cli or set HIVE_MCP2CLI=external with it on PATH", supportErr)
	}
}

// runNativeMCP2CLI runs the in-process implementation with the same
// environment and working directory the binary would have received.
func runNativeMCP2CLI(args []string, env map[string]string, cwd *string) (int, error) {
	commandEnv := mcp2cliEnvironment(env)
	directory := ""
	if cwd != nil {
		directory = *cwd
	}
	code, err := nativecli.Run(nativecli.Invocation{
		Args:   args,
		Env:    commandEnv,
		Dir:    directory,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		var unsupported *nativecli.UnsupportedError
		if errors.As(err, &unsupported) {
			return 1, fmt.Errorf("%v; set HIVE_MCP2CLI=external to use the mcp2cli binary", err)
		}
		return 1, err
	}
	return code, nil
}

// runExternalMCP2CLI runs the reference binary, preserving the historical
// behaviour of `hive mcp run`.
func runExternalMCP2CLI(args []string, env map[string]string, cwd *string) (int, error) {
	command := exec.Command("mcp2cli", args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = mcp2cliEnvironment(env)
	if cwd != nil {
		command.Dir = *cwd
	}

	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

// mcp2cliEnvironment merges the transport environment over the process
// environment and points the cache at the hive home by default.
func mcp2cliEnvironment(env map[string]string) []string {
	merged := mergedEnvironment(env)
	if _, ok := env["MCP2CLI_CACHE_DIR"]; ok {
		return merged
	}
	if home, err := hivehome.Resolve(); err == nil {
		merged = append(merged, "MCP2CLI_CACHE_DIR="+filepath.Join(home.CacheDir(), "mcp2cli"))
	}
	return merged
}

func mergedEnvironment(overrides map[string]string) []string {
	environ := os.Environ()
	if len(overrides) == 0 {
		return environ
	}
	merged := map[string]string{}
	order := []string{}
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			key, value = entry, ""
		}
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = value
	}
	for key, value := range overrides {
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+merged[key])
	}
	return out
}

func CLIServersPrompt(servers []map[string]any) string {
	if len(servers) == 0 {
		return ""
	}

	lines := []string{
		"Command-line MCP servers (run them with terminal_exec; their tools are not in your tool list):",
	}
	for _, server := range servers {
		detail := ""
		if count := server["tool_count"]; truthyValue(count) {
			detail = fmt.Sprintf(" (%s commands)", pyStr(count))
		}
		description := asString(server["description"])
		if description == "" {
			description = "no description"
		}
		lines = append(lines, fmt.Sprintf("- %s: %s%s", pyStr(server["name"]), description, detail))
	}
	lines = append(lines,
		"List a server's commands with `hive mcp run <server> --list` (add `--search <word>` to filter), "+
			"read one's flags with `hive mcp run <server> <command> --help`, then run "+
			"`hive mcp run <server> <command> --flag value`. Add `--json` before the command for the full "+
			"result envelope. Use absolute paths in flags: the server may run in another directory.")
	return strings.Join(lines, "\n")
}

func RegistryCLIServersPrompt() string {
	registry, err := Default()
	if err != nil {
		return ""
	}
	if err := registry.Initialize(); err != nil {
		return ""
	}
	servers, err := registry.CLIServers()
	if err != nil {
		return ""
	}
	return CLIServersPrompt(servers)
}
