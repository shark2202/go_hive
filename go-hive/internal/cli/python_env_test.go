package cli

// The serve/CLI entry is where upstream drops the launcher's PYTHONHOME; this
// test drives the real Runner entry instead of the pyenv helper alone.

import (
	"bytes"
	"os"
	"testing"
)

// Parity: core/tests/test_python_env.py::test_hive_drops_it_before_running_a_command
func TestRunnerDropsLauncherPythonHomeBeforeRunning(t *testing.T) {
	const home = `C:\Python\cpython-3.11`
	t.Setenv("PYTHONHOME", home)
	t.Setenv("UV_INTERNAL__PYTHONHOME", home)

	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	if code := runner.Run([]string{"--version"}); code != 0 {
		t.Fatalf("Run(--version) = %d, stderr = %q", code, stderr.String())
	}

	if _, ok := os.LookupEnv("PYTHONHOME"); ok {
		t.Fatal("PYTHONHOME survived the CLI entry point")
	}
	if _, ok := os.LookupEnv("UV_INTERNAL__PYTHONHOME"); ok {
		t.Fatal("UV_INTERNAL__PYTHONHOME survived the CLI entry point")
	}
}
