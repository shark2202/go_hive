package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
)

// TestHelpRendersArgparseLayout pins the rendered page for one leaf command at
// COLUMNS=80: usage wraps with argparse's hanging indent, the section names are
// `positional arguments:` / `options:`, and the label column follows
// min(action_max_length + 2, 24) rather than a fixed width.
//
// Parity: tests/test_cli_entry_point.py::test_hive_help
func TestHelpRendersArgparseLayout(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	code, stdout, _ := runCLI(t, "skill", "doctor", "--help")
	if code != 0 {
		t.Fatalf("Run(skill doctor --help) = %d, want 0", code)
	}
	want := "usage: hive skill doctor [-h] [--defaults] [--project-dir PATH] [--json]\n" +
		"                         [name]\n" +
		"\n" +
		"positional arguments:\n" +
		"  name                Skill name to check (default: all discovered skills)\n" +
		"\n" +
		"options:\n" +
		"  -h, --help          show this help message and exit\n" +
		"  --defaults          Check all 6 framework default skills\n" +
		"  --project-dir PATH\n" +
		"  --json              Output as JSON\n"
	if stdout != want {
		t.Fatalf("help page =\n%q\nwant\n%q", stdout, want)
	}
}

// TestHelpRepeatsMetavarPerAlias pins argparse's alias rendering: every option
// string carries the metavar, and the usage line uses the first (long) form.
//
// Parity: tests/test_cli_entry_point.py::test_module_serve_subcommand
func TestHelpRepeatsMetavarPerAlias(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	code, stdout, _ := runCLI(t, "serve", "--help")
	if code != 0 {
		t.Fatalf("Run(serve --help) = %d, want 0", code)
	}
	for _, want := range []string{
		"usage: hive serve [-h] [--host HOST] [--port PORT] [--colony COLONY]",
		"  --port PORT, -p PORT  Port to listen on (default: 8787)",
		"  --colony COLONY, -c COLONY",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
}

// TestMCPVerbHelpRendersOwnPage pins `hive mcp <verb> --help`: upstream's
// argparse subparsers render the verb's own usage/positionals/options page,
// not the group help.
//
// Parity-exempt: upstream's test_mcp_registry_cli asserts subcommand
// registration; the per-verb page text is pinned by the frozen CLI contract.
func TestMCPVerbHelpRendersOwnPage(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	registry := mcpregistry.NewAtBase(filepath.Join(t.TempDir(), "mcp_registry"))
	if err := registry.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	runner.MCPMain = func(argv []string) int {
		mcpCLI := &mcpregistry.CLI{Out: &stdout, Err: &stderr, In: strings.NewReader("")}
		mcpCLI.RegistryFunc = func() (*mcpregistry.Registry, error) { return registry, nil }
		return mcpCLI.Main(argv)
	}
	if code := runner.Run([]string{"mcp", "run", "--help"}); code != 0 {
		t.Fatalf("Run(mcp run --help) = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	want := "usage: hive mcp run [-h] name ...\n" +
		"\n" +
		"Run an installed MCP server's tools as commands, through mcp2cli. Examples:\n" +
		"hive mcp run NAME --list; hive mcp run NAME TOOL --help; hive mcp run NAME\n" +
		"TOOL --flag value\n" +
		"\n" +
		"positional arguments:\n" +
		"  name          Server name\n" +
		"  mcp2cli_args  mcp2cli arguments (default: --list)\n" +
		"\n" +
		"options:\n" +
		"  -h, --help    show this help message and exit\n"
	if stdout.String() != want {
		t.Fatalf("mcp run help =\n%q\nwant\n%q", stdout.String(), want)
	}

	// The subcommand tree still recurses: every verb now has its own page.
	for _, verb := range []string{"install", "add", "mode", "run", "remove", "enable",
		"disable", "list", "info", "config", "search", "health", "init", "update"} {
		stdout.Reset()
		if code := runner.Run([]string{"mcp", verb, "--help"}); code != 0 {
			t.Fatalf("Run(mcp %s --help) = %d, want 0 (stderr: %s)", verb, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage: hive mcp "+verb) {
			t.Fatalf("mcp %s help page lacks its own usage:\n%s", verb, stdout.String())
		}
		if !strings.Contains(stdout.String(), "-h, --help") {
			t.Fatalf("mcp %s help page lacks the options block:\n%s", verb, stdout.String())
		}
	}
}
