package nativecli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeGatedServer writes a script that only spawns the MCP fixture while a
// flag file exists, so a test can turn the same server command "off" without
// changing the cache key derived from that command.
func writeGatedServer(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "alive")
	script := filepath.Join(dir, "gated-server.sh")
	content := "#!/bin/sh\n" +
		"if [ -f " + pyShellQuote(flag) + " ]; then\n" +
		"  exec " + pyShellQuote(os.Args[0]) + " -test.run=TestNativeCLIStdioFixture\n" +
		"fi\n" +
		"exit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write gated server: %v", err)
	}
	return script, flag
}

func sourceFor(command string) []string {
	return []string{"--mcp-stdio", command}
}

// Parity-exempt: native CLI end-to-end test; the same behaviour is compared with the
// reference binary in the parity suite.
func TestNativeCLIListFollowsPagination(t *testing.T) {
	result := runCLI(t, withSource("--env", "HIVE_MCP_FIXTURE_PAGINATE=1", "--list", "--json"), "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	for _, name := range []string{`"echo"`, `"add"`, `"tag-list"`, `"boom"`, `"env-check"`, `"structured"`} {
		if !strings.Contains(result.stdout, `"name": `+name) {
			t.Fatalf("paginated list missing %s:\n%s", name, result.stdout)
		}
	}
}

// Parity-exempt: cache behaviour mirrors mcp2cli's load_cached/save_cache, observed
// directly against the reference binary; the fixture here needs a gated server command.
func TestNativeCLIToolHelpServedFromCache(t *testing.T) {
	script, flag := writeGatedServer(t)
	cacheDir := t.TempDir()

	if err := os.WriteFile(flag, []byte("alive"), 0o600); err != nil {
		t.Fatalf("touch flag: %v", err)
	}
	warm := runCLIWithCache(t, append(sourceFor(script), warmArgs()...), "", nil, cacheDir)
	if warm.code != 0 {
		t.Fatalf("warm-up call failed: code = %d, stderr = %s", warm.code, warm.stderr)
	}
	if !strings.Contains(warm.stdout, "hi") {
		t.Fatalf("warm-up stdout = %q", warm.stdout)
	}
	if len(cacheFiles(t, cacheDir)) == 0 {
		t.Fatal("no cache file was written")
	}

	// The server is gone, but the cached schema still answers --help.
	if err := os.Remove(flag); err != nil {
		t.Fatalf("remove flag: %v", err)
	}
	help := runCLIWithCache(t, append(sourceFor(script), "echo", "--help"), "", nil, cacheDir)
	if help.code != 0 {
		t.Fatalf("cached --help failed: code = %d, stderr = %s", help.code, help.stderr)
	}
	if !strings.Contains(help.stdout, "usage: mcp2cli echo") {
		t.Fatalf("cached help stdout = %q", help.stdout)
	}

	// --cache-ttl 0 and --refresh both bypass the cache and hit the dead server.
	for _, args := range [][]string{
		append(sourceFor(script), "--cache-ttl", "0", "echo", "--help"),
		append(sourceFor(script), "--refresh", "echo", "--help"),
	} {
		result := runCLIWithCache(t, args, "", nil, cacheDir)
		if result.code != 1 {
			t.Fatalf("args %v: code = %d, stdout = %q", args, result.code, result.stdout)
		}
		if !strings.Contains(result.stderr, "Error: cannot use MCP server at") {
			t.Fatalf("args %v: stderr = %q", args, result.stderr)
		}
	}
}

// warmArgs returns the arguments used to populate the cache in the gated test.
func warmArgs() []string {
	return []string{"echo", "--text", "hi"}
}

// Parity-exempt: native CLI end-to-end test; the argv shape is the one
// MCP2CLIInvocation produces for `hive mcp run`.
func TestNativeCLIUnknownToolIsAnArgparseError(t *testing.T) {
	result := runCLI(t, withSource("nope"), "", nil)
	if result.code != 2 {
		t.Fatalf("code = %d, stdout = %q", result.code, result.stdout)
	}
	if !strings.Contains(result.stderr, "invalid choice: 'nope'") {
		t.Fatalf("stderr = %q", result.stderr)
	}
}

// Parity-exempt: native CLI end-to-end test; the isError path is compared with the
// reference binary in the parity suite.
func TestNativeCLIToolErrorGoesToStderr(t *testing.T) {
	result := runCLI(t, withSource("boom"), "", nil)
	if result.code != 1 {
		t.Fatalf("code = %d", result.code)
	}
	if result.stdout != "" {
		t.Fatalf("stdout = %q, want empty", result.stdout)
	}
	if result.stderr != "Error: kaboom\n" {
		t.Fatalf("stderr = %q", result.stderr)
	}
}

// Parity-exempt: native CLI end-to-end test; structured content fallback is compared
// with the reference binary in the parity suite.
func TestNativeCLIStructuredContentFallback(t *testing.T) {
	result := runCLI(t, withSource("structured"), "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if result.stdout != "{\"answer\": 42, \"ok\": true}\n" {
		t.Fatalf("stdout = %q", result.stdout)
	}
}

// Parity-exempt: native CLI end-to-end test; --version is compared with the
// reference binary in the parity suite.
func TestNativeCLIVersion(t *testing.T) {
	result := runCLI(t, []string{"--version"}, "", nil)
	if result.code != 0 || result.stdout != "mcp2cli 3.8.0\n" {
		t.Fatalf("code = %d, stdout = %q", result.code, result.stdout)
	}
}

// Parity-exempt: native CLI end-to-end test; stdin validation mirrors mcp2cli's
// read_stdin_json, whose pydantic message for a non-object is not reproduced.
func TestNativeCLIStdinValidation(t *testing.T) {
	invalid := runCLI(t, withSource("echo", "--stdin"), `{"text": }`, nil)
	if invalid.code != 1 || !strings.Contains(invalid.stderr, "invalid JSON on stdin for MCP tool arguments (line 1, column 10)") {
		t.Fatalf("invalid: code = %d, stderr = %q", invalid.code, invalid.stderr)
	}
	array := runCLI(t, withSource("echo", "--stdin"), `["a"]`, nil)
	if array.code != 1 || !strings.Contains(array.stderr, "expects a JSON object") {
		t.Fatalf("array: code = %d, stderr = %q", array.code, array.stderr)
	}
}

// Parity-exempt: native CLI end-to-end test; the cache-key plumbing is the shape
// `hive mcp run` always passes.
func TestNativeCLIAcceptsHiveCacheKey(t *testing.T) {
	cacheDir := t.TempDir()
	args := withSource("--cache-key", "hive-0123456789abcdef", "echo", "--text", "hi")
	result := runCLIWithCache(t, args, "", nil, cacheDir)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if _, err := os.Stat(cacheFile(cacheDir, "hive-0123456789abcdef")); err != nil {
		t.Fatalf("cache file missing: %v (files: %v)", err, cacheFiles(t, cacheDir))
	}
}

// Parity-exempt: Content-Length (LSP) framing is a native-only extension; the
// reference mcp2cli speaks newline framing only.
func TestNativeCLIRetriesWithContentLengthFraming(t *testing.T) {
	result := runCLI(t, withSource("--env", "HIVE_MCP_FIXTURE_FRAMING=header", "echo", "--text", "framed"), "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if result.stdout != "framed\n" {
		t.Fatalf("stdout = %q", result.stdout)
	}
}
