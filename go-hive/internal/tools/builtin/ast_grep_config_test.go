package builtin

// Registration-gate tests for the Go-only ast_grep tool. The tool itself is
// unchanged; these pin the frozen opt-in config chain that decides whether the
// session catalogue may advertise it.
//
// Parity-exempt: ast_grep is a Go-only post-1.0 tool; upstream has no
// ast_grep.json gate or registration surface to mirror.

import (
	"os"
	"path/filepath"
	"testing"
)

// writeASTGrepConfig writes one gate file, creating its parent directory.
func writeASTGrepConfig(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestASTGrepEnabledUserOnly pins the single operator-level chain: missing
// config is off, <home>/ast_grep.json opts in, enabled=false opts out, and an
// empty home falls back to HIVE_HOME.
//
// Parity-exempt: Go-only ast_grep registration gate; no Python counterpart.
func TestASTGrepEnabledUserOnly(t *testing.T) {
	home := t.TempDir()
	user := filepath.Join(home, "ast_grep.json")

	// Missing file: default off.
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled with no config file")
	}

	// The user file opts in.
	writeASTGrepConfig(t, user, `{"enabled":true}`)
	if !ASTGrepEnabled(home) {
		t.Fatal("ast_grep disabled while the user file says enabled=true")
	}

	// enabled=false opts out.
	writeASTGrepConfig(t, user, `{"enabled":false}`)
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled while the user file says enabled=false")
	}

	// A present object without an explicit enabled boolean stays off.
	writeASTGrepConfig(t, user, `{}`)
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled by a file without an enabled field")
	}

	// An empty home falls back to HIVE_HOME.
	t.Setenv("HIVE_HOME", home)
	writeASTGrepConfig(t, user, `{"enabled":true}`)
	if !ASTGrepEnabled("") {
		t.Fatal("ast_grep disabled while $HIVE_HOME/ast_grep.json says enabled=true")
	}
}

// TestASTGrepEnabledIgnoresSessionConfig proves the session directory is not
// a config source: it is the file tools' sandbox root, so an agent-written
// <sessionDir>/.hive/ast_grep.json must not opt the tool in.
//
// Parity-exempt: Go-only security regression test; no Python counterpart.
func TestASTGrepEnabledIgnoresSessionConfig(t *testing.T) {
	home := t.TempDir()
	session := t.TempDir()
	writeASTGrepConfig(t, filepath.Join(session, ".hive", "ast_grep.json"), `{"enabled":true}`)
	if ASTGrepEnabled(home) {
		t.Fatal("session-dir config enabled ast_grep; config must come from $HIVE_HOME only")
	}
}

// TestASTGrepEnabledFailsClosed pins malformed and unreadable config to false.
//
// Parity-exempt: Go-only ast_grep registration gate; no Python counterpart.
func TestASTGrepEnabledFailsClosed(t *testing.T) {
	home := t.TempDir()
	user := filepath.Join(home, "ast_grep.json")

	// Malformed user JSON: off.
	writeASTGrepConfig(t, user, `{not json`)
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled by malformed user JSON")
	}

	// enabled must be a boolean, not a truthy string.
	writeASTGrepConfig(t, user, `{"enabled":"true"}`)
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled by a non-boolean enabled field")
	}

	// An unreadable path (a directory named ast_grep.json) is also off.
	if err := os.Remove(user); err != nil {
		t.Fatalf("remove user config: %v", err)
	}
	if err := os.Mkdir(user, 0o755); err != nil {
		t.Fatalf("mkdir user config path: %v", err)
	}
	if ASTGrepEnabled(home) {
		t.Fatal("ast_grep enabled by an unreadable user config path")
	}
}
