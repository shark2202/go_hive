package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func runCLI(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	code := runner.Run(argv)
	return code, stdout.String(), stderr.String()
}

// Parity: tests/test_cli_entry_point.py::TestFrameworkModule::test_module_help
func TestModuleHelp(t *testing.T) {
	code, stdout, _ := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("Run(--help) = %d, want 0", code)
	}
	if !strings.Contains(strings.ToLower(stdout), "hive") {
		t.Fatalf("stdout = %q, want it to mention hive", stdout)
	}
}

// Parity: tests/test_cli_entry_point.py::TestFrameworkModule::test_module_serve_subcommand
func TestModuleServeSubcommand(t *testing.T) {
	code, stdout, _ := runCLI(t, "serve", "--help")
	if code != 0 {
		t.Fatalf("Run(serve --help) = %d, want 0", code)
	}
	lower := strings.ToLower(stdout)
	if !strings.Contains(lower, "host") && !strings.Contains(lower, "port") {
		t.Fatalf("stdout = %q, want host or port", stdout)
	}
}

// Parity: tests/test_cli_entry_point.py::TestHiveEntryPoint::test_hive_help
func TestHiveHelp(t *testing.T) {
	code, stdout, _ := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("Run(--help) = %d, want 0", code)
	}
	lower := strings.ToLower(stdout)
	for _, command := range []string{"serve", "queen", "colony", "session", "chat"} {
		if !strings.Contains(lower, command) {
			t.Fatalf("stdout missing %q:\n%s", command, stdout)
		}
	}
}

// Parity: tests/test_cli_entry_point.py::TestHiveEntryPoint::test_hive_queen_list_help
func TestHiveQueenListHelp(t *testing.T) {
	code, _, _ := runCLI(t, "queen", "list", "--help")
	if code != 0 {
		t.Fatalf("Run(queen list --help) = %d, want 0", code)
	}
}

// Parity: tests/test_cli_entry_point.py::TestHiveEntryPoint::test_hive_colony_list_help
func TestHiveColonyListHelp(t *testing.T) {
	code, _, _ := runCLI(t, "colony", "list", "--help")
	if code != 0 {
		t.Fatalf("Run(colony list --help) = %d, want 0", code)
	}
}

// Parity: tests/test_cli_entry_point.py::TestHiveEntryPoint::test_hive_unknown_command_exits_nonzero
func TestHiveUnknownCommandExitsNonzero(t *testing.T) {
	code, _, _ := runCLI(t, "definitely-not-a-command")
	if code == 0 {
		t.Fatal("Run(definitely-not-a-command) = 0, want nonzero")
	}
}

// Parity-exempt: pins the sys.stdin.isatty() analogue the Python tests fake
func TestStdinIsTTYFalseForDevNull(t *testing.T) {
	if runtimeGOOS() == "windows" {
		t.Skip("/dev/null is POSIX-only")
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	original := os.Stdin
	os.Stdin = devNull
	defer func() { os.Stdin = original }()

	if stdinIsTTY() {
		t.Fatal("stdinIsTTY() = true for /dev/null, want false")
	}
}
