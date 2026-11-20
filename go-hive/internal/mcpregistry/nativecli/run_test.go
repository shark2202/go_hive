package nativecli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cliResult captures one in-process CLI run.
type cliResult struct {
	code   int
	stdout string
	stderr string
}

// runCLI runs the native CLI with a private cache directory.
func runCLI(t *testing.T, args []string, stdin string, extraEnv map[string]string) cliResult {
	t.Helper()
	return runCLIWithCache(t, args, stdin, extraEnv, t.TempDir())
}

func runCLIWithCache(t *testing.T, args []string, stdin string, extraEnv map[string]string, cacheDir string) cliResult {
	t.Helper()
	env := append([]string{}, os.Environ()...)
	env = append(env, "MCP2CLI_CACHE_DIR="+cacheDir, fixtureEnv+"=1")
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}
	var stdout, stderr strings.Builder
	code, err := Run(Invocation{
		Args:   args,
		Env:    env,
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Run(%v): %v", args, err)
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// stdioSource returns the mcp2cli argv prefix for the stdio fixture.
func stdioSource() []string {
	return []string{"--mcp-stdio", fixtureCommand()}
}

// withSource appends the fixture source to per-test arguments.
func withSource(args ...string) []string {
	return append(stdioSource(), args...)
}

// cacheFiles lists the files in a cache directory.
func cacheFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// cacheFile returns the tool cache path for a key.
func cacheFile(dir, key string) string {
	return filepath.Join(dir, key+"_tools.json")
}

// Parity-exempt: native CLI smoke test; the behaviour is compared against the real
// mcp2cli binary in parity_test.go, which is stricter than a Python-source parity case.
func TestNativeCLIRunsListAgainstStdioFixture(t *testing.T) {
	result := runCLI(t, withSource("--list"), "", nil)
	if result.code != 0 {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.HasPrefix(result.stdout, "\nAvailable tools:\n") {
		t.Fatalf("stdout = %q", result.stdout)
	}
	for _, name := range []string{"echo", "add", "tag-list", "boom", "env-check", "structured"} {
		if !strings.Contains(result.stdout, "  "+name+" ") {
			t.Fatalf("stdout missing %q:\n%s", name, result.stdout)
		}
	}
	if result.stderr != "" {
		t.Fatalf("stderr = %q", result.stderr)
	}
}
