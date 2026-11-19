package mcpregistry

// Shell-quoting regression tests: MCP stdio server configs are flattened into
// a single --mcp-stdio command line, so a command or argument containing shell
// metacharacters must survive as data instead of being spliced into a shell.

import (
	"slices"
	"testing"
)

// Parity-exempt: Go-only shell-quoting hardening; the Python reference builds
// the mcp2cli argv directly without an intermediate joined command string.
func TestShellJoinNeutralizesMetacharacters(t *testing.T) {
	words := []string{
		"bash",
		"-c",
		"echo pwned; rm -rf /tmp/hive-shell-test",
		"$(touch /tmp/hive-shell-test-pwned)",
		"`touch /tmp/hive-shell-test-pwned`",
		"a && b || c",
		"line one\nline two",
		"quote ' and \" inside",
		`back\slash`,
		"$HOME",
		"*.go",
		"plain-value",
	}
	joined := ShellJoin(words)
	if joined == "" {
		t.Fatal("ShellJoin returned an empty string")
	}
	roundTripped, err := ShellSplit(joined)
	if err != nil {
		t.Fatalf("ShellSplit(ShellJoin(words)): %v (joined %q)", err, joined)
	}
	if !slices.Equal(roundTripped, words) {
		t.Fatalf("round-trip = %#v, want %#v (joined %q)", roundTripped, words, joined)
	}
	if ShellQuote("plain-value") != "plain-value" {
		t.Fatalf("ShellQuote(plain-value) = %q", ShellQuote("plain-value"))
	}
	for _, dangerous := range words {
		if !shellSafe(dangerous) && ShellQuote(dangerous) == dangerous {
			t.Fatalf("ShellQuote(%q) left metacharacters unquoted", dangerous)
		}
	}
}

// Parity-exempt: Go-only shell-quoting hardening; pins the MCP stdio handoff
// shape so a metacharacter-bearing command reaches mcp2cli as one quoted word
// list, never as a shell fragment.
func TestMCPStdioInvocationQuotesCommandAndArgs(t *testing.T) {
	command := "bash"
	config := &ServerConfig{
		Name:      "test-server",
		Transport: "stdio",
		Command:   &command,
		Args:      []string{"-c", "echo hello; touch /tmp/hive-mcp-pwned"},
	}
	argv, _, _, err := MCP2CLIInvocation(config)
	if err != nil {
		t.Fatalf("MCP2CLIInvocation: %v", err)
	}
	if len(argv) < 2 || argv[0] != "--mcp-stdio" {
		t.Fatalf("argv = %#v, want --mcp-stdio <command>", argv)
	}
	parsed, err := ShellSplit(argv[1])
	if err != nil {
		t.Fatalf("ShellSplit(%q): %v", argv[1], err)
	}
	want := []string{"bash", "-c", "echo hello; touch /tmp/hive-mcp-pwned"}
	if !slices.Equal(parsed, want) {
		t.Fatalf("parsed command = %#v, want %#v", parsed, want)
	}
}
