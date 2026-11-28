package cliargs

// Hidden / SUPPRESS renderer tests: hidden commands and flags must stay out of
// every rendered help surface (usage choice set, commands block, options block)
// while remaining fully parseable, runnable and resolvable by name. Upstream
// has no hidden subcommands to port; the analogous upstream mechanism is
// argparse.SUPPRESS (`--profile` in tools/src/gcu/cli.py).
//
// Parity-exempt: pins the parser's Hidden support (the argparse.SUPPRESS
// analogue) — upstream Python has no hidden subcommand, and the mechanism has
// no behaviour beyond rendering.

import (
	"strings"
	"testing"
)

// Parity-exempt: pins the parser's Hidden support; upstream has no hidden subcommand.
func TestHiddenCommandStaysOutOfHelpButRuns(t *testing.T) {
	ranWith := ""
	hidden := &CommandSpec[ParsedArgs]{
		Prog:   "tool secret",
		Help:   "internal verb",
		Hidden: true,
		Flags: []ArgSpec{
			{Dest: "level", Long: "level", Kind: KindString, Default: "info", Hidden: true},
		},
		Run: func(args *ParsedArgs) int {
			ranWith = args.str("level")
			return 0
		},
	}
	visible := &CommandSpec[ParsedArgs]{Prog: "tool show", Help: "visible verb", Run: func(*ParsedArgs) int { return 0 }}
	root := &CommandSpec[ParsedArgs]{
		Prog:        "tool",
		Dest:        "command",
		Flags:       []ArgSpec{{Dest: "verbose", Long: "verbose", Kind: KindBool, Help: "visible flag"}},
		Subcommands: []*CommandSpec[ParsedArgs]{visible, hidden},
	}

	help := root.HelpText()
	if strings.Contains(help, "secret") {
		t.Fatalf("hidden command leaked into root help:\n%s", help)
	}
	if strings.Contains(help, "--level") {
		t.Fatalf("hidden flag leaked into root help:\n%s", help)
	}
	if !strings.Contains(help, "show") {
		t.Fatalf("visible command missing from root help:\n%s", help)
	}

	args, err := ParseCommand(root, []string{"secret", "--level", "debug"})
	if err != nil {
		t.Fatalf("parseCommand(secret): %v", err)
	}
	if args.cmd != hidden {
		t.Fatalf("cmd = %p, want the hidden command %p", args.cmd, hidden)
	}
	if code := args.cmd.(*CommandSpec[ParsedArgs]).Run(args); code != 0 || ranWith != "debug" {
		t.Fatalf("hidden command run: code=%d level=%q, want 0/debug", code, ranWith)
	}
	if FindSubcommand(root, "secret") != hidden {
		t.Fatal("findSubcommand must still resolve hidden commands")
	}

	// An unknown choice lists only the visible verbs.
	if _, err := ParseCommand(root, []string{"nosuch"}); err == nil ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid-choice error = %v, want it to hide %q", err, "secret")
	}
}

// Parity-exempt: InvokeAs is a Go-side mounting mechanism, used to expose the
// standalone `hive-global-db` argparse pages under `hive global-db`; upstream
// argparse has no program-alias concept. It must resolve the alias while the
// rendered pages keep the standalone program's prog.
func TestInvokeAsResolvesStandaloneProgram(t *testing.T) {
	sub := &CommandSpec[ParsedArgs]{
		Prog:     "hive-global-db",
		Help:     "standalone program",
		InvokeAs: "global-db",
		Hidden:   true,
		Run:      func(*ParsedArgs) int { return 0 },
	}
	root := &CommandSpec[ParsedArgs]{Prog: "hive", Dest: "command", Subcommands: []*CommandSpec[ParsedArgs]{sub}}

	if FindSubcommand(root, "global-db") != sub {
		t.Fatal("InvokeAs alias must resolve")
	}
	args, err := ParseCommand(root, []string{"global-db"})
	if err != nil {
		t.Fatalf("parseCommand(global-db): %v", err)
	}
	if args.cmd != sub {
		t.Fatalf("cmd = %p, want the aliased command %p", args.cmd, sub)
	}
	if help := sub.HelpText(); !strings.Contains(help, "usage: hive-global-db") {
		t.Fatalf("aliased command lost its standalone prog:\n%s", help)
	}
	// A hidden alias stays out of the parent's choice set.
	if names := SubcommandNames(root.Subcommands); names != "" {
		t.Fatalf("hidden alias leaked into the choice set: %q", names)
	}
}

// Parity-exempt: same renderer mechanism as above, leaf-level — a hidden flag
// keeps its default/parse behaviour (the Go counterpart of the upstream
// SUPPRESSed --profile) but renders nowhere.
func TestHiddenFlagStaysOutOfHelpButParses(t *testing.T) {
	spec := &CommandSpec[ParsedArgs]{
		Prog: "tool run",
		Flags: []ArgSpec{
			{Dest: "target", Long: "target", Kind: KindString, Help: "visible"},
			{Dest: "bridge", Long: "bridge", Kind: KindString, Default: "http://127.0.0.1:14830", Hidden: true},
		},
		Run: func(*ParsedArgs) int { return 0 },
	}

	help := spec.HelpText()
	if strings.Contains(help, "--bridge") {
		t.Fatalf("hidden flag leaked into help:\n%s", help)
	}
	if !strings.Contains(help, "--target") {
		t.Fatalf("visible flag missing from help:\n%s", help)
	}

	args, err := ParseCommand(spec, []string{"--bridge", "http://127.0.0.1:9", "--target", "t"})
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
