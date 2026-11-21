package credentials

import (
	"bytes"
	"log"
	"regexp"
	"strings"
	"testing"
)

// TestCredLogSinkFormatsLikePythonLogging pins the HIVE_CRED_LOG line format:
// upstream's _setup_cli_logging installs
// logging.Formatter("%(asctime)s %(levelname)-7s [%(name)s] %(message)s",
// datefmt="%H:%M:%S"), so a sink record reads
// `HH:MM:SS WARNING [framework.credentials.storage] <message>`.
//
// Parity-exempt: the format lives in framework/credentials/cli.py::_setup_cli_logging;
// upstream has no unit test for the formatter string itself.
func TestCredLogSinkFormatsLikePythonLogging(t *testing.T) {
	var sink bytes.Buffer
	SetLogSink(&sink)
	t.Cleanup(func() { SetLogSink(nil) })

	LogWarning("framework.credentials.storage", "credentials: Generated new encryption key. set %s", "HIVE_CREDENTIAL_KEY")
	line := sink.String()
	pattern := regexp.MustCompile(`^\d{2}:\d{2}:\d{2} WARNING \[framework\.credentials\.storage\] Generated new encryption key\. set HIVE_CREDENTIAL_KEY\n$`)
	if !pattern.MatchString(line) {
		t.Fatalf("log line = %q, want the Python formatter shape", line)
	}
	if strings.Contains(line, "credentials: ") {
		t.Fatalf("the hand-written prefix must not survive into the Python-format line: %q", line)
	}
}

// TestCredLogFallbackKeepsStandardLogger pins the non-hive-cred path: with no
// sink installed the helpers write through the standard logger, keeping the
// historical "credentials: ..." lines on stderr.
//
// Parity-exempt: serve/runtime logging has no upstream CLI formatter (no
// HIVE_CRED_LOG file sink there); the fallback preserves the pre-existing Go
// behavior asserted by TestSyncEnvFromStoreInjectsValues.
func TestCredLogFallbackKeepsStandardLogger(t *testing.T) {
	SetLogSink(nil)
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	LogWarning("framework.credentials.key_storage", "credentials: failed to persist %s", "HIVE_CREDENTIAL_KEY")
	if !strings.Contains(logged.String(), "credentials: failed to persist HIVE_CREDENTIAL_KEY") {
		t.Fatalf("fallback log = %q", logged.String())
	}
}
