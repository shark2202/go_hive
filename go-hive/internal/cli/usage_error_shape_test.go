package cli

// Parity: core/framework/cli.py::main
// Parity: core/framework/loader/cli.py::register_commands
// Parity: core/framework/skills/cli.py::register_skill_commands
//
// The error surface of an argparse CLI, measured against the Python side at
// COLUMNS=100. Everything argparse prints for a parse failure goes to stderr
// and exits 2, in two shapes:
//
//   * a usage failure prints the RESPONSIBLE page's usage block (wrapped), then
//     "<prog>: error: <message>";
//   * "unrecognized arguments" is raised by the top-level parse_args after the
//     whole command tree parsed, so it always prints the ROOT page — even when
//     the offending token sat on a leaf;
//   * a missing required argument (positional or subcommand) is raised by the
//     parser that declares it, BEFORE the root reports extras, so it wins over
//     an unknown flag collected on the way.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
)

func newUsageShapeRunner(t *testing.T) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("COLUMNS", "100")
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	return runner, &stdout, &stderr
}

const rootUsageTwoLines = "usage: hive [-h] [--version] [--model MODEL]\n" +
	"            {serve,open,queen,colony,session,chat,skill,debugger,mcp,janitor} ...\n"

// Parity: core/framework/cli.py::main
func TestUsageErrorShape(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			// A root-level unknown flag does not hide the required subcommand:
			// argparse checks its required actions before parse_args reports
			// the extras, so the required error wins.
			name: "root unknown flag with no subcommand",
			argv: []string{"--bogus"},
			want: rootUsageTwoLines +
				"hive: error: the following arguments are required: command\n",
		},
		{
			name: "leaf unknown flag prints the root page",
			argv: []string{"skill", "doctor", "--bogus"},
			want: rootUsageTwoLines +
				"hive: error: unrecognized arguments: --bogus\n",
		},
		{
			// The group's required subcommand fires before the root extras,
			// and its page is the group's.
			name: "group unknown flag with no subcommand",
			argv: []string{"skill", "--bogus"},
			want: "usage: hive skill [-h]\n" +
				"                  {list,install,remove,info,init,validate,doctor,update,search,fork,test,trust}\n" +
				"                  ...\n" +
				"hive skill: error: the following arguments are required: skill_command\n",
		},
		{
			name: "leaf missing required positional",
			argv: []string{"skill", "validate"},
			want: "usage: hive skill validate [-h] [--json] path\n" +
				"hive skill validate: error: the following arguments are required: path\n",
		},
		{
			name: "root invalid choice (single-quoted name)",
			argv: []string{"nosuchcmd"},
			want: rootUsageTwoLines +
				"hive: error: argument command: invalid choice: 'nosuchcmd' " +
				"(choose from 'serve', 'open', 'queen', 'colony', 'session', 'chat', 'skill', " +
				"'debugger', 'mcp', 'janitor')\n",
		},
		{
			// A positional the leaf has no slot for is an argparse extra, so
			// the root page reports it.
			name: "extra positional prints the root page",
			argv: []string{"queen", "show", "a", "b"},
			want: rootUsageTwoLines +
				"hive: error: unrecognized arguments: b\n",
		},
		{
			name: "flag missing its value",
			argv: []string{"--model"},
			want: rootUsageTwoLines +
				"hive: error: argument --model: expected one argument\n",
		},
		{
			// A value-taking flag refuses a token that looks like an option:
			// argparse matches it against the 'A' pattern.
			name: "value refuses an option-like token",
			argv: []string{"session", "stop", "--server", "--json", "s"},
			want: "usage: hive session stop [-h] [--server SERVER] session_id\n" +
				"hive session stop: error: argument --server: expected one argument\n",
		},
		{
			name: "bool flag with explicit argument",
			argv: []string{"session", "list", "--json=1"},
			want: "usage: hive session list [-h] [--cold] [--server SERVER] [--json]\n" +
				"hive session list: error: argument --json: ignored explicit argument '1'\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner, stdout, stderr := newUsageShapeRunner(t)
			code := runner.Run(testCase.argv)
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != testCase.want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), testCase.want)
			}
		})
	}
}

// Parity: core/framework/cli.py — argparse's --version action fires while the
// token stream is consumed, so it wins over an earlier unknown flag.
func TestUnknownFlagDoesNotHideVersion(t *testing.T) {
	runner, stdout, stderr := newUsageShapeRunner(t)
	if code := runner.Run([]string{"--bogus", "--version"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.HasPrefix(stdout.String(), "hive ") {
		t.Fatalf("stdout = %q, want the version line", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

// Parity: core/framework/cli.py — argparse's allow_abbrev is on by default, so
// a unique prefix of a long option resolves to it.
func TestUniqueLongFlagPrefixResolves(t *testing.T) {
	runner, stdout, stderr := newUsageShapeRunner(t)
	if code := runner.Run([]string{"--vers"}); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "hive ") {
		t.Fatalf("stdout = %q, want the version line", stdout.String())
	}
}

// Parity: framework/loader/mcp_registry_cli.py — an unknown flag on an mcp verb
// is an argparse extra, so the ROOT page reports it (not the verb's page).
func TestMCPVerbUnknownFlagPrintsRootPage(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	root := runner.rootCommand()
	runner.MCPMain = func(argv []string) int {
		mcpCLI := &mcpregistry.CLI{
			Out: &stdout, Err: &stderr, In: strings.NewReader(""),
			RootProg:  "hive",
			RootUsage: func() string { return root.HelpSpec().UsageText() },
		}
		return mcpCLI.Main(argv)
	}

	if code := runner.Run([]string{"mcp", "list", "--bogus"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	want := rootUsageTwoLines + "hive: error: unrecognized arguments: --bogus\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
