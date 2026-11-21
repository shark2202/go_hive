package cli

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// TestCredLogFileMatchesPythonLineFormat pins the end-to-end HIVE_CRED_LOG
// line: a fresh home makes the store mint an encryption key, and the warning
// must land in the log file in Python's logging formatter shape
// (`HH:MM:SS WARNING [framework.credentials.storage] <message>`), not the Go
// standard logger's `2006/01/02 15:04:05` prefix.
//
// Parity-exempt: the formatter lives in framework/credentials/cli.py::_setup_cli_logging;
// upstream's test_hive_cred_cli does not assert the log line shape.
func TestCredLogFileMatchesPythonLineFormat(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "cred.log")
	t.Setenv("HIVE_CRED_LOG", logPath)
	previous := log.Writer()
	t.Cleanup(func() {
		credentials.SetLogSink(nil)
		log.SetOutput(previous)
		log.SetFlags(log.LstdFlags)
	})

	runner := NewCredRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	if code := runner.Run([]string{"browse", "--json"}); code != tracker.ExitOK {
		t.Fatalf("cred browse exit = %d (stderr: %s)", code, stderr.String())
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	pattern := regexp.MustCompile(`(?m)^\d{2}:\d{2}:\d{2} WARNING \[framework\.credentials\.storage\] Generated new encryption key\. To persist credentials across restarts, set HIVE_CREDENTIAL_KEY$`)
	if !pattern.Match(data) {
		t.Fatalf("cred log lacks the Python-format key warning:\n%s", data)
	}
}
