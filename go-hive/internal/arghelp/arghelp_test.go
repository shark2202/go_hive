package arghelp

import (
	"strings"
	"testing"
)

func simpleSpec() Spec {
	return Spec{
		Prog:           "hive-browser interact",
		UsageOptionals: []string{"[-h]", "[--json]", "--action", "{left_click,right_click}", "[--tab TAB]"},
		Options: []Option{
			{Label: "-h, --help", Help: "show this help message and exit"},
			{Label: "--json", Help: "machine output (agent mode)"},
			{Label: "--action {left_click,right_click}", Help: ""},
			{Label: "--tab TAB", Help: "tab id (default: this profile's active tab)"},
		},
	}
}

// Parity-exempt: port of argparse.HelpFormatter's layout rules
// (CPython Lib/argparse.py); the Python side of the contract pins the same
// behaviour through the frozen help capture, not through a CPython unit test.
func TestUsageWrapsWithHangingIndent(t *testing.T) {
	text := simpleSpec().TextAt(80)
	lines := strings.Split(text, "\n")
	if !strings.HasPrefix(lines[0], "usage: hive-browser interact [-h] [--json] --action") {
		t.Fatalf("first usage line = %q", lines[0])
	}
	// The next part does not fit; the continuation line is indented to
	// len("usage: ")+len(prog)+1, exactly like argparse.
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", 29)+"[--tab TAB]") {
		t.Fatalf("continuation line = %q (want 29-space indent)", lines[1])
	}
}

// Parity-exempt: port of argparse.HelpFormatter's layout rules (see above).
func TestOptionColumnIsDynamic(t *testing.T) {
	text := simpleSpec().TextAt(80)
	if !strings.Contains(text, "  -h, --help            show this help message and exit") {
		t.Fatalf("dynamic label column missing:\n%s", text)
	}
	if !strings.Contains(text, "  --tab TAB             tab id (default: this profile's active tab)") {
		t.Fatalf("short label should use the same column:\n%s", text)
	}
	// A label wider than the 20-column action width drops to its own line with
	// help indented to the help column.
	if !strings.Contains(text, "  --action {left_click,right_click}\n") {
		t.Fatalf("long label should stand alone:\n%s", text)
	}
}

// Parity-exempt: port of argparse.HelpFormatter's layout rules (see above).
func TestChoicesAndSubcommandsRenderArgparseStyle(t *testing.T) {
	spec := Spec{
		Prog:             "hive colony",
		UsageOptionals:   []string{"[-h]"},
		UsagePositionals: []string{"{list,info,delete}", "..."},
		Options:          []Option{{Label: "-h, --help", Help: "show this help message and exit"}},
		Choices: []Choice{
			{Name: "list", Help: "List all colonies"},
			{Name: "info", Help: "Show colony details"},
			{Name: "delete", Help: "Delete a colony from disk"},
		},
	}
	text := spec.TextAt(80)
	want := "usage: hive colony [-h] {list,info,delete} ...\n\n" +
		"positional arguments:\n" +
		"  {list,info,delete}\n" +
		"    list              List all colonies\n" +
		"    info              Show colony details\n" +
		"    delete            Delete a colony from disk\n\n" +
		"options:\n" +
		"  -h, --help          show this help message and exit\n"
	if text != want {
		t.Fatalf("group page =\n%q\nwant\n%q", text, want)
	}
}

// Parity-exempt: port of argparse.HelpFormatter's layout rules (see above).
func TestRawDescriptionKeepsLineBreaks(t *testing.T) {
	spec := Spec{
		Prog:           "hive-cred browse",
		UsageOptionals: []string{"[-h]"},
		Options:        []Option{{Label: "-h, --help", Help: "show this help message and exit"}},
		RawDescription: true,
		Epilog:         "examples:\n  hive-cred browse --json\n  hive-cred browse google",
	}
	text := spec.TextAt(80)
	if !strings.HasSuffix(text, "options:\n  -h, --help  show this help message and exit\n\nexamples:\n  hive-cred browse --json\n  hive-cred browse google\n") {
		t.Fatalf("raw epilog =\n%q", text)
	}
}
