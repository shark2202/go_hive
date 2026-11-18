package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shellHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	return home
}

// Parity-exempt: Python's shell helpers take the shell name explicitly; there is
func TestDetectShell(t *testing.T) {
	home := shellHome(t)
	if got := DetectShell(); got != ShellZsh {
		t.Fatalf("SHELL=zsh detected %q", got)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/bin/bash")
	if got := DetectShell(); got != ShellBash {
		t.Fatalf("SHELL=bash detected %q", got)
	}
	t.Setenv("SHELL", "/bin/fish")
	if got := DetectShell(); got != ShellBash {
		t.Fatalf("config fallback detected %q", got)
	}
}

// Parity: tools/tests/test_shell_config.py::test_check_env_var_in_shell_config_reads_bash_profile,
func TestCheckEnvVarInShellConfig(t *testing.T) {
	home := shellHome(t)
	zshrc := filepath.Join(home, ".zshrc")
	content := strings.Join([]string{
		"# comment",
		`export QUOTED="double-value"`,
		`export SINGLE='single-value'`,
		`export BARE=bare-value`,
		"export SPACED = ignored",
		"",
	}, "\n")
	if err := os.WriteFile(zshrc, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, want string }{
		{"QUOTED", "double-value"},
		{"SINGLE", "single-value"},
		{"BARE", "bare-value"},
	} {
		found, value := CheckEnvVarInShellConfig(tc.name)
		if !found || value != tc.want {
			t.Fatalf("%s = %v %q", tc.name, found, value)
		}
	}
	if found, value := CheckEnvVarInShellConfig("MISSING"); found || value != "" {
		t.Fatalf("missing = %v %q", found, value)
	}

	if path := GetShellConfigPath(ShellZsh); path != zshrc {
		t.Fatalf("config path = %q", path)
	}
	if got := GetShellSourceCommand(ShellZsh); got != "source "+zshrc {
		t.Fatalf("source command = %q", got)
	}
}

// Parity-exempt: add/remove_env_var config editing has no Python test in
func TestAddAndRemoveEnvVar(t *testing.T) {
	home := shellHome(t)
	zshrc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(zshrc, []byte("export KEEP=me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if ok, path := AddEnvVarToShellConfig("NEW_KEY", "new-value"); !ok || path != zshrc {
		t.Fatalf("append = %v %q", ok, path)
	}
	data, _ := os.ReadFile(zshrc)
	if !strings.Contains(string(data), "# Added by Hive credential setup") || !strings.Contains(string(data), `export NEW_KEY="new-value"`) {
		t.Fatalf("appended content = %q", data)
	}

	if ok, _ := AddEnvVarToShellConfig("NEW_KEY", "updated"); !ok {
		t.Fatal("update failed")
	}
	data, _ = os.ReadFile(zshrc)
	if strings.Count(string(data), "export NEW_KEY=") != 1 || !strings.Contains(string(data), `export NEW_KEY="updated"`) {
		t.Fatalf("update content = %q", data)
	}

	if ok, _ := RemoveEnvVarFromShellConfig("NEW_KEY"); !ok {
		t.Fatal("remove failed")
	}
	data, _ = os.ReadFile(zshrc)
	if strings.Contains(string(data), "NEW_KEY") || strings.Contains(string(data), "# Added by Hive") {
		t.Fatalf("remove left residue: %q", data)
	}
	if !strings.Contains(string(data), "export KEEP=me") {
		t.Fatalf("remove dropped unrelated lines: %q", data)
	}
}

// Parity: tools/tests/test_shell_config.py::test_get_shell_config_path_falls_back_to_profile_for_unknown_shell
func TestShellConfigCandidates(t *testing.T) {
	home := "/tmp/example-home"
	zsh := shellCandidates(home, ShellZsh)
	if len(zsh) != 2 || zsh[0] != filepath.Join(home, ".zshrc") {
		t.Fatalf("zsh candidates = %v", zsh)
	}
	bash := shellCandidates(home, ShellBash)
	if len(bash) != 3 || bash[0] != filepath.Join(home, ".bashrc") {
		t.Fatalf("bash candidates = %v", bash)
	}
	unknown := shellCandidates(home, ShellUnknown)
	if len(unknown) != 2 || unknown[0] != filepath.Join(home, ".profile") {
		t.Fatalf("unknown candidates = %v", unknown)
	}
}

// Parity-exempt: HIVE_DESKTOP_MODE gating of shell reads has no Python test.
func TestShellDesktopModeStillReadableAtShellLayer(t *testing.T) {
	home := shellHome(t)
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export ADEN_API_KEY=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readFromShellConfig("ADEN_API_KEY"); got != "x" {
		t.Fatalf("shell read = %q", got)
	}
	t.Setenv("HIVE_DESKTOP_MODE", "1")
	if got := readFromShellConfig("ADEN_API_KEY"); got != "" {
		t.Fatalf("desktop mode read shell config: %q", got)
	}
}
