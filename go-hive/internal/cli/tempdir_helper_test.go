package cli

import (
	"log"
	"os"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// testTempDir returns a temp dir whose cleanup tolerates late background
// writes. Session runtimes and their event listeners can still persist one
// record after the test body returns; testing.T.TempDir's built-in cleanup
// then fails with "directory not empty" (wave-34 TestCreatedSessionSurface…
// cleanup race). We retry RemoveAll for up to 3s before declaring failure.
//
// It also isolates the process-global state these tests share, so a test can
// never inherit it from the developer's machine, an earlier test in the same
// binary (-count=2 / -shuffle reorders them), or a leaked temp path:
//
//   - HIVE_HOME points at this fresh dir. Production paths resolve the home
//     from the process env (credentials.LoadCredentialKey reads
//     $HIVE_HOME/secrets/credential_key), so without the pin a fixture test
//     would read the developer's real ~/.hive key and export it as
//     HIVE_CREDENTIAL_KEY for every later test — which suppresses the
//     fresh-key path TestCredLogFileMatchesPythonLineFormat asserts.
//   - HIVE_CRED_LOG starts empty, so an exported or leaked log path cannot
//     change what the test exercises.
//   - the standard logger's writer, flags and prefix are restored on cleanup;
//     production installs process-wide sinks (installRuntimeLogBuffer,
//     setupCredLogging) and a later test must not inherit that wiring.
func testTempDir(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "hive-cli-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Setenv(hivehome.EnvHome, base)
	t.Setenv("HIVE_CRED_LOG", "")
	logWriter, logFlags, logPrefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() {
		log.SetOutput(logWriter)
		log.SetFlags(logFlags)
		log.SetPrefix(logPrefix)
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := os.RemoveAll(base); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("temp dir cleanup: %v", os.RemoveAll(base))
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return base
}
