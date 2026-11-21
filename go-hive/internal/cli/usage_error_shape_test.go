package cli

// Parity: core/framework/cli.py + framework/loader/cli.py + framework/skills/cli.py
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

	"github.com/aden-hive/hive-go/internal/mcpregistry"
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

// Parity: tools/src/gcu/cli.py::_build_parser — required=True flags and
// choices= are argparse-level checks: the page's usage block, then
// "hive-browser <verb>: error: ...", exit 2, before any command body runs.
func TestBrowserUsageErrorShape(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	t.Setenv("HIVE_HOME", t.TempDir())
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{
			// The missing action is reported against the declaring page; the
			// action's own choice set renders in the usage block.
			name: "required flag",
			argv: []string{"interact"},
			want: "usage: hive-browser interact [-h] [--json] --action\n" +
				"                             {left_click,right_click,middle_click,double_click,triple_click,hover,type,key,scroll,drag,screenshot,zoom,wait}\n" +
				"                             [--tab TAB] [--selector SELECTOR] [--coordinate x,y]\n" +
				"                             [--start-selector START_SELECTOR] [--start-coordinate x,y]\n" +
				"                             [--text TEXT] [--no-clear-first] [--no-insert-text]\n" +
				"                             [--modifiers MODIFIERS] [--repeat REPEAT]\n" +
				"                             [--scroll-direction {up,down,left,right}]\n" +
				"                             [--scroll-amount SCROLL_AMOUNT] [--intent INTENT] [--full-page]\n" +
				"                             [--no-annotate] [--region x0,y0,x1,y1] [--duration DURATION]\n" +
				"                             [--wait-for-selector WAIT_FOR_SELECTOR]\n" +
				"                             [--wait-for-text WAIT_FOR_TEXT] [--timeout-ms TIMEOUT_MS]\n" +
				"                             [--auto-snapshot-mode {simple,default,interactive,off}]\n" +
				"                             [--wait-after-ms WAIT_AFTER_MS]\n" +
				"hive-browser interact: error: the following arguments are required: --action\n",
		},
		{
			// A required positional and a required flag are one argparse
			// check, listed in declaration order (positional declared first).
			name: "required positional and flag together",
			argv: []string{"upload"},
			want: "usage: hive-browser upload [-h] [--json] --file FILE [--trigger-selector TRIGGER_SELECTOR]\n" +
				"                           [--tab TAB] [--timeout-ms TIMEOUT_MS]\n" +
				"                           selector\n" +
				"hive-browser upload: error: the following arguments are required: selector, --file\n",
		},
		{
			name: "only the still-missing required flag is listed",
			argv: []string{"page", "resize", "--width", "10"},
			want: "usage: hive-browser page resize [-h] [--json] --width WIDTH --height HEIGHT [--tab TAB]\n" +
				"hive-browser page resize: error: the following arguments are required: --height\n",
		},
		{
			name: "flag choice",
			argv: []string{"navigate", "u", "--wait-until", "bogus"},
			want: "usage: hive-browser navigate [-h] [--json] [--tab TAB]\n" +
				"                             [--wait-until {commit,domcontentloaded,load,networkidle}]\n" +
				"                             [--timeout-ms TIMEOUT_MS] [--browser-profile BROWSER_PROFILE]\n" +
				"                             url\n" +
				"hive-browser navigate: error: argument --wait-until: invalid choice: 'bogus' " +
				"(choose from 'commit', 'domcontentloaded', 'load', 'networkidle')\n",
		},
		{
			// A positional choice is named by its dest in argparse's message.
			name: "positional choice",
			argv: []string{"dialog", "respond", "maybe"},
			want: "usage: hive-browser dialog respond [-h] [--json] [--prompt-text PROMPT_TEXT] [--tab TAB]\n" +
				"                                   {accept,dismiss}\n" +
				"hive-browser dialog respond: error: argument action: invalid choice: 'maybe' " +
				"(choose from 'accept', 'dismiss')\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stdout, stderr, code := runBrowser(t, t.TempDir(), testCase.argv...)
			if code != browserExitUsage {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, browserExitUsage, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if stderr != testCase.want {
				t.Fatalf("stderr = %q, want %q", stderr, testCase.want)
			}
		})
	}
}

// Parity: tools/src/gcu/cli_commands/script.py::cmd_script — these two are
// command-level validations (exit 6), not argparse: the message keeps
// upstream's wording and the "error: " prefix of gcu.errors.run.
func TestBrowserScriptValidationMessages(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	cases := []struct {
		argv []string
		want string
	}{
		{
			argv: []string{"script"},
			want: "error: --skill is required (scripts are scoped to a skill dir). " +
				"For ad-hoc JavaScript, use: hive-browser evaluate --js '<code>'\n",
		},
		{
			argv: []string{"script", "--skill", "s"},
			want: "error: --script is required (a script file under <skill>/scripts/)\n",
		},
	}
	for _, testCase := range cases {
		stdout, stderr, code := runBrowser(t, t.TempDir(), testCase.argv...)
		if code != browserExitBadArgs {
			t.Fatalf("%v: exit = %d, want %d", testCase.argv, code, browserExitBadArgs)
		}
		if stdout != "" || stderr != testCase.want {
			t.Fatalf("%v: stdout=%q stderr=%q, want stderr %q", testCase.argv, stdout, stderr, testCase.want)
		}
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
			RootUsage: func() string { return root.helpSpec().UsageText() },
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
