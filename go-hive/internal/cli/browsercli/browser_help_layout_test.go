package browsercli

// Parity: tools/src/gcu/cli.py::_build_parser

import (
	"testing"
)

// TestDialogRespondRendersChoicePositional pins argparse's positional choice
// rendering: `{accept,dismiss}` instead of a named slot.
//
// Parity: tools/src/gcu/cli.py::_build_parser
func TestDialogRespondRendersChoicePositional(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	stdout, _, code := runBrowser(t, "", "dialog", "respond", "--help")
	if code != browserExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	want := "usage: hive-browser dialog respond [-h] [--json] [--prompt-text PROMPT_TEXT]\n" +
		"                                   [--tab TAB]\n" +
		"                                   {accept,dismiss}\n" +
		"\n" +
		"positional arguments:\n" +
		"  {accept,dismiss}\n" +
		"\n" +
		"options:\n" +
		"  -h, --help            show this help message and exit\n" +
		"  --json                machine output (agent mode)\n" +
		"  --prompt-text PROMPT_TEXT\n" +
		"  --tab TAB\n"
	if stdout != want {
		t.Fatalf("dialog respond help =\n%q\nwant\n%q", stdout, want)
	}
}
