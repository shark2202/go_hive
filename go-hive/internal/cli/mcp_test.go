package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/mcpregistry"
)

func newMCPRunner(t *testing.T) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
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
	return runner, &stdout, &stderr
}

// Parity-exempt: test_cli_entry_point.py::test_hive_help predates the mcp subcommand and
// asserts only serve/queen/colony/session/chat; pinning "mcp" in root help is Go-only.
func TestRootHelpListsMCP(t *testing.T) {
	code, stdout, _ := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("Run(--help) = %d, want 0", code)
	}
	if !strings.Contains(stdout, "mcp") {
		t.Fatalf("stdout missing mcp:\n%s", stdout)
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_register_mcp_commands_creates_all_subcommands
func TestMCPHelpThroughRootListsEverySubcommand(t *testing.T) {
	code, stdout, _ := runCLI(t, "mcp", "--help")
	if code != 0 {
		t.Fatalf("Run(mcp --help) = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Manage MCP servers") {
		t.Fatalf("stdout missing group description:\n%s", stdout)
	}
	for _, command := range []string{
		"install", "add", "mode", "run", "remove", "enable", "disable",
		"list", "info", "config", "search", "health", "init", "update",
	} {
		if !strings.Contains(stdout, command) {
			t.Fatalf("stdout missing subcommand %q:\n%s", command, stdout)
		}
	}
}

// Parity-exempt: argparse hands `hive mcp` its own parser, so mcp's flags and
func TestMCPForwardsOwnFlagsAndDoubleDash(t *testing.T) {
	cases := [][]string{
		{"mcp", "list", "--json"},
		{"mcp", "add", "local", "--", "--json", "-p", "8787"},
		{"mcp", "run", "jira", "--", "search", "--jql", "project = X"},
	}
	for _, argv := range cases {
		runner, _, _ := newMCPRunner(t)
		var captured []string
		runner.MCPMain = func(tokens []string) int {
			captured = append([]string{}, tokens...)
			return 0
		}
		if code := runner.Run(argv); code != 0 {
			t.Fatalf("Run(%v) = %d, want 0", argv, code)
		}
		want := argv[1:]
		if strings.Join(captured, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("Run(%v) forwarded %v, want %v", argv, captured, want)
		}
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_main_dispatches_mcp_list_through_real_argparse
func TestMCPListThroughRootRunner(t *testing.T) {
	runner, stdout, stderr := newMCPRunner(t)
	if code := runner.Run([]string{"mcp", "list"}); code != 0 {
		t.Fatalf("Run(mcp list) = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No servers installed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// Parity: core/tests/test_mcp_registry_cli.py::test_main_dispatches_mcp_update_named_through_real_argparse
func TestMCPUpdateNamedThroughRootRunner(t *testing.T) {
	runner, _, stderr := newMCPRunner(t)
	if code := runner.Run([]string{"mcp", "update", "nonexistent"}); code != 1 {
		t.Fatalf("Run(mcp update nonexistent) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not installed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// Parity-exempt: no upstream test drives an invalid mcp subcommand through the
func TestMCPUnknownSubcommandExitsTwo(t *testing.T) {
	runner, _, stderr := newMCPRunner(t)
	if code := runner.Run([]string{"mcp", "bogus"}); code != 2 {
		t.Fatalf("Run(mcp bogus) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "invalid choice") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
