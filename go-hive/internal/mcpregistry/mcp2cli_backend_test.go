package mcpregistry

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeMCP2CLI installs a stand-in for the external binary that records
// its argv and exits with a fixed status.
func writeFakeMCP2CLI(t *testing.T, dir string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	marker := filepath.Join(dir, "invoked.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + ShellQuote(marker) + "\nexit " + itoa(exitCode) + "\n"
	path := filepath.Join(dir, "mcp2cli")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mcp2cli: %v", err)
	}
	return marker
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func cliConfig() *ServerConfig {
	command := "/nonexistent-mcp-server"
	return &ServerConfig{Name: "fake", Transport: "stdio", Command: &command}
}

// Parity-exempt: Go-only backend switch introduced by this port; upstream Python has no
// equivalent because it always shells out to mcp2cli.
func TestMCP2CLIBackendFromEnv(t *testing.T) {
	t.Setenv("HIVE_MCP2CLI", "")
	if got := MCP2CLIBackendFromEnv(); got != BackendAuto {
		t.Fatalf("unset backend = %q, want auto", got)
	}
	t.Setenv("HIVE_MCP2CLI", "native")
	if got := MCP2CLIBackendFromEnv(); got != BackendNative {
		t.Fatalf("native backend = %q", got)
	}
	t.Setenv("HIVE_MCP2CLI", "EXTERNAL")
	if got := MCP2CLIBackendFromEnv(); got != BackendExternal {
		t.Fatalf("EXTERNAL backend = %q, want external", got)
	}
	t.Setenv("HIVE_MCP2CLI", "nonsense")
	if got := MCP2CLIBackendFromEnv(); got != BackendAuto {
		t.Fatalf("unknown backend = %q, want auto", got)
	}
}

// Parity-exempt: Go-only backend switch introduced by this port.
func TestExternalBackendRunsBinaryAndForwardsArgs(t *testing.T) {
	dir := t.TempDir()
	marker := writeFakeMCP2CLI(t, dir, 3)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HIVE_MCP2CLI", "external")

	code, err := RunMCP2CLI(cliConfig(), []string{"--json", "echo", "--text", "hi"})
	if err != nil {
		t.Fatalf("RunMCP2CLI: %v", err)
	}
	if code != 3 {
		t.Fatalf("code = %d, want 3", code)
	}
	recorded, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("fake binary was not invoked: %v", err)
	}
	argv := string(recorded)
	for _, expected := range []string{"--mcp-stdio", "--json", "echo", "--text", "hi"} {
		if !strings.Contains(argv, expected) {
			t.Fatalf("argv missing %q:\n%s", expected, argv)
		}
	}
}

// Parity-exempt: Go-only backend switch introduced by this port.
func TestAutoFallsBackToExternalForUnsupportedFlags(t *testing.T) {
	dir := t.TempDir()
	marker := writeFakeMCP2CLI(t, dir, 7)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HIVE_MCP2CLI", "")

	code, err := RunMCP2CLI(cliConfig(), []string{"--toon", "--list"})
	if err != nil {
		t.Fatalf("RunMCP2CLI: %v", err)
	}
	if code != 7 {
		t.Fatalf("code = %d, want the external binary's 7", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("external binary was not invoked: %v", err)
	}
}

// Parity-exempt: Go-only backend switch introduced by this port.
func TestAutoPrefersNativeForSupportedFlags(t *testing.T) {
	dir := t.TempDir()
	marker := writeFakeMCP2CLI(t, dir, 7)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HIVE_MCP2CLI", "")

	code, err := RunMCP2CLI(cliConfig(), []string{"--list"})
	if err != nil {
		t.Fatalf("RunMCP2CLI: %v", err)
	}
	if code != 1 {
		t.Fatalf("code = %d, want 1 from the native connection failure", code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("auto called the external binary for a supported invocation")
	}
}

// Parity-exempt: Go-only backend switch introduced by this port.
func TestNativeBackendRefusesUnsupportedFlags(t *testing.T) {
	t.Setenv("HIVE_MCP2CLI", "native")
	_, err := RunMCP2CLI(cliConfig(), []string{"--spec", "openapi.yaml", "--list"})
	if err == nil {
		t.Fatal("expected an unsupported-feature error")
	}
	if !strings.Contains(err.Error(), "not supported by the native implementation") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "HIVE_MCP2CLI=external") {
		t.Fatalf("err lacks the escape hatch: %v", err)
	}
}

// Parity-exempt: Go-only backend switch introduced by this port.
func TestAutoWithoutExternalExplainsTheGap(t *testing.T) {
	t.Setenv("HIVE_MCP2CLI", "")
	t.Setenv("PATH", t.TempDir())
	_, err := RunMCP2CLI(cliConfig(), []string{"--toon", "--list"})
	if err == nil {
		t.Fatal("expected an error when neither backend can run the invocation")
	}
	if !strings.Contains(err.Error(), "install mcp2cli") {
		t.Fatalf("err = %v", err)
	}
}
