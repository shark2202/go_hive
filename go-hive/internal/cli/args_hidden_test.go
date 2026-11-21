package cli

// Hidden / SUPPRESS renderer tests: hidden commands and flags must stay out of
// every rendered help surface (usage choice set, commands block, options block)
// while remaining fully parseable, runnable and resolvable by name. Upstream
// has no hidden subcommands to port; the analogous upstream mechanism is
// argparse.SUPPRESS (`--profile` in tools/src/gcu/cli.py).

import (
	"io"
	"os"
	"strings"
	"testing"
)

// Parity-exempt: pins the Go help renderer's Hidden support (the
// argparse.SUPPRESS analogue) — upstream Python has no hidden subcommand, and
// the mechanism has no behaviour beyond rendering.
func TestHiddenCommandStaysOutOfHelpButRuns(t *testing.T) {
	ranWith := ""
	hidden := &commandSpec{
		Prog:   "tool secret",
		Help:   "internal verb",
		Hidden: true,
		Flags: []argSpec{
			{Dest: "level", Long: "level", Kind: kindString, Default: "info", Hidden: true},
		},
		Run: func(args *parsedArgs) int {
			ranWith = args.str("level")
			return 0
		},
	}
	visible := &commandSpec{Prog: "tool show", Help: "visible verb", Run: func(*parsedArgs) int { return 0 }}
	root := &commandSpec{
		Prog:        "tool",
		Dest:        "command",
		Flags:       []argSpec{{Dest: "verbose", Long: "verbose", Kind: kindBool, Help: "visible flag"}},
		Subcommands: []*commandSpec{visible, hidden},
	}

	help := root.helpText()
	if strings.Contains(help, "secret") {
		t.Fatalf("hidden command leaked into root help:\n%s", help)
	}
	if strings.Contains(help, "--level") {
		t.Fatalf("hidden flag leaked into root help:\n%s", help)
	}
	if !strings.Contains(help, "show") {
		t.Fatalf("visible command missing from root help:\n%s", help)
	}

	args, err := parseCommand(root, []string{"secret", "--level", "debug"})
	if err != nil {
		t.Fatalf("parseCommand(secret): %v", err)
	}
	if args.cmd != hidden {
		t.Fatalf("cmd = %p, want the hidden command %p", args.cmd, hidden)
	}
	if code := args.cmd.Run(args); code != 0 || ranWith != "debug" {
		t.Fatalf("hidden command run: code=%d level=%q, want 0/debug", code, ranWith)
	}
	if findSubcommand(root, "secret") != hidden {
		t.Fatal("findSubcommand must still resolve hidden commands")
	}

	// An unknown choice lists only the visible verbs.
	if _, err := parseCommand(root, []string{"nosuch"}); err == nil ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid-choice error = %v, want it to hide %q", err, "secret")
	}
}

// Parity-exempt: same renderer mechanism as above, leaf-level — a hidden flag
// keeps its default/parse behaviour (the Go counterpart of the upstream
// SUPPRESSed --profile) but renders nowhere.
func TestHiddenFlagStaysOutOfHelpButParses(t *testing.T) {
	spec := &commandSpec{
		Prog: "tool run",
		Flags: []argSpec{
			{Dest: "target", Long: "target", Kind: kindString, Help: "visible"},
			{Dest: "bridge", Long: "bridge", Kind: kindString, Default: "http://127.0.0.1:14830", Hidden: true},
		},
		Run: func(*parsedArgs) int { return 0 },
	}

	help := spec.helpText()
	if strings.Contains(help, "--bridge") {
		t.Fatalf("hidden flag leaked into help:\n%s", help)
	}
	if !strings.Contains(help, "--target") {
		t.Fatalf("visible flag missing from help:\n%s", help)
	}

	args, err := parseCommand(spec, []string{"--bridge", "http://127.0.0.1:9", "--target", "t"})
	if err != nil {
		t.Fatalf("parseCommand: %v", err)
	}
	if got := args.str("bridge"); got != "http://127.0.0.1:9" {
		t.Fatalf("hidden flag value = %q", got)
	}
	if got := args.str("target"); got != "t" {
		t.Fatalf("visible flag value = %q", got)
	}
}

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

// Parity-exempt: Go-only hidden browser extensions (legacy verbs, --bridge)
// plus the upstream-SUPPRESSed --profile; pins that none of them render while
// the shared verbs still do. Running the verbs themselves is covered by
// browser_cli_test.go (they are exercised with --bridge against a stub bridge).
func TestBrowserHelpHidesLegacyVerbsAndInternalFlags(t *testing.T) {
	app := &browserCLI{stdout: io.Discard, stderr: io.Discard, env: os.Getenv, stdin: strings.NewReader("")}
	root := app.rootCommand()
	rootHelp := root.helpText()

	choiceSet := subcommandNames(root.Subcommands)
	for _, verb := range []string{"click", "type", "type-focused", "press", "scroll"} {
		for _, name := range strings.Split(choiceSet, ",") {
			if name == verb {
				t.Fatalf("legacy verb %q leaked into the usage choice set: %s", verb, choiceSet)
			}
		}
		if findSubcommand(root, verb) == nil {
			t.Fatalf("legacy verb %q must stay resolvable by name", verb)
		}
		if strings.Contains(rootHelp, "\n  "+verb+" ") {
			t.Fatalf("legacy verb %q leaked into the commands block:\n%s", verb, rootHelp)
		}
	}
	for _, flag := range []string{"--bridge", "--profile"} {
		if strings.Contains(rootHelp, flag) {
			t.Fatalf("hidden flag %s leaked into root help:\n%s", flag, rootHelp)
		}
		leafHelp := findSubcommand(root, "status").helpText()
		if strings.Contains(leafHelp, flag) {
			t.Fatalf("hidden flag %s leaked into `status --help`:\n%s", flag, leafHelp)
		}
	}
	if !strings.Contains(rootHelp, "interact") || !strings.Contains(rootHelp, "upload") {
		t.Fatalf("visible verbs missing from root help:\n%s", rootHelp)
	}

	// The `tab` subcommands keep Go's per-command --tab callable but hidden
	// (upstream's tab subparsers define no --tab).
	tabGroup := findSubcommand(root, "tab")
	if tabGroup == nil {
		t.Fatal("tab group missing")
	}
	for _, verb := range []string{"list", "close", "activate"} {
		sub := findSubcommand(tabGroup, verb)
		if sub == nil {
			t.Fatalf("tab %s missing", verb)
		}
		if strings.Contains(sub.helpText(), "--tab") {
			t.Fatalf("hidden --tab leaked into `tab %s --help`:\n%s", verb, sub.helpText())
		}
	}
	tabArgs, err := parseCommand(root, []string{"tab", "list", "--tab", "7"})
	if err != nil {
		t.Fatalf("parseCommand(tab list --tab 7): %v", err)
	}
	if tabArgs.intValue("tab") != 7 {
		t.Fatalf("hidden --tab value = %d, want 7", tabArgs.intValue("tab"))
	}

	// Hidden verbs stay runnable through the parser (exit through the bridge
	// connect path, not a parse error).
	args, err := parseCommand(root, []string{"click", "#b", "--bridge", "http://127.0.0.1:9"})
	if err != nil {
		t.Fatalf("parseCommand(click): %v", err)
	}
	if args.cmd == nil || progTail(args.cmd.Prog) != "click" {
		t.Fatalf("click did not resolve to its own command: %#v", args.cmd)
	}
}
