package cli

import (
	"os"
	"testing"
	"time"
)

// testTempDir returns a temp dir whose cleanup tolerates late background
// writes. Session runtimes and their event listeners can still persist one
// record after the test body returns; testing.T.TempDir's built-in cleanup
// then fails with "directory not empty" (wave-34 TestCreatedSessionSurface…
// cleanup race). We retry RemoveAll for up to 3s before declaring failure.
func testTempDir(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "hive-cli-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
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
