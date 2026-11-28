package browsercli

// Parity: tools/src/gcu/cli.py::_build_parser — the argparse error surface of
// the hive-browser pages.

import (
	"testing"
)

// testTempDir isolates the process-global HOME state these cases share.
func testTempDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	return home
}

// Parity: tools/src/gcu/cli.py::_build_parser — required=True flags and
// choices= are argparse-level checks: the page's usage block, then
// "hive-browser <verb>: error: ...", exit 2, before any command body runs.
func TestBrowserUsageErrorShape(t *testing.T) {
	t.Setenv("COLUMNS", "100")
	testTempDir(t)
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
	testTempDir(t)
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
