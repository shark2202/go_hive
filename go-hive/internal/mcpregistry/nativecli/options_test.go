package nativecli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Parity-exempt: direct port of mcp2cli's _split_at_subcommand.
func TestSplitAtSubcommand(t *testing.T) {
	cases := []struct {
		argv   []string
		global []string
		tool   []string
	}{
		{
			argv:   []string{"--mcp-stdio", "srv --flag", "--list"},
			global: []string{"--mcp-stdio", "srv --flag", "--list"},
		},
		{
			argv:   []string{"--mcp", "http://x", "--json", "echo", "--text", "hi"},
			global: []string{"--mcp", "http://x", "--json"},
			tool:   []string{"echo", "--text", "hi"},
		},
		{
			// A tool parameter that shadows a global option stays with the tool.
			argv:   []string{"--mcp-stdio", "srv", "env-check", "--env", "X=1"},
			global: []string{"--mcp-stdio", "srv"},
			tool:   []string{"env-check", "--env", "X=1"},
		},
		{
			argv:   []string{"--mcp", "http://x", "--", "echo", "--raw"},
			global: []string{"--mcp", "http://x"},
			tool:   []string{"echo", "--raw"},
		},
		{
			argv:   []string{"--cache-key=k1", "--list"},
			global: []string{"--cache-key=k1", "--list"},
		},
	}
	for _, item := range cases {
		global, tool := splitAtSubcommand(item.argv)
		if !reflect.DeepEqual(global, item.global) {
			t.Fatalf("split(%v) global = %v, want %v", item.argv, global, item.global)
		}
		if !reflect.DeepEqual(tool, item.tool) {
			t.Fatalf("split(%v) tool = %v, want %v", item.argv, tool, item.tool)
		}
	}
}

// Parity-exempt: direct port of argparse's value parsing for the global options.
func TestParseGlobalOptionValues(t *testing.T) {
	options, leftover, err := parseGlobal([]string{"--mcp-stdio", "srv x", "--cache-ttl", "42", "--head", "3", "--transport", "sse", "--json", "--nope", "tool"})
	if err != nil {
		t.Fatalf("parseGlobal: %v", err)
	}
	if options.MCPStdio != "srv x" || options.CacheTTL != 42 || options.Head != 3 {
		t.Fatalf("options = %+v", options)
	}
	if options.Transport != "sse" || !options.JSON {
		t.Fatalf("options = %+v", options)
	}
	if !reflect.DeepEqual(leftover, []string{"--nope", "tool"}) {
		t.Fatalf("leftover = %v", leftover)
	}
}

// Parity-exempt: direct port of argparse's conversion errors.
func TestParseGlobalRejectsBadValues(t *testing.T) {
	_, _, err := parseGlobal([]string{"--cache-ttl", "abc"})
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(usage.render(), "argument --cache-ttl: invalid int value: 'abc'") {
		t.Fatalf("render = %q", usage.render())
	}

	_, _, err = parseGlobal([]string{"--transport", "bogus"})
	if !errors.As(err, &usage) || !strings.Contains(usage.render(), "invalid choice: 'bogus'") {
		t.Fatalf("transport err = %v", err)
	}

	_, _, err = parseGlobal([]string{"--mcp"})
	if !errors.As(err, &usage) || !strings.Contains(usage.render(), "expected one argument") {
		t.Fatalf("missing value err = %v", err)
	}
}

// Parity-exempt: golden output captured from mcp2cli 3.8.0 for the same tool schema
// (also asserted byte-for-byte against the binary in TestParityWithReferenceStdio).
func TestRenderToolHelpMatchesReference(t *testing.T) {
	commands := extractCommands(fixtureTools())
	help := renderToolHelp(&commands[0])
	want := "usage: mcp2cli echo [-h] [--stdin] [--text TEXT] [--loud]\n" +
		"\n" +
		"Echo the text back, optionally shouting.\n" +
		"\n" +
		"options:\n" +
		"  -h, --help   show this help message and exit\n" +
		"  --stdin      Read JSON body/arguments from stdin; parameter flags are merged\n" +
		"               in (a conflicting value is an error)\n" +
		"  --text TEXT  Text to echo\n" +
		"  --loud       Uppercase the text\n"
	if help != want {
		t.Fatalf("help =\n%s\nwant\n%s", help, want)
	}
}

// Parity-exempt: direct port of build_argparse's subcommand parsing.
func TestParseToolArgs(t *testing.T) {
	commands := extractCommands(fixtureTools())
	var echo *Command
	for index := range commands {
		if commands[index].Name == "echo" {
			echo = &commands[index]
		}
	}
	if echo == nil {
		t.Fatal("echo tool missing")
	}

	values, stdin, help, extras, failure := parseToolArgs(echo, []string{"--text=hi", "--loud", "--stdin"})
	if failure != nil {
		t.Fatalf("failure: %v", failure.message)
	}
	if help || len(extras) > 0 {
		t.Fatalf("help = %v, extras = %v", help, extras)
	}
	if !stdin || values["text"] != "hi" || values["loud"] != true {
		t.Fatalf("values = %v, stdin = %v", values, stdin)
	}

	_, _, helpFlag, _, _ := parseToolArgs(echo, []string{"--help"})
	if !helpFlag {
		t.Fatal("--help was not detected")
	}

	_, _, _, extras, _ = parseToolArgs(echo, []string{"--text", "hi", "--nope", "stray"})
	if !reflect.DeepEqual(extras, []string{"--nope", "stray"}) {
		t.Fatalf("extras = %v", extras)
	}

	_, _, _, _, failure = parseToolArgs(echo, []string{"--text"})
	if failure == nil || !strings.Contains(failure.render(), "argument --text: expected one argument") {
		t.Fatalf("failure = %v", failure)
	}
}

// Parity-exempt: direct port of argparse's type conversion.
func TestConvertArg(t *testing.T) {
	if value, err := convertArg("12", kindInt); err != nil || value.(int64) != 12 {
		t.Fatalf("int = %v, %v", value, err)
	}
	if value, err := convertArg("1.5", kindFloat); err != nil || value.(float64) != 1.5 {
		t.Fatalf("float = %v, %v", value, err)
	}
	if _, err := convertArg("x", kindInt); err == nil {
		t.Fatal("expected an int conversion error")
	}
	if value, err := convertArg("abc", kindUnion); err != nil || value != "abc" {
		t.Fatalf("union = %v, %v", value, err)
	}
	if value, err := convertArg("7", kindUnion); err != nil || value.(int64) != 7 {
		t.Fatalf("union numeric = %v, %v", value, err)
	}
}
