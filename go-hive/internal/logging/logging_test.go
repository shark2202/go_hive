package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the ported rotating server log: the file path, the rotation and
// the HIVE_NO_FILE_LOG gate (any non-empty value disables the file).

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "x")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

// Parity: framework/observability/logging.py::configure_logging
func TestServerLogWritesUnderHiveHomeLogs(t *testing.T) {
	unsetEnv(t, "HIVE_NO_FILE_LOG")
	home := t.TempDir()
	writer, enabled, err := OpenServerLog(home)
	if err != nil || !enabled {
		t.Fatalf("OpenServerLog = (%v, %v), want an enabled writer", enabled, err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("hello server\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "logs", "hive-server.log"))
	if err != nil {
		t.Fatalf("read server log: %v", err)
	}
	if !strings.Contains(string(raw), "hello server") {
		t.Fatalf("server log content = %q", raw)
	}
}

// Parity: framework/observability/logging.py::configure_logging
func TestNoFileLogDisablesTheFileForAnyNonEmptyValue(t *testing.T) {
	for _, value := range []string{"1", "0", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HIVE_NO_FILE_LOG", value)
			home := t.TempDir()
			writer, enabled, err := OpenServerLog(home)
			if err != nil {
				t.Fatalf("OpenServerLog: %v", err)
			}
			if enabled || writer != nil {
				t.Fatalf("HIVE_NO_FILE_LOG=%q still enabled the file (upstream gates on os.getenv truthiness)", value)
			}
			if _, err := os.Stat(filepath.Join(home, "logs", "hive-server.log")); !os.IsNotExist(err) {
				t.Fatalf("server log created despite HIVE_NO_FILE_LOG=%q (%v)", value, err)
			}
		})
	}
}

// Parity-exempt: upstream delegates rotation to logging.handlers.RotatingFileHandler
// (20MB x 3); this pins the Go reimplementation's shifting and backup cap.
func TestRotatingFileShiftsBackups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.log")
	writer, err := OpenRotating(path, 8, 3)
	if err != nil {
		t.Fatalf("OpenRotating: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for i := 0; i < 4; i++ {
		if _, err := writer.Write([]byte("0123456789")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	for _, want := range []string{path, path + ".1", path + ".2", path + ".3"} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected %s after rotation: %v", want, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("backupCount=3 must not keep a .4 (%v)", err)
	}
}
