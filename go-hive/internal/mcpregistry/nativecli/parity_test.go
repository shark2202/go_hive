package nativecli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// referenceBinary locates mcp2cli 3.8.0. It returns "" (and the caller skips)
// when the reference implementation is not installed.
func referenceBinary(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if configured := os.Getenv("MCP2CLI_BIN"); configured != "" {
		candidates = append(candidates, configured)
	}
	if found, err := exec.LookPath("mcp2cli"); err == nil {
		candidates = append(candidates, found)
	}
	// Walk up from the package directory looking for a project venv.
	directory, err := os.Getwd()
	if err == nil {
		for depth := 0; depth < 8; depth++ {
			candidates = append(candidates, filepath.Join(directory, ".venv", "bin", "mcp2cli"))
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		version, err := exec.Command(candidate, "--version").Output()
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(version)) == "mcp2cli "+Version {
			return candidate
		}
	}
	return ""
}

// runReference executes the reference binary with the same environment shape
// the native CLI gets.
func runReference(t *testing.T, binary string, args []string, stdin, cacheDir string, extraEnv map[string]string) cliResult {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(),
		"MCP2CLI_CACHE_DIR="+cacheDir,
		fixtureEnv+"=1",
	)
	for key, value := range extraEnv {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if ok := asExitError(err, &exitError); ok {
			code = exitError.ExitCode()
		} else {
			t.Fatalf("running reference mcp2cli: %v", err)
		}
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func asExitError(err error, target **exec.ExitError) bool {
	exitError, ok := err.(*exec.ExitError)
	if ok {
		*target = exitError
	}
	return ok
}

// parityCase is one argv/stdin pair exercised against both implementations.
type parityCase struct {
	name  string
	args  []string
	stdin string
	env   map[string]string
}

// compareParity runs one case through both implementations and reports any
// difference in stdout, stderr or exit code.
func compareParity(t *testing.T, binary string, source []string, item parityCase) {
	t.Helper()
	nativeCache := t.TempDir()
	referenceCache := t.TempDir()
	args := append(append([]string{}, source...), item.args...)
	native := runCLIWithCache(t, args, item.stdin, item.env, nativeCache)
	reference := runReference(t, binary, args, item.stdin, referenceCache, item.env)
	if native.code != reference.code {
		t.Errorf("exit code: native %d, reference %d\nnative stderr: %s\nreference stderr: %s",
			native.code, reference.code, native.stderr, reference.stderr)
	}
	if native.stdout != reference.stdout {
		t.Errorf("stdout mismatch\n--- native ---\n%s\n--- reference ---\n%s", native.stdout, reference.stdout)
	}
	if native.stderr != reference.stderr {
		t.Errorf("stderr mismatch\n--- native ---\n%s\n--- reference ---\n%s", native.stderr, reference.stderr)
	}
}

// stdioParityCases covers listing, searching, calling and every error path the
// hive CLI can reach with a stdio server.
func stdioParityCases() []parityCase {
	return []parityCase{
		{name: "list", args: []string{"--list"}},
		{name: "list-json", args: []string{"--list", "--json"}},
		{name: "list-json-pretty", args: []string{"--list", "--json", "--pretty"}},
		{name: "list-compact", args: []string{"--list", "--compact"}},
		{name: "list-verbose", args: []string{"--list", "--verbose"}},
		{name: "list-verbose-json", args: []string{"--list", "--verbose", "--json"}},
		{name: "long-help", args: []string{"long-doc", "--help"}},
		{name: "search", args: []string{"--search", "add"}},
		{name: "search-description", args: []string{"--search", "operand"}},
		{name: "search-json", args: []string{"--search", "add", "--json"}},
		{name: "search-no-match", args: []string{"--search", "zzz"}},
		{name: "no-subcommand", args: nil},
		{name: "call-text", args: []string{"echo", "--text", "hi"}},
		{name: "call-bool", args: []string{"echo", "--text", "hi", "--loud"}},
		{name: "call-json", args: []string{"--json", "echo", "--text", "hi"}},
		{name: "call-json-pretty", args: []string{"--json", "--pretty", "echo", "--text", "hi"}},
		{name: "call-number", args: []string{"add", "--a", "1", "--b", "2.5"}},
		{name: "call-array-csv", args: []string{"tag-list", "--tags", "a,b,c"}},
		{name: "call-array-json", args: []string{"tag-list", "--tags", `["x","y"]`}},
		{name: "call-limit", args: []string{"tag-list", "--tags", "a,b,c", "--limit", "2"}},
		{name: "call-prefix", args: []string{"tag-list", "--tags", "a,b", "--prefix", "p-"}},
		{name: "call-env-flag-collision", args: []string{"env-check", "--env", "X=1"}},
		{name: "call-env-equals", args: []string{"env-check", "--env=X=1"}},
		{name: "call-structured", args: []string{"structured"}},
		{name: "tool-error", args: []string{"boom"}},
		{name: "tool-error-json", args: []string{"--json", "boom"}},
		{name: "tool-help", args: []string{"echo", "--help"}},
		{name: "server-help", args: []string{"--help"}},
		{name: "server-help-short", args: []string{"-h"}},
		{name: "bad-type", args: []string{"add", "--a", "x", "--b", "2"}},
		{name: "unknown-flag", args: []string{"echo", "--text", "hi", "--nope"}},
		{name: "raw", args: []string{"--raw", "echo", "--text", "hi"}},
		{name: "head", args: []string{"--json", "--head", "1", "tag-list", "--tags", "a,b,c"}},
		{name: "stdin", args: []string{"echo", "--stdin"}, stdin: `{"text":"from stdin"}`},
		{name: "stdin-merge", args: []string{"echo", "--stdin", "--loud"}, stdin: `{"text":"loud one"}`},
		{name: "stdin-conflict", args: []string{"echo", "--stdin", "--text", "a"}, stdin: `{"text":"b"}`},
		{name: "stdin-empty", args: []string{"echo", "--stdin"}, stdin: ""},
		{name: "top", args: []string{"--list", "--top", "2"}},
		{name: "alpha", args: []string{"--list", "--sort", "alpha"}},
		{name: "list-resources", args: []string{"--list-resources"}},
		{name: "list-resources-json", args: []string{"--list-resources", "--json"}},
		{name: "list-templates", args: []string{"--list-resource-templates"}},
		{name: "read-resource", args: []string{"--read-resource", "fixture://notes"}},
		{name: "list-prompts", args: []string{"--list-prompts"}},
		{name: "get-prompt", args: []string{"--get-prompt", "greet", "--prompt-arg", "who=hive"}},
		{name: "version", args: []string{"--version"}},
		{name: "unknown-tool", args: []string{"nope"}},
		{name: "unknown-tool-flag", args: []string{"nope", "--x", "1"}},
		{name: "stdin-invalid-json", args: []string{"echo", "--stdin"}, stdin: `{"text": }`},
		{name: "stdin-non-object-with-flag", args: []string{"echo", "--stdin", "--loud"}, stdin: `["a"]`},
		{name: "stdin-no-flags", args: []string{"echo", "--stdin"}, stdin: `{"text": "quiet"}`},
	}
}

// TestParityWithReferenceStdio compares stdout, stderr and exit codes against
// the real mcp2cli 3.8.0 over a stdio server.
//
// Parity-exempt: differential test against the reference binary (skipped when it is
// not installed); there is no Python test case to point at.
func TestParityWithReferenceStdio(t *testing.T) {
	binary := referenceBinary(t)
	if binary == "" {
		t.Skip("reference mcp2cli 3.8.0 not found; set MCP2CLI_BIN to run the parity suite")
	}
	for _, item := range stdioParityCases() {
		t.Run(item.name, func(t *testing.T) {
			compareParity(t, binary, stdioSource(), item)
		})
	}
}

// httpParityCases covers the same surface over HTTP, which shares the tool
// layer but exercises different transports.
func httpParityCases() []parityCase {
	return []parityCase{
		{name: "list", args: []string{"--list"}},
		{name: "list-json", args: []string{"--list", "--json"}},
		{name: "list-compact", args: []string{"--list", "--compact"}},
		{name: "call", args: []string{"echo", "--text", "http"}},
		{name: "call-json", args: []string{"--json", "echo", "--text", "http"}},
		{name: "call-number", args: []string{"add", "--a", "7", "--b", "0.25"}},
		{name: "tool-error", args: []string{"boom"}},
		{name: "search", args: []string{"--search", "operand"}},
		{name: "auth-header", args: []string{"--auth-header", "Authorization:env:HIVE_TEST_TOKEN", "echo", "--text", "auth"}, env: map[string]string{"HIVE_TEST_TOKEN": "Bearer http-token"}},
	}
}

func runHTTPParity(t *testing.T, binary string, url string, extra []string) {
	t.Helper()
	for _, item := range httpParityCases() {
		t.Run(item.name, func(t *testing.T) {
			args := append(append([]string{"--mcp", url}, extra...), item.args...)
			compareParity(t, binary, nil, parityCase{name: item.name, args: args, env: item.env})
		})
	}
}

// TestParityWithReferenceHTTPStreamable compares the streamable HTTP transport
// with JSON responses.
//
// Parity-exempt: differential test against the reference binary (skipped when it is
// not installed); there is no Python test case to point at.
func TestParityWithReferenceHTTPStreamable(t *testing.T) {
	binary := referenceBinary(t)
	if binary == "" {
		t.Skip("reference mcp2cli 3.8.0 not found; set MCP2CLI_BIN to run the parity suite")
	}
	server, _ := newFakeStreamableServer(t, false)
	runHTTPParity(t, binary, server.URL, []string{"--transport", "streamable"})
}

// TestParityWithReferenceHTTPStreamableSSE compares the streamable transport
// when the server answers with an SSE stream on the POST response.
//
// Parity-exempt: differential test against the reference binary (skipped when it is
// not installed); there is no Python test case to point at.
func TestParityWithReferenceHTTPStreamableSSE(t *testing.T) {
	binary := referenceBinary(t)
	if binary == "" {
		t.Skip("reference mcp2cli 3.8.0 not found; set MCP2CLI_BIN to run the parity suite")
	}
	server, _ := newFakeStreamableServer(t, true)
	runHTTPParity(t, binary, server.URL, []string{"--transport", "streamable"})
}

// TestParityWithReferenceHTTPSSE compares the legacy HTTP+SSE transport.
//
// Parity-exempt: differential test against the reference binary (skipped when it is
// not installed); there is no Python test case to point at.
func TestParityWithReferenceHTTPSSE(t *testing.T) {
	binary := referenceBinary(t)
	if binary == "" {
		t.Skip("reference mcp2cli 3.8.0 not found; set MCP2CLI_BIN to run the parity suite")
	}
	server := newFakeSSEServer(t)
	runHTTPParity(t, binary, server.url(), []string{"--transport", "sse"})
}
