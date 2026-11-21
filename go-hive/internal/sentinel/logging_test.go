package sentinel

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for the ported HIVE_SENTINEL_LOG file handler: the opt-in gate, the
// file path and the divert (file, not the main log).

func unsetSentinelLogEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HIVE_SENTINEL_LOG", "x")
	if err := os.Unsetenv("HIVE_SENTINEL_LOG"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
}

// Parity: framework/sentinel/manager.py::_install_sentinel_log_file
func TestSentinelLogOffByDefault(t *testing.T) {
	unsetSentinelLogEnv(t)
	resetSentinelLoggingForTest()
	t.Cleanup(resetSentinelLoggingForTest)
	home := t.TempDir()
	ConfigureLogging(home)
	if _, err := os.Stat(filepath.Join(home, "logs", "sentinel.log")); !os.IsNotExist(err) {
		t.Fatalf("sentinel.log created without HIVE_SENTINEL_LOG (%v)", err)
	}
	sentinelLogf("no file here")
}

// Parity: framework/sentinel/manager.py::_install_sentinel_log_file
func TestSentinelLogDivertsToItsFile(t *testing.T) {
	t.Setenv("HIVE_SENTINEL_LOG", "1")
	resetSentinelLoggingForTest()
	t.Cleanup(resetSentinelLoggingForTest)
	home := t.TempDir()
	ConfigureLogging(home)
	ConfigureLogging(home) // idempotent: a second install must not double-write
	sentinelLogf("sentinel divergence %d", 7)

	raw, err := os.ReadFile(filepath.Join(home, "logs", "sentinel.log"))
	if err != nil {
		t.Fatalf("read sentinel.log: %v", err)
	}
	if !strings.Contains(string(raw), "sentinel divergence 7") {
		t.Fatalf("sentinel.log content = %q", raw)
	}
	// Lines carry upstream's human-log timestamp shape ("2006-01-02 15:04:05").
	if !regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} sentinel divergence 7$`).Match(raw) {
		t.Fatalf("sentinel.log line is not in the upstream timestamp shape: %q", raw)
	}
}

// Parity: framework/sentinel/manager.py::_install_sentinel_log_file
func TestSentinelLogSizesMatchUpstreamRotation(t *testing.T) {
	if sentinelLogMaxBytes != 5_000_000 || sentinelLogBackups != 3 {
		t.Fatalf("sentinel log rotation = %d x %d, want 5MB x 3 (RotatingFileHandler maxBytes=5_000_000, backupCount=3)",
			sentinelLogMaxBytes, sentinelLogBackups)
	}
}

// Parity: framework/sentinel/manager.py::_install_sentinel_log_file
func TestSentinelLogPresenceGateAcceptsZero(t *testing.T) {
	// Upstream: `if not os.environ.get("HIVE_SENTINEL_LOG"): return` — "0" is a
	// non-empty string, so it still installs the file.
	t.Setenv("HIVE_SENTINEL_LOG", "0")
	resetSentinelLoggingForTest()
	t.Cleanup(resetSentinelLoggingForTest)
	home := t.TempDir()
	ConfigureLogging(home)
	sentinelLogf("zero still diverts")
	if _, err := os.Stat(filepath.Join(home, "logs", "sentinel.log")); err != nil {
		t.Fatalf("sentinel.log missing for HIVE_SENTINEL_LOG=0: %v", err)
	}
}
