package browsercli

// Integration coverage for the hidden/SUPPRESS surfaces of the hive-browser
// root. The parser-level renderer tests moved with the parser to
// internal/cli/cliargs.

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/cli/cliargs"
)

// Parity-exempt: Go-only hidden browser extensions (legacy verbs, --bridge)
// plus the upstream-SUPPRESSed --profile; pins that none of them render while
// the shared verbs still do. Running the verbs themselves is covered by
// browser_cli_test.go (they are exercised with --bridge against a stub bridge).
func TestBrowserHelpHidesLegacyVerbsAndInternalFlags(t *testing.T) {
	app := &browserCLI{stdout: io.Discard, stderr: io.Discard, env: os.Getenv, stdin: strings.NewReader("")}
	root := app.rootCommand()
	rootHelp := root.HelpText()

	choiceSet := cliargs.SubcommandNames(root.Subcommands)
	for _, verb := range []string{"click", "type", "type-focused", "press", "scroll"} {
		for _, name := range strings.Split(choiceSet, ",") {
			if name == verb {
				t.Fatalf("legacy verb %q leaked into the usage choice set: %s", verb, choiceSet)
			}
		}
		if cliargs.FindSubcommand(root, verb) == nil {
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
		leafHelp := cliargs.FindSubcommand(root, "status").HelpText()
		if strings.Contains(leafHelp, flag) {
			t.Fatalf("hidden flag %s leaked into `status --help`:\n%s", flag, leafHelp)
		}
	}
	if !strings.Contains(rootHelp, "interact") || !strings.Contains(rootHelp, "upload") {
		t.Fatalf("visible verbs missing from root help:\n%s", rootHelp)
	}

	// The `tab` subcommands keep Go's per-command --tab callable but hidden
	// (upstream's tab subparsers define no --tab).
	tabGroup := cliargs.FindSubcommand(root, "tab")
	if tabGroup == nil {
		t.Fatal("tab group missing")
	}
	for _, verb := range []string{"list", "close", "activate"} {
		sub := cliargs.FindSubcommand(tabGroup, verb)
		if sub == nil {
			t.Fatalf("tab %s missing", verb)
		}
		if strings.Contains(sub.HelpText(), "--tab") {
			t.Fatalf("hidden --tab leaked into `tab %s --help`:\n%s", verb, sub.HelpText())
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
	if args.cmd == nil || cliargs.ProgTail(args.cmd.Prog) != "click" {
		t.Fatalf("click did not resolve to its own command: %#v", args.cmd)
	}
}
