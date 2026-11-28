package cli

// Integration coverage for the hidden/SUPPRESS surfaces of the real CLI roots.
// The parser-level renderer tests moved with the parser to internal/cli/cliargs.

import (
	"strings"
	"testing"
)

// Parity-exempt: queen-mail-mcp is a Go extension with no Python CLI path; the
// test pins that it stays callable (`hive queen-mail-mcp` is what a Claude Code
// queen's runs start) while never appearing in `hive --help`.
func TestHiveRootHelpHidesInternalExtensions(t *testing.T) {
	code, stdout, _ := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("Run(--help) = %d, want 0", code)
	}
	if strings.Contains(stdout, "queen-mail-mcp") {
		t.Fatalf("queen-mail-mcp leaked into `hive --help`:\n%s", stdout)
	}
	runner := NewRunner()
	if findSubcommand(runner.rootCommand(), "queen-mail-mcp") == nil {
		t.Fatal("queen-mail-mcp must stay resolvable by name")
	}
}
